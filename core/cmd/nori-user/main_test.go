package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"nori/internal/billing"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestInviteCLISubcommands(t *testing.T) {
	dir := users.NewMemoryDirectory()
	open := func(context.Context) (users.Directory, error) { return dir, nil }
	ctx := context.Background()

	var createOut bytes.Buffer
	if err := runCLI(ctx, []string{"invite", "create", "-max-uses", "3", "-domain", "example.com", "-expires", "24h", "-note", "founders"}, open, &createOut); err != nil {
		t.Fatalf("invite create: %v", err)
	}
	code := strings.TrimSpace(createOut.String())
	if !strings.HasPrefix(code, "NORI-") {
		t.Fatalf("unexpected generated code: %q", code)
	}

	var listOut bytes.Buffer
	if err := runCLI(ctx, []string{"invite", "list"}, open, &listOut); err != nil {
		t.Fatalf("invite list: %v", err)
	}
	if !strings.Contains(listOut.String(), code) || !strings.Contains(listOut.String(), "0/3") || !strings.Contains(listOut.String(), "@example.com") {
		t.Fatalf("unexpected list output:\n%s", listOut.String())
	}

	var revokeOut bytes.Buffer
	if err := runCLI(ctx, []string{"invite", "revoke", code}, open, &revokeOut); err != nil {
		t.Fatalf("invite revoke: %v", err)
	}
	if !strings.Contains(revokeOut.String(), "revoked "+code) {
		t.Fatalf("unexpected revoke output: %q", revokeOut.String())
	}

	listOut.Reset()
	if err := runCLI(ctx, []string{"invite", "list"}, open, &listOut); err != nil {
		t.Fatalf("invite list after revoke: %v", err)
	}
	if !strings.Contains(listOut.String(), "revoked") {
		t.Fatalf("expected revoked status in list output:\n%s", listOut.String())
	}
}

func TestBillingCLISubcommands(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	backend := memstore.New(memstore.WithClock(func() time.Time { return now }))
	t.Cleanup(func() { _ = backend.Close() })

	dir := records.NewSharedUsers(backend)
	alice := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", DisplayName: "Alice", TimeZone: "UTC"}
	if err := dir.Create(ctx, alice); err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	// Create a second user directly in docstore without an entitlement row (simulating mid-rollout user).
	bob := users.User{ID: "22222222-2222-4222-8222-222222222222", Email: "bob@example.com", DisplayName: "Bob", TimeZone: "UTC"}
	if _, err := docstore.New(backend).Collection("users").Doc(bob.ID).Set(ctx, bob); err != nil {
		t.Fatalf("seed bob without entitlement: %v", err)
	}

	// Record one hosted usage row for alice.
	cost := 1.25
	db := records.NewShared(backend, alice, 5.0)
	if err := db.RecordUsage(ctx, state.Usage{
		ID:            "u-1",
		Provider:      "gateway",
		Model:         "openai/gpt-5.4",
		Billing:       state.UsageBillingHosted,
		Category:      state.UsageCategoryLLM,
		CostUSD:       &cost,
		CostStatus:    "provider",
		RequestStatus: "completed",
		CreatedAt:     now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	openUsers := func(context.Context) (users.Directory, error) { return dir, nil }
	openEnv := func(context.Context) (*cliEnv, error) {
		return &cliEnv{
			Users:   dir,
			Billing: backend,
			ReadUsageCosts: func(ctx context.Context, u users.User, start, end time.Time, visit func(docstore.UsageCostRecord)) error {
				return records.NewShared(backend, u, 5.0).ReadUsageCosts(ctx, start, end, visit)
			},
			GetStorageBytes: func(ctx context.Context, u users.User) (int64, error) {
				return records.GetUserStorageBytes(ctx, backend, u)
			},
			Now: func() time.Time { return now },
		}, nil
	}

	// 1. backfill-entitlements assigns byok/active/manual to bob only, leaving alice untouched.
	var out bytes.Buffer
	if err := runCLIWithEnv(ctx, []string{"backfill-entitlements"}, openUsers, openEnv, &out); err != nil {
		t.Fatalf("backfill-entitlements: %v", err)
	}
	if !strings.Contains(out.String(), "backfilled=1") || !strings.Contains(out.String(), "already_present=1") {
		t.Fatalf("unexpected backfill output: %s", out.String())
	}
	bobEnt, err := backend.GetEntitlement(ctx, bob.ID)
	if err != nil || bobEnt.Tier != billing.TierBYOK || bobEnt.Status != billing.StatusActive || bobEnt.BillingProvider != billing.ProviderManual {
		t.Fatalf("unexpected bob entitlement: %+v, err=%v", bobEnt, err)
	}
	// Idempotent second run.
	out.Reset()
	if err := runCLIWithEnv(ctx, []string{"backfill-entitlements"}, openUsers, openEnv, &out); err != nil {
		t.Fatalf("backfill-entitlements #2: %v", err)
	}
	if !strings.Contains(out.String(), "backfilled=0") {
		t.Fatalf("expected idempotent backfill=0, got: %s", out.String())
	}

	// 2. set-tier promotes alice to power with credit override ($5 trial + $40 power period = $45 active grant).
	out.Reset()
	if err := runCLIWithEnv(ctx, []string{"set-tier", "--email", "alice@example.com", "--tier", "power", "--credit-override-usd", "40"}, openUsers, openEnv, &out); err != nil {
		t.Fatalf("set-tier: %v", err)
	}
	if !strings.Contains(out.String(), "tier=power") || !strings.Contains(out.String(), "hosted_grant=$45.00") || !strings.Contains(out.String(), "remaining=$43.75") {
		t.Fatalf("unexpected set-tier output: %s", out.String())
	}

	// 3. suspend & unsuspend alice.
	out.Reset()
	if err := runCLIWithEnv(ctx, []string{"suspend", "--user-id", alice.ID, "--reason", "abuse test"}, openUsers, openEnv, &out); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	ent, err := backend.GetEntitlement(ctx, alice.ID)
	if err != nil || !ent.IsSuspended() || ent.SuspendedReason != "abuse test" {
		t.Fatalf("expected suspended alice, got %+v err=%v", ent, err)
	}

	out.Reset()
	if err := runCLIWithEnv(ctx, []string{"unsuspend", "--email", "alice@example.com"}, openUsers, openEnv, &out); err != nil {
		t.Fatalf("unsuspend: %v", err)
	}
	ent, err = backend.GetEntitlement(ctx, alice.ID)
	if err != nil || ent.IsSuspended() {
		t.Fatalf("expected unsuspended alice, got %+v err=%v", ent, err)
	}

	// 4. usage report (fleet and per-user).
	out.Reset()
	if err := runCLIWithEnv(ctx, []string{"usage", "--days", "7"}, openUsers, openEnv, &out); err != nil {
		t.Fatalf("usage fleet: %v", err)
	}
	if !strings.Contains(out.String(), "alice@example.com") || !strings.Contains(out.String(), "bob@example.com") || !strings.Contains(out.String(), "$1.2500") {
		t.Fatalf("unexpected fleet usage output:\n%s", out.String())
	}
}
