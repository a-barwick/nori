// Package invoke is the adapter contract of the Tool executor. App connectors, the MCP client and plugin executors
// implement Adapter; the executor in the task package owns admission, authority, attempts and outcomes, so an
// adapter never retries, schedules or records on its own.
package invoke

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

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
	adapter, ok := r.adapters[key(binding.Adapter, prefix)]
	return adapter, ok
}

// Validation errors are the caller's fault and are never sent to the provider.
var ErrInvalidArguments = errors.New("invalid arguments")

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
