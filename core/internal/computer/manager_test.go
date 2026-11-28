package computer_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/billing"
	"nori/internal/computer"
	"nori/internal/computer/fake"
	"nori/internal/state"
	"nori/internal/state/docstore"
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

type countingStandbyProvider struct {
	*fake.Provider
	ensureCalls          int
	standby              bool
	eventuallyConsistent bool
	overrideStatus       computer.Status
}

func (p *countingStandbyProvider) Ensure(ctx context.Context, spec computer.Spec) (computer.Instance, error) {
	p.ensureCalls++
	return p.Provider.Ensure(ctx, spec)
}

func (p *countingStandbyProvider) Status(ctx context.Context, providerID string) (computer.InstanceStatus, error) {
	st, err := p.Provider.Status(ctx, providerID)
	if err != nil {
		return st, err
	}
	st.EventuallyConsistent = p.eventuallyConsistent
	if p.overrideStatus != "" {
		st.Status = p.overrideStatus
		st.Standby = p.standby
	} else if p.standby {
		st.Status = computer.StatusReady
		st.Standby = true
	}
	return st, nil
}

func TestManagerEnsureCachingAndStandbySleepWakeSemantics(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	prov := &countingStandbyProvider{Provider: fake.New(), standby: true}
	prov.Now = func() time.Time { return now }
	store := computer.NewMemoryStore()
	store.Now = func() time.Time { return now }
	mgr := computer.NewManager(prov, store, computer.Config{})
	mgr.Now = func() time.Time { return now }

	// Consecutive tool operations reuse cached ensure within TTL instead of re-ensuring every call.
	if err := mgr.WriteFile(ctx, "u1", "a.txt", []byte("one"), 0o644); err != nil {
		t.Fatalf("WriteFile 1: %v", err)
	}
	if err := mgr.WriteFile(ctx, "u1", "b.txt", []byte("two"), 0o644); err != nil {
		t.Fatalf("WriteFile 2: %v", err)
	}
	if _, err := mgr.Exec(ctx, "u1", computer.ExecRequest{Command: "echo hi"}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if prov.ensureCalls != 1 {
		t.Fatalf("ensureCalls = %d, want 1 across consecutive operations", prov.ensureCalls)
	}

	// Standby provider reports Ready while active (Wake -> Ready, not Sleeping).
	st, err := mgr.Wake(ctx, "u1")
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if st.Status != computer.StatusReady {
		t.Fatalf("status after Wake = %q, want ready", st.Status)
	}
	if prov.ensureCalls != 2 {
		t.Fatalf("expected Wake to invalidate cache and re-ensure (ensureCalls=%d, want 2)", prov.ensureCalls)
	}

	// Explicit Sleep keeps status Sleeping even while Fly Sprites briefly reports warm standby.
	if err := mgr.Sleep(ctx, "u1"); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	stAfterSleep, err := mgr.Status(ctx, "u1")
	if err != nil {
		t.Fatalf("Status after Sleep: %v", err)
	}
	if stAfterSleep.Status != computer.StatusSleeping {
		t.Fatalf("status after Sleep = %q, want sleeping", stAfterSleep.Status)
	}

	// Subsequent Wake transitions cleanly back to Ready.
	stAfterRewake, err := mgr.Wake(ctx, "u1")
	if err != nil {
		t.Fatalf("second Wake: %v", err)
	}
	if stAfterRewake.Status != computer.StatusReady {
		t.Fatalf("status after second Wake = %q, want ready", stAfterRewake.Status)
	}

	// Eventually-consistent provider (Fly Sprites) returning stale "cold" right after Wake
	// must stay Ready during the grace window, then reflect Sleeping once grace expires.
	prov.eventuallyConsistent = true
	prov.standby = false
	prov.overrideStatus = computer.StatusSleeping
	stStaleWake, err := mgr.Wake(ctx, "u1")
	if err != nil {
		t.Fatalf("Wake with stale provider status: %v", err)
	}
	if stStaleWake.Status != computer.StatusReady {
		t.Fatalf("status after Wake with stale cold read = %q, want ready", stStaleWake.Status)
	}
	now = now.Add(35 * time.Second)
	stCooled, err := mgr.Status(ctx, "u1")
	if err != nil {
		t.Fatalf("Status after grace window: %v", err)
	}
	if stCooled.Status != computer.StatusSleeping {
		t.Fatalf("status after grace window = %q, want sleeping", stCooled.Status)
	}

	// Stale "running" right after Sleep must stay Sleeping during the grace window.
	prov.overrideStatus = computer.StatusReady
	if err := mgr.Sleep(ctx, "u1"); err != nil {
		t.Fatalf("Sleep with stale running status: %v", err)
	}
	stStaleSleep, err := mgr.Status(ctx, "u1")
	if err != nil {
		t.Fatalf("Status right after Sleep: %v", err)
	}
	if stStaleSleep.Status != computer.StatusSleeping {
		t.Fatalf("status right after Sleep with stale running read = %q, want sleeping", stStaleSleep.Status)
	}
}

type countingStatusProvider struct {
	*fake.Provider
	statusCalls atomic.Int64
	blockCh     chan struct{}
	enteredCh   chan struct{}
}

func (p *countingStatusProvider) Status(ctx context.Context, providerID string) (computer.InstanceStatus, error) {
	p.statusCalls.Add(1)
	if p.enteredCh != nil {
		select {
		case p.enteredCh <- struct{}{}:
		default:
		}
	}
	if p.blockCh != nil {
		<-p.blockCh
	}
	return p.Provider.Status(ctx, providerID)
}

func TestManagerStatusCacheAndInvalidation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	fp := fake.New()
	fp.Now = func() time.Time { return now }
	prov := &countingStatusProvider{Provider: fp}
	store := computer.NewMemoryStore()
	store.Now = func() time.Time { return now }

	mgr := computer.NewManager(prov, store, computer.Config{})
	mgr.Now = func() time.Time { return now }

	if _, err := mgr.Ensure(ctx, "u1"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	prov.statusCalls.Store(0)

	// Repeated Status() calls within 5s TTL must hit provider.Status only once.
	for i := 0; i < 5; i++ {
		st, err := mgr.Status(ctx, "u1")
		if err != nil {
			t.Fatalf("Status #%d: %v", i, err)
		}
		if st.Status != computer.StatusReady {
			t.Fatalf("Status #%d = %q, want ready", i, st.Status)
		}
	}
	if got := prov.statusCalls.Load(); got != 1 {
		t.Fatalf("provider.Status calls within TTL = %d, want 1", got)
	}

	// Advancing past the 5s TTL must trigger a fresh provider.Status call.
	now = now.Add(6 * time.Second)
	if _, err := mgr.Status(ctx, "u1"); err != nil {
		t.Fatalf("Status after TTL: %v", err)
	}
	if got := prov.statusCalls.Load(); got != 2 {
		t.Fatalf("provider.Status calls after TTL = %d, want 2", got)
	}

	// Sleep invalidates the cache immediately so the next Status hits provider.Status.
	if err := mgr.Sleep(ctx, "u1"); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if _, err := mgr.Status(ctx, "u1"); err != nil {
		t.Fatalf("Status after Sleep: %v", err)
	}
	if got := prov.statusCalls.Load(); got != 3 {
		t.Fatalf("provider.Status calls after Sleep = %d, want 3", got)
	}

	// Wake invalidates the cache and refreshes Status.
	if _, err := mgr.Wake(ctx, "u1"); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if got := prov.statusCalls.Load(); got != 4 {
		t.Fatalf("provider.Status calls after Wake = %d, want 4", got)
	}

	// Concurrent Status calls after TTL expiry deduplicate into a single provider.Status call.
	now = now.Add(6 * time.Second)
	prov.blockCh = make(chan struct{})
	prov.enteredCh = make(chan struct{}, 1)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = mgr.Status(ctx, "u1")
		}(i)
	}
	<-prov.enteredCh
	time.Sleep(10 * time.Millisecond)
	close(prov.blockCh)
	wg.Wait()
	prov.blockCh = nil
	prov.enteredCh = nil
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Status #%d: %v", i, err)
		}
	}
	if got := prov.statusCalls.Load(); got != 5 {
		t.Fatalf("provider.Status calls after 4 concurrent misses = %d, want 5", got)
	}
}

