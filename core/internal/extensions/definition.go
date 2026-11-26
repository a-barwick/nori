package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Effect string

const (
	EffectRead            Effect = "read"
	EffectReversibleWrite Effect = "reversible_write"
	EffectSensitiveSend   Effect = "sensitive_send"
	EffectUnknown         Effect = "unknown"
	EffectControl         Effect = "control"
)

func (e Effect) Valid() bool {
	switch e {
	case EffectRead, EffectReversibleWrite, EffectSensitiveSend, EffectUnknown, EffectControl:
		return true
	}
	return false
}

type Exposure string

const (
	ExposureDirect       Exposure = "direct"
	ExposureDiscoverable Exposure = "discoverable"
	ExposureProgrammatic Exposure = "programmatic"
	ExposureDisabled     Exposure = "disabled"
)

type Location string

const (
	LocationRuntime Location = "runtime"
)

type Adapter string

const (
	AdapterNative Adapter = "native"
	AdapterMCP    Adapter = "mcp"
)

const BuiltinExtension = "nori/builtin"

type DefinitionRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type Binding struct {
	Adapter Adapter `json:"adapter"`
	Target  string  `json:"target"`
	Version string  `json:"version"`
}

type Policy struct {
	Effect                 Effect   `json:"effect"`
	Retry                  string   `json:"retry"`
	TimeoutMS              int      `json:"timeout_ms"`
	Location               Location `json:"location"`
	InterruptedCostUSD     float64  `json:"interrupted_cost_usd,omitempty"`
	InterruptedCostUnknown bool     `json:"interrupted_cost_unknown,omitempty"`
	Cancellation           string   `json:"cancellation,omitempty"`
}

type Definition struct {
	Ref                  DefinitionRef   `json:"ref"`
	Extension            DefinitionRef   `json:"extension"`
	ModelName            string          `json:"model_name"`
	Title                string          `json:"title"`
	Description          string          `json:"description"`
	Capability           string          `json:"capability"`
	CapabilityTitle      string          `json:"capability_title,omitempty"`
	IntegrationID        string          `json:"integration_id,omitempty"`
	InputSchema          json.RawMessage `json:"input_schema"`
	OutputSchema         json.RawMessage `json:"output_schema,omitempty"`
	ModelSchemaTransform string          `json:"model_schema_transform,omitempty"`
	Binding              Binding         `json:"binding"`
	ConnectionType       string          `json:"connection_type,omitempty"`
	RequiredCapability   string          `json:"required_capability,omitempty"`
	Policy               Policy          `json:"policy"`
	Exposure             Exposure        `json:"exposure"`
}

func (d Definition) Builtin() bool { return d.Extension.ID == BuiltinExtension }

type Exec struct {
	Tool               DefinitionRef `json:"tool"`
	Builtin            bool          `json:"builtin,omitempty"`
	Binding            Binding       `json:"binding"`
	ConnectionType     string        `json:"connection_type,omitempty"`
	RequiredCapability string        `json:"required_capability,omitempty"`
	Policy             Policy        `json:"policy"`
}

func (d Definition) Exec() Exec {
	return Exec{Tool: d.Ref, Builtin: d.Builtin(), Binding: d.Binding, ConnectionType: d.ConnectionType, RequiredCapability: d.RequiredCapability, Policy: d.Policy}
}

