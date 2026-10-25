package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
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
	Intent     Intent  `json:"intent,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Available  bool    `json:"available"`
}

type Classifier struct {
	Key    string
	Client *http.Client
}

func NewClassifier(key string) *Classifier {
	return &Classifier{Key: key, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Classifier) Classify(ctx context.Context, input string, history []Message) (Classification, error) {
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
		return Classification{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.typesafe.ai/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return Classification{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Client.Do(req)
	if err != nil {
		return Classification{}, fmt.Errorf("classification unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Classification{}, fmt.Errorf("classification returned HTTP %d", res.StatusCode)
	}
	var result struct {
		Answers map[string]struct {
			Choice     Intent  `json:"choice"`
			Confidence float64 `json:"confidence"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result); err != nil {
		return Classification{}, fmt.Errorf("classification response was unreadable")
	}
	answer, ok := result.Answers["intent"]
	if _, valid := intentDescriptions[answer.Choice]; !ok || !valid {
		return Classification{}, fmt.Errorf("classification returned an unknown intent")
	}
	return Classification{Intent: answer.Choice, Confidence: answer.Confidence, Available: true}, nil
}