func setupBillingForTest(t *testing.T, nowFn func() time.Time, compStore *computer.MemoryStore) (*billing.MemoryStore, *billing.Service) {
	t.Helper()
	bs := billing.NewMemoryStore(nowFn)
	if compStore != nil {
		bs.UsageReader = compStore.UsageSamplesForBilling
	}
	svc := billing.NewService(bs, false, billing.GlobalBudgetConfig{})
	svc.Now = nowFn
	return bs, svc
}

func TestManagerSingleTxAwakeMeteringAndZeroDoubleBill(t *testing.T) {
	ctx := context.Background()
	var clockMu sync.Mutex
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	fp := fake.New()
	fp.Now = nowFn
	store := computer.NewMemoryStore()
	bs, bsvc := setupBillingForTest(t, nowFn, store)
	if _, err := bs.SetTier(ctx, "u-meter", billing.TierStandard, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier: %v", err)
	}

	mgr := computer.NewManager(fp, store, computer.Config{
		RatePerHourUSD: 0.15,
		IdleAutoSleep:  15 * time.Minute,
	})
	mgr.SetClock(nowFn)
	mgr.SetBilling(bsvc)

	// 1. Provision & wake at 12:00.
	rec, err := mgr.Ensure(ctx, "u-meter")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if rec.AwakeSince == nil || !rec.AwakeSince.Equal(nowFn()) {
		t.Fatalf("AwakeSince = %v, want %v", rec.AwakeSince, nowFn())
	}
	if rec.LastMeteredAt == nil || !rec.LastMeteredAt.Equal(nowFn()) {
		t.Fatalf("LastMeteredAt = %v, want %v", rec.LastMeteredAt, nowFn())
	}

	// 2. Advance 10m (active, below 15m idle) and run janitor pass -> meters 10m = $0.025 (25,000 micros).
	advance(10 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("RunJanitorPass #1: %v", err)
	}
	rows := store.MeteredUsage("u-meter")
	if len(rows) != 1 {
		t.Fatalf("usage rows after 10m janitor = %d, want 1", len(rows))
	}
	u := rows[0]
	if u.Billing != state.UsageBillingHosted || u.Category != state.UsageCategoryComputer ||
		u.Provider != "sprites" || u.Model != "sprites-compute" ||
		u.RequestStatus != "completed" || u.CostStatus != "calculated" {
		t.Fatalf("unexpected usage metadata: %+v", u)
	}
	if u.CostUSD == nil || billing.USDToMicros(*u.CostUSD) != 25_000 {
		t.Fatalf("10m cost = %v, want 25000 micros ($0.025)", u.CostUSD)
	}

	// 3. Advance 4 more minutes (total 14m awake) and race 8 concurrent janitor passes + Sleep calls!
	// Total billed across ALL concurrent calls must equal EXACTLY 14m = $0.035 (35,000 micros) — ZERO double-billing!
	advance(4 * time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = mgr.RunJanitorPass(ctx)
		}()
		go func() {
			defer wg.Done()
			_ = mgr.Sleep(ctx, "u-meter")
		}()
	}
	wg.Wait()

	rows = store.MeteredUsage("u-meter")
	var totalMicros int64
	for _, r := range rows {
		if r.CostUSD != nil {
			totalMicros += billing.USDToMicros(*r.CostUSD)
		}
	}
	if totalMicros != 35_000 {
		t.Fatalf("total billed micros across concurrent janitor+Sleep = %d, want exactly 35000 (14m @ $0.15/hr)", totalMicros)
	}
	// Global budget tables must match exactly.
	gbAll, _ := bs.GetGlobalBudget(ctx, nowFn(), billing.ScopeHostedAll)
	gbSprites, _ := bs.GetGlobalBudget(ctx, nowFn(), billing.ScopeHostedSprites)
	if gbAll.SpentMicros != 35_000 || gbSprites.SpentMicros != 35_000 {
		t.Fatalf("global budget mismatch: hosted_all=%d hosted_sprites=%d, want 35000", gbAll.SpentMicros, gbSprites.SpentMicros)
	}

	// 4. Stay asleep for 3 HOURS, then wake and advance 6m: sleep time must NOT be billed!
	advance(3 * time.Hour)
	if _, err := mgr.Wake(ctx, "u-meter"); err != nil {
		t.Fatalf("Wake after 3h sleep: %v", err)
	}
	advance(6 * time.Minute)
	if err := mgr.Sleep(ctx, "u-meter"); err != nil {
		t.Fatalf("Sleep after 6m wake: %v", err)
	}
	rows = store.MeteredUsage("u-meter")
	totalMicros = 0
	for _, r := range rows {
		if r.CostUSD != nil {
			totalMicros += billing.USDToMicros(*r.CostUSD)
		}
	}
	// 14m + 6m = 20m = $0.05 = 50,000 micros (NOT 3h20m!).
	if totalMicros != 50_000 {
		t.Fatalf("total billed micros after 3h sleep + 6m wake = %d, want 50000 (20m total)", totalMicros)
	}
}

