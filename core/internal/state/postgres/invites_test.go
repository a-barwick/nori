package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/state/records"
	"nori/internal/users"
)

func TestPostgresFindOrCreateByEmailAndInvites(t *testing.T) {
	b, prefix := openTestBackend(t)
	dir := records.NewUsers(b)
	ctx := context.Background()

	// Concurrent FindOrCreateByEmail for the same email is idempotent and never errors.
	targetEmail := fmt.Sprintf("open-%s@example.com", prefix)
	const workers = 10
	ids := make([]string, workers)
	var createdCount atomic.Int32
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			u, created, err := dir.FindOrCreateByEmail(ctx, targetEmail, "Open User")
			if err != nil {
				t.Errorf("worker %d FindOrCreateByEmail: %v", idx, err)
				return
			}
			ids[idx] = u.ID
			if created {
				createdCount.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if createdCount.Load() != 1 {
		t.Fatalf("createdCount = %d, want 1", createdCount.Load())
	}
	for i := 1; i < workers; i++ {
		if ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("worker %d ID = %q, want %q", i, ids[i], ids[0])
		}
	}

	// Single-use invite atomic redemption under contention.
	exp := time.Now().UTC().Add(time.Hour)
	inv, err := dir.CreateInvite(ctx, users.CreateInviteOptions{
		MaxUses:   1,
		Domain:    "example.com",
		ExpiresAt: &exp,
		Note:      "pg-test-" + prefix,
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	list, err := dir.ListInvites(ctx)
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	foundInList := false
	for _, item := range list {
		if item.Code == inv.Code {
			foundInList = true
			break
		}
	}
	if !foundInList {
		t.Fatalf("created invite %s not found in ListInvites", inv.Code)
	}

	var wins atomic.Int32
	var exhausted atomic.Int32
	for i := range 8 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, err := dir.RedeemInviteAndCreateUser(ctx, inv.Code, fmt.Sprintf("invite-%s-%d@example.com", prefix, idx), "Invited")
			if err == nil {
				wins.Add(1)
			} else if errors.Is(err, users.ErrInviteExhausted) {
				exhausted.Add(1)
			} else {
				t.Errorf("contender %d unexpected err: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 || exhausted.Load() != 7 {
		t.Fatalf("wins=%d exhausted=%d, want 1 and 7", wins.Load(), exhausted.Load())
	}

	// Revoke invite and verify rejection.
	inv2, err := dir.CreateInvite(ctx, users.CreateInviteOptions{MaxUses: 2})
	if err != nil {
		t.Fatalf("CreateInvite 2: %v", err)
	}
	if _, err := dir.RevokeInvite(ctx, inv2.Code); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if _, _, err := dir.RedeemInviteAndCreateUser(ctx, inv2.Code, fmt.Sprintf("revoked-%s@example.com", prefix), "Revoked"); !errors.Is(err, users.ErrInviteRevoked) {
		t.Fatalf("revoked redeem err = %v, want ErrInviteRevoked", err)
	}
}
