package invoke

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/state"
)

const (
	OriginUser       = "user"
	OriginAutomation = "automation"
)

type Call struct {
	RunID        string
	Revision     int64
	OperationID  string
	Attempt      int
	Origin       string
	Exec         extensions.Exec
	Args         map[string]any
	Grant        *connections.Grant
	Progress     func(text string)
	TimeZone     string
	ReadOnly     bool
	UserSources  []string
	Upload       func(ctx context.Context, id string) (name, mediaType string, data []byte, err error)
	InspectImage func(ctx context.Context, label, instruction, question string, data []byte, mediaType string) (map[string]any, error)
	PublishFile  func(ctx context.Context, title, fileName, mediaType string, data []byte) (state.Result, error)
}

type Prepared struct {
	Summary                 string
	Effect                  extensions.Effect
	Resources               []state.ProviderRef
	Recipients              []string
	Precondition            string
	Spec                    map[string]string
	MaterialEffects         []string
	Keys                    map[string]string
	PromptTitle             string
	SkipAutoReview          bool
	RequireConfirmation     bool
	RequireExplicitApproval bool
}

type Result struct {
	Outcome      string
	Content      map[string]any
	ProviderRefs []state.ProviderRef
	CostUSD      float64
	CostUnknown  bool
	Err          error
	Cleared      string
}

type Adapter interface {
	Prepare(ctx context.Context, call Call) (Prepared, error)
	Execute(ctx context.Context, call Call, prepared Prepared) Result
}

type Reconciler interface {
	Reconcile(ctx context.Context, call Call, prepared Prepared) (result Result, ok bool)
}

type Canceller interface {
	Cancel(ctx context.Context, call Call) error
}

type Registry struct{ adapters map[string]Adapter }

func NewRegistry() *Registry { return &Registry{adapters: map[string]Adapter{}} }

func key(kind extensions.Adapter, prefix string) string { return string(kind) + ":" + prefix }

func (r *Registry) Register(kind extensions.Adapter, prefix string, adapter Adapter) {
	r.adapters[key(kind, prefix)] = adapter
}

var ErrToolForbiddenForOrigin = errors.New("tool is not permitted for this run origin")

// IsForbiddenForOrigin reports whether a tool must be stripped from unattended
// automation-origin runs (automation_create, automation_change, and any
// permission-rule write tool).
func IsForbiddenForOrigin(origin, toolID, modelName, target string) bool {
	if origin != OriginAutomation {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(modelName))
	id := strings.ToLower(strings.TrimSpace(toolID))
	tgt := strings.ToLower(strings.TrimSpace(target))

	switch name {
	case "automation_create", "automation_change":
		return true
	}
	switch tgt {
	case "nori.automations/create", "nori.automations/act":
		return true
	}
	switch id {
	case "nori/assistant/automation-create", "nori/assistant/automation-change":
		return true
	}
	if isPermissionRuleWriteTool(id, name, tgt) {
		return true
	}
	return false
}

func isPermissionRuleWriteTool(id, name, target string) bool {
	isPerm := strings.Contains(name, "permission") ||
		strings.HasPrefix(target, "nori.permissions/") ||
		strings.HasPrefix(id, "nori/permissions/") ||
		strings.HasPrefix(id, "nori/assistant/permission")
	if !isPerm {
		return false
	}
	// Allow explicit read/list permission inspection tools; block all writes/changes/deletes/revokes.
	for _, readSuffix := range []string{"_list", "_get", "_read", "/list", "/get", "/read", "-list", "-get", "-read"} {
		if strings.HasSuffix(name, readSuffix) || strings.HasSuffix(target, readSuffix) || strings.HasSuffix(id, readSuffix) {
			return false
		}
	}
	return true
}

// FilterDefinitionsForOrigin strips tools that are forbidden for the given run
// origin (e.g. automation_create, automation_change, and permission-rule write
// tools for automation-origin runs).
func FilterDefinitionsForOrigin(origin string, defs []extensions.Definition) []extensions.Definition {
	if origin != OriginAutomation || len(defs) == 0 {
		return defs
	}
	out := make([]extensions.Definition, 0, len(defs))
	for _, d := range defs {
		if IsForbiddenForOrigin(origin, d.Ref.ID, d.ModelName, d.Binding.Target) {
			continue
		}
		if d.Policy.Effect != extensions.EffectRead &&
			(strings.Contains(strings.ToLower(d.ModelName), "permission") ||
				strings.HasPrefix(strings.ToLower(d.Binding.Target), "nori.permissions/")) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (r *Registry) Lookup(binding extensions.Binding) (Adapter, bool) {
	if r == nil {
		return nil, false
	}
	prefix, _, _ := strings.Cut(binding.Target, "/")
	if adapter, ok := r.adapters[key(binding.Adapter, prefix)]; ok {
		return adapter, true
	}
	adapter, ok := r.adapters[key(binding.Adapter, "")]
	return adapter, ok
}

// LookupForOrigin resolves an adapter while enforcing origin-based tool limits.
// It returns ErrToolForbiddenForOrigin if the tool is stripped for origin.
func (r *Registry) LookupForOrigin(binding extensions.Binding, origin, toolID, modelName string) (Adapter, error) {
	if IsForbiddenForOrigin(origin, toolID, modelName, binding.Target) {
		return nil, fmt.Errorf("%w: %s", ErrToolForbiddenForOrigin, binding.Target)
	}
	adapter, ok := r.Lookup(binding)
	if !ok {
		return nil, nil
	}
	return adapter, nil
}

var ErrInvalidArguments = errors.New("invalid arguments")

type TemporaryError struct {
	After time.Duration
	Err   error
}

func (e *TemporaryError) Error() string { return e.Err.Error() }
func (e *TemporaryError) Unwrap() error { return e.Err }

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

func String(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return strings.TrimSpace(value)
}

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

type SignInStart struct {
	IntegrationID, ConnectionID, Label string
	AttemptID, AuthorizeURL            string
	ExpiresAt                          time.Time
}

type SignInChecker interface {
	SignInNeeded(ctx context.Context, call Call, returnTo string) (*SignInStart, error)
}

type TakeoverRequest struct {
	Prompt     string
	URL        string
	Reason     string
	ComputerID string
}

type TakeoverRequester interface {
	TakeoverNeeded(ctx context.Context, call Call) (*TakeoverRequest, error)
}
