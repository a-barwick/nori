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

// Management resources (connections, permission rules, interactions, automations, goals, preferences,
// installations, MCP servers, delivery destinations) share one storage shape: a user-scoped document with a
// positive revision, an exact JSON body, and a few indexed scalar fields for queries. Every change goes through
// Mutate, which deduplicates by request ID and commits the resource change and its receipt in one transaction.

var (
	ErrRevisionConflict    = errors.New("the resource changed since it was read")
	ErrIdempotencyConflict = errors.New("this request ID was already used for a different change")
	ErrInvalidResource     = errors.New("invalid resource")
)

// Resource is one stored management resource.
type Resource struct {
	Collection string
	ID         string
	Revision   int64
	Body       json.RawMessage
	// Index holds scalar fields the store can filter on, such as status or a linked ID.
	Index     map[string]string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MutationReceipt is the saved acceptance of one management request. A retry with the same request ID and payload
// returns it unchanged, even if the resource has changed since.
type MutationReceipt struct {
	RequestID string `json:"request_id"`
	// Binding identifies what the request ID was used for: method, target, expected revision and payload digest.
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

// Mutation describes one request. Apply runs inside the transaction with the current resource (nil when absent)
// and returns the resources to write; the first is the target. Apply must not have side effects outside its return
// value, because a transaction may run it more than once.
type Mutation struct {
	RequestID        string
	Method, Target   string
	ExpectedRevision int64
	Payload          any
	Collection, ID   string
	// SkipRevisionCheck is for commands whose guard is the resource's state rather than a client-read revision.
	SkipRevisionCheck bool
	Apply             func(current *Resource, read func(collection, id string) (*Resource, error)) (MutationResult, error)
}

type MutationResult struct {
	Writes  []Resource
	Deletes []ResourceKey
	Receipt MutationReceipt
}

type ResourceKey struct{ Collection, ID string }

// ResourceStore is the user-scoped storage for management resources.
type ResourceStore interface {
	Resource(ctx context.Context, collection, id string) (Resource, error)
	// Resources lists a collection in ID order after a cursor. An empty filter value matches any.
	Resources(ctx context.Context, collection string, filter map[string]string, after string, limit int) ([]Resource, string, error)
	Mutate(ctx context.Context, m Mutation) (MutationReceipt, error)
	Receipt(ctx context.Context, requestID string) (MutationReceipt, error)
	// PutResource writes a resource outside the client mutation path, for server-observed state such as health.
	// expected is the revision it must still have; 0 creates it.
	PutResource(ctx context.Context, r Resource, expected int64) (Resource, error)
}

var resourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

// ValidResourceID reports whether id is safe as a document key.
func ValidResourceID(id string) bool { return resourceID.MatchString(id) }

// MutationBinding is the digest that ties a request ID to one change.
func MutationBinding(m Mutation) (string, error) {
	payload, err := json.Marshal(m.Payload)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal([]any{m.Method, m.Target, m.ExpectedRevision, json.RawMessage(payload)})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// NewResource encodes a typed body into a Resource.
func NewResource(collection, id string, revision int64, body any, index map[string]string, now time.Time) (Resource, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return Resource{}, err
	}
	return Resource{Collection: collection, ID: id, Revision: revision, Body: data, Index: index, UpdatedAt: now}, nil
}

// Decode reads a resource body into a typed value.
func (r Resource) Decode(v any) error { return json.Unmarshal(r.Body, v) }
