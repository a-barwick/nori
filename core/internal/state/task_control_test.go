package state_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"nori/internal/extensions"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func TestTaskControlLeaseAndFenceRules(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	var ctrl state.TaskControl
	if err := state.CanAcquireTask(ctrl, t0); err != nil {
		t.Fatalf("fresh CanAcquireTask: %v", err)
	}

	ctrl, f1 := state.LeaseTask(ctrl, "run-1", "worker-a", t0)
	if ctrl.RunID != "run-1" || ctrl.Phase != state.TaskPhaseSync {
		t.Fatalf("unexpected leased control: %+v", ctrl)
	}
	if f1.Owner != "worker-a" || f1.Fence != 1 || !ctrl.LeaseUntil.Equal(t0.Add(state.TaskLease)) {
		t.Fatalf("unexpected fence/lease: fence=%+v until=%v", f1, ctrl.LeaseUntil)
	}

	// Cannot acquire while lease is live.
	if err := state.CanAcquireTask(ctrl, t0.Add(10*time.Second)); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("CanAcquireTask while active = %v, want ErrTaskBusy", err)
	}
	// Ownership holds before expiry, fails at exact expiry.
	if err := state.CheckTaskOwned(ctrl, f1, t0.Add(29*time.Second)); err != nil {
		t.Fatalf("CheckTaskOwned before expiry: %v", err)
	}
	if err := state.CheckTaskOwned(ctrl, f1, t0.Add(state.TaskLease)); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("CheckTaskOwned at expiry = %v, want ErrTaskBusy", err)
	}

	// Renew extends lease.
	t1 := t0.Add(10 * time.Second)
	renewed, err := state.RenewTask(ctrl, f1, t1)
	if err != nil {
		t.Fatalf("RenewTask: %v", err)
	}
	if !renewed.LeaseUntil.Equal(t1.Add(state.TaskLease)) {
		t.Fatalf("RenewTask LeaseUntil = %v, want %v", renewed.LeaseUntil, t1.Add(state.TaskLease))
	}

	// ApplyTaskControlUpdate preserves stored Owner/Fence/LeaseUntil even if caller passes stale lease metadata.
	staleNext := ctrl // older LeaseUntil from t0
	staleNext.Phase = state.TaskPhaseCalls
	staleNext.Models = 2
	staleNext.Owner = "spoofed"
	staleNext.Fence = 999
	updated, err := state.ApplyTaskControlUpdate(renewed, staleNext, f1, t1.Add(5*time.Second))
	if err != nil {
		t.Fatalf("ApplyTaskControlUpdate: %v", err)
	}
	if updated.Owner != "worker-a" || updated.Fence != 1 || !updated.LeaseUntil.Equal(renewed.LeaseUntil) {
		t.Fatalf("ApplyTaskControlUpdate clobbered lease fields: %+v", updated)
	}
	if updated.Phase != state.TaskPhaseCalls || updated.Models != 2 {
		t.Fatalf("ApplyTaskControlUpdate dropped cursor fields: %+v", updated)
	}

	// Defense-in-depth: ApplyTaskControlUpdate rejects counter/watermark regressions under the same fence.
	regressed := updated
	regressed.Models = 1
	if _, err := state.ApplyTaskControlUpdate(updated, regressed, f1, t1.Add(6*time.Second)); err == nil {
		t.Fatal("expected ApplyTaskControlUpdate to reject Models regression")
	}

	// After expiry, worker-b takes over with Fence=2 and worker-a is fenced out.
	tExp := renewed.LeaseUntil.Add(time.Second)
	if err := state.CanAcquireTask(updated, tExp); err != nil {
		t.Fatalf("CanAcquireTask after expiry: %v", err)
	}
	taken, f2 := state.LeaseTask(updated, "run-1", "worker-b", tExp)
	if f2.Owner != "worker-b" || f2.Fence != 2 {
		t.Fatalf("takeover fence = %+v, want worker-b/2", f2)
	}
	if _, err := state.ApplyTaskControlUpdate(taken, staleNext, f1, tExp.Add(time.Second)); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("stale worker-a write = %v, want ErrTaskBusy", err)
	}

	// Release clears Owner and LeaseUntil while preserving Fence and cursor.
	released, err := state.ReleaseTask(taken, f2, tExp.Add(time.Second))
	if err != nil {
		t.Fatalf("ReleaseTask: %v", err)
	}
	if released.Owner != "" || !released.LeaseUntil.IsZero() || released.Fence != 2 || released.Models != 2 {
		t.Fatalf("unexpected released control: %+v", released)
	}
}

func TestTaskControlDocstoreRoundTrip(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 12, 30, 0, 123456000, time.UTC)
	started := t0.Add(-5 * time.Second)
	orig := state.TaskControl{
		RunID:              "run-42",
		Phase:              state.TaskPhaseCalls,
		Owner:              "pod-1",
		Fence:              3,
		LeaseUntil:         t0.Add(state.TaskLease),
		Ref:                "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Revision:           2,
		Position:           5,
		Request:            "draft report",
		Subtask:            true,
		Inherited:          true,
		Models:             4,
		Tools:              7,
		Checks:             1,
		PendingModelID:     "run-42-m5-r0",
		ModelRetries:       1,
		ModelStepStartedAt: &started,
		PendingCalls: []state.TaskCallRef{
			{
				ID:    "call-1",
				Name:  "gmail_send",
				Index: 0,
				Exec: &extensions.Exec{
					Tool:               extensions.DefinitionRef{ID: "google/gmail.send", Version: "1.0.0", Digest: "sha256:abc"},
					Builtin:            false,
					Binding:            extensions.Binding{Adapter: extensions.AdapterNative, Target: "gmail.send", Version: "1"},
					ConnectionType:     "google",
					RequiredCapability: "mail.send",
					Policy: extensions.Policy{
						Effect:                 extensions.EffectSensitiveSend,
						Retry:                  "reconcile_only",
						TimeoutMS:              30000,
						Location:               extensions.LocationRuntime,
						InterruptedCostUSD:     0.02,
						InterruptedCostUnknown: true,
					},
				},
			},
		},
		CallIndex:         0,
		CallsChanged:      true,
		CallsRevised:      false,
		Question:          &state.Question{ID: "q-1", Kind: "input", Text: "proceed?", Revision: 2, Clears: []string{"op-1"}},
		QuestionPublished: true,
		PendingPublication: &state.TaskPublication{
			RunID:              "run-42",
			Revision:           2,
			Status:             state.RunCompleted,
			Detail:             "ok",
			Text:               "done",
			Ref:                "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			StopConfirmed:      true,
			CostUnknown:        true,
			Spent:              0.42,
			BypassRouting:      false,
			ExpectedIncomplete: false,
			Answer:             &state.ResultDraft{Checked: true, Evidence: []string{"op-1"}},
			CompletionCall:     &state.TaskCallRef{ID: "call-complete", Name: "complete", Index: 0},
		},
	}

	encoded, err := docstore.Encode(orig)
	if err != nil {
		t.Fatalf("docstore.Encode: %v", err)
	}
	raw, err := json.Marshal(docstore.JSONValue(encoded))
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		t.Fatalf("json.Decode: %v", err)
	}
	docstore.NormalizeNumbers(fields)

	var decoded state.TaskControl
	if err := docstore.Decode(fields, &decoded); err != nil {
		t.Fatalf("docstore.Decode: %v", err)
	}
	if !reflect.DeepEqual(decoded, orig) {
		t.Fatalf("round-trip mismatch:\ngot:  %+v\nwant: %+v", decoded, orig)
	}
}
