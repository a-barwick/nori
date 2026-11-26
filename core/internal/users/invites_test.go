package users_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/users"
)

func TestMemoryDirectoryFindOrCreateByEmailConcurrent(t *testing.T) {
	dir := users.NewMemoryDirectory()
	ctx := context.Background()

	const workers = 16
	ids := make([]string, workers)
	var createdCount atomic.Int32
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			email := "Alice@Example.COM"
			if idx%2 == 1 {
				email = "  alice@example.com "
			}
			u, created, err := dir.FindOrCreateByEmail(ctx, email, "Alice")
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
			t.Fatalf("worker %d got ID %q, want %q", i, ids[i], ids[0])
		}
	}
}

func TestInviteLifecycleAndAtomicRedemption(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	dir := users.NewMemoryDirectory().WithClock(func() time.Time { return now })
	ctx := context.Background()

	exp := now.Add(time.Hour)
	inv, err := dir.CreateInvite(ctx, users.CreateInviteOptions{
		MaxUses:   1,
		Domain:    "@example.com",
		ExpiresAt: &exp,
		Note:      "beta wave",
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if inv.Code == "" || inv.Domain != "example.com" || inv.MaxUses != 1 {
		t.Fatalf("unexpected invite: %+v", inv)
	}

	// Wrong domain rejected without consuming use.
	if _, _, err := dir.RedeemInviteAndCreateUser(ctx, inv.Code, "bob@other.org", "Bob"); !errors.Is(err, users.ErrInviteEmailMismatch) {
		t.Fatalf("wrong domain err = %v, want ErrInviteEmailMismatch", err)
	}

	// Concurrent redemption of a single-use code: exactly one succeeds.
	const contenders = 10
	var wins atomic.Int32
	var exhausted atomic.Int32
	var wg sync.WaitGroup
	for i := range contenders {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, err := dir.RedeemInviteAndCreateUser(ctx, inv.Code, fmt.Sprintf("user%d@example.com", idx), "User")
			if err == nil {
				wins.Add(1)
			} else if errors.Is(err, users.ErrInviteExhausted) {
				exhausted.Add(1)
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 || exhausted.Load() != contenders-1 {
		t.Fatalf("wins=%d exhausted=%d, want 1 and %d", wins.Load(), exhausted.Load(), contenders-1)
	}

	// Revocation and expiration checks.
	exp2 := now.Add(10 * time.Minute)
	inv2, err := dir.CreateInvite(ctx, users.CreateInviteOptions{Email: "carol@example.com", ExpiresAt: &exp2})
	if err != nil {
		t.Fatalf("CreateInvite 2: %v", err)
	}
	if _, err := dir.RevokeInvite(ctx, inv2.Code); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if _, _, err := dir.RedeemInviteAndCreateUser(ctx, inv2.Code, "carol@example.com", "Carol"); !errors.Is(err, users.ErrInviteRevoked) {
		t.Fatalf("revoked err = %v, want ErrInviteRevoked", err)
	}

	exp3 := now.Add(5 * time.Minute)
	inv3, err := dir.CreateInvite(ctx, users.CreateInviteOptions{ExpiresAt: &exp3})
	if err != nil {
		t.Fatalf("CreateInvite 3: %v", err)
	}
	now = now.Add(10 * time.Minute)
	if _, _, err := dir.RedeemInviteAndCreateUser(ctx, inv3.Code, "dave@example.com", "Dave"); !errors.Is(err, users.ErrInviteExpired) {
		t.Fatalf("expired err = %v, want ErrInviteExpired", err)
	}
}
