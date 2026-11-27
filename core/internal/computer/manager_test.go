package computer_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nori/internal/computer"
	"nori/internal/computer/fake"
)

func TestManagerLifecyclePolicyAndEnvSanitization(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	fp := fake.New()
	fp.Now = func() time.Time { return now }
	store := computer.NewMemoryStore()
	store.Now = func() time.Time { return now }

	mgr := computer.NewManager(fp, store, computer.Config{MemoryLimitMB: 3072, MaxDiskGB: 10})
	mgr.Now = func() time.Time { return now }

	// Before provisioning, Status reports not_provisioned without creating a VM.
	st, err := mgr.Status(ctx, "user-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Status != computer.StatusNotProvisioned {
		t.Fatalf("Status = %q, want not_provisioned", st.Status)
	}

	// Ensure provisions VM, applies SSRF DefaultNetworkPolicy, and records baseline checkpoint.
	rec, err := mgr.Ensure(ctx, "user-1")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if rec.BaselineCheckpointID == "" {
		t.Fatalf("expected baseline checkpoint ID to be set")
	}
	pol, ok := fp.InstancePolicy(rec.ProviderID)
	if !ok || !pol.BlockPrivate || len(pol.DenyDomains) == 0 {
		t.Fatalf("expected SSRF default network policy to be applied, got ok=%v pol=%+v", ok, pol)
	}

	// Verify Exec strips host/secret env vars, caps timeout <= 90s, and truncates >32KiB output.
	var seenEnv map[string]string
	var seenTimeout time.Duration
	fp.OnExec = func(_ context.Context, _ string, req computer.ExecRequest, _ map[string][]byte) (computer.ExecResult, bool, error) {
		seenEnv = req.Env
		seenTimeout = req.Timeout
		return computer.ExecResult{
			ExitCode: 0,
			Stdout:   strings.Repeat("a", computer.MaxExecOutputBytes+500),
			Stderr:   "ok",
			CostUSD:  0.0012,
		}, true, nil
	}
	res, err := mgr.Exec(ctx, "user-1", computer.ExecRequest{
		Command: "run-script",
		Timeout: 5 * time.Minute,
		Env: map[string]string{
			"SAFE_VAR":              "hello",
			"NORI_POSTGRES_URL":     "postgres://secret",
			"OPENAI_API_KEY":        "sk-secret",
			"CUSTOM_SECRET_TOKEN":   "tok",
			"AWS_SECRET_ACCESS_KEY": "aws-secret",
		},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if seenTimeout != computer.MaxExecTimeout {
		t.Fatalf("timeout = %v, want %v", seenTimeout, computer.MaxExecTimeout)
	}
	if len(seenEnv) != 1 || seenEnv["SAFE_VAR"] != "hello" {
		t.Fatalf("expected only SAFE_VAR in env, got %+v", seenEnv)
	}
	if !res.StdoutTruncated || len(res.Stdout) != computer.MaxExecOutputBytes || res.StdoutBytes != computer.MaxExecOutputBytes+500 {
		t.Fatalf("unexpected truncation metadata: truncated=%v len=%d bytes=%d", res.StdoutTruncated, len(res.Stdout), res.StdoutBytes)
	}
	if res.CostUSD != 0.0012 {
		t.Fatalf("CostUSD = %v, want 0.0012", res.CostUSD)
	}
}

func TestManagerPathConfinementAndFileLinks(t *testing.T) {
	ctx := context.Background()
	fp := fake.New()
	store := computer.NewMemoryStore()
	mgr := computer.NewManager(fp, store, computer.Config{})

	// Path traversal must be rejected.
	for _, bad := range []string{"/etc/passwd", "../etc/passwd", "/home/nori/../../etc/shadow", "/tmp/x"} {
		if err := mgr.WriteFile(ctx, "u1", bad, []byte("x"), 0o644); !errors.Is(err, computer.ErrPathTraversal) {
			t.Fatalf("WriteFile(%q) = %v, want ErrPathTraversal", bad, err)
		}
	}

	// StageUpload places files inside /home/nori/workspace/uploads/
	staged, err := mgr.StageUpload(ctx, "u1", "", "budget.csv", []byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatalf("StageUpload: %v", err)
	}
	if staged != "/home/nori/workspace/uploads/budget.csv" {
		t.Fatalf("staged path = %q", staged)
	}

	// Publish & link a file, then verify PurgeLinkedResultFiles and PurgeLinkedEntityFiles remove workspace files.
	if err := mgr.WriteFile(ctx, "u1", "reports/q4.pdf", []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := mgr.LinkFile(ctx, "u1", "reports/q4.pdf", "res-123", []string{"Acme Corp", "Q4 Budget"}); err != nil {
		t.Fatalf("LinkFile: %v", err)
	}
	if err := mgr.PurgeLinkedResultFiles(ctx, "u1", "res-123"); err != nil {
		t.Fatalf("PurgeLinkedResultFiles: %v", err)
	}
	if _, err := mgr.ReadFile(ctx, "u1", "reports/q4.pdf", 1024); !errors.Is(err, computer.ErrNotFound) {
		t.Fatalf("expected linked file to be deleted, got err=%v", err)
	}

	// Entity-tag forget cleanup.
	if err := mgr.WriteFile(ctx, "u1", "notes/acme.md", []byte("notes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := mgr.LinkFile(ctx, "u1", "notes/acme.md", "", []string{"Acme Corp"}); err != nil {
		t.Fatalf("LinkFile: %v", err)
	}
	if err := mgr.PurgeLinkedEntityFiles(ctx, "u1", "acme corp"); err != nil {
		t.Fatalf("PurgeLinkedEntityFiles: %v", err)
	}
	if _, err := mgr.ReadFile(ctx, "u1", "notes/acme.md", 1024); !errors.Is(err, computer.ErrNotFound) {
		t.Fatalf("expected entity-linked file to be deleted, got err=%v", err)
	}
}

func TestManagerSessionsIdleCapAndAccountDeleteReaper(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	fp := fake.New()
	fp.Now = func() time.Time { return now }
	store := computer.NewMemoryStore()
	store.Now = func() time.Time { return now }

	mgr := computer.NewManager(fp, store, computer.Config{
		SessionTTL:     15 * time.Minute,
		SessionIdleCap: 10 * time.Minute,
	})
	mgr.Now = func() time.Time { return now }

	created, err := mgr.CreateSession(ctx, "u1", computer.SessionModeView, "task-1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.RawToken == "" || created.Session.Mode != computer.SessionModeView {
		t.Fatalf("unexpected created session: %+v", created)
	}

	// Wrong token or wrong user fails validation.
	if _, err := mgr.ValidateSession(ctx, "u1", created.Session.ID, "wrong-token"); !errors.Is(err, computer.ErrNotFound) {
		t.Fatalf("ValidateSession wrong token = %v, want ErrNotFound", err)
	}
	if _, err := mgr.ValidateSession(ctx, "u2", created.Session.ID, created.RawToken); !errors.Is(err, computer.ErrNotFound) {
		t.Fatalf("ValidateSession wrong user = %v, want ErrNotFound", err)
	}

	// Switch to control mode and validate.
	updated, err := mgr.SetSessionMode(ctx, "u1", created.Session.ID, computer.SessionModeControl)
	if err != nil || updated.Mode != computer.SessionModeControl {
		t.Fatalf("SetSessionMode = (%+v, %v)", updated, err)
	}
	if _, err := mgr.ValidateSession(ctx, "u1", created.Session.ID, created.RawToken); err != nil {
		t.Fatalf("ValidateSession valid = %v", err)
	}

	// Idle > 10m expires session automatically.
	now = now.Add(11 * time.Minute)
	if _, err := mgr.ValidateSession(ctx, "u1", created.Session.ID, created.RawToken); !errors.Is(err, computer.ErrSessionExpired) {
		t.Fatalf("ValidateSession after idle = %v, want ErrSessionExpired", err)
	}

	// Account deletion teardown with transient provider failure + reaper retry.
	fp.Faults.DestroyErr = errors.New("sprites API 503")
	if err := mgr.DestroyForAccountDelete(ctx, "u1"); err == nil {
		t.Fatalf("expected DestroyForAccountDelete to fail when provider.Destroy fails")
	}
	// While deleting, Ensure must refuse to re-provision.
	if _, err := mgr.Ensure(ctx, "u1"); !errors.Is(err, computer.ErrDeleting) {
		t.Fatalf("Ensure during deleting = %v, want ErrDeleting", err)
	}

	// Clear fault and run reaper; VM is destroyed and marked deleted.
	fp.Faults.DestroyErr = nil
	if err := mgr.ReapDeletingComputers(ctx); err != nil {
		t.Fatalf("ReapDeletingComputers: %v", err)
	}
	rec, err := store.GetComputer(ctx, "u1")
	if err != nil || rec.Status != computer.StatusDeleted {
		t.Fatalf("computer after reap = (%+v, %v), want StatusDeleted", rec, err)
	}
}

func TestManagerUserControlLeasePurgeTombstonesAndProviderMismatch(t *testing.T) {
	ctx := context.Background()
	fp := fake.New()
	store := computer.NewMemoryStore()
	mgr := computer.NewManager(fp, store, computer.Config{})

	// 1. Provision, write linked files, and take checkpoint.
	if err := mgr.WriteFile(ctx, "u1", "secrets/alice.txt", []byte("private"), 0o644); err != nil {
		t.Fatalf("WriteFile alice: %v", err)
	}
	if err := mgr.WriteFile(ctx, "u1", "secrets/alicia.txt", []byte("unrelated"), 0o644); err != nil {
		t.Fatalf("WriteFile alicia: %v", err)
	}
	if err := mgr.LinkFile(ctx, "u1", "secrets/alice.txt", "res-alice", []string{"Alice"}); err != nil {
		t.Fatalf("LinkFile alice: %v", err)
	}
	if err := mgr.LinkFile(ctx, "u1", "secrets/alicia.txt", "res-alicia", []string{"Alicia"}); err != nil {
		t.Fatalf("LinkFile alicia: %v", err)
	}
	ckptID, err := mgr.Checkpoint(ctx, "u1", "with-both")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// 2. Active user control session blocks agent Exec/WriteFile/DeletePath (exclusive user control lease).
	sess, err := mgr.CreateSession(ctx, "u1", computer.SessionModeControl, "t1", "")
	if err != nil {
		t.Fatalf("CreateSession control: %v", err)
	}
	if _, err := mgr.Exec(ctx, "u1", computer.ExecRequest{Command: "echo hi"}); !errors.Is(err, computer.ErrUserInControl) {
		t.Fatalf("expected ErrUserInControl during active control session, got %v", err)
	}
	if err := mgr.WriteFile(ctx, "u1", "blocked.txt", []byte("x"), 0o644); !errors.Is(err, computer.ErrUserInControl) {
		t.Fatalf("expected WriteFile ErrUserInControl, got %v", err)
	}
	// Hand back control -> agent Exec immediately allowed again.
	if _, err := mgr.SetSessionMode(ctx, "u1", sess.Session.ID, computer.SessionModeView); err != nil {
		t.Fatalf("SetSessionMode view: %v", err)
	}
	if _, err := mgr.Exec(ctx, "u1", computer.ExecRequest{Command: "echo ok"}); err != nil {
		t.Fatalf("Exec after handback: %v", err)
	}
	_ = mgr.EndSession(ctx, "u1", sess.Session.ID)

	// 3. Exact tag match: forgetting "Alice" must purge alice.txt ONLY (not alicia.txt),
	// and restoring the older checkpoint must NOT resurrect alice.txt!
	if err := mgr.PurgeLinkedEntityFiles(ctx, "u1", "alice"); err != nil {
		t.Fatalf("PurgeLinkedEntityFiles: %v", err)
	}
	if _, err := mgr.ReadFile(ctx, "u1", "secrets/alice.txt", 1024); !errors.Is(err, computer.ErrNotFound) {
		t.Fatalf("expected alice.txt purged, got %v", err)
	}
	if _, err := mgr.ReadFile(ctx, "u1", "secrets/alicia.txt", 1024); err != nil {
		t.Fatalf("expected alicia.txt untouched by exact tag match, got %v", err)
	}
	if err := mgr.Restore(ctx, "u1", ckptID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := mgr.ReadFile(ctx, "u1", "secrets/alice.txt", 1024); !errors.Is(err, computer.ErrNotFound) {
		t.Fatalf("expected purged tombstone to prevent alice.txt resurrection after Restore, got %v", err)
	}

	// 4. Provider mismatch on DestroyForAccountDelete must fail closed (never declare deleted via wrong provider).
	rec, _ := store.GetComputer(ctx, "u1")
	rec.Provider = "sprites" // stored under a different provider than active "fake"
	_ = store.UpsertComputer(ctx, rec)
	if err := mgr.DestroyForAccountDelete(ctx, "u1"); !errors.Is(err, computer.ErrProviderMismatch) {
		t.Fatalf("expected ErrProviderMismatch on wrong-provider teardown, got %v", err)
	}
}
