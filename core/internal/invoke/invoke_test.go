package invoke_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"nori/internal/extensions"
	"nori/internal/invoke"
)

type stubAdapter struct{}

func (stubAdapter) Prepare(context.Context, invoke.Call) (invoke.Prepared, error) {
	return invoke.Prepared{}, nil
}

func (stubAdapter) Execute(context.Context, invoke.Call, invoke.Prepared) invoke.Result {
	return invoke.Result{Outcome: "confirmed"}
}

func sealDef(t *testing.T, id, name, target string, effect extensions.Effect) extensions.Definition {
	t.Helper()
	retry := "safe"
	if effect != extensions.EffectRead {
		retry = "provider_key"
	}
	d, err := extensions.Seal(extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: id, Version: "1"},
		Extension:       extensions.DefinitionRef{ID: "nori/assistant", Version: "1"},
		ModelName:       name,
		Title:           name,
		Description:     name,
		Capability:      "test",
		CapabilityTitle: "Test",
		InputSchema:     json.RawMessage(`{"type":"object"}`),
		Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: target, Version: "1"},
		Policy:          extensions.Policy{Effect: effect, Retry: retry, TimeoutMS: 5000, Location: extensions.LocationRuntime},
		Exposure:        extensions.ExposureDirect,
	})
	if err != nil {
		t.Fatalf("Seal(%s): %v", id, err)
	}
	return d
}

func TestRegistryLookupForOriginAndFiltering(t *testing.T) {
	reg := invoke.NewRegistry()
	reg.Register(extensions.AdapterNative, "nori.automations", stubAdapter{})
	reg.Register(extensions.AdapterNative, "nori.permissions", stubAdapter{})

	cases := []struct {
		name      string
		origin    string
		toolID    string
		modelName string
		target    string
		wantForb  bool
	}{
		{"user_can_create_automation", invoke.OriginUser, "nori/assistant/automation-create", "automation_create", "nori.automations/create", false},
		{"automation_cannot_create_automation", invoke.OriginAutomation, "nori/assistant/automation-create", "automation_create", "nori.automations/create", true},
		{"automation_cannot_change_automation", invoke.OriginAutomation, "nori/assistant/automation-change", "automation_change", "nori.automations/act", true},
		{"automation_can_list_automations", invoke.OriginAutomation, "nori/assistant/automations-list", "automations_list", "nori.automations/list", false},
		{"automation_cannot_write_permission_rule", invoke.OriginAutomation, "nori/permissions/save", "permission_rule_create", "nori.permissions/save", true},
		{"automation_cannot_revoke_permission_rule", invoke.OriginAutomation, "nori/permissions/revoke", "permission_rule_revoke", "nori.permissions/revoke", true},
		{"automation_can_list_permission_rules", invoke.OriginAutomation, "nori/permissions/list", "permission_rules_list", "nori.permissions/list", false},
		{"user_can_write_permission_rule", invoke.OriginUser, "nori/permissions/save", "permission_rule_create", "nori.permissions/save", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter, err := reg.LookupForOrigin(extensions.Binding{Adapter: extensions.AdapterNative, Target: tc.target}, tc.origin, tc.toolID, tc.modelName)
			if tc.wantForb {
				if !errors.Is(err, invoke.ErrToolForbiddenForOrigin) || adapter != nil {
					t.Fatalf("LookupForOrigin = (%v, %v), want (nil, ErrToolForbiddenForOrigin)", adapter, err)
				}
				return
			}
			if err != nil || adapter == nil {
				t.Fatalf("LookupForOrigin = (%v, %v), want non-nil adapter and nil err", adapter, err)
			}
		})
	}

	defs := []extensions.Definition{
		sealDef(t, "nori/assistant/automation-create", "automation_create", "nori.automations/create", extensions.EffectReversibleWrite),
		sealDef(t, "nori/assistant/automation-change", "automation_change", "nori.automations/act", extensions.EffectReversibleWrite),
		sealDef(t, "nori/assistant/automations-list", "automations_list", "nori.automations/list", extensions.EffectRead),
		sealDef(t, "nori/permissions/save", "permission_rule_create", "nori.permissions/save", extensions.EffectReversibleWrite),
		sealDef(t, "nori/permissions/list", "permission_rules_list", "nori.permissions/list", extensions.EffectRead),
	}

	userDefs := invoke.FilterDefinitionsForOrigin(invoke.OriginUser, defs)
	if len(userDefs) != len(defs) {
		t.Fatalf("user origin filtered len = %d, want %d", len(userDefs), len(defs))
	}

	autoDefs := invoke.FilterDefinitionsForOrigin(invoke.OriginAutomation, defs)
	if len(autoDefs) != 2 {
		t.Fatalf("automation origin filtered len = %d, want 2 (automations_list + permission_rules_list); got %+v", len(autoDefs), autoDefs)
	}
	for _, d := range autoDefs {
		if d.ModelName == "automation_create" || d.ModelName == "automation_change" || d.ModelName == "permission_rule_create" {
			t.Fatalf("forbidden tool %q leaked into automation definitions", d.ModelName)
		}
	}
}