var modelName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var definitionID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*){1,3}$`)

var Reserved = map[string]bool{
	"complete": true, "compose": true, "request_input": true, "request_work": true, "capability_load": true, "tool_search": true,
	"operation_read": true, "task_history_read": true, "answer": true, "dispatch": true, "forget": true, "hold": true, "remember": true,
	"show_result": true, "steer": true, "stop": true,
}

func ValidModelName(name string) bool { return modelName.MatchString(name) }

func Seal(d Definition) (Definition, error) {
	if !definitionID.MatchString(d.Ref.ID) || d.Ref.Version == "" || len(d.Ref.Version) > 64 {
		return d, fmt.Errorf("tool definition %q needs a namespaced id and a version", d.Ref.ID)
	}
	if d.Extension.ID == "" || d.Extension.Version == "" {
		return d, fmt.Errorf("tool definition %s needs its extension", d.Ref.ID)
	}
	if !ValidModelName(d.ModelName) {
		return d, fmt.Errorf("tool definition %s: model name %q must match [A-Za-z0-9_-]{1,64}", d.Ref.ID, d.ModelName)
	}
	if !d.Builtin() {
		if Reserved[d.ModelName] {
			return d, fmt.Errorf("tool definition %s claims the reserved name %s", d.Ref.ID, d.ModelName)
		}
		if d.Policy.Effect == EffectControl {
			return d, fmt.Errorf("tool definition %s: only engine tools may be control operations", d.Ref.ID)
		}
	}
	if strings.TrimSpace(d.Title) == "" || len(d.Description) > 4096 || d.Capability == "" {
		return d, fmt.Errorf("tool definition %s needs a title, a capability and a description under 4 KiB", d.Ref.ID)
	}
	if !d.Policy.Effect.Valid() {
		return d, fmt.Errorf("tool definition %s has an invalid effect", d.Ref.ID)
	}
	switch d.Policy.Retry {
	case "safe", "provider_key", "reconcile_only":
	default:
		return d, fmt.Errorf("tool definition %s has an invalid retry policy", d.Ref.ID)
	}
	if d.Policy.Retry == "safe" && d.Policy.Effect != EffectRead && d.Policy.Effect != EffectControl {
		return d, fmt.Errorf("tool definition %s: only reads may declare transport retries safe", d.Ref.ID)
	}
	if d.Policy.TimeoutMS <= 0 || d.Policy.TimeoutMS > 600_000 || d.Policy.InterruptedCostUSD < 0 {
		return d, fmt.Errorf("tool definition %s has an invalid timeout or cost", d.Ref.ID)
	}
	switch d.Policy.Location {
	case LocationRuntime:
	default:
		return d, fmt.Errorf("tool definition %s has an invalid location", d.Ref.ID)
	}
	switch d.Exposure {
	case ExposureDirect, ExposureDiscoverable, ExposureProgrammatic, ExposureDisabled:
	default:
		return d, fmt.Errorf("tool definition %s has an invalid exposure", d.Ref.ID)
	}
	switch d.Binding.Adapter {
	case AdapterNative, AdapterMCP:
	default:
		return d, fmt.Errorf("tool definition %s has an invalid binding", d.Ref.ID)
	}
	if d.Binding.Target == "" || d.Binding.Version == "" {
		return d, fmt.Errorf("tool definition %s binding needs a target and version", d.Ref.ID)
	}
	if _, err := CompileSchema(d.InputSchema); err != nil {
		return d, fmt.Errorf("tool definition %s: %w", d.Ref.ID, err)
	}
	if len(d.OutputSchema) > 0 {
		if _, err := CompileSchema(d.OutputSchema); err != nil {
			return d, fmt.Errorf("tool definition %s output schema: %w", d.Ref.ID, err)
		}
	}
	digest, err := Digest(d)
	if err != nil {
		return d, err
	}
	if d.Ref.Digest != "" && d.Ref.Digest != digest {
		return d, fmt.Errorf("tool definition %s digest does not match its content", d.Ref.ID)
	}
	d.Ref.Digest = digest
	return d, nil
}

func Digest(d Definition) (string, error) {
	canonical := func(raw json.RawMessage) (any, error) {
		if len(raw) == 0 {
			return nil, nil
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("schema is not valid JSON")
		}
		return value, nil
	}
	input, err := canonical(d.InputSchema)
	if err != nil {
		return "", err
	}
	output, err := canonical(d.OutputSchema)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(map[string]any{
		"id": d.Ref.ID, "version": d.Ref.Version, "extension": d.Extension.ID + "@" + d.Extension.Version,
		"model_name": d.ModelName, "description": d.Description, "input_schema": input, "output_schema": output,
		"binding": d.Binding, "connection_type": d.ConnectionType, "required_capability": d.RequiredCapability, "policy": d.Policy,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
