package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

var (
	ErrRevisionConflict    = errors.New("the resource changed since it was read")
	ErrIdempotencyConflict = errors.New("this request ID was already used for a different change")
	ErrInvalidResource     = errors.New("invalid resource")
)

type Resource struct {
	Collection string
	ID         string
	Revision   int64
	Body       json.RawMessage
	Index      map[string]string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type MutationReceipt struct {
	RequestID  string          `json:"request_id"`
	Binding    string          `json:"-"`
	Accepted   bool            `json:"accepted"`
	Status     string          `json:"status"`
	Resource   json.RawMessage `json:"resource"`
	Work       *WorkRef        `json:"work,omitempty"`
	Auth       json.RawMessage `json:"auth,omitempty"`
	HTTPStatus int             `json:"-"`
	CreatedAt  time.Time       `json:"-"`
}

type WorkRef struct {
	TaskID      string `json:"task_id"`
	OperationID string `json:"operation_id,omitempty"`
}

type Mutation struct {
	RequestID         string
	Method, Target    string
	ExpectedRevision  int64
	Payload           any
	Collection, ID    string
	SkipRevisionCheck bool
	Apply             func(current *Resource, read func(collection, id string) (*Resource, error)) (MutationResult, error)
}

type MutationResult struct {
	Writes  []Resource
	Deletes []ResourceKey
	Receipt MutationReceipt
}

type ResourceKey struct{ Collection, ID string }

type ResourceStore interface {
	Resource(ctx context.Context, collection, id string) (Resource, error)
	Resources(ctx context.Context, collection string, filter map[string]string, after string, limit int) ([]Resource, string, error)
	Mutate(ctx context.Context, m Mutation) (MutationReceipt, error)
	Receipt(ctx context.Context, requestID string) (MutationReceipt, error)
	PutResource(ctx context.Context, r Resource, expected int64) (Resource, error)
}

var resourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

func ValidResourceID(id string) bool { return resourceID.MatchString(id) }

func MutationBinding(m Mutation) (string, error) {
	payload, err := json.Marshal(m.Payload)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal([]any{m.Method, m.Target, m.ExpectedRevision, json.RawMessage(payload)})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func NewResource(collection, id string, revision int64, body any, index map[string]string, now time.Time) (Resource, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return Resource{}, err
	}
	return Resource{Collection: collection, ID: id, Revision: revision, Body: data, Index: index, UpdatedAt: now}, nil
}

func (r Resource) Decode(v any) error { return json.Unmarshal(r.Body, v) }
