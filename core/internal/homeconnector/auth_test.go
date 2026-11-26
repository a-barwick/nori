package homeconnector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

type testCredentialStore struct {
	credential Credential
	removed    bool
	readErr    error
	saveErr    error
}

func (s *testCredentialStore) read(string) (Credential, error) {
	return s.credential, s.readErr
}

func (s *testCredentialStore) save(_ string, credential Credential) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.credential = credential
	return nil
}

func (s *testCredentialStore) remove(string) error {
	s.removed = true
	return nil
}

func TestCredentialRefreshSurvivesTransientOutage(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("refresh_token") != "refresh-old" {
			t.Fatalf("unexpected refresh token")
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"temporarily_unavailable"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"access-new","refresh_token":"refresh-new","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{RelayURL: server.URL, StateDir: dir}
	store := &testCredentialStore{credential: Credential{
		RelayURL: server.URL, AccessToken: "access-old", RefreshToken: "refresh-old",
		Expiry: time.Now().Add(-time.Minute),
	}}
	if _, err := accessToken(context.Background(), cfg, store, false); err == nil || errors.Is(err, ErrPairRequired) {
		t.Fatalf("temporary outage should be retryable, got %v", err)
	}
	if store.removed || store.credential.RefreshToken != "refresh-old" {
		t.Fatal("temporary outage changed stored credentials")
	}
	credential, err := accessToken(context.Background(), cfg, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "access-new" || store.credential.RefreshToken != "refresh-new" {
		t.Fatal("recovery did not persist rotated credentials")
	}
}

func TestCredentialRefreshRejectsRevokedGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{RelayURL: server.URL, StateDir: dir}
	store := &testCredentialStore{credential: Credential{
		RelayURL: server.URL, AccessToken: "access-old", RefreshToken: "refresh-old",
		Expiry: time.Now().Add(-time.Minute),
	}}
	if _, err := accessToken(context.Background(), cfg, store, false); !errors.Is(err, ErrPairRequired) || !store.removed {
		t.Fatalf("revoked grant should require pairing and clear credentials, got %v", err)
	}
}

func TestPairingStorageFailureRevokesNewDevice(t *testing.T) {
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/revoke" || r.Method != http.MethodPost {
			t.Error("unexpected cleanup request")
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("token") != "new-refresh" {
			t.Error("cleanup did not use the new device credential")
		}
		revocations++
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	saveErr := errors.New("disk failed")
	store := &testCredentialStore{readErr: os.ErrNotExist, saveErr: saveErr}
	credential := Credential{RelayURL: server.URL, RefreshToken: "new-refresh"}
	err := storePairedCredential(Config{StateDir: dir}, credential, store)
	if !errors.Is(err, saveErr) || revocations != 1 {
		t.Fatalf("expected storage error and one revoke, got %v and %d", err, revocations)
	}
}
