package homeaiwire

import (
	"encoding/json"
	"time"
)

const Subprotocol = "nori.inference.v1"
const RefreshLead = 6 * time.Minute

type Model struct {
	Name         string   `json:"name"`
	Digest       string   `json:"digest"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type Message struct {
	Type            string          `json:"type"`
	RequestID       string          `json:"request_id,omitempty"`
	AttemptID       string          `json:"attempt_id,omitempty"`
	Sequence        int64           `json:"sequence,omitempty"`
	Operation       string          `json:"operation,omitempty"`
	Model           string          `json:"model,omitempty"`
	Digest          string          `json:"digest,omitempty"`
	Deadline        time.Time       `json:"deadline,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Data            []byte          `json:"data,omitempty"`
	Status          int             `json:"status,omitempty"`
	ContentType     string          `json:"content_type,omitempty"`
	Error           string          `json:"error,omitempty"`
	Models          []Model         `json:"models,omitempty"`
	Version         string          `json:"version,omitempty"`
	OllamaAvailable bool            `json:"ollama_available"`
}
