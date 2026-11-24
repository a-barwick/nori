package sim_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/permissions"
	"nori/internal/sim/env"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

type permSimHarness struct {
	clock   *env.Clock
	rng     *rand.Rand
	faults  *memstore.FaultInjector
	backend *memstore.Backend
	db      *records.Database
	svc     *permissions.Service
	reqSeq  atomic.Int64
}

func newPermSimHarness(t *testing.T, seed int64, userSends, unattended string) *permSimHarness {
	t.Helper()
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	faultRNG := rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64()))
	clock := env.NewClock(env.DefaultBaseTime)
	faults := memstore.NewFaultInjector(memstore.FaultConfig{}, faultRNG)
	faults.SetEnabled(false)
	backend := memstore.New(memstore.WithFaultInjector(faults))
	idGen := env.NewIDGen(rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64())))
	u := users.User{
		ID:          idGen.Next(),
		Email:       "owner@example.com",
		DisplayName: "Owner",
		TimeZone:    "UTC",
	}
	if err := u.Validate(); err != nil {
		t.Fatalf("user validate: %v", err)
	}
	db := records.NewShared(backend, u, 1.0)
	db.Now = clock.Now
	svc := &permissions.Service{
		Store:            db,
		UnattendedWrites: unattended,
		UserSends:        userSends,
		Now:              clock.Now,
	}
	return &permSimHarness{
		clock:   clock,
		rng:     rng,
		faults:  faults,
		backend: backend,
		db:      db,
		svc:     svc,
	}
}

func (h *permSimHarness) nextReqID() string {
	return fmt.Sprintf("req_%08d", h.reqSeq.Add(1))
}

func randomizeCase(rng *rand.Rand, s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' && rng.IntN(2) == 1 {
			c = c - 'a' + 'A'
		} else if c >= 'A' && c <= 'Z' && rng.IntN(2) == 1 {
			c = c - 'A' + 'a'
		}
		b.WriteByte(c)
	}
	return b.String()
}

