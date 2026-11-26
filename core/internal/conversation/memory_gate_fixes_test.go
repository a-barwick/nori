package conversation

import (
	"testing"
	"time"

	"nori/internal/state"
)

func TestVerifyDreamOutput_UISentinelDoesNotBypassPinnedProtection(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(2 * time.Hour)
	now := t1.Add(time.Hour)

	pinnedUI := state.Memory{
		ID:            "pinned-ui-1",
		Text:          "Always use ISO-8601 UTC dates.",
		About:         "the user",
		Section:       "Preferences",
		Pinned:        true,
		Provenance:    state.ProvenanceUserExplicit,
		SourceInputID: "ui",
		CreatedAt:     t0,
		UpdatedAt:     t0,
	}
	newerUI := state.Memory{
		ID:            "other-ui-2",
		Text:          "Lives in Seattle.",
		About:         "the user",
		Section:       "Profile",
		Pinned:        false,
		Provenance:    state.ProvenanceUserExplicit,
		SourceInputID: "ui",
		CreatedAt:     t1,
		UpdatedAt:     t1,
	}

	verifiedSources := map[string]dreamSource{
		"ui": {
			Text:      newerUI.Text,
			CreatedAt: t1,
		},
		"conv-in-newer": {
			Text:      "Actually switch to local dates instead of UTC.",
			CreatedAt: t1,
		},
	}

	upserts, deletes, _ := verifyDreamOutput(
		dreamModelOutput{
			CoreUpserts: []dreamCoreUpsertCandidate{
				{ID: pinnedUI.ID, Text: "Overwrite pinned via omitted source_input_id"},
				{ID: pinnedUI.ID, Text: "Overwrite pinned via ui sentinel", SourceInputID: "ui"},
			},
			CoreDeletes: []dreamDeleteCandidate{
				{MemoryID: pinnedUI.ID, SourceInputID: "ui"},
			},
		},
		[]state.Memory{pinnedUI, newerUI},
		verifiedSources,
		now,
	)
	if len(upserts) != 0 {
		t.Fatalf("expected 0 upserts against pinned UI memory via ui sentinel, got %+v", upserts)
	}
	if len(deletes) != 0 {
		t.Fatalf("expected 0 deletes against pinned UI memory via ui sentinel, got %+v", deletes)
	}

	upserts, deletes, _ = verifyDreamOutput(
		dreamModelOutput{
			CoreUpserts: []dreamCoreUpsertCandidate{
				{ID: pinnedUI.ID, Text: "Use local dates instead of UTC.", SourceInputID: "conv-in-newer"},
			},
		},
		[]state.Memory{pinnedUI, newerUI},
		verifiedSources,
		now,
	)
	if len(upserts) != 1 || upserts[0].ID != pinnedUI.ID || !upserts[0].Pinned {
		t.Fatalf("expected conversational contradiction to update pinned memory while preserving Pinned, got upserts=%+v deletes=%+v", upserts, deletes)
	}
}

func TestVerifyFlushCoreUpserts_ProtectsNewerPinnedCoreFromOlderTurn(t *testing.T) {
	turnTime := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	pinnedEditTime := turnTime.Add(30 * time.Second)
	laterTurnTime := pinnedEditTime.Add(30 * time.Second)
	now := laterTurnTime.Add(time.Minute)

	pinned := state.Memory{
		ID:            "mem-pinned-1",
		Text:          "User manually pinned preference in UI.",
		About:         "the user",
		Section:       "Preferences",
		Pinned:        true,
		Provenance:    state.ProvenanceUserExplicit,
		SourceInputID: "ui",
		CreatedAt:     turnTime,
		UpdatedAt:     pinnedEditTime,
	}

	batchUsers := map[string]string{
		"in-older": "Old turn preference text.",
		"in-newer": "Newer turn overriding preference.",
	}
	batchUserTimes := map[string]time.Time{
		"in-older": turnTime,
		"in-newer": laterTurnTime,
	}

	got := verifyFlushCoreUpserts(
		[]flushCoreUpsertCandidate{
			{Text: "Stale overwrite", InputID: "in-older", Quote: "Old turn preference text.", Replaces: pinned.ID},
		},
		batchUsers,
		batchUserTimes,
		[]state.Memory{pinned},
		now,
	)
	if len(got) != 0 {
		t.Fatalf("expected older turn replace against newer pinned memory to be skipped, got %+v", got)
	}

	got = verifyFlushCoreUpserts(
		[]flushCoreUpsertCandidate{
			{Text: "Newer turn overriding preference.", InputID: "in-newer", Quote: "Newer turn overriding preference.", Replaces: pinned.ID},
		},
		batchUsers,
		batchUserTimes,
		[]state.Memory{pinned},
		now,
	)
	if len(got) != 1 || got[0].ID != pinned.ID || !got[0].Pinned {
		t.Fatalf("expected newer turn replace to succeed and preserve Pinned, got %+v", got)
	}
}