func TestManagerIdleAutoSleepAndActivityTouch(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	fp := fake.New()
	fp.Now = nowFn
	store := computer.NewMemoryStore()
	mgr := computer.NewManager(fp, store, computer.Config{
		RatePerHourUSD: 0.15,
		IdleAutoSleep:  15 * time.Minute,
	})
	mgr.SetClock(nowFn)

	if _, err := mgr.Ensure(ctx, "u-idle"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Advance 10m and run a command -> refreshes last_activity_at.
	now = now.Add(10 * time.Minute)
	if _, err := mgr.Exec(ctx, "u-idle", computer.ExecRequest{Command: "echo active"}); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// Advance 10m (20m total awake, but only 10m since last activity) -> stays awake.
	now = now.Add(10 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("RunJanitorPass active: %v", err)
	}
	rec, _ := store.GetComputer(ctx, "u-idle")
	if rec.Status != computer.StatusReady {
		t.Fatalf("status at 10m idle = %q, want ready", rec.Status)
	}

	// Advance 5 more minutes (15m since last activity) -> auto-sleeps!
	now = now.Add(5 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("RunJanitorPass idle: %v", err)
	}
	rec, _ = store.GetComputer(ctx, "u-idle")
	if rec.Status != computer.StatusSleeping || rec.SleepingSince == nil || rec.AwakeSince != nil {
		t.Fatalf("expected sleeping after 15m idle, got %+v", rec)
	}
}

func TestManagerReaperSuspendedInactiveExhaustedAndForceSleep(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	fp := fake.New()
	fp.Now = nowFn
	store := computer.NewMemoryStore()
	bs, bsvc := setupBillingForTest(t, nowFn, store)

	mgr := computer.NewManager(fp, store, computer.Config{RatePerHourUSD: 0.15})
	mgr.SetClock(nowFn)
	mgr.SetBilling(bsvc)

	// Missing entitlement fails closed on Ensure.
	if _, err := mgr.Ensure(ctx, "u-missing"); !errors.Is(err, billing.ErrEntitlementMissing) {
		t.Fatalf("Ensure missing entitlement = %v, want ErrEntitlementMissing", err)
	}

	// Setup 3 users: suspended, inactive (past_due), credit_exhausted.
	_, _ = bs.SetTier(ctx, "u-susp", billing.TierStandard, billing.SetTierOptions{})
	_, _ = bs.SetTier(ctx, "u-pastdue", billing.TierStandard, billing.SetTierOptions{})
	smallCredit := int64(10_000) // $0.01
	_, _ = bs.SetTier(ctx, "u-exhaust", billing.TierStandard, billing.SetTierOptions{CreditOverrideMicros: &smallCredit})

	for _, uid := range []string{"u-susp", "u-pastdue", "u-exhaust"} {
		if _, err := mgr.Ensure(ctx, uid); err != nil {
			t.Fatalf("Ensure %s: %v", uid, err)
		}
	}

	// Suspend u-susp, mark u-pastdue past_due, and drain u-exhaust via 6m awake ($0.015 > $0.01).
	_, _ = bsvc.Suspend(ctx, "u-susp", "abuse")
	_, _ = bs.SetTier(ctx, "u-pastdue", billing.TierStandard, billing.SetTierOptions{Status: billing.StatusPastDue, SkipGrant: true})

	// Fail closed on Ensure immediately for suspended and past_due.
	if _, err := mgr.Ensure(ctx, "u-susp"); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("Ensure suspended = %v, want ErrAccountSuspended", err)
	}
	if _, err := mgr.Ensure(ctx, "u-pastdue"); !errors.Is(err, billing.ErrSubscriptionInactive) {
		t.Fatalf("Ensure past_due = %v, want ErrSubscriptionInactive", err)
	}

	// Register a desktop WS cancel hook on u-susp to verify ForceSleepUser / reaper closes it.
	wsClosed := false
	mgr.RegisterDesktopSession("u-susp", func() { wsClosed = true })

	now = now.Add(6 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("RunJanitorPass reaper: %v", err)
	}
	if !wsClosed {
		t.Fatal("expected reaper to close active desktop WS for suspended user")
	}
	for _, uid := range []string{"u-susp", "u-pastdue", "u-exhaust"} {
		rec, err := store.GetComputer(ctx, uid)
		if err != nil || rec.Status != computer.StatusSleeping {
			t.Fatalf("user %s after reaper = (%+v, %v), want sleeping", uid, rec, err)
		}
	}
	// Exhausted user can no longer wake.
	if _, err := mgr.Ensure(ctx, "u-exhaust"); !errors.Is(err, billing.ErrCreditExhausted) {
		t.Fatalf("Ensure exhausted = %v, want ErrCreditExhausted", err)
	}

	// ForceSleepUser on active user immediately closes WS and sleeps Sprite.
	_, _ = bs.SetTier(ctx, "u-force", billing.TierPower, billing.SetTierOptions{})
	if _, err := mgr.Ensure(ctx, "u-force"); err != nil {
		t.Fatalf("Ensure u-force: %v", err)
	}
	forceWSClosed := false
	mgr.RegisterDesktopSession("u-force", func() { forceWSClosed = true })
	now = now.Add(2 * time.Minute)
	if err := mgr.ForceSleepUser(ctx, "u-force"); err != nil {
		t.Fatalf("ForceSleepUser: %v", err)
	}
	if !forceWSClosed {
		t.Fatal("expected ForceSleepUser to close desktop WS immediately")
	}
	rec, _ := store.GetComputer(ctx, "u-force")
	if rec.Status != computer.StatusSleeping {
		t.Fatalf("u-force status = %q, want sleeping", rec.Status)
	}
	// ForceSleepUser on user with no computer is a clean no-op.
	if err := mgr.ForceSleepUser(ctx, "u-nonexistent"); err != nil {
		t.Fatalf("ForceSleepUser nonexistent = %v, want nil", err)
	}
}

