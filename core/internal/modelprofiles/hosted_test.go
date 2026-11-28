package modelprofiles_test

import (
	"context"
	"errors"
	"testing"

	"nori/internal/billing"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestHostedProfilesAndTrialAccountRestrictions(t *testing.T) {
	ctx := context.Background()
	hosted := modelprofiles.HostedProfiles()
	if len(hosted) != 9 {
		t.Fatalf("HostedProfiles len = %d, want 9", len(hosted))
	}
	if name, provider, loc := modelprofiles.Describe("hosted-neon", modelprofiles.BackendHosted, "google/gemini-4-neon"); name != "Gemini 4 Neon" || provider != "Nori Hosted" || loc != "cloud" {
		t.Fatalf("Describe hosted = (%q, %q, %q)", name, provider, loc)
	}

	account := &modelprofiles.Account{
		Keys:          &modelkeys.Service{HostedAvailable: true},
		Store:         records.NewShared(memstore.New(), users.User{ID: "owner", Email: "trial@example.test", TimeZone: "UTC"}, 1),
		HostedEnabled: true,
		AllowBYOK:     func(context.Context) (bool, error) { return false, nil },
		AllowHomeAI:   func(context.Context) (bool, error) { return false, nil },
	}

	view, err := account.View(ctx)
	if err != nil {
		t.Fatalf("View failed: %v", err)
	}
	if view.Selection.ChatProfileID != "hosted-neon" || view.Selection.TaskProfileID != "hosted-hydrogen" {
		t.Fatalf("expected hosted default selection, got %+v", view.Selection)
	}
	if len(view.ModelProfiles.Profiles) != 9 {
		t.Fatalf("expected 9 hosted profiles in view, got %d", len(view.ModelProfiles.Profiles))
	}

	// Trial user can switch between hosted models.
	updated, err := account.Save(ctx, state.ModelSelection{
		ChatProfileID: "hosted-opus", ChatReasoning: "medium",
		TaskProfileID: "hosted-sol", TaskReasoning: "low",
	}, view.Revision, nil)
	if err != nil {
		t.Fatalf("Save hosted selection failed: %v", err)
	}
	p, effort, key, err := account.Resolve(ctx, modelprofiles.LaneChat, nil)
	if err != nil {
		t.Fatalf("Resolve hosted chat failed: %v", err)
	}
	if p.Backend != modelprofiles.BackendHosted || p.Model != "anthropic/claude-opus-5.5" || effort != "medium" || key != "" {
		t.Fatalf("unexpected resolved hosted profile: %+v effort=%q key=%q", p, effort, key)
	}

	// Trial user is blocked from saving BYOK or Home AI selections.
	if _, err := account.Save(ctx, state.ModelSelection{
		ChatProfileID: "openai-sol", ChatReasoning: "low",
	}, updated.Revision, nil); !errors.Is(err, billing.ErrBYOKNotAllowed) {
		t.Fatalf("expected ErrBYOKNotAllowed for BYOK on trial, got %v", err)
	}
	if _, err := account.Save(ctx, state.ModelSelection{
		ChatProfileID: "home-device1",
	}, updated.Revision, nil); !errors.Is(err, billing.ErrBYOKNotAllowed) {
		t.Fatalf("expected ErrBYOKNotAllowed for Home AI on trial, got %v", err)
	}

	// Trial user cannot resolve a pinned BYOK selection either.
	pinnedBYOK := modelprofiles.PinAccountSelection(&state.ModelSelection{ChatProfileID: "openai-sol"})
	if _, _, _, err := account.Resolve(ctx, modelprofiles.LaneChat, pinnedBYOK); !errors.Is(err, billing.ErrBYOKNotAllowed) {
		t.Fatalf("expected ErrBYOKNotAllowed on Resolve BYOK, got %v", err)
	}
}
