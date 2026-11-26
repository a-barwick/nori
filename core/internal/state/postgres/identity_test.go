package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestIdentityBindingConcurrentIsolation(t *testing.T) {
	backend, prefix := openTestBackend(t)
	directory := records.NewSharedUsers(backend)
	ctx := context.Background()
	legacy := users.User{ID: uuid.NewString(), Email: prefix + "legacy@gmail.com", TimeZone: "UTC"}
	if err := directory.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	type outcome struct {
		subject string
		row     users.User
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			subject := fmt.Sprintf("%s-subject-%d", prefix, i)
			row, err := directory.ResolveIdentity(ctx, users.Identity{
				Issuer: "https://accounts.google.com", Subject: subject, EmailAuthoritative: true,
			}, legacy.Email, "", "manual", "")
			results <- outcome{subject: subject, row: row, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winner := ""
	for result := range results {
		if result.err == nil {
			if winner != "" || result.row.ID != legacy.ID {
				t.Fatalf("unexpected additional identity binding: %+v", result)
			}
			winner = result.subject
		} else if !errors.Is(result.err, users.ErrIdentityConflict) {
			t.Fatalf("unexpected binding error: %v", result.err)
		}
	}
	if winner == "" {
		t.Fatal("no identity acquired the legacy account")
	}
	row, err := directory.ResolveIdentity(ctx, users.Identity{Issuer: "https://accounts.google.com", Subject: winner}, "changed@example.com", "", "manual", "")
	if err != nil || row.ID != legacy.ID {
		t.Fatalf("bound identity did not retain its account: row=%+v err=%v", row, err)
	}

	newSubject := prefix + "new-subject"
	start = make(chan struct{})
	results = make(chan outcome, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			row, err := directory.ResolveIdentity(ctx, users.Identity{Issuer: "https://accounts.google.com", Subject: newSubject},
				fmt.Sprintf("%s-new-%d@example.com", prefix, i), "", "open", "")
			results <- outcome{row: row, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	createdID := ""
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent identity enrollment: %v", result.err)
		}
		if createdID == "" {
			createdID = result.row.ID
		}
		if result.row.ID != createdID {
			t.Fatalf("one identity resolved to multiple accounts: %s and %s", createdID, result.row.ID)
		}
	}
	if _, err := directory.BindIdentity(ctx, legacy.ID, users.Identity{Issuer: "https://accounts.google.com", Subject: newSubject}); !errors.Is(err, users.ErrIdentityConflict) {
		t.Fatalf("identity was rebound to another account: %v", err)
	}
}
