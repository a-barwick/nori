// Package invoke is the adapter contract of the Tool executor. App connectors and the MCP client
// implement Adapter; the executor in the task package owns admission, authority, attempts and outcomes, so an
// adapter never retries, schedules or records on its own.
package invoke

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/state"
)

// Call is one pinned invocation as an adapter sees it.
type Call struct {
	RunID       string
	Revision    int64
	OperationID string
	Attempt     int
	Exec        extensions.Exec
	Args        map[string]any
	// Grant is the account access for this attempt only; nil when the tool needs no connection.
	Grant *connections.Grant
	// Progress reports a short progress line for long calls; it may be nil.
	Progress func(text string)
	// TimeZone is the user's IANA time zone, for writing times in summaries the user reads.
	TimeZone string
	// Upload reads a file the task was given, by upload ID; it refuses files the task was not given.
	Upload func(ctx context.Context, id string) (name, mediaType string, data []byte, err error)
}

// Prepared is the normalized action. Preparing makes no external writes.
type Prepared struct {
	Summary         string
	Effect          extensions.Effect
	Resources       []state.ProviderRef
	Recipients      []string
	Precondition    string
	Spec            map[string]string
	MaterialEffects []string
	// Keys are provider idempotency keys derived from the action, such as a client-chosen event ID. They are not
	// part of the approval digest: an approved action re-called unchanged must match its approval.
	Keys map[string]string
	// PromptTitle optionally overrides the approval card heading (while Summary stays the body).
	PromptTitle string
	// SkipAutoReview bypasses AutoReviewer for simple internal user-requested actions (e.g. one-time Nori reminders).
	SkipAutoReview bool
	// RequireConfirmation requests an explicit approval card unless already human-approved or covered by a standing rule.
	RequireConfirmation bool
}

// Result is what the provider showed. The executor maps it onto the operation ledger.
type Result struct {
	// Outcome is confirmed, failed, submitted or uncertain.
	Outcome      string
	Content      map[string]any
	ProviderRefs []state.ProviderRef
	CostUSD      float64
	CostUnknown  bool
	Err          error
	// Cleared, set with a failed or uncertain outcome, says why the call can't have changed anything (a
	// state.Cleared* value), so it doesn't block later calls or need the user's answer.
	Cleared string
}

type Adapter interface {
	Prepare(ctx context.Context, call Call) (Prepared, error)
	Execute(ctx context.Context, call Call, prepared Prepared) Result
}

// Reconciler resolves an interrupted attempt by reading provider state. ok is false when the outcome cannot be
// established, in which case the operation stays uncertain.
type Reconciler interface {
	Reconcile(ctx context.Context, call Call, prepared Prepared) (result Result, ok bool)
}

// Canceller asks the provider to stop an in-flight call where supported.
type Canceller interface {
	Cancel(ctx context.Context, call Call) error
}

// Registry maps bindings to adapters by adapter kind and the target's prefix before the first "/".
type Registry struct{ adapters map[string]Adapter }

func NewRegistry() *Registry { return &Registry{adapters: map[string]Adapter{}} }

func key(kind extensions.Adapter, prefix string) string { return string(kind) + ":" + prefix }

func (r *Registry) Register(kind extensions.Adapter, prefix string, adapter Adapter) {
	r.adapters[key(kind, prefix)] = adapter
}

// Lookup finds the adapter for a binding. A missing adapter means this worker does not carry the implementation;
// the executor reports it unavailable and never substitutes another.
func (r *Registry) Lookup(binding extensions.Binding) (Adapter, bool) {
	if r == nil {
		return nil, false
	}
	prefix, _, _ := strings.Cut(binding.Target, "/")
	if adapter, ok := r.adapters[key(binding.Adapter, prefix)]; ok {
		return adapter, true
	}
	// An adapter registered with an empty prefix serves every target of its kind.
	adapter, ok := r.adapters[key(binding.Adapter, "")]
	return adapter, ok
}

// Validation errors are the caller's fault and are never sent to the provider.
var ErrInvalidArguments = errors.New("invalid arguments")

// TemporaryError is a provider refusal worth retrying after a pause, such as 429 or 503. After is the provider's
// Retry-After when it sent one.
type TemporaryError struct {
	After time.Duration
	Err   error
}

func (e *TemporaryError) Error() string { return e.Err.Error() }
func (e *TemporaryError) Unwrap() error { return e.Err }

// RetryAfter parses a Retry-After header in seconds or as an HTTP date; zero when absent or unparseable.
func RetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}

// Digest binds an approval to the exact action: the pinned definition, the account and its authorization epoch,
// the normalized parameters, targets, recipients and the provider precondition. A display label is not part of it.
func Digest(call Call, connectionID string, epoch int64, p Prepared) string {
	keys := make([]string, 0, len(p.Spec))
	for k := range p.Spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	spec := make([][2]string, 0, len(keys))
	for _, k := range keys {
		spec = append(spec, [2]string{k, p.Spec[k]})
	}
	resources := make([]string, 0, len(p.Resources))
	for _, r := range p.Resources {
		resources = append(resources, r.Kind+":"+r.ProviderID+"@"+r.ProviderRevision)
	}
	recipients := append([]string{}, p.Recipients...)
	sort.Strings(recipients)
	data, _ := json.Marshal([]any{call.Exec.Tool.ID, call.Exec.Tool.Digest, connectionID, epoch, spec, resources, recipients, p.Precondition, p.Effect})
	sum := sha256.Sum256(data)
	return "act_" + hex.EncodeToString(sum[:20])
}

// ActionKey identifies an action within one task revision independently of the call that made it: the same task,
// revision, tool and normalized parameters give the same key, so a re-call after approval reuses the provider
// idempotency key instead of creating a duplicate.
func ActionKey(call Call, spec map[string]string) string {
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{call.RunID, fmt.Sprint(call.Revision), call.Exec.Tool.ID}
	for _, k := range keys {
		parts = append(parts, k+"="+spec[k])
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// String reads a string argument.
func String(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return strings.TrimSpace(value)
}

// Strings reads a string-array argument.
func Strings(args map[string]any, name string) []string {
	raw, _ := args[name].([]any)
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

// Int reads an integer argument with a default.
func Int(args map[string]any, name string, fallback int) int {
	switch v := args[name].(type) {
	case float64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return int(n)
		}
	case int:
		return v
	}
	return fallback
}

// Bool reads a boolean argument.
func Bool(args map[string]any, name string) bool {
	value, _ := args[name].(bool)
	return value
}

// HumanTime writes an RFC 3339 instant or a YYYY-MM-DD date the way the user reads it, in their zone when known:
// "Tue 10 Nov 2026, 10:00 PST" or "Sat 14 Mar 2027".
func HumanTime(value, zone string) string {
	if d, err := time.Parse("2006-01-02", value); err == nil {
		return d.Format("Mon 2 Jan 2006")
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	if loc, err := time.LoadLocation(zone); err == nil && zone != "" {
		t = t.In(loc)
	}
	return t.Format("Mon 2 Jan 2006, 15:04 MST")
}

// SignInStart is a sign-in an adapter needs before it can run a call: the attempt to open and what it signs in to.
type SignInStart struct {
	IntegrationID, ConnectionID, Label string
	AttemptID, AuthorizeURL            string
	ExpiresAt                          time.Time
}

// SignInChecker is implemented by adapters whose calls can need the user to sign in first (MCP servers with OAuth).
// A nil start means the call can run.
type SignInChecker interface {
	SignInNeeded(ctx context.Context, call Call, returnTo string) (*SignInStart, error)
}
