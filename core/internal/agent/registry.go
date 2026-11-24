package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"nori/internal/extensions"
	"nori/internal/transcript"
)

// builtinVersion versions the engine's own tool definitions. Bump it when a built-in's schema, binding or policy
// changes so pinned calls keep identifying what they ran against.
const builtinVersion = "1"

var builtinCapabilityIDs = map[string]string{
	searchCapability: "web_search", attachmentCapability: "attachments",
	resultCapability: "results", memoryCapability: "memory",
}

// BuiltinDefinitions are the engine's tools as registry definitions, in the stable order models see them.
var BuiltinDefinitions = func() []extensions.Definition {
	definitions := make([]extensions.Definition, 0, len(taskToolDefinitions))
	for _, tool := range taskToolDefinitions {
		d, err := extensions.Seal(builtinDefinition(tool))
		if err != nil {
			panic(err)
		}
		definitions = append(definitions, d)
	}
	return definitions
}()

var builtinsByName = func() map[string]extensions.Definition {
	byName := map[string]extensions.Definition{}
	for _, d := range BuiltinDefinitions {
		byName[d.ModelName] = d
	}
	return byName
}()

func builtinDefinition(tool taskToolDefinition) extensions.Definition {
	schema, _ := json.Marshal(jsonSchema(tool.Parameters))
	capability, title := builtinCapabilityIDs[tool.capability], tool.capability
	if tool.capability == "" {
		capability, title = "control", "Task control"
	}
	location, adapter := extensions.LocationRuntime, extensions.AdapterNative
	retry, timeout := "reconcile_only", 45_000
	if tool.policy.Effect == "read" || tool.policy.Effect == "control" {
		retry = "safe"
	}
	if tool.policy.Runtime {
		timeout = 240_000
	}
	exposure := extensions.ExposureDirect
	if tool.capability == attachmentCapability {
		exposure = extensions.ExposureDiscoverable
	}
	return extensions.Definition{
		Ref:       extensions.DefinitionRef{ID: extensions.BuiltinExtension + "/" + strings.ReplaceAll(tool.Name, "_", "-"), Version: builtinVersion},
		Extension: extensions.DefinitionRef{ID: extensions.BuiltinExtension, Version: builtinVersion},
		ModelName: tool.Name, Title: tool.Name, Description: tool.Description,
		Capability: capability, CapabilityTitle: title,
		InputSchema: schema,
		Binding:     extensions.Binding{Adapter: adapter, Target: tool.Name, Version: builtinVersion},
		Policy: extensions.Policy{
			Effect: extensions.Effect(tool.policy.Effect), Retry: retry, TimeoutMS: timeout, Location: location,
			InterruptedCostUSD: tool.policy.InterruptedCostUSD, InterruptedCostUnknown: tool.policy.InterruptedCostUnknown,
		},
		Exposure: exposure,
	}
}

// jsonSchema writes the engine's restricted schema as JSON Schema in the strict form the model already receives.
func jsonSchema(schema *transcript.Schema) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
	}
	result := map[string]any{"type": string(schema.Type)}
	if schema.Description != "" {
		result["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		result["enum"] = schema.Enum
	}
	switch schema.Type {
	case transcript.TypeObject:
		properties := map[string]any{}
		for name, child := range schema.Properties {
			properties[name] = jsonSchema(child)
		}
		required := schema.Required
		if required == nil {
			required = []string{}
		}
		result["properties"], result["required"], result["additionalProperties"] = properties, required, false
	case transcript.TypeArray:
		result["items"] = jsonSchema(schema.Items)
	}
	return result
}

// PolicyFor returns the policy a call runs under: its pinned execution metadata when the call carries one, and the
// name-based engine policy for calls recorded before pinning existed.
func PolicyFor(name string, exec *extensions.Exec) ToolPolicy {
	if exec == nil {
		return TaskToolPolicy(name)
	}
	return ToolPolicy{
		Name: name, Effect: string(exec.Policy.Effect), Runtime: exec.Policy.Location == extensions.LocationRuntime,
		InterruptedCostUSD: exec.Policy.InterruptedCostUSD, InterruptedCostUnknown: exec.Policy.InterruptedCostUnknown,
	}
}

// Tools returns the definitions this task can choose from now: built-ins first, then the user's current inventory.
// A failing source leaves the built-ins usable and is logged; it never blocks the engine's own tools.
func (s *Service) Tools(ctx context.Context) (*extensions.Snapshot, []extensions.CapabilityState) {
	if s.Extensions == nil {
		return extensions.NewSnapshot(BuiltinDefinitions), nil
	}
	inventory, err := s.Extensions.Inventory(ctx)
	if err != nil {
		slog.WarnContext(ctx, "tool inventory unavailable; offering engine tools only", "err", err)
		return extensions.NewSnapshot(BuiltinDefinitions), nil
	}
	snapshot := extensions.NewSnapshot(BuiltinDefinitions, inventory.Definitions)
	for _, rejected := range snapshot.Rejected {
		slog.WarnContext(ctx, "tool definition not offered", "reason", rejected)
	}
	return snapshot, inventory.Capabilities
}

// modelTool turns a definition into the model-facing declaration. Built-ins keep their strict restricted schema;
// other definitions send their projected canonical schema.
func modelTool(d extensions.Definition) (transcript.Tool, bool) {
	if d.Builtin() {
		for _, tool := range taskToolDefinitions {
			if tool.Name == d.ModelName {
				return tool.Tool, true
			}
		}
		return transcript.Tool{}, false
	}
	schema, _, err := extensions.ModelSchema(d.InputSchema)
	if err != nil {
		slog.Warn("tool definition not offered", "tool_id", d.Ref.ID, "err", err)
		return transcript.Tool{}, false
	}
	return transcript.Tool{Name: d.ModelName, Description: d.Description, Schema: schema}, true
}
