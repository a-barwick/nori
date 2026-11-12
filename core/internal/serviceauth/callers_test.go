package serviceauth

import (
	"strings"
	"testing"
)

func TestCallerRolesAndQueueBinding(t *testing.T) {
	cfg := Callers{Web: strings.Repeat("w", 32), Internal: strings.Repeat("i", 32), Queue: strings.Repeat("q", 32)}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	web, err := SignCaller(cfg.Web, "browser", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if role, subject := cfg.Verify(web); role != "browser" || subject != "owner" {
		t.Fatalf("browser proof yielded %q, %q", role, subject)
	}
	wrong, err := SignCaller(cfg.Web, "internal", "")
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := cfg.Verify(wrong); role != "" {
		t.Fatal("web signer authenticated as internal")
	}
	path := "/users/owner/api/process"
	proof := QueueAssertion(cfg.Queue, path)
	if !cfg.QueueValid(path, proof) || cfg.QueueValid("/users/owner/api/resume", proof) {
		t.Fatal("queue proof was not bound to the process route")
	}
}
