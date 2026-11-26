package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/llmguard"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
)

const (
	jevURL      = "https://api.typesafe.ai/v1/systemone"
	jevModel    = "jev-latest"
	jevProvider = "typesafe"
)

type JevClassifier struct {
	key    string
	Client *http.Client
	Guard  *llmguard.Guard
	UserID string
	Now    func() time.Time
	NewID  func() string
}

func (c *JevClassifier) guard() *llmguard.Guard {
	if c != nil && c.Guard != nil {
		return c.Guard
	}
	return llmguard.Default()
}

func (c *JevClassifier) httpClient() *http.Client {
	return c.guard().WrapClient(c.Client, 10*time.Second, llmguard.CallMeta{
		UserID:   c.UserID,
		Scope:    llmguard.ScopeClassify,
		Provider: jevProvider,
		Model:    jevModel,
	})
}

func (c *JevClassifier) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *JevClassifier) newID() string {
	if c != nil && c.NewID != nil {
		return c.NewID()
	}
	return uuid.NewString()
}

func NewJevClassifier(key string) *JevClassifier {
	g := llmguard.New(llmguard.DefaultConfig())
	return &JevClassifier{
		key:    key,
		Guard:  g,
		Client: g.Client(10*time.Second, llmguard.CallMeta{Scope: llmguard.ScopeClassify, Provider: jevProvider, Model: jevModel}),
	}
}

func (c *JevClassifier) Classify(ctx context.Context, request ClassifyRequest) (classification Classification, err error) {
	inputs := request.Inputs
	current := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		if !input.IsUser() {
			continue
		}
		current = append(current, map[string]any{"id": input.ID, "message": inputMessage(input)})
	}
	if len(current) == 0 {
		return Classification{Available: false}, nil
	}
	var requestStarted time.Time
	ctx, span := conversationTracer.Start(ctx, "classify "+jevModel, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("openinference.span.kind", "LLM"), attribute.String("session.id", observability.SessionID()),
		attribute.String("gen_ai.operation.name", "classify"), attribute.String("gen_ai.provider.name", jevProvider),
		attribute.String("gen_ai.request.model", jevModel), attribute.String("llm.model_name", jevModel),
	))
	for i := len(inputs) - 1; i >= 0; i-- {
		if inputs[i].IsUser() {
			if a, ok := observability.ContentAttribute("input.value", inputs[i].Text); ok {
				span.SetAttributes(a)
			}
			break
		}
	}
	defer func() {
		span.SetAttributes(
			attribute.Bool("nori.output.present", classification.Available),
			attribute.String("nori.classification.intent", string(classification.Intent)),
			attribute.String("nori.cost.status", "unknown"),
		)
		provider, model := attribute.String("gen_ai.provider.name", jevProvider), attribute.String("gen_ai.request.model", jevModel)
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) && !isSupersededCallError(err) {
			observability.RecordErrorCategory(ctx, "model.classifier", err)
			if !requestStarted.IsZero() {
				observability.Event(ctx, "model.call.failed", provider, model)
			}
		} else if err == nil && !requestStarted.IsZero() {
			observability.Event(ctx, "model.call.completed", provider, model)
		}
		if !requestStarted.IsZero() && !errors.Is(ctx.Err(), context.Canceled) {
			observability.Metric(ctx, "nori.model.duration_ms", float64(c.now().Sub(requestStarted).Milliseconds()))
		}
		span.End()
	}()
	classification.Usage = state.Usage{
		ID: c.newID(), Provider: "jev", Model: jevModel, CostStatus: "unknown", RequestStatus: "not_sent", CreatedAt: c.now().UTC(),
	}
	const maxJevHistoryMessages = 8
	history := request.History
	if len(history) > maxJevHistoryMessages {
		history = history[len(history)-maxJevHistoryMessages:]
	}
	conversation := make([]map[string]string, 0, len(history))
	for _, message := range history {
		conversation = append(conversation, map[string]string{"role": message.Role, "content": message.Text})
	}
	questions := map[string]any{}
	for intent, description := range intentDescriptions {
		if intent == Mixed {
			continue
		}
		questions[string(intent)] = map[string]any{
			"type":         "choice",
			"instructions": prompts.Runtime("classify-intent", map[string]any{"Intent": description}),
			"criteria": map[string]string{
				"present": prompts.Runtime("intent-present", nil), "absent": prompts.Runtime("intent-absent", nil),
			},
		}
	}
	body, err := json.Marshal(map[string]any{
		"model": jevModel, "state": map[string]any{"messages": current, "conversation": conversation}, "questions": questions,
	})
	if err != nil {
		return classification, err
	}
	reqCtx := llmguard.WithMeta(ctx, llmguard.CallMeta{
		UserID:   c.UserID,
		Scope:    llmguard.ScopeClassify,
		Provider: jevProvider,
		Model:    jevModel,
	})
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, jevURL, bytes.NewReader(body))
	if err != nil {
		return classification, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	classification.Usage.RequestStatus = "pending"
	if request.RecordUsage != nil {
		if err := request.RecordUsage(ctx, classification.Usage); err != nil {
			classification.Usage.RequestStatus = "not_sent"
			return classification, usageRecordError(err)
		}
	}
	classification.Usage.RequestStatus = "unknown"
	requestStarted = c.now()
	res, err := c.httpClient().Do(req)
	if err != nil {
		return classification, fmt.Errorf("classification unavailable")
	}
	classification.Usage.RequestStatus = "received"
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return classification, fmt.Errorf("classification returned HTTP %d", res.StatusCode)
	}
	var result struct {
		Answers map[string]struct {
			Choice     string  `json:"choice"`
			Confidence float64 `json:"confidence"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result); err != nil {
		return classification, fmt.Errorf("classification response was unreadable")
	}
	for _, intent := range []Intent{Converse, Ask, RequestWork, Steer, ProvideInput, Decide, Inspect, Control, UpdateMemory} {
		answer, ok := result.Answers[string(intent)]
		if !ok || (answer.Choice != "present" && answer.Choice != "absent") {
			return classification, fmt.Errorf("classification returned incomplete intent hints")
		}
		if answer.Choice == "present" {
			classification.Intents = append(classification.Intents, intent)
			if len(classification.Intents) == 1 {
				classification.Confidence = answer.Confidence
			} else {
				classification.Confidence = min(classification.Confidence, answer.Confidence)
			}
		}
	}
	classification.Available = true
	if len(classification.Intents) == 0 {
		classification.Intents = []Intent{Converse}
	}
	if len(classification.Intents) == 1 {
		classification.Intent = classification.Intents[0]
	} else if len(classification.Intents) > 1 {
		classification.Intent = Mixed
	}
	return classification, nil
}