func TestPermissionsScopedAllowAndCaseInsensitiveDeny(t *testing.T) {
	ctx := context.Background()

	runSeed := func(seed int64) string {
		h := newPermSimHarness(t, seed, "ask", "confirm_writes")

		if _, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"mail.send"},
			ConnectionIDs: []string{"conn_1"},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{"alice@example.com"},
			Title:         "Allow Alice",
		}); err != nil {
			t.Fatalf("seed=%d SaveRule recipient allow: %v", seed, err)
		}

		if _, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"docs.edit"},
			ConnectionIDs: []string{"conn_1"},
			ResourceIDs:   []string{"doc_allowed"},
			ActionClasses: []string{"unknown"},
			Title:         "Allow doc_allowed",
		}); err != nil {
			t.Fatalf("seed=%d SaveRule resource allow: %v", seed, err)
		}

		if _, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "deny",
			ToolIDs:       []string{"mail.send"},
			ConnectionIDs: []string{permissions.Wildcard},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{"boss@example.com"},
			Title:         "Deny Boss",
		}); err != nil {
			t.Fatalf("seed=%d SaveRule recipient deny: %v", seed, err)
		}

		if _, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"mail.send"},
			ConnectionIDs: []string{"conn_wild"},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{permissions.Wildcard},
			ResourceIDs:   []string{permissions.Wildcard},
			Title:         "Allow wildcard recipients on conn_wild",
		}); err != nil {
			t.Fatalf("seed=%d SaveRule wildcard allow: %v", seed, err)
		}

		emptyRecipDec, err := h.svc.Check(ctx, permissions.Request{
			RunID:        "run_1",
			Revision:     1,
			Origin:       "user",
			ToolID:       "mail.send",
			Effect:       "sensitive_send",
			ConnectionID: "conn_1",
			Digest:       "dig_empty_recip",
		})
		if err != nil {
			t.Fatalf("seed=%d check empty recipients: %v", seed, err)
		}
		if emptyRecipDec.Result != "needs_interaction" {
			t.Fatalf("seed=%d scoped recipient allow must not match empty recipients; got %+v", seed, emptyRecipDec)
		}

		emptyResDec, err := h.svc.Check(ctx, permissions.Request{
			RunID:        "run_1",
			Revision:     1,
			Origin:       "user",
			ToolID:       "docs.edit",
			Effect:       "unknown",
			ConnectionID: "conn_1",
			Digest:       "dig_empty_res",
		})
		if err != nil {
			t.Fatalf("seed=%d check empty resources: %v", seed, err)
		}
		if emptyResDec.Result != "needs_interaction" {
			t.Fatalf("seed=%d scoped resource allow must not match empty resources; got %+v", seed, emptyResDec)
		}

		exactResDec, err := h.svc.Check(ctx, permissions.Request{
			RunID:        "run_1",
			Revision:     1,
			Origin:       "user",
			ToolID:       "docs.edit",
			Effect:       "unknown",
			ConnectionID: "conn_1",
			ResourceIDs:  []string{"doc_allowed"},
			Digest:       "dig_exact_res",
		})
		if err != nil {
			t.Fatalf("seed=%d check exact resource: %v", seed, err)
		}
		if exactResDec.Result != "allow" || exactResDec.Source != "permission_rule" {
			t.Fatalf("seed=%d expected resource allow, got %+v", seed, exactResDec)
		}

		mixedResDec, err := h.svc.Check(ctx, permissions.Request{
			RunID:        "run_1",
			Revision:     1,
			Origin:       "user",
			ToolID:       "docs.edit",
			Effect:       "unknown",
			ConnectionID: "conn_1",
			ResourceIDs:  []string{"doc_allowed", "doc_extra"},
			Digest:       "dig_mixed_res",
		})
		if err != nil {
			t.Fatalf("seed=%d check mixed resources: %v", seed, err)
		}
		if mixedResDec.Result != "needs_interaction" {
			t.Fatalf("seed=%d expected needs_interaction for partially uncovered resources, got %+v", seed, mixedResDec)
		}

		for i := range 8 {
			aliceVariant := "ALICE@Example.COM"
			if i > 0 {
				aliceVariant = randomizeCase(h.rng, "alice@example.com")
			}
			allowDec, err := h.svc.Check(ctx, permissions.Request{
				RunID:        "run_1",
				Revision:     1,
				Origin:       "user",
				ToolID:       "mail.send",
				Effect:       "sensitive_send",
				ConnectionID: "conn_1",
				Recipients:   []string{aliceVariant},
				Digest:       fmt.Sprintf("dig_alice_%d", i),
			})
			if err != nil {
				t.Fatalf("seed=%d check alice variant %q: %v", seed, aliceVariant, err)
			}
			if allowDec.Result != "allow" || allowDec.Source != "permission_rule" {
				t.Fatalf("seed=%d expected case-insensitive allow for %q, got %+v", seed, aliceVariant, allowDec)
			}

			bossVariant := "Boss@EXAMPLE.COM"
			if i > 0 {
				bossVariant = randomizeCase(h.rng, "boss@example.com")
			}
			denyDec, err := h.svc.Check(ctx, permissions.Request{
				RunID:        "run_1",
				Revision:     1,
				Origin:       "user",
				ToolID:       "mail.send",
				Effect:       "sensitive_send",
				ConnectionID: "conn_wild",
				Recipients:   []string{"alice@example.com", bossVariant},
				Digest:       fmt.Sprintf("dig_boss_%d", i),
			})
			if err != nil {
				t.Fatalf("seed=%d check boss variant %q: %v", seed, bossVariant, err)
			}
			if denyDec.Result != "deny" || len(denyDec.RuleIDs) != 1 {
				t.Fatalf("seed=%d expected case-insensitive deny for %q beating wildcard allow, got %+v", seed, bossVariant, denyDec)
			}
		}

		wildEmptyDec, err := h.svc.Check(ctx, permissions.Request{
			RunID:        "run_1",
			Revision:     1,
			Origin:       "user",
			ToolID:       "mail.send",
			Effect:       "sensitive_send",
			ConnectionID: "conn_wild",
			Digest:       "dig_wild_empty",
		})
		if err != nil {
			t.Fatalf("seed=%d check wildcard empty targets: %v", seed, err)
		}
		if wildEmptyDec.Result != "allow" || wildEmptyDec.Source != "permission_rule" {
			t.Fatalf("seed=%d expected wildcard allow with empty targets, got %+v", seed, wildEmptyDec)
		}

		return h.backend.Digest()
	}

	for _, seed := range []int64{1, 7, 42} {
		d1 := runSeed(seed)
		d2 := runSeed(seed)
		if d1 != d2 {
			t.Fatalf("seed=%d non-deterministic digest: %s vs %s", seed, d1, d2)
		}
	}
}

