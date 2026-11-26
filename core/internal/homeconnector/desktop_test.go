package homeconnector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type desktopTestStore map[string]Credential

func privateDesktopDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (s desktopTestStore) read(account string) (Credential, error) {
	credential, ok := s[account]
	if !ok {
		return Credential{}, os.ErrNotExist
	}
	return credential, nil
}

func (s desktopTestStore) save(account string, credential Credential) error {
	s[account] = credential
	return nil
}

func (s desktopTestStore) remove(account string) error {
	delete(s, account)
	return nil
}

func TestDesktopRevocationRecoversAfterOutage(t *testing.T) {
	online := false
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/revoke" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		requests[r.Form.Get("token_type_hint")]++
		if !online {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	dir := privateDesktopDir(t)
	cfg := Config{RelayURL: "https://new-relay.example", StateDir: dir}
	store := desktopTestStore{
		dir:              {RelayURL: server.URL, RefreshToken: "active-token"},
		dir + ".pairing": {RelayURL: server.URL, RefreshToken: "device-code"},
	}
	pending, err := desktopRevocationPending(cfg, store)
	if err != nil || !pending {
		t.Fatalf("disabled active credential must show pending, got %v, %v", pending, err)
	}
	pending, err = desktopRevokePending(context.Background(), cfg, store)
	if err != nil || !pending {
		t.Fatalf("outage should leave revocation pending, got %v, %v", pending, err)
	}
	if _, exists := store[dir]; exists {
		t.Fatal("active credential remains available to the connector")
	}
	if _, exists := store[dir+".pending-revocation"]; !exists {
		t.Fatal("refresh credential was lost before revocation")
	}
	if _, exists := store[dir+".pairing"]; !exists {
		t.Fatal("pairing grant was lost before cancellation")
	}

	online = true
	pending, err = desktopRevokePending(context.Background(), cfg, store)
	if err != nil || pending || len(store) != 0 {
		t.Fatalf("retry should revoke both credentials, got pending %v, %v, %v", pending, err, store)
	}
	if requests[""] != 2 || requests["device_code"] != 2 {
		t.Fatalf("expected both revocation forms on each attempt: %v", requests)
	}
}

func TestDesktopRevocationFlagsChangedRelay(t *testing.T) {
	dir := privateDesktopDir(t)
	cfg := Config{RelayURL: "https://new-relay.example", StateDir: dir}
	if err := os.WriteFile(filepath.Join(dir, desktopSharingFile), []byte("enabled"), 0600); err != nil {
		t.Fatal(err)
	}
	store := desktopTestStore{dir: {RelayURL: "https://old-relay.example", RefreshToken: "active-token"}}
	pending, err := desktopRevocationPending(cfg, store)
	if err != nil || !pending {
		t.Fatalf("changed relay must trigger disconnect recovery, got %v, %v", pending, err)
	}
}

func TestDesktopPairingGrantRecovery(t *testing.T) {
	dir := privateDesktopDir(t)
	cfg := Config{RelayURL: "https://relay.example", StateDir: dir}
	if err := os.WriteFile(filepath.Join(dir, desktopSharingFile), []byte("enabled"), 0600); err != nil {
		t.Fatal(err)
	}
	store := desktopTestStore{dir + ".pairing": {RelayURL: cfg.RelayURL, RefreshToken: "device-code"}}
	pending, err := desktopRevocationPending(cfg, store)
	if err != nil || !pending {
		t.Fatalf("abandoned grant must be visible for recovery, got %v, %v", pending, err)
	}
	store[dir] = Credential{RelayURL: cfg.RelayURL, RefreshToken: "active-token"}
	pending, err = desktopRevokePending(context.Background(), cfg, store)
	if err != nil || pending {
		t.Fatalf("completed pairing must keep its active device, got %v, %v", pending, err)
	}
	if _, exists := store[dir+".pairing"]; exists {
		t.Fatal("completed pairing grant was not cleared")
	}
	if _, exists := store[dir]; !exists {
		t.Fatal("recovery removed an active paired device")
	}
}

func TestDesktopSharingContextCancelsWhenDisabled(t *testing.T) {
	dir := privateDesktopDir(t)
	marker := filepath.Join(dir, desktopSharingFile)
	if err := os.WriteFile(marker, []byte("enabled"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := desktopSharingContext(context.Background(), dir)
	defer cancel()
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("unexpected context error: %v", ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disabled sharing did not cancel the running connector")
	}
}

func TestDesktopConfigIgnoresEnvironmentAndFlags(t *testing.T) {
	oldMode, oldURL := DesktopMode, DefaultRelayURL
	DesktopMode, DefaultRelayURL = "1", "https://relay.example"
	t.Cleanup(func() { DesktopMode, DefaultRelayURL = oldMode, oldURL })
	t.Setenv("NORI_CONNECTOR_RELAY_URL", "https://other.example")
	t.Setenv("NORI_CONNECTOR_OLLAMA_URL", "http://127.0.0.1:9999")
	t.Setenv("NORI_CONNECTOR_STATE_DIR", t.TempDir())
	cfg, err := (Config{
		RelayURL: "https://flag.example", OllamaURL: "http://127.0.0.1:9998", StateDir: t.TempDir(),
	}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	userConfig, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RelayURL != "https://relay.example" || cfg.OllamaURL != "http://127.0.0.1:11434" ||
		cfg.StateDir != filepath.Join(userConfig, "Nori", "Home AI") {
		t.Fatalf("desktop config was overridden: %+v", cfg)
	}
}