func TestManagerFlyStatusReconciliationNoPhantomBilling(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	fp := fake.New()
	fp.Now = nowFn
	store := computer.NewMemoryStore()
	mgr := computer.NewManager(fp, store, computer.Config{
		RatePerHourUSD: 0.15,
		IdleAutoSleep:  30 * time.Minute,
	})
	mgr.SetClock(nowFn)

	rec, err := mgr.Ensure(ctx, "u-recon")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Sprite ran until 12:04 (lastActive on provider = 12:04), then Fly stopped it on its own.
	now = now.Add(4 * time.Minute)
	if _, err := mgr.Exec(ctx, "u-recon", computer.ExecRequest{Command: "echo work"}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	fp.SetStatus(rec.ProviderID, computer.StatusSleeping)

	// Janitor runs 20 minutes later at 12:24 — must reconcile to sleeping and bill ONLY 4m ($0.01 = 10,000 micros),
	// NOT 24m ($0.06)!
	now = now.Add(20 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("RunJanitorPass reconcile: %v", err)
	}
	after, _ := store.GetComputer(ctx, "u-recon")
	if after.Status != computer.StatusSleeping || after.AwakeSince != nil {
		t.Fatalf("expected reconciled sleeping state, got %+v", after)
	}
	rows := store.MeteredUsage("u-recon")
	var totalMicros int64
	for _, r := range rows {
		if r.CostUSD != nil {
			totalMicros += billing.USDToMicros(*r.CostUSD)
		}
	}
	if totalMicros != 10_000 {
		t.Fatalf("reconciled billed micros = %d, want 10000 (4m only, zero phantom time)", totalMicros)
	}

	// Subsequent janitor passes bill NOTHING.
	now = now.Add(30 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("RunJanitorPass #2: %v", err)
	}
	if len(store.MeteredUsage("u-recon")) != len(rows) {
		t.Fatalf("expected no further billing after reconciliation")
	}
}

func TestManagerAwakeOnlyFleetCapAndTierCheckpointCap(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	fp := fake.New()
	fp.Now = nowFn
	store := computer.NewMemoryStore()
	bs, bsvc := setupBillingForTest(t, nowFn, store)

	mgr := computer.NewManager(fp, store, computer.Config{
		MaxFleet: 2,
	})
	mgr.SetClock(nowFn)
	mgr.SetBilling(bsvc)

	_, _ = bs.SetTier(ctx, "u-trial", billing.TierTrial, billing.SetTierOptions{})
	_, _ = bs.SetTier(ctx, "u-std", billing.TierStandard, billing.SetTierOptions{})
	_, _ = bs.SetTier(ctx, "u-pwr", billing.TierPower, billing.SetTierOptions{})

	// 1. Awake-only fleet cap = 2: first 2 users wake cleanly, 3rd hits ErrFleetCapReached.
	if _, err := mgr.Ensure(ctx, "u-trial"); err != nil {
		t.Fatalf("Ensure u-trial: %v", err)
	}
	if _, err := mgr.Ensure(ctx, "u-std"); err != nil {
		t.Fatalf("Ensure u-std: %v", err)
	}
	if _, err := mgr.Ensure(ctx, "u-pwr"); !errors.Is(err, computer.ErrFleetCapReached) {
		t.Fatalf("Ensure 3rd awake user = %v, want ErrFleetCapReached", err)
	}

	// Put u-std to sleep -> sleeping Sprite does NOT count toward awake fleet cap, so u-pwr succeeds!
	if err := mgr.Sleep(ctx, "u-std"); err != nil {
		t.Fatalf("Sleep u-std: %v", err)
	}
	if _, err := mgr.Ensure(ctx, "u-pwr"); err != nil {
		t.Fatalf("Ensure u-pwr after u-std slept: %v", err)
	}

	// 2. Per-tier MaxSpriteCheckpoints: trial allows 1 user checkpoint (+ baseline), 2nd rejected.
	if _, err := mgr.Checkpoint(ctx, "u-trial", "snap-1"); err != nil {
		t.Fatalf("trial checkpoint #1: %v", err)
	}
	if _, err := mgr.Checkpoint(ctx, "u-trial", "snap-2"); !errors.Is(err, computer.ErrCheckpointLimitReached) {
		t.Fatalf("trial checkpoint #2 = %v, want ErrCheckpointLimitReached", err)
	}

	// Power allows 10 user checkpoints, 11th rejected.
	for i := 1; i <= 10; i++ {
		if _, err := mgr.Checkpoint(ctx, "u-pwr", "snap"); err != nil {
			t.Fatalf("power checkpoint #%d: %v", i, err)
		}
	}
	if _, err := mgr.Checkpoint(ctx, "u-pwr", "snap-11"); !errors.Is(err, computer.ErrCheckpointLimitReached) {
		t.Fatalf("power checkpoint #11 = %v, want ErrCheckpointLimitReached", err)
	}
}

type stubLeaderLeaseStore struct {
	docstore.LeaseStore
	mu         sync.Mutex
	now        func() time.Time
	exists     bool
	runnable   bool
	owner      string
	leaseUntil time.Time
}

func (s *stubLeaderLeaseStore) ListUserWakes(_ context.Context, _ string) ([]docstore.UserWake, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.exists {
		return nil, nil
	}
	u := s.leaseUntil
	return []docstore.UserWake{{UserID: "__computer_janitor__", Lane: "computer_janitor", Runnable: s.runnable, Owner: s.owner, LeaseUntil: &u}}, nil
}

func (s *stubLeaderLeaseStore) WakeUser(_ context.Context, _, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exists = true
	s.runnable = true
	return nil
}

func (s *stubLeaderLeaseStore) ClaimUserWake(_ context.Context, _, owner string, lease time.Duration) (docstore.UserWake, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.exists || !s.runnable {
		return docstore.UserWake{}, false, nil
	}
	if s.owner != "" && s.leaseUntil.After(now) {
		return docstore.UserWake{}, false, nil
	}
	s.owner = owner
	s.leaseUntil = now.Add(lease)
	u := s.leaseUntil
	return docstore.UserWake{Owner: owner, LeaseUntil: &u}, true, nil
}

func (s *stubLeaderLeaseStore) RenewUserWake(_ context.Context, _, _, owner string, lease time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.owner == owner && s.leaseUntil.After(now) {
		s.leaseUntil = now.Add(lease)
		return true, nil
	}
	return false, nil
}

func (s *stubLeaderLeaseStore) ReleaseUserWake(_ context.Context, _, _, owner string, _ int64, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == owner {
		s.owner = ""
		s.leaseUntil = time.Time{}
	}
	return nil
}

func TestManagerJanitorLeaderElection(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	fp := fake.New()
	fp.Now = nowFn
	prov1 := &countingStatusProvider{Provider: fp}
	prov2 := &countingStatusProvider{Provider: fp}
	store := computer.NewMemoryStore()
	leases := &stubLeaderLeaseStore{now: nowFn}

	m1 := computer.NewManager(prov1, store, computer.Config{JanitorInterval: time.Minute})
	m1.SetClock(nowFn)
	m1.SetLeases(leases)
	m2 := computer.NewManager(prov2, store, computer.Config{JanitorInterval: time.Minute})
	m2.SetClock(nowFn)
	m2.SetLeases(leases)

	if _, err := m1.Ensure(ctx, "u-lead"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	prov1.statusCalls.Store(0)
	prov2.statusCalls.Store(0)

	// m1 wins leadership and runs the pass; m2 sees lease held by m1 and skips completely!
	now = now.Add(2 * time.Minute)
	if err := m1.RunJanitorPass(ctx); err != nil {
		t.Fatalf("m1 RunJanitorPass: %v", err)
	}
	if err := m2.RunJanitorPass(ctx); err != nil {
		t.Fatalf("m2 RunJanitorPass: %v", err)
	}
	if prov1.statusCalls.Load() != 1 || prov2.statusCalls.Load() != 0 {
		t.Fatalf("statusCalls m1=%d m2=%d, want 1 and 0 (single leader)", prov1.statusCalls.Load(), prov2.statusCalls.Load())
	}

	// After lease expires without renewal (m1 crashed), m2 takes over leadership.
	now = now.Add(3 * time.Minute)
	if err := m2.RunJanitorPass(ctx); err != nil {
		t.Fatalf("m2 failover RunJanitorPass: %v", err)
	}
	if prov2.statusCalls.Load() != 1 {
		t.Fatalf("m2 statusCalls after failover = %d, want 1", prov2.statusCalls.Load())
	}
}

type failingRestoreProvider struct {
	computer.Provider
	restoreCalls atomic.Int32
	failRestore  error
}

func (p *failingRestoreProvider) Restore(ctx context.Context, providerID, checkpointID string) error {
	p.restoreCalls.Add(1)
	if p.failRestore != nil {
		return p.failRestore
	}
	return p.Provider.Restore(ctx, providerID, checkpointID)
}

func TestRestoreAndResetEnforceFleetCap(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	fp := fake.New()
	fp.Now = nowFn
	prov := &failingRestoreProvider{Provider: fp}
	store := computer.NewMemoryStore()
	store.Now = nowFn
	mgr := computer.NewManager(prov, store, computer.Config{
		MaxFleet:       1,
		RatePerHourUSD: 0.15,
	})
	mgr.SetClock(nowFn)

	// Provision user-b with a baseline checkpoint, then put user-b to sleep.
	recB, err := mgr.Ensure(ctx, "user-b")
	if err != nil {
		t.Fatalf("Ensure user-b: %v", err)
	}
	if recB.BaselineCheckpointID == "" {
		t.Fatal("expected user-b baseline checkpoint")
	}
	if err := mgr.Sleep(ctx, "user-b"); err != nil {
		t.Fatalf("Sleep user-b: %v", err)
	}

	// Fill the 1-slot fleet with user-a.
	if _, err := mgr.Ensure(ctx, "user-a"); err != nil {
		t.Fatalf("Ensure user-a: %v", err)
	}

	// Sleeping user-b MUST fail Restore with ErrFleetCapReached without calling provider.Restore!
	if err := mgr.Restore(ctx, "user-b", recB.BaselineCheckpointID); !errors.Is(err, computer.ErrFleetCapReached) {
		t.Fatalf("Restore sleeping user-b with full fleet = %v, want ErrFleetCapReached", err)
	}
	if prov.restoreCalls.Load() != 0 {
		t.Fatalf("provider.Restore called %d times despite full fleet, want 0", prov.restoreCalls.Load())
	}

	// Sleeping user-b MUST also fail Reset with ErrFleetCapReached!
	if _, err := mgr.Reset(ctx, "user-b"); !errors.Is(err, computer.ErrFleetCapReached) {
		t.Fatalf("Reset sleeping user-b with full fleet = %v, want ErrFleetCapReached", err)
	}
	if prov.restoreCalls.Load() != 0 {
		t.Fatalf("provider.Restore called %d times on Reset despite full fleet, want 0", prov.restoreCalls.Load())
	}

	// Verify user-b remained sleeping.
	gotB, err := store.GetComputer(ctx, "user-b")
	if err != nil || gotB.Status != computer.StatusSleeping {
		t.Fatalf("user-b status = (%+v, %v), want sleeping", gotB, err)
	}

	// Once user-a sleeps, fleet slot opens and user-b Restore + Reset succeed.
	if err := mgr.Sleep(ctx, "user-a"); err != nil {
		t.Fatalf("Sleep user-a: %v", err)
	}
	if err := mgr.Restore(ctx, "user-b", recB.BaselineCheckpointID); err != nil {
		t.Fatalf("Restore user-b after slot freed: %v", err)
	}
	if _, err := mgr.Reset(ctx, "user-b"); err != nil {
		t.Fatalf("Reset user-b after slot freed: %v", err)
	}
	if prov.restoreCalls.Load() != 2 {
		t.Fatalf("provider.Restore calls = %d, want 2", prov.restoreCalls.Load())
	}
}

func TestStatusErrorMetersAwakeTime(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	fp := fake.New()
	fp.Now = nowFn
	prov := &failingRestoreProvider{Provider: fp, failRestore: errors.New("restore boom")}
	store := computer.NewMemoryStore()
	store.Now = nowFn
	mgr := computer.NewManager(prov, store, computer.Config{
		RatePerHourUSD: 0.15,
	})
	mgr.SetClock(nowFn)

	rec, err := mgr.Ensure(ctx, "user-err")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Advance 1 hour of awake time, then trigger a restore failure -> StatusError.
	now = now.Add(time.Hour)
	if err := mgr.Restore(ctx, "user-err", rec.BaselineCheckpointID); err == nil {
		t.Fatal("expected Restore to fail")
	}

	got, err := store.GetComputer(ctx, "user-err")
	if err != nil || got.Status != computer.StatusError || got.AwakeSince != nil {
		t.Fatalf("after failed Restore, rec = (%+v, %v), want status=error awake_since=nil", got, err)
	}
	usage := store.MeteredUsage("user-err")
	if len(usage) != 1 || usage[0].CostUSD == nil || *usage[0].CostUSD < 0.149 || *usage[0].CostUSD > 0.151 {
		t.Fatalf("expected 1h awake time ($0.15) metered before StatusError, got %+v", usage)
	}
}
