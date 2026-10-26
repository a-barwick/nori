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
	Usage      state.Usage `json:"-"`
	Intent     Intent      `json:"intent,omitempty"`
	Confidence float64     `json:"confidence,omitempty"`
	Available  bool        `json:"available"`
}

type Classifier struct {
	RecordUsage func(context.Context, state.Usage) error
	Key         string
	Client      *http.Client
}

func NewClassifier(key string) *Classifier {
	return &Classifier{Key: key, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Classifier) Classify(ctx context.Context, input string, history []Message) (classification Classification, err error) {
	// Jev does not supply a verified USD price in this integration. Record the
	// request explicitly as unknown rather than estimate an undocumented cost.
	classification.Usage = state.Usage{ID: uuid.NewString(), Provider: "jev", Model: "jev-latest", CostStatus: "unknown", RequestStatus: "not_sent", CreatedAt: time.Now().UTC()}
	// Classify against the whole visible conversation. Provider reasoning and
	// protocol metadata belong to the conversational model's history, not Jev.
	conversation := make([]map[string]string, 0, len(history))
	for _, message := range history {
		conversation = append(conversation, map[string]string{"role": message.Role, "content": message.Text})
	}
	body, err := json.Marshal(map[string]any{
		"model": "jev-latest",
		"state": map[string]any{"message": input, "conversation": conversation},
		"questions": map[string]any{"intent": map[string]any{
			"type":         "choice",
			"instructions": "Which intent best describes the CURRENT message? Use the conversation only to resolve references. Classify the underlying request independently of available tools or requested reply format. A factual question remains ask even if answering needs a lookup or unavailable access. Choose mixed when multiple distinct intents are present. This is a non-exhaustive advisory hint, not a route or permission to act. Choose converse for random or incomplete input.",
			"criteria":     intentDescriptions,
		}},
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
			Choice     Intent  `json:"choice"`
			Confidence float64 `json:"confidence"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result); err != nil {
		return classification, fmt.Errorf("classification response was unreadable")
	}
	answer, ok := result.Answers["intent"]
	if _, valid := intentDescriptions[answer.Choice]; !ok || !valid {
		return classification, fmt.Errorf("classification returned an unknown intent")
	}
	classification.Intent, classification.Confidence, classification.Available = answer.Choice, answer.Confidence, true
	return classification, nil
}
