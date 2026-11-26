package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"nori/internal/api"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

type accountDeleter interface {
	LockUserData(ctx context.Context, userID string, exclusive bool) (release func(), err error)
	IsAccountDeletePending(ctx context.Context, userID string) (bool, error)
	BeginAccountDelete(ctx context.Context, userID string) error
	FinishAccountDelete(ctx context.Context, userID, owner string) error
}

type accountDeleteRequestKey struct{}
type accountDeletePendingKey struct{}

func withAccountDeleteRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, accountDeleteRequestKey{}, true)
}

func isAccountDeleteRequest(ctx context.Context) bool {
	ok, _ := ctx.Value(accountDeleteRequestKey{}).(bool)
	return ok
}

func withAccountDeletePending(ctx context.Context) context.Context {
	return context.WithValue(ctx, accountDeletePendingKey{}, true)
}

func isAccountDeletePendingCtx(ctx context.Context) bool {
	ok, _ := ctx.Value(accountDeletePendingKey{}).(bool)
	return ok
}

func (s *Service) isAccountDeletePending(ctx context.Context, userID string) (bool, error) {
	store, ok := s.Backend().(accountDeleter)
	if !ok {
		return false, nil
	}
	pending, err := store.IsAccountDeletePending(ctx, userID)
	if errors.Is(err, docstore.ErrNotFound) {
		return false, nil
	}
	return pending, err
}

func (s *Service) evictUser(userID string) {
	var toClose *App
	s.mu.Lock()
	if entry := s.apps[userID]; entry != nil {
		entry.retired = true
		delete(s.apps, userID)
		if entry.refs == 0 {
			toClose = entry.app
		}
	}
	delete(s.collapsed, userID)
	s.mu.Unlock()
	if toClose != nil {
		toClose.Close()
	}
}

func (s *Service) deleteUserAccount(ctx context.Context, row users.User, confirmation string, resumePending bool) error {
	if row.ID == users.LegacyID {
		return api.ErrAccountDeleteOwnerForbidden
	}
	store, ok := s.Backend().(accountDeleter)
	if !ok {
		return fmt.Errorf("account deletion requires a delete-capable store")
	}
	if strings.TrimSpace(confirmation) == "" || !strings.EqualFold(strings.TrimSpace(confirmation), strings.TrimSpace(row.Email)) {
		return api.ErrAccountDeleteEmailMismatch
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	release, err := store.LockUserData(ctx, row.ID, true)
	if err != nil {
		return err
	}
	defer release()
	row, err = s.Users.Get(ctx, row.ID)
	if err != nil {
		return err
	}
	if row.ID == users.LegacyID {
		return api.ErrAccountDeleteOwnerForbidden
	}
	if !strings.EqualFold(strings.TrimSpace(confirmation), strings.TrimSpace(row.Email)) {
		return api.ErrAccountDeleteEmailMismatch
	}
	pending, err := store.IsAccountDeletePending(ctx, row.ID)
	if err != nil {
		return err
	}
	if resumePending && !pending {
		return api.ErrAccountDeleteNotPending
	}
	if err := store.BeginAccountDelete(ctx, row.ID); err != nil {
		return err
	}
	if err := s.storage.PurgeUserArtifacts(ctx, s.cfg, row); err != nil {
		return fmt.Errorf("%w: purge user artifacts: %w", api.ErrAccountDeletePending, err)
	}
	if err := store.FinishAccountDelete(users.WithID(ctx, row.DataOwner()), row.ID, row.DataOwner()); err != nil {
		return fmt.Errorf("%w: clear account records: %w", api.ErrAccountDeletePending, err)
	}
	s.storage.EvictUserArtifactKey(row)
	s.evictUser(row.ID)
	release()
	return nil
}

func isAccountDeletePath(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	if !strings.HasPrefix(path, "/users/") {
		return false
	}
	_, rest, ok := strings.Cut(strings.TrimPrefix(path, "/users/"), "/")
	return ok && rest == "v1/settings/account/delete"
}

func wrapAccountDeleteRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAccountDeletePath(r.Method, r.URL.Path) {
			r = r.WithContext(withAccountDeleteRequest(r.Context()))
		}
		next.ServeHTTP(w, r)
	})
}

func wrapAccountDeleteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAccountDeletePendingCtx(r.Context()) && !(r.Method == http.MethodPost && r.URL.Path == "/v1/settings/account/delete") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":     api.ErrAccountDeletePending.Error(),
				"code":      "account_delete_pending",
				"retryable": true,
				"accepted":  false,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