func TestPermissionsRuleUsagePreservedAndConcurrentLimitFallthrough(t *testing.T) {
	ctx := context.Background()

	for _, seed := range []int64{3, 19} {
		h := newPermSimHarness(t, seed, "ask", "confirm_writes")

		for _, neg := range []int{-1, -42} {
			_, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
				Effect:        "allow",
				ToolIDs:       []string{"mail.send"},
				ConnectionIDs: []string{permissions.Wildcard},
				ActionClasses: []string{"sensitive_send"},
				MaxOperations: neg,
				Title:         "Invalid negative limit",
			})
			if !errors.Is(err, state.ErrInvalidResource) {
				t.Fatalf("seed=%d SaveRule MaxOperations=%d expected ErrInvalidResource, got %v", seed, neg, err)
			}
		}

		receipt, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"mail.send"},
			ConnectionIDs: []string{"conn_limit"},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{permissions.Wildcard},
			MaxOperations: 2,
			Title:         "Limited rule v1",
		})
		if err != nil {
			t.Fatalf("seed=%d SaveRule create: %v", seed, err)
		}
		var created permissions.Rule
		if err := json.Unmarshal(receipt.Resource, &created); err != nil {
			t.Fatalf("seed=%d unmarshal rule: %v", seed, err)
		}

		dec1, err := h.svc.Check(ctx, permissions.Request{
			RunID:        "run_lim",
			Revision:     1,
			Origin:       "user",
			ToolID:       "mail.send",
			Effect:       "sensitive_send",
			ConnectionID: "conn_limit",
			Recipients:   []string{"a@example.com"},
			Digest:       "dig_lim_1",
		})
		if err != nil || dec1.Result != "allow" {
			t.Fatalf("seed=%d first check expected allow, got dec=%+v err=%v", seed, dec1, err)
		}
		afterUse1, err := h.svc.Rule(ctx, created.ID)
		if err != nil || afterUse1.Used != 1 {
			t.Fatalf("seed=%d expected Used=1, got rule=%+v err=%v", seed, afterUse1, err)
		}

		h.clock.Advance(5 * time.Second)
		if _, err := h.svc.SaveRule(ctx, created.ID, h.nextReqID(), afterUse1.Revision, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"mail.send"},
			ConnectionIDs: []string{"conn_limit"},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{permissions.Wildcard},
			MaxOperations: 2,
			Title:         "Limited rule v2",
		}); err != nil {
			t.Fatalf("seed=%d SaveRule update: %v", seed, err)
		}

		afterUpdate, err := h.svc.Rule(ctx, created.ID)
		if err != nil {
			t.Fatalf("seed=%d Rule after update: %v", seed, err)
		}
		if afterUpdate.Used != 1 {
			t.Fatalf("seed=%d SaveRule update reset Used: got %d want 1", seed, afterUpdate.Used)
		}

		dec2, err := h.svc.Check(ctx, permissions.Request{
			RunID:        "run_lim",
			Revision:     1,
			Origin:       "user",
			ToolID:       "mail.send",
			Effect:       "sensitive_send",
			ConnectionID: "conn_limit",
			Recipients:   []string{"a@example.com"},
			Digest:       "dig_lim_2",
		})
		if err != nil || dec2.Result != "allow" {
			t.Fatalf("seed=%d second check expected allow, got dec=%+v err=%v", seed, dec2, err)
		}

		dec3, err := h.svc.Check(ctx, permissions.Request{
			RunID:        "run_lim",
			Revision:     1,
			Origin:       "user",
			ToolID:       "mail.send",
			Effect:       "sensitive_send",
			ConnectionID: "conn_limit",
			Recipients:   []string{"a@example.com"},
			Digest:       "dig_lim_3",
		})
		if err != nil || dec3.Result != "needs_interaction" {
			t.Fatalf("seed=%d third check expected needs_interaction after Used==2, got dec=%+v err=%v", seed, dec3, err)
		}

		beforeRevoke, err := h.svc.Rule(ctx, created.ID)
		if err != nil {
			t.Fatalf("seed=%d Rule before revoke: %v", seed, err)
		}
		revokeReceipt, err := h.svc.RevokeRule(ctx, created.ID, h.nextReqID(), beforeRevoke.Revision)
		if err != nil {
			t.Fatalf("seed=%d RevokeRule: %v", seed, err)
		}
		var rawRevoked map[string]any
		if err := json.Unmarshal(revokeReceipt.Resource, &rawRevoked); err != nil {
			t.Fatalf("seed=%d unmarshal revoke receipt: %v", seed, err)
		}
		if _, leaked := rawRevoked["used"]; leaked {
			t.Fatalf("seed=%d RevokeRule leaked private used field: %s", seed, string(revokeReceipt.Resource))
		}
		if _, hasCreated := rawRevoked["created_at"]; !hasCreated {
			t.Fatalf("seed=%d RevokeRule missing created_at: %s", seed, string(revokeReceipt.Resource))
		}
		if _, hasUpdated := rawRevoked["updated_at"]; !hasUpdated {
			t.Fatalf("seed=%d RevokeRule missing updated_at: %s", seed, string(revokeReceipt.Resource))
		}
		var revokedRule permissions.Rule
		if err := json.Unmarshal(revokeReceipt.Resource, &revokedRule); err != nil {
			t.Fatalf("seed=%d unmarshal revoked Rule: %v", seed, err)
		}
		if revokedRule.Revision != beforeRevoke.Revision || revokedRule.CreatedAt == nil || revokedRule.UpdatedAt == nil {
			t.Fatalf("seed=%d RevokeRule metadata mismatch: got %+v want rev=%d", seed, revokedRule, beforeRevoke.Revision)
		}

		rPrimaryRec, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"mail.send"},
			ConnectionIDs: []string{"conn_race_backup"},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{permissions.Wildcard},
			MaxOperations: 1,
			Title:         "Primary cap 1",
		})
		if err != nil {
			t.Fatalf("seed=%d SaveRule primary: %v", seed, err)
		}
		rBackupRec, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"mail.send"},
			ConnectionIDs: []string{"conn_race_backup"},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{permissions.Wildcard},
			MaxOperations: 10,
			Title:         "Backup cap 10",
		})
		if err != nil {
			t.Fatalf("seed=%d SaveRule backup: %v", seed, err)
		}
		var primaryRule, backupRule permissions.Rule
		_ = json.Unmarshal(rPrimaryRec.Resource, &primaryRule)
		_ = json.Unmarshal(rBackupRec.Resource, &backupRule)

		const workers = 6
		start := make(chan struct{})
		results := make([]permissions.Decision, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				results[idx], errs[idx] = h.svc.Check(ctx, permissions.Request{
					RunID:        fmt.Sprintf("run_race_%d", idx),
					Revision:     1,
					Origin:       "user",
					ToolID:       "mail.send",
					Effect:       "sensitive_send",
					ConnectionID: "conn_race_backup",
					Recipients:   []string{"x@example.com"},
					Digest:       fmt.Sprintf("dig_race_backup_%d", idx),
				})
			}(i)
		}
		close(start)
		wg.Wait()

		primaryHits, backupHits := 0, 0
		for i := range workers {
			if errs[i] != nil {
				t.Fatalf("seed=%d worker %d unexpected error on concurrent exhaustion: %v", seed, i, errs[i])
			}
			if results[i].Result != "allow" || len(results[i].RuleIDs) != 1 {
				t.Fatalf("seed=%d worker %d expected allow with backup fallthrough, got %+v", seed, i, results[i])
			}
			switch results[i].RuleIDs[0] {
			case primaryRule.ID:
				primaryHits++
			case backupRule.ID:
				backupHits++
			default:
				t.Fatalf("seed=%d worker %d unexpected rule ID %v", seed, i, results[i].RuleIDs)
			}
		}
		if primaryHits != 1 || backupHits != workers-1 {
			t.Fatalf("seed=%d concurrent backup hits mismatch: primary=%d backup=%d", seed, primaryHits, backupHits)
		}

		rSoloRec, err := h.svc.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"mail.send"},
			ConnectionIDs: []string{"conn_race_solo"},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{permissions.Wildcard},
			MaxOperations: 1,
			Title:         "Solo cap 1",
		})
		if err != nil {
			t.Fatalf("seed=%d SaveRule solo: %v", seed, err)
		}
		var soloRule permissions.Rule
		_ = json.Unmarshal(rSoloRec.Resource, &soloRule)

		startSolo := make(chan struct{})
		soloResults := make([]permissions.Decision, workers)
		soloErrs := make([]error, workers)
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-startSolo
				soloResults[idx], soloErrs[idx] = h.svc.Check(ctx, permissions.Request{
					RunID:        fmt.Sprintf("run_solo_%d", idx),
					Revision:     1,
					Origin:       "user",
					ToolID:       "mail.send",
					Effect:       "sensitive_send",
					ConnectionID: "conn_race_solo",
					Recipients:   []string{"y@example.com"},
					Digest:       fmt.Sprintf("dig_race_solo_%d", idx),
				})
			}(i)
		}
		close(startSolo)
		wg.Wait()

		allowCount, needsInteractionCount := 0, 0
		for i := range workers {
			if soloErrs[i] != nil {
				t.Fatalf("seed=%d solo worker %d unexpected error: %v", seed, i, soloErrs[i])
			}
			switch soloResults[i].Result {
			case "allow":
				allowCount++
			case "needs_interaction":
				needsInteractionCount++
			default:
				t.Fatalf("seed=%d solo worker %d unexpected decision: %+v", seed, i, soloResults[i])
			}
		}
		if allowCount != 1 || needsInteractionCount != workers-1 {
			t.Fatalf("seed=%d solo concurrent exhaustion mismatch: allow=%d needs_interaction=%d", seed, allowCount, needsInteractionCount)
		}
		soloAfter, err := h.svc.Rule(ctx, soloRule.ID)
		if err != nil || soloAfter.Used != 1 {
			t.Fatalf("seed=%d expected solo Used=1, got %+v err=%v", seed, soloAfter, err)
		}
	}
}

