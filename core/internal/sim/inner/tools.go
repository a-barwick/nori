package inner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/sim/env"
	"nori/internal/state"
	"nori/internal/users"
)

const (
	// ExtensionID is the synthetic extension identifier for simulated tools.
	ExtensionID = "sim/ext"

	// ToolIDRead, ToolIDWrite, and ToolIDSend are the namespaced tool definition IDs.
	ToolIDRead  = "sim/tools/read"
	ToolIDWrite = "sim/tools/write"
	ToolIDSend  = "sim/tools/send"

	// ToolNameRead, ToolNameWrite, and ToolNameSend are the model-facing tool names.
	ToolNameRead  = "sim_read"
	ToolNameWrite = "sim_write"
	ToolNameSend  = "sim_send"
)

var (
	// ErrInjectedToolTemp is returned inside *invoke.TemporaryError when a retryable tool fault triggers.
	ErrInjectedToolTemp = errors.New("sim/inner: temporary tool refusal (429/503)")
	// ErrInjectedToolFail is returned when a clean non-side-effecting tool failure triggers.
	ErrInjectedToolFail = errors.New("sim/inner: injected tool failure before side effect")
	// ErrInjectedToolUncertain is returned when a simulated crash/drop occurs after applying a write.
	ErrInjectedToolUncertain = errors.New("sim/inner: connection dropped after external write")
)

// Sealed tool definitions registered in the simulator's extension source.
var (
	SimReadDef  = mustSeal(buildSimReadDef())
	SimWriteDef = mustSeal(buildSimWriteDef())
	SimSendDef  = mustSeal(buildSimSendDef())
)

func mustSeal(d extensions.Definition) extensions.Definition {
	sealed, err := extensions.Seal(d)
	if err != nil {
		panic(fmt.Sprintf("sim/inner: seal %s: %v", d.Ref.ID, err))
	}
	return sealed
}

func buildSimReadDef() extensions.Definition {
	return extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: ToolIDRead, Version: "1"},
		Extension:       extensions.DefinitionRef{ID: ExtensionID, Version: "1"},
		ModelName:       ToolNameRead,
		Title:           "Sim Read",
		Description:     "Read a key from the simulated external provider without making changes.",
		Capability:      "sim",
		CapabilityTitle: "Simulated tools",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"key": {"type": "string", "minLength": 1}
			},
			"required": ["key"],
			"additionalProperties": false
		}`),
		Binding: extensions.Binding{
			Adapter: extensions.AdapterNative,
			Target:  "sim/read",
			Version: "1",
		},
		Policy: extensions.Policy{
			Effect:    extensions.EffectRead,
			Retry:     "safe",
			TimeoutMS: 30_000,
			Location:  extensions.LocationRuntime,
		},
		Exposure: extensions.ExposureDirect,
	}
}

func buildSimWriteDef() extensions.Definition {
	return extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: ToolIDWrite, Version: "1"},
		Extension:       extensions.DefinitionRef{ID: ExtensionID, Version: "1"},
		ModelName:       ToolNameWrite,
		Title:           "Sim Write",
		Description:     "Write a value to a target in the simulated external provider (requires approval or allow rule).",
		Capability:      "sim",
		CapabilityTitle: "Simulated tools",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"target": {"type": "string", "minLength": 1},
				"value": {"type": "string", "minLength": 1},
				"resource_id": {"type": "string"}
			},
			"required": ["target", "value"],
			"additionalProperties": false
		}`),
		Binding: extensions.Binding{
			Adapter: extensions.AdapterNative,
			Target:  "sim/write",
			Version: "1",
		},
		Policy: extensions.Policy{
			// EffectUnknown guarantees unruled calls open a real approval interaction card
			// bound to (run_id, revision, action_digest) even for direct user requests.
			Effect:    extensions.EffectUnknown,
			Retry:     "reconcile_only",
			TimeoutMS: 30_000,
			Location:  extensions.LocationRuntime,
		},
		Exposure: extensions.ExposureDirect,
	}
}

