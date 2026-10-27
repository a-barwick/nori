package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

type Intent string

const (
	Converse     Intent = "converse"
	Ask          Intent = "ask"
	RequestWork  Intent = "request_work"
	Steer        Intent = "steer"
	ProvideInput Intent = "provide_input"
	Decide       Intent = "decide"
	Inspect      Intent = "inspect"
	Control      Intent = "control"
	UpdateMemory Intent = "update_memory"
	Mixed        Intent = "mixed"
)

var intentDescriptions = map[Intent]string{
	Converse:     "Conversation, discussion, or unclassifiable input. Discussion alone does not request work.",
	Ask:          "Ask a question; answering may or may not require independent work.",
	RequestWork:  "Ask Nori to do independent work.",
	Steer:        "Change instructions for existing work.",
	ProvideInput: "Supply information or answer a question from existing work.",
	Decide:       "Give an approval or another requested decision.",
	Inspect:      "Ask about existing work or its outcome.",
	Control:      "Ask to stop or otherwise control existing work.",
	UpdateMemory: "Explicitly request a persistent memory change.",
	Mixed:        "The message combines multiple distinct intents; one label would omit part of it.",
}

type Classification struct {
	Usage  state.Usage `json:"-"`
	Intent Intent      `json:"intent,omitempty"`
	// Confidence is the lowest Jev concentration statistic among selected flags,
	// not the probability that an intent is present. It never gates action.
	Confidence float64  `json:"confidence,omitempty"`
	Intents    []Intent `json:"intents,omitempty"`
	Available  bool     `json:"available"`
}

type Classifier struct {
	RecordUsage func(context.Context, state.Usage) error
	Key         string
	Client      *http.Client
}

func NewClassifier(key string) *Classifier {
	return &Classifier{Key: key, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Classifier) Classify(ctx context.Context, inputs []state.ConversationInput, history []Message) (classification Classification, err error) {
	current := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		if input.Introduction {
			continue
		}
		current = append(current, map[string]any{"id": input.ID, "message": inputMessage(input)})
	}
	if len(current) == 0 {
		return Classification{Available: false}, nil
	}
	// Jev does not supply a verified USD price in this integration. Record the
	// request explicitly as unknown rather than estimate an undocumented cost.
	classification.Usage = state.Usage{ID: uuid.NewString(), Provider: "jev", Model: "jev-latest", CostStatus: "unknown", RequestStatus: "not_sent", CreatedAt: time.Now().UTC()}
	// Classify against the whole visible conversation. Provider reasoning and
	// protocol metadata belong to the conversational model's history, not Jev.
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
			"instructions": "Independently check whether the CURRENT input batch contains this intent. Read its messages in their supplied order; later messages can correct earlier ones. Several intents may be present. Use conversation only to resolve references. Classify the underlying request independently of available tools, access or requested reply format. This is advisory, never a route or permission. Intent: " + description,
			"criteria":     map[string]string{"present": "The current input batch contains this intent.", "absent": "The current input batch does not contain this intent."},
		}
	}
	body, err := json.Marshal(map[string]any{
		"model": "jev-latest", "state": map[string]any{"messages": current, "conversation": conversation}, "questions": questions,
	})
	if err != nil {
		return classification, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.typesafe.ai/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return classification, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	// Persist an unknown-cost attempt before provider I/O. A crash or failed
	// response projection can leave its cost unknown, but cannot erase the call.
	classification.Usage.RequestStatus = "pending"
	if c.RecordUsage != nil {
		if err := c.RecordUsage(ctx, classification.Usage); err != nil {
			classification.Usage.RequestStatus = "not_sent"
			return classification, fmt.Errorf("usage recording unavailable; provider request was not sent")
		}
	}
	classification.Usage.RequestStatus = "unknown"
	res, err := c.Client.Do(req)
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
		// No selected flag is unclassifiable input, so retain the conversational
		// fallback without inventing a provider confidence for that fallback.
		classification.Intents = []Intent{Converse}
	}
	if len(classification.Intents) == 1 {
		classification.Intent = classification.Intents[0]
	} else if len(classification.Intents) > 1 {
		classification.Intent = Mixed
	}
	return classification, nil
}