func TestInteractionsAtomicDeclineAndIdempotentRetryDefaults(t *testing.T) {
	ctx := context.Background()

	for _, seed := range []int64{5, 29} {
		h := newPermSimHarness(t, seed, "ask", "confirm_writes")

		var declineCalls atomic.Int64
		var declinedIDsMu sync.Mutex
		var declinedIDs []string
		h.svc.OnDecline = func(_ context.Context, i permissions.Interaction) string {
			declineCalls.Add(1)
			declinedIDsMu.Lock()
			declinedIDs = append(declinedIDs, i.ID)
			declinedIDsMu.Unlock()
			return " Draft removed."
		}

		rev := int64(2)
		exp := h.clock.Now().Add(30 * time.Minute)
		card1, err := h.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_decline_1",
			TaskID:              "run_dec_1",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Send email to external contact",
			ActionDigest:        "dig_dec_1",
			ExpiresAt:           &exp,
		}, "")
		if err != nil {
			t.Fatalf("seed=%d Open card1: %v", seed, err)
		}

		if _, _, err := h.svc.Respond(ctx, card1.ID, h.nextReqID(), card1.Revision+5, permissions.Response{
			Kind:         "decline",
			ActionDigest: card1.ActionDigest,
		}); !errors.Is(err, state.ErrRevisionConflict) {
			t.Fatalf("seed=%d stale revision decline expected ErrRevisionConflict, got %v", seed, err)
		}
		if declineCalls.Load() != 0 {
			t.Fatalf("seed=%d OnDecline ran on stale revision rejection: calls=%d", seed, declineCalls.Load())
		}

		if _, _, err := h.svc.Respond(ctx, card1.ID, h.nextReqID(), card1.Revision, permissions.Response{
			Kind:         "decline",
			ActionDigest: "wrong_digest",
		}); !errors.Is(err, permissions.ErrInteractionStale) {
			t.Fatalf("seed=%d wrong digest decline expected ErrInteractionStale, got %v", seed, err)
		}
		if declineCalls.Load() != 0 {
			t.Fatalf("seed=%d OnDecline ran on digest mismatch: calls=%d", seed, declineCalls.Load())
		}

		declineReqID := h.nextReqID()
		rec1, ans1, err := h.svc.Respond(ctx, card1.ID, declineReqID, card1.Revision, permissions.Response{
			Kind:         "decline",
			ActionDigest: card1.ActionDigest,
		})
		if err != nil {
			t.Fatalf("seed=%d valid decline failed: %v", seed, err)
		}
		if declineCalls.Load() != 1 {
			t.Fatalf("seed=%d expected OnDecline called once, got %d", seed, declineCalls.Load())
		}
		if ans1 == nil || !strings.Contains(ans1.Text, "Draft removed.") {
			t.Fatalf("seed=%d expected decline note in answer, got %+v", seed, ans1)
		}

		recRetry, ansRetry, err := h.svc.Respond(ctx, card1.ID, declineReqID, card1.Revision, permissions.Response{
			Kind:         "decline",
			ActionDigest: card1.ActionDigest,
		})
		if err != nil {
			t.Fatalf("seed=%d idempotent decline retry failed: %v", seed, err)
		}
		if declineCalls.Load() != 1 {
			t.Fatalf("seed=%d OnDecline ran again on idempotent retry: calls=%d", seed, declineCalls.Load())
		}
		if string(recRetry.Resource) != string(rec1.Resource) {
			t.Fatalf("seed=%d idempotent decline retry receipt mismatch", seed)
		}
		if ansRetry == nil || ansRetry.CommandID != ans1.CommandID || !strings.Contains(ansRetry.Text, "Draft removed.") {
			t.Fatalf("seed=%d idempotent decline retry answer mismatch: %+v vs %+v", seed, ansRetry, ans1)
		}

		if _, _, err := h.svc.Respond(ctx, card1.ID, h.nextReqID(), card1.Revision+1, permissions.Response{
			Kind:         "decline",
			ActionDigest: card1.ActionDigest,
		}); !errors.Is(err, permissions.ErrInteractionStale) {
			t.Fatalf("seed=%d decline on resolved card expected ErrInteractionStale, got %v", seed, err)
		}
		if declineCalls.Load() != 1 {
			t.Fatalf("seed=%d OnDecline ran on already-resolved card: calls=%d", seed, declineCalls.Load())
		}

		cardRace, err := h.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_decline_race",
			TaskID:              "run_dec_race",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Concurrent decline test",
			ActionDigest:        "dig_dec_race",
			ExpiresAt:           &exp,
		}, "")
		if err != nil {
			t.Fatalf("seed=%d Open cardRace: %v", seed, err)
		}
		raceReqID := h.nextReqID()
		beforeRace := declineCalls.Load()
		startRace := make(chan struct{})
		raceErrs := make([]error, 4)
		var wg sync.WaitGroup
		for i := range 4 {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-startRace
				_, _, raceErrs[idx] = h.svc.Respond(ctx, cardRace.ID, raceReqID, cardRace.Revision, permissions.Response{
					Kind:         "decline",
					ActionDigest: cardRace.ActionDigest,
				})
			}(i)
		}
		close(startRace)
		wg.Wait()
		for i, rerr := range raceErrs {
			if rerr != nil {
				t.Fatalf("seed=%d concurrent duplicate decline worker %d failed: %v", seed, i, rerr)
			}
		}
		if got := declineCalls.Load() - beforeRace; got != 1 {
			t.Fatalf("seed=%d concurrent duplicate decline invoked OnDecline %d times, want 1", seed, got)
		}

		acctCard, err := h.svc.Open(ctx, permissions.Interaction{
			ID:      "int_acct_1",
			TaskID:  "run_acct_1",
			Kind:    "account_selection",
			Prompt:  "Choose email account",
			Payload: json.RawMessage(`{"purpose":"email_send","eligible_connection_ids":["conn_a","conn_b"]}`),
		}, "email_send")
		if err != nil {
			t.Fatalf("seed=%d Open acctCard: %v", seed, err)
		}
		acctReqID := h.nextReqID()
		_, acctAns1, err := h.svc.Respond(ctx, acctCard.ID, acctReqID, acctCard.Revision, permissions.Response{
			Kind:              "choose_account",
			ConnectionID:      "conn_b",
			RememberAsDefault: true,
		})
		if err != nil {
			t.Fatalf("seed=%d Respond choose_account: %v", seed, err)
		}
		if acctAns1 == nil || acctAns1.DefaultPurpose != "email_send" || acctAns1.DefaultConnection != "conn_b" {
			t.Fatalf("seed=%d first choose_account answer missing defaults: %+v", seed, acctAns1)
		}

		_, acctAns2, err := h.svc.Respond(ctx, acctCard.ID, acctReqID, acctCard.Revision, permissions.Response{
			Kind:              "choose_account",
			ConnectionID:      "conn_b",
			RememberAsDefault: true,
		})
		if err != nil {
			t.Fatalf("seed=%d Respond choose_account retry: %v", seed, err)
		}
		if acctAns2 == nil || acctAns2.CommandID != acctAns1.CommandID || acctAns2.Text != acctAns1.Text {
			t.Fatalf("seed=%d retry choose_account answer mismatch: %+v vs %+v", seed, acctAns2, acctAns1)
		}
		if acctAns2.DefaultPurpose != "email_send" || acctAns2.DefaultConnection != "conn_b" {
			t.Fatalf("seed=%d idempotent retry lost defaults: %+v", seed, acctAns2)
		}

		h.svc.MarkDelivered(ctx, acctCard.ID)
		_, acctAns3, err := h.svc.Respond(ctx, acctCard.ID, acctReqID, acctCard.Revision, permissions.Response{
			Kind:              "choose_account",
			ConnectionID:      "conn_b",
			RememberAsDefault: true,
		})
		if err != nil || acctAns3 != nil {
			t.Fatalf("seed=%d retry after MarkDelivered expected nil answer, got ans=%+v err=%v", seed, acctAns3, err)
		}

		shortExp := h.clock.Now().Add(10 * time.Minute)
		longExp := h.clock.Now().Add(60 * time.Minute)
		expectedExpired := []string{"int_exp_01", "int_exp_03", "int_exp_05"}
		expectedPending := []string{"int_exp_02", "int_exp_04", "int_exp_06"}
		for idx, id := range []string{"int_exp_01", "int_exp_02", "int_exp_03", "int_exp_04", "int_exp_05", "int_exp_06"} {
			expAt := longExp
			if idx%2 == 0 {
				expAt = shortExp
			}
			if _, err := h.svc.Open(ctx, permissions.Interaction{
				ID:           id,
				TaskID:       "run_exp_filter",
				Kind:         "approval",
				Prompt:       "Expirable approval " + id,
				ActionDigest: "dig_" + id,
				ExpiresAt:    &expAt,
			}, ""); err != nil {
				t.Fatalf("seed=%d Open %s: %v", seed, id, err)
			}
		}

		collectAll := func(status string, pageSize int) []string {
			var ids []string
			cursor := ""
			for pages := 0; pages < 20; pages++ {
				items, next, err := h.svc.Interactions(ctx, status, "run_exp_filter", cursor, pageSize)
				if err != nil {
					t.Fatalf("seed=%d Interactions(status=%s): %v", seed, status, err)
				}
				for _, item := range items {
					if item.Status != status {
						t.Fatalf("seed=%d Interactions(status=%s) returned item %s with status %s", seed, status, item.ID, item.Status)
					}
					ids = append(ids, item.ID)
				}
				if next == "" {
					return ids
				}
				cursor = next
			}
			t.Fatalf("seed=%d Interactions(status=%s) pagination did not terminate", seed, status)
			return nil
		}

		if got := collectAll("pending", 2); len(got) != 6 {
			t.Fatalf("seed=%d before expiry expected 6 pending, got %v", seed, got)
		}
		if got := collectAll("expired", 2); len(got) != 0 {
			t.Fatalf("seed=%d before expiry expected 0 expired, got %v", seed, got)
		}

		h.clock.Advance(30 * time.Minute)

		gotPending := collectAll("pending", 2)
		if !slices.Equal(gotPending, expectedPending) {
			t.Fatalf("seed=%d after expiry pending=%v want %v", seed, gotPending, expectedPending)
		}
		gotExpired := collectAll("expired", 2)
		if !slices.Equal(gotExpired, expectedExpired) {
			t.Fatalf("seed=%d after expiry expired=%v want %v", seed, gotExpired, expectedExpired)
		}

		beforeExpDecline := declineCalls.Load()
		if _, _, err := h.svc.Respond(ctx, expectedExpired[0], h.nextReqID(), 1, permissions.Response{
			Kind:         "decline",
			ActionDigest: "dig_" + expectedExpired[0],
		}); !errors.Is(err, permissions.ErrInteractionStale) {
			t.Fatalf("seed=%d declining expired card expected ErrInteractionStale, got %v", seed, err)
		}
		if declineCalls.Load() != beforeExpDecline {
			t.Fatalf("seed=%d OnDecline ran on expired card", seed)
		}
	}
}