func buildSimSendDef() extensions.Definition {
	return extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: ToolIDSend, Version: "1"},
		Extension:       extensions.DefinitionRef{ID: ExtensionID, Version: "1"},
		ModelName:       ToolNameSend,
		Title:           "Sim Send",
		Description:     "Send a message to a recipient via the simulated external provider.",
		Capability:      "sim",
		CapabilityTitle: "Simulated tools",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"to": {"type": "string", "minLength": 1},
				"body": {"type": "string", "minLength": 1}
			},
			"required": ["to", "body"],
			"additionalProperties": false
		}`),
		Binding: extensions.Binding{
			Adapter: extensions.AdapterNative,
			Target:  "sim/send",
			Version: "1",
		},
		Policy: extensions.Policy{
			Effect:    extensions.EffectSensitiveSend,
			Retry:     "reconcile_only",
			TimeoutMS: 30_000,
			Location:  extensions.LocationRuntime,
		},
		Exposure: extensions.ExposureDirect,
	}
}

// ToolDefinitions returns the sealed simulated external tool definitions in canonical order.
func ToolDefinitions() []extensions.Definition {
	return []extensions.Definition{SimReadDef, SimWriteDef, SimSendDef}
}

// ExtensionSource supplies the simulated sealed tool definitions to agent.Service.
type ExtensionSource struct {
	Definitions []extensions.Definition
}

var _ extensions.Source = (*ExtensionSource)(nil)

// NewExtensionSource creates an ExtensionSource offering sim_read, sim_write, and sim_send.
func NewExtensionSource() *ExtensionSource {
	return &ExtensionSource{Definitions: ToolDefinitions()}
}

// Inventory implements extensions.Source.
func (s *ExtensionSource) Inventory(_ context.Context) (extensions.Inventory, error) {
	defs := s.Definitions
	if len(defs) == 0 {
		defs = ToolDefinitions()
	}
	return extensions.Inventory{
		Definitions: slices.Clone(defs),
		Capabilities: []extensions.CapabilityState{
			{
				Capability:   "sim",
				Title:        "Simulated tools",
				Account:      "sim-provider",
				Availability: "Available",
				Ready:        true,
			},
		},
	}, nil
}

// AdapterConfig configures deterministic fault injection probabilities in [0.0, 1.0] for FakeAdapter.
type AdapterConfig struct {
	// TempErrProb returns a retryable *invoke.TemporaryError (safe reads retry via externalSleep).
	TempErrProb float64
	// FailProb returns a clean failed outcome with ClearedDidNotRun (no side effect).
	FailProb float64
	// UncertainProb applies the external side effect on writes/sends and then drops the response
	// (returning outcome="uncertain") so Reconcile is exercised.
	UncertainProb float64
	// ReconcileMissProb causes Reconcile to return (_, false) so the operation remains uncertain
	// and opens an uncertain-action question + unresolved write block.
	ReconcileMissProb float64
}

// DefaultAdapterConfig returns a moderate adversarial tool fault profile.
func DefaultAdapterConfig() AdapterConfig {
	return AdapterConfig{
		TempErrProb:       0.08,
		FailProb:          0.05,
		UncertainProb:     0.12,
		ReconcileMissProb: 0.30,
	}
}

// ExecutedCall is one immutable entry in FakeAdapter's append-only external execution ledger.
type ExecutedCall struct {
	UserID            string
	RunID             string
	Revision          int64
	OperationID       string
	ToolName          string
	ToolID            string
	Effect            string
	ActionDigest      string
	ActionKey         string
	Args              map[string]any
	Recipients        []string
	Resources         []string
	Outcome           string
	SideEffectApplied bool
	At                time.Time
}

type providerOpKey struct {
	runID       string
	operationID string
}

type providerOpState struct {
	toolName     string
	providerRef  state.ProviderRef
	content      map[string]any
	appliedAt    time.Time
	actionDigest string
}

// ForcedOutcome overrides the next Execute/Reconcile decision in targeted tests.
type ForcedOutcome struct {
	// Outcome may be "confirmed", "failed", "temporary_error", or "uncertain".
	Outcome string
	// ReconcileMiss, when true, forces the next Reconcile call to return (_, false).
	ReconcileMiss bool
}

// FakeAdapter is a deterministic external tool adapter implementing both invoke.Adapter
// and invoke.Reconciler with an append-only execution ledger for invariant checking.
type FakeAdapter struct {
	mu            sync.Mutex
	clock         *env.Clock
	rng           *rand.Rand
	cfg           AdapterConfig
	faultsEnabled bool

	executed     []ExecutedCall
	providerOps  map[providerOpKey]*providerOpState
	kv           map[string]string
	sentMessages []string
	nextRefSeq   int64
	forcedQueue  []ForcedOutcome
	reconcileSeq int64
}

var (
	_ invoke.Adapter    = (*FakeAdapter)(nil)
	_ invoke.Reconciler = (*FakeAdapter)(nil)
)

// NewFakeAdapter constructs a deterministic FakeAdapter.
func NewFakeAdapter(clock *env.Clock, cfg AdapterConfig, rng *rand.Rand) *FakeAdapter {
	if clock == nil {
		clock = env.NewClock(time.Time{})
	}
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 1))
	}
	return &FakeAdapter{
		clock:         clock,
		rng:           rng,
		cfg:           cfg,
		faultsEnabled: true,
		providerOps:   make(map[providerOpKey]*providerOpState),
		kv:            make(map[string]string),
	}
}

// Register binds this adapter into reg for all "sim/*" native bindings.
func (a *FakeAdapter) Register(reg *invoke.Registry) {
	reg.Register(extensions.AdapterNative, "sim", a)
}

// SetFaultsEnabled toggles fault injection without changing probabilities.
func (a *FakeAdapter) SetFaultsEnabled(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.faultsEnabled = enabled
}

// FaultsEnabled reports whether fault injection is active.
func (a *FakeAdapter) FaultsEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.faultsEnabled
}

// SetConfig updates the fault probabilities.
func (a *FakeAdapter) SetConfig(cfg AdapterConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = cfg
}

// QueueForcedOutcome queues a deterministic override consumed by the next Execute call.
func (a *FakeAdapter) QueueForcedOutcome(forced ...ForcedOutcome) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forcedQueue = append(a.forcedQueue, forced...)
}

// Executed returns a deep-copied snapshot of the append-only external execution ledger.
func (a *FakeAdapter) Executed() []ExecutedCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ExecutedCall, len(a.executed))
	for i, c := range a.executed {
		c.Args = cloneMap(c.Args)
		c.Recipients = slices.Clone(c.Recipients)
		c.Resources = slices.Clone(c.Resources)
		out[i] = c
	}
	return out
}

// ReconcileCount returns how many times Reconcile was invoked.
func (a *FakeAdapter) ReconcileCount() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reconcileSeq
}

// Prepare implements invoke.Adapter without performing any external writes.
func (a *FakeAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	switch call.Exec.Binding.Target {
	case "sim/read":
		key := invoke.String(call.Args, "key")
		if key == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: key is required", invoke.ErrInvalidArguments)
		}
		spec := map[string]string{"key": key}
		return invoke.Prepared{
			Summary: fmt.Sprintf("Read key %q", key),
			Effect:  extensions.EffectRead,
			Resources: []state.ProviderRef{
				{Kind: "sim_kv", ProviderID: key},
			},
			Spec: spec,
			Keys: map[string]string{"action_key": invoke.ActionKey(call, spec)},
		}, nil

	case "sim/write":
		target := invoke.String(call.Args, "target")
		value := invoke.String(call.Args, "value")
		if target == "" || value == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: target and value are required", invoke.ErrInvalidArguments)
		}
		resID := invoke.String(call.Args, "resource_id")
		if resID == "" {
			resID = target
		}
		spec := map[string]string{"target": target, "value": value, "resource_id": resID}
		return invoke.Prepared{
			Summary: fmt.Sprintf("Write %q = %q", target, value),
			Effect:  extensions.EffectUnknown,
			Resources: []state.ProviderRef{
				{Kind: "sim_kv", ProviderID: resID},
			},
			Spec:            spec,
			MaterialEffects: []string{fmt.Sprintf("Sets %s to %s", target, value)},
			Keys:            map[string]string{"action_key": invoke.ActionKey(call, spec)},
		}, nil

	case "sim/send":
		to := invoke.String(call.Args, "to")
		body := invoke.String(call.Args, "body")
		if to == "" || body == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: to and body are required", invoke.ErrInvalidArguments)
		}
		spec := map[string]string{"to": to, "body": body}
		return invoke.Prepared{
			Summary:         fmt.Sprintf("Send message to %s", to),
			Effect:          extensions.EffectSensitiveSend,
			Recipients:      []string{to},
			Spec:            spec,
			MaterialEffects: []string{fmt.Sprintf("Delivers message to %s", to)},
			Keys:            map[string]string{"action_key": invoke.ActionKey(call, spec)},
		}, nil

	default:
		return invoke.Prepared{}, fmt.Errorf("%w: unknown binding target %q", invoke.ErrInvalidArguments, call.Exec.Binding.Target)
	}
}

// Execute implements invoke.Adapter and records every execution attempt in the append-only ledger.
func (a *FakeAdapter) Execute(ctx context.Context, call invoke.Call, prepared invoke.Prepared) invoke.Result {
	a.mu.Lock()
	defer a.mu.Unlock()

	userID := simUserID(ctx)
	if userID == "" {
		userID = users.ID(ctx)
	}
	connectionID, epoch := "", int64(0)
	if call.Grant != nil {
		connectionID = call.Grant.Connection.ID
		epoch = call.Grant.Connection.AuthorizationEpoch
	}
	digest := invoke.Digest(call, connectionID, epoch, prepared)
	actionKey := ""
	if prepared.Keys != nil {
		actionKey = prepared.Keys["action_key"]
	}
	toolName := toolNameForTarget(call.Exec.Binding.Target)
	resources := make([]string, 0, len(prepared.Resources))
	for _, r := range prepared.Resources {
		resources = append(resources, r.ProviderID)
	}
	now := a.clock.Now()

	outcomeChoice := a.chooseOutcomeLocked(prepared.Effect)
	record := ExecutedCall{
		UserID:       userID,
		RunID:        call.RunID,
		Revision:     call.Revision,
		OperationID:  call.OperationID,
		ToolName:     toolName,
		ToolID:       call.Exec.Tool.ID,
		Effect:       string(prepared.Effect),
		ActionDigest: digest,
		ActionKey:    actionKey,
		Args:         cloneMap(call.Args),
		Recipients:   slices.Clone(prepared.Recipients),
		Resources:    resources,
		At:           now,
	}

	switch outcomeChoice {
	case "temporary_error":
		record.Outcome = "temporary_error"
		a.executed = append(a.executed, record)
		return invoke.Result{
			Outcome: "failed",
			Err:     &invoke.TemporaryError{After: 500 * time.Millisecond, Err: ErrInjectedToolTemp},
			Cleared: state.ClearedDidNotRun,
		}

	case "failed":
		record.Outcome = "failed"
		a.executed = append(a.executed, record)
		return invoke.Result{
			Outcome: "failed",
			Err:     ErrInjectedToolFail,
			Cleared: state.ClearedDidNotRun,
		}
	}

	// Execute the operation against the fake external provider state.
	if prepared.Effect == extensions.EffectRead {
		key := prepared.Spec["key"]
		val, found := a.kv[userID+":"+key]
		record.Outcome = "confirmed"
		a.executed = append(a.executed, record)
		return invoke.Result{
			Outcome: "confirmed",
			Content: map[string]any{
				"key":   key,
				"value": val,
				"found": found,
			},
		}
	}

	// Non-read side effect (sim_write or sim_send).
	a.nextRefSeq++
	ref := state.ProviderRef{
		Kind:       toolName,
		ProviderID: fmt.Sprintf("prov-%d", a.nextRefSeq),
	}
	var content map[string]any
	switch call.Exec.Binding.Target {
	case "sim/write":
		target := prepared.Spec["target"]
		value := prepared.Spec["value"]
		a.kv[userID+":"+target] = value
		content = map[string]any{
			"target":      target,
			"value":       value,
			"provider_id": ref.ProviderID,
		}
	case "sim/send":
		to := prepared.Spec["to"]
		body := prepared.Spec["body"]
		a.sentMessages = append(a.sentMessages, fmt.Sprintf("%s->%s:%s", userID, to, body))
		content = map[string]any{
			"to":          to,
			"body":        body,
			"provider_id": ref.ProviderID,
		}
	}

	opKey := providerOpKey{runID: call.RunID, operationID: call.OperationID}
	a.providerOps[opKey] = &providerOpState{
		toolName:     toolName,
		providerRef:  ref,
		content:      cloneMap(content),
		appliedAt:    now,
		actionDigest: digest,
	}
	record.SideEffectApplied = true

	if outcomeChoice == "uncertain" {
		record.Outcome = "uncertain"
		a.executed = append(a.executed, record)
		return invoke.Result{
			Outcome: "uncertain",
			Err:     ErrInjectedToolUncertain,
		}
	}

	record.Outcome = "confirmed"
	a.executed = append(a.executed, record)
	return invoke.Result{
		Outcome:      "confirmed",
		Content:      content,
		ProviderRefs: []state.ProviderRef{ref},
	}
}

// Reconcile implements invoke.Reconciler by checking the fake provider's state for an
// interrupted or uncertain operation without ever re-executing the write.
func (a *FakeAdapter) Reconcile(_ context.Context, call invoke.Call, prepared invoke.Prepared) (invoke.Result, bool) {
	if prepared.Effect == extensions.EffectRead {
		return invoke.Result{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reconcileSeq++

	if len(a.forcedQueue) > 0 && a.forcedQueue[0].Outcome == "" && a.forcedQueue[0].ReconcileMiss {
		a.forcedQueue = a.forcedQueue[1:]
		return invoke.Result{}, false
	}

	if a.faultsEnabled && a.cfg.ReconcileMissProb > 0 && a.rng.Float64() < a.cfg.ReconcileMissProb {
		return invoke.Result{}, false
	}

	opKey := providerOpKey{runID: call.RunID, operationID: call.OperationID}
	if op, ok := a.providerOps[opKey]; ok {
		content := cloneMap(op.content)
		content["reconciled"] = true
		return invoke.Result{
			Outcome:      "confirmed",
			Content:      content,
			ProviderRefs: []state.ProviderRef{op.providerRef},
		}, true
	}

	return invoke.Result{
		Outcome: "failed",
		Content: map[string]any{"reconciled": true, "applied": false},
		Cleared: state.ClearedDidNotRun,
		Err:     errors.New("sim/inner: provider state confirms operation never ran"),
	}, true
}

func (a *FakeAdapter) chooseOutcomeLocked(effect extensions.Effect) string {
	if len(a.forcedQueue) > 0 && a.forcedQueue[0].Outcome != "" {
		choice := a.forcedQueue[0]
		if choice.ReconcileMiss {
			// Leave the ReconcileMiss flag queued for the subsequent Reconcile call.
			a.forcedQueue[0] = ForcedOutcome{ReconcileMiss: true}
		} else {
			a.forcedQueue = a.forcedQueue[1:]
		}
		return choice.Outcome
	}
	if !a.faultsEnabled {
		return "confirmed"
	}
	roll := a.rng.Float64()
	if roll < a.cfg.TempErrProb {
		return "temporary_error"
	}
	roll -= a.cfg.TempErrProb
	if roll < a.cfg.FailProb {
		return "failed"
	}
	roll -= a.cfg.FailProb
	if effect != extensions.EffectRead && roll < a.cfg.UncertainProb {
		return "uncertain"
	}
	return "confirmed"
}

// Digest returns a canonical hex-encoded SHA-256 hash of FakeAdapter's Executed ledger.
func (a *FakeAdapter) Digest() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	h := sha256.New()
	writeHashString(h, "sim.inner.adapter.v1")
	writeHashUint64(h, uint64(len(a.executed)))
	for _, c := range a.executed {
		writeHashString(h, c.UserID)
		writeHashString(h, c.RunID)
		writeHashInt64(h, c.Revision)
		writeHashString(h, c.OperationID)
		writeHashString(h, c.ToolName)
		writeHashString(h, c.ToolID)
		writeHashString(h, c.Effect)
		writeHashString(h, c.ActionDigest)
		writeHashString(h, c.ActionKey)
		writeHashString(h, c.Outcome)
		writeHashBool(h, c.SideEffectApplied)
		writeHashInt64(h, c.At.UTC().UnixNano())
		rawArgs, _ := json.Marshal(c.Args)
		writeHashString(h, string(rawArgs))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func toolNameForTarget(target string) string {
	switch target {
	case "sim/read":
		return ToolNameRead
	case "sim/write":
		return ToolNameWrite
	case "sim/send":
		return ToolNameSend
	default:
		return target
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func writeHashUint64(h hash.Hash, v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	_, _ = h.Write(buf[:])
}

func writeHashInt64(h hash.Hash, v int64) {
	writeHashUint64(h, uint64(v))
}

func writeHashString(h hash.Hash, s string) {
	writeHashUint64(h, uint64(len(s)))
	_, _ = h.Write([]byte(s))
}

func writeHashBool(h hash.Hash, b bool) {
	if b {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
}

type simUserKey struct{}

func withSimUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, simUserKey{}, userID)
}

func simUserID(ctx context.Context) string {
	v, _ := ctx.Value(simUserKey{}).(string)
	return v
}