func TestInteractionsUndeliveredAnswersPast200(t *testing.T) {
	ctx := context.Background()

	runScenario := func(seed int64) string {
		h := newPermSimHarness(t, seed, "ask", "confirm_writes")

		const deliveredCount = 235
		for i := 1; i <= deliveredCount; i++ {
			id := fmt.Sprintf("int_%04d", i)
			card, err := h.svc.Open(ctx, permissions.Interaction{
				ID:     id,
				TaskID: fmt.Sprintf("run_%04d", i),
				Kind:   "question",
				Prompt: "Historical question " + id,
			}, "")
			if err != nil {
				t.Fatalf("seed=%d Open %s: %v", seed, id, err)
			}
			if _, ans, err := h.svc.Respond(ctx, card.ID, h.nextReqID(), card.Revision, permissions.Response{
				Kind: "answer",
				Text: "Answered " + id,
			}); err != nil || ans == nil {
				t.Fatalf("seed=%d Respond %s: ans=%+v err=%v", seed, id, ans, err)
			}
			h.svc.MarkDelivered(ctx, card.ID)
		}

		wantIDs := []string{"int_0236", "int_0237", "int_0238"}
		wantCmdIDs := make(map[string]string, len(wantIDs))

		c236, err := h.svc.Open(ctx, permissions.Interaction{
			ID:     "int_0236",
			TaskID: "run_0236",
			Kind:   "question",
			Prompt: "Tail question",
		}, "")
		if err != nil {
			t.Fatalf("seed=%d Open int_0236: %v", seed, err)
		}
		_, a236, err := h.svc.Respond(ctx, c236.ID, h.nextReqID(), c236.Revision, permissions.Response{
			Kind: "answer",
			Text: "Tail answer",
		})
		if err != nil || a236 == nil {
			t.Fatalf("seed=%d Respond int_0236: %v", seed, err)
		}
		wantCmdIDs[c236.ID] = a236.CommandID

		c237, err := h.svc.Open(ctx, permissions.Interaction{
			ID:           "int_0237",
			TaskID:       "run_0237",
			Kind:         "approval",
			Prompt:       "Tail approval",
			ActionDigest: "dig_0237",
		}, "")
		if err != nil {
			t.Fatalf("seed=%d Open int_0237: %v", seed, err)
		}
		_, a237, err := h.svc.Respond(ctx, c237.ID, h.nextReqID(), c237.Revision, permissions.Response{
			Kind:         "approve",
			ActionDigest: "dig_0237",
		})
		if err != nil || a237 == nil {
			t.Fatalf("seed=%d Respond int_0237: %v", seed, err)
		}
		wantCmdIDs[c237.ID] = a237.CommandID

		c238, err := h.svc.Open(ctx, permissions.Interaction{
			ID:      "int_0238",
			TaskID:  "run_0238",
			Kind:    "account_selection",
			Prompt:  "Tail account selection",
			Payload: json.RawMessage(`{"purpose":"mail","eligible_connection_ids":["conn_tail"]}`),
		}, "mail")
		if err != nil {
			t.Fatalf("seed=%d Open int_0238: %v", seed, err)
		}
		_, a238, err := h.svc.Respond(ctx, c238.ID, h.nextReqID(), c238.Revision, permissions.Response{
			Kind:              "choose_account",
			ConnectionID:      "conn_tail",
			RememberAsDefault: true,
		})
		if err != nil || a238 == nil {
			t.Fatalf("seed=%d Respond int_0238: %v", seed, err)
		}
		wantCmdIDs[c238.ID] = a238.CommandID

		answers, ids, err := h.svc.UndeliveredAnswers(ctx)
		if err != nil {
			t.Fatalf("seed=%d UndeliveredAnswers: %v", seed, err)
		}
		if !slices.Equal(ids, wantIDs) {
			t.Fatalf("seed=%d UndeliveredAnswers ids=%v want %v", seed, ids, wantIDs)
		}
		if len(answers) != len(wantIDs) {
			t.Fatalf("seed=%d UndeliveredAnswers len=%d want %d", seed, len(answers), len(wantIDs))
		}
		for i, ans := range answers {
			id := ids[i]
			if ans.Recheck {
				t.Fatalf("seed=%d answer %s unexpectedly marked Recheck=true", seed, id)
			}
			if ans.CommandID != wantCmdIDs[id] {
				t.Fatalf("seed=%d answer %s CommandID=%s want %s", seed, id, ans.CommandID, wantCmdIDs[id])
			}
			if ans.Question != id {
				t.Fatalf("seed=%d answer %s Question=%s want %s", seed, id, ans.Question, id)
			}
			if id == "int_0238" && (ans.DefaultPurpose != "mail" || ans.DefaultConnection != "conn_tail") {
				t.Fatalf("seed=%d answer int_0238 missing defaults: %+v", seed, ans)
			}
		}

		h.svc.MarkDelivered(ctx, "int_0236")
		answers2, ids2, err := h.svc.UndeliveredAnswers(ctx)
		if err != nil {
			t.Fatalf("seed=%d UndeliveredAnswers after 1 delivered: %v", seed, err)
		}
		if !slices.Equal(ids2, []string{"int_0237", "int_0238"}) || len(answers2) != 2 {
			t.Fatalf("seed=%d expected [int_0237 int_0238], got ids=%v len=%d", seed, ids2, len(answers2))
		}

		h.svc.MarkDelivered(ctx, "int_0237")
		h.svc.MarkDelivered(ctx, "int_0238")
		answers3, ids3, err := h.svc.UndeliveredAnswers(ctx)
		if err != nil {
			t.Fatalf("seed=%d UndeliveredAnswers after all delivered: %v", seed, err)
		}
		if len(answers3) != 0 || len(ids3) != 0 {
			t.Fatalf("seed=%d expected 0 undelivered answers after marking all delivered, got ids=%v", seed, ids3)
		}

		return h.backend.Digest()
	}

	d1 := runScenario(11)
	d2 := runScenario(11)
	if d1 != d2 {
		t.Fatalf("non-deterministic digest for past-200 undelivered simulation: %s vs %s", d1, d2)
	}
}
