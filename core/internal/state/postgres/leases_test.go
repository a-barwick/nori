package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"nori/internal/state/docstore"
)

func openTestBackend(t *testing.T) (*backend, string) {
	t.Helper()
	url := os.Getenv("NORI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NORI_TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	b, err := openBackend(ctx, url, Options{})
	if err != nil {
		t.Fatalf("openBackend: %v", err)
	}
	prefix := fmt.Sprintf("test-%d-", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = b.pool.Exec(cleanCtx, "DELETE FROM nori_task_leases WHERE user_id LIKE $1", prefix+"%")
		_, _ = b.pool.Exec(cleanCtx, "DELETE FROM nori_user_wakes WHERE user_id LIKE $1", prefix+"%")
		_, _ = b.pool.Exec(cleanCtx, "DELETE FROM nori_records WHERE collection LIKE $1", "owners/"+prefix+"%")
		_ = b.Close()
	})
	return b, prefix
}

func TestPostgresTaskLeaseLifecycleAndWakeSeqRace(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx := context.Background()
	userID := prefix + "u1"
	runID := "run-1"

	// WakeTask creates runnable row with wake_seq = 1.
	if err := b.WakeTask(ctx, userID, runID, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatalf("WakeTask: %v", err)
	}

	// ClaimTaskByID claims it without modifying wake_seq.
	claimed, ok, err := b.ClaimTaskByID(ctx, userID, runID, "worker-1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimTaskByID = (%+v, %v, %v), want ok=true", claimed, ok, err)
	}
	if claimed.WakeSeq != 1 || claimed.Owner != "worker-1" || claimed.LeaseUntil == nil {
		t.Fatalf("unexpected claimed lease: %+v", claimed)
	}

	// Competing worker cannot claim while lease is held.
	if _, ok2, err := b.ClaimTaskByID(ctx, userID, runID, "worker-2", 30*time.Second); err != nil || ok2 {
		t.Fatalf("competing ClaimTaskByID = (%v, %v), want ok=false", ok2, err)
	}

	// RenewTaskLeases extends for matching owner only.
	n, err := b.RenewTaskLeases(ctx, "worker-1", []docstore.TaskKey{{UserID: userID, RunID: runID}}, 45*time.Second)
	if err != nil || n != 1 {
		t.Fatalf("RenewTaskLeases owner = (%d, %v), want 1", n, err)
	}
	nWrong, err := b.RenewTaskLeases(ctx, "worker-2", []docstore.TaskKey{{UserID: userID, RunID: runID}}, 45*time.Second)
	if err != nil || nWrong != 0 {
		t.Fatalf("RenewTaskLeases wrong owner = (%d, %v), want 0", nWrong, err)
	}

	// Concurrent wake while owned: preserves owner/lease_until and bumps wake_seq to 2.
	if err := b.WakeTask(ctx, userID, runID, time.Now().UTC()); err != nil {
		t.Fatalf("concurrent WakeTask: %v", err)
	}

	// ParkTask with stale wake_seq=1 MUST fail (parked=false) so wake is never lost!
	parked, err := b.ParkTask(ctx, userID, runID, "worker-1", 1, docstore.WaitQuestion)
	if err != nil || parked {
		t.Fatalf("stale ParkTask = (%v, %v), want (false, nil)", parked, err)
	}

	// RefreshTaskClaim re-reads wake_seq=2 while preserving ownership.
	refreshed, ok, err := b.RefreshTaskClaim(ctx, userID, runID, "worker-1", 30*time.Second)
	if err != nil || !ok || refreshed.WakeSeq != 2 || refreshed.Owner != "worker-1" {
		t.Fatalf("RefreshTaskClaim = (%+v, %v, %v), want wake_seq=2 owner=worker-1", refreshed, ok, err)
	}

	// ParkTask with fresh wake_seq=2 succeeds.
	parked, err = b.ParkTask(ctx, userID, runID, "worker-1", 2, docstore.WaitQuestion)
	if err != nil || !parked {
		t.Fatalf("fresh ParkTask = (%v, %v), want (true, nil)", parked, err)
	}

	// WakeWaitingTasks wakes parked task (wake_seq -> 3).
	if err := b.WakeWaitingTasks(ctx, userID, docstore.WaitQuestion); err != nil {
		t.Fatalf("WakeWaitingTasks: %v", err)
	}
	leases, err := b.ListTaskLeases(ctx, userID)
	if err != nil || len(leases) != 1 || !leases[0].Runnable || leases[0].WaitingReason != "" || leases[0].WakeSeq != 3 {
		t.Fatalf("ListTaskLeases after WakeWaitingTasks = (%+v, %v)", leases, err)
	}

	// ReleaseTask with future nextWake: pushes wake_at when wake_seq matches.
	claimed, ok, err = b.ClaimTaskByID(ctx, userID, runID, "worker-1", 30*time.Second)
	if err != nil || !ok || claimed.WakeSeq != 3 {
		t.Fatalf("re-claim = (%+v, %v, %v)", claimed, ok, err)
	}
	future := time.Now().UTC().Add(time.Hour)
	if err := b.ReleaseTask(ctx, userID, runID, "worker-1", 3, future); err != nil {
		t.Fatalf("ReleaseTask matching wake_seq: %v", err)
	}
	if _, ok, err := b.ClaimTaskByID(ctx, userID, runID, "worker-2", 30*time.Second); err != nil || ok {
		t.Fatalf("claim before future nextWake = (%v, %v), want false", ok, err)
	}

	// ReleaseTask when wake_seq changed concurrently does NOT push wake_at forward.
	if err := b.WakeTask(ctx, userID, runID, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatalf("WakeTask immediate: %v", err)
	}
	claimed, ok, err = b.ClaimTaskByID(ctx, userID, runID, "worker-1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim for concurrent release test = (%+v, %v, %v)", claimed, ok, err)
	}
	if err := b.WakeTask(ctx, userID, runID, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatalf("concurrent WakeTask before release: %v", err)
	}
	if err := b.ReleaseTask(ctx, userID, runID, "worker-1", claimed.WakeSeq, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("ReleaseTask stale wake_seq: %v", err)
	}
	claimed2, ok, err := b.ClaimTaskByID(ctx, userID, runID, "worker-2", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("expected task to stay immediately claimable after stale ReleaseTask; ok=%v err=%v", ok, err)
	}

	// DeleteTaskLease: guarded rejects stale wake_seq, accepts matching wake_seq; unconditional deletes any.
	deleted, err := b.DeleteTaskLease(ctx, userID, runID, claimed2.WakeSeq-1)
	if err != nil || deleted {
		t.Fatalf("stale DeleteTaskLease = (%v, %v), want false", deleted, err)
	}
	deleted, err = b.DeleteTaskLease(ctx, userID, runID, claimed2.WakeSeq)
	if err != nil || !deleted {
		t.Fatalf("matching DeleteTaskLease = (%v, %v), want true", deleted, err)
	}

	// EnsureTaskLease inserts once and is idempotent on conflict.
	created, err := b.EnsureTaskLease(ctx, userID, runID, time.Now().UTC())
	if err != nil || !created {
		t.Fatalf("EnsureTaskLease first = (%v, %v), want true", created, err)
	}
	created, err = b.EnsureTaskLease(ctx, userID, runID, time.Now().UTC())
	if err != nil || created {
		t.Fatalf("EnsureTaskLease second = (%v, %v), want false", created, err)
	}
	deleted, err = b.DeleteTaskLease(ctx, userID, runID, 0)
	if err != nil || !deleted {
		t.Fatalf("unconditional DeleteTaskLease = (%v, %v), want true", deleted, err)
	}
}

func TestPostgresUserWakeLifecycle(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx := context.Background()
	userID := prefix + "u-conv"

	// Use an older wake_at so ClaimUserWake picks this test's user first even on a shared local dev DB.
	if err := b.WakeUser(ctx, userID, docstore.LaneConversation, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("WakeUser: %v", err)
	}
	claimed, ok, err := b.ClaimUserWake(ctx, docstore.LaneConversation, "outer-1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimUserWake = (%+v, %v, %v)", claimed, ok, err)
	}
	if claimed.UserID != userID || claimed.WakeSeq != 1 || claimed.Owner != "outer-1" {
		t.Fatalf("unexpected claimed user wake: %+v", claimed)
	}

	renewed, err := b.RenewUserWake(ctx, userID, docstore.LaneConversation, "outer-1", 40*time.Second)
	if err != nil || !renewed {
		t.Fatalf("RenewUserWake = (%v, %v), want true", renewed, err)
	}

	// Concurrent WakeUser bumps wake_seq -> ParkUserWake with stale seq returns false.
	if err := b.WakeUser(ctx, userID, docstore.LaneConversation, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatalf("concurrent WakeUser: %v", err)
	}
	parked, err := b.ParkUserWake(ctx, userID, docstore.LaneConversation, "outer-1", 1)
	if err != nil || parked {
		t.Fatalf("stale ParkUserWake = (%v, %v), want false", parked, err)
	}

	// ReleaseUserWake with stale wake_seq preserves earlier concurrent wake_at via LEAST.
	if err := b.ReleaseUserWake(ctx, userID, docstore.LaneConversation, "outer-1", 1, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("ReleaseUserWake stale: %v", err)
	}
	wakes, err := b.ListUserWakes(ctx, userID)
	if err != nil || len(wakes) != 1 {
		t.Fatalf("ListUserWakes = (%+v, %v)", wakes, err)
	}
	if wakes[0].WakeAt.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("ReleaseUserWake clobbered earlier concurrent wake_at: %v", wakes[0].WakeAt)
	}

	// Re-claim and park cleanly with fresh wake_seq=2.
	claimed, ok, err = b.ClaimUserWake(ctx, docstore.LaneConversation, "outer-2", 30*time.Second)
	if err != nil || !ok || claimed.WakeSeq != 2 {
		t.Fatalf("re-claim user wake = (%+v, %v, %v)", claimed, ok, err)
	}
	parked, err = b.ParkUserWake(ctx, userID, docstore.LaneConversation, "outer-2", 2)
	if err != nil || !parked {
		t.Fatalf("fresh ParkUserWake = (%v, %v), want true", parked, err)
	}
}

func startWakeListener(t *testing.T, ctx context.Context, b *backend, buf int) (<-chan docstore.WakeEvent, context.CancelFunc, <-chan error) {
	t.Helper()
	listenCtx, cancelListen := context.WithCancel(ctx)
	eventsCh := make(chan docstore.WakeEvent, buf)
	ready := make(chan struct{})
	listenDone := make(chan error, 1)
	go func() {
		listenDone <- b.ListenWakesReady(listenCtx, func(ev docstore.WakeEvent) {
			eventsCh <- ev
		}, ready)
	}()
	select {
	case <-ready:
	case err := <-listenDone:
		cancelListen()
		t.Fatalf("ListenWakesReady exited before subscribing: %v", err)
	case <-time.After(3 * time.Second):
		cancelListen()
		t.Fatalf("timed out waiting for ListenWakes subscription")
	}
	return eventsCh, cancelListen, listenDone
}

func TestListenWakesReadyErrorsWithoutDB(t *testing.T) {
	t.Run("missing_url", func(t *testing.T) {
		b := &backend{}
		ready := make(chan struct{})
		if err := b.ListenWakesReady(context.Background(), nil, ready); err == nil {
			t.Fatalf("ListenWakesReady with empty URL = nil, want error")
		}
		select {
		case <-ready:
			t.Fatalf("ready channel closed despite missing URL")
		default:
		}
	})

	t.Run("canceled_context", func(t *testing.T) {
		b := &backend{connURL: "postgres://127.0.0.1:1/nori"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ready := make(chan struct{})
		if err := b.ListenWakesReady(ctx, nil, ready); !errors.Is(err, context.Canceled) {
			t.Fatalf("ListenWakesReady with canceled ctx = %v, want context.Canceled", err)
		}
		select {
		case <-ready:
			t.Fatalf("ready channel closed despite canceled ctx")
		default:
		}
	})
}

func TestPostgresSameTxWakeRollbackVsCommitAndListenNotify(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx := context.Background()
	userID := prefix + "u-listen"

	eventsCh, cancelListen, listenDone := startWakeListener(t, ctx, b, 64)
	defer cancelListen()

	// 1. Rolled-back transaction must NOT persist lease row OR emit pg_notify.
	rollbackErr := errors.New("intentional rollback")
	err := b.RunTransaction(ctx, false, 1, func(tb docstore.TransactionBackend) error {
		w := tb.(docstore.WakeWriter)
		if err := w.WakeTask(ctx, userID, "run-rolled-back", time.Now().UTC()); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback tx err = %v, want rollbackErr", err)
	}
	leases, err := b.ListTaskLeases(ctx, userID)
	if err != nil || len(leases) != 0 {
		t.Fatalf("rolled-back tx persisted leases: %+v, err=%v", leases, err)
	}
	// Intentional short negative-assertion window confirming rollback emitted no pg_notify.
	select {
	case ev := <-eventsCh:
		if ev.Payload == docstore.TaskWakePayload(userID, "run-rolled-back") {
			t.Fatalf("rolled-back tx emitted pg_notify: %+v", ev)
		}
	case <-time.After(75 * time.Millisecond):
	}

	// 2. Committed transaction persists rows and emits post-commit pg_notify (skipping future WakeUser).
	err = b.RunTransaction(ctx, false, 1, func(tb docstore.TransactionBackend) error {
		w := tb.(docstore.WakeWriter)
		if err := w.WakeTask(ctx, userID, "run-committed", time.Now().UTC()); err != nil {
			return err
		}
		if err := w.WakeUser(ctx, userID, docstore.LaneConversation, time.Now().UTC()); err != nil {
			return err
		}
		if err := w.WakeUser(ctx, userID, docstore.LaneScheduler, time.Now().UTC().Add(time.Hour)); err != nil {
			return err
		}
		return w.NotifyPause(ctx)
	})
	if err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	want := map[string]bool{
		docstore.TaskWakePayload(userID, "run-committed"):           false,
		docstore.UserWakePayload(userID, docstore.LaneConversation): false,
		docstore.WakePayloadPause:                                   false,
	}
	forbidden := docstore.UserWakePayload(userID, docstore.LaneScheduler)
	deadline := time.After(3 * time.Second)
	remaining := len(want)
	for remaining > 0 {
		select {
		case ev := <-eventsCh:
			if ev.Payload == forbidden {
				t.Fatalf("future WakeUser unexpectedly emitted immediate pg_notify: %+v", ev)
			}
			if seen, ok := want[ev.Payload]; ok && !seen {
				want[ev.Payload] = true
				remaining--
			}
		case <-deadline:
			t.Fatalf("timed out waiting for post-commit pg_notify; seen=%+v", want)
		}
	}

	// 3. WakeWaitingTasks fanout: >32 tasks for one user emits t:<user>:*; global unpause emits t:*.
	bulkUser := prefix + "u-bulk"
	for i := 0; i < 33; i++ {
		rid := fmt.Sprintf("r-%02d", i)
		if err := b.WakeTask(ctx, bulkUser, rid, time.Now().UTC().Add(-time.Second)); err != nil {
			t.Fatalf("WakeTask %s: %v", rid, err)
		}
		claimed, ok, err := b.ClaimTaskByID(ctx, bulkUser, rid, "bulk-owner", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID %s: ok=%v err=%v", rid, ok, err)
		}
		if parked, err := b.ParkTask(ctx, bulkUser, rid, "bulk-owner", claimed.WakeSeq, docstore.WaitRoutingPending); err != nil || !parked {
			t.Fatalf("ParkTask %s: parked=%v err=%v", rid, parked, err)
		}
	}
	if err := b.WakeWaitingTasks(ctx, bulkUser, docstore.WaitRoutingPending); err != nil {
		t.Fatalf("WakeWaitingTasks bulkUser: %v", err)
	}
	wantBulk := docstore.UserTasksWakePayload(bulkUser)
	deadline = time.After(3 * time.Second)
	for {
		select {
		case ev := <-eventsCh:
			if ev.Payload == wantBulk {
				goto bulkOK
			}
		case <-deadline:
			t.Fatalf("timed out waiting for bulk wake payload %q", wantBulk)
		}
	}
bulkOK:

	// Global unpause (userID == "") emits t:*.
	claimed, ok, err := b.ClaimTaskByID(ctx, bulkUser, "r-00", "pause-owner", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimTaskByID r-00: ok=%v err=%v", ok, err)
	}
	if parked, err := b.ParkTask(ctx, bulkUser, "r-00", "pause-owner", claimed.WakeSeq, docstore.WaitPaused); err != nil || !parked {
		t.Fatalf("ParkTask r-00 paused: parked=%v err=%v", parked, err)
	}
	if err := b.WakeWaitingTasks(ctx, "", docstore.WaitPaused); err != nil {
		t.Fatalf("WakeWaitingTasks global unpause: %v", err)
	}
	deadline = time.After(3 * time.Second)
	for {
		select {
		case ev := <-eventsCh:
			if ev.Payload == docstore.WakePayloadAllTask {
				goto globalOK
			}
		case <-deadline:
			t.Fatalf("timed out waiting for global wake payload %q", docstore.WakePayloadAllTask)
		}
	}
globalOK:

	cancelListen()
	<-listenDone
}

func appendQueryParam(rawURL, key, val string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(key, val)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func TestPostgresBatchedEmitWakesSingleRoundTrip(t *testing.T) {
	b, counter, prefix := openTracedTestBackend(t, Options{})
	ctx := context.Background()

	eventsCh, cancelListen, listenDone := startWakeListener(t, ctx, b, 32)
	defer cancelListen()

	counter.notifyExecs.Store(0)
	u := prefix + "batch"
	batch := []docstore.WakeEvent{
		{Channel: "", Payload: "ignored-empty-channel"},
		{Channel: docstore.WakeChannel, Payload: ""},
		{Channel: docstore.WakeChannel, Payload: docstore.TaskWakePayload(u, "r1")},
		{Channel: docstore.WakeChannel, Payload: docstore.TaskWakePayload(u, "r2")},
		{Channel: docstore.WakeChannel, Payload: docstore.TaskWakePayload(u, "r1")}, // duplicate
		{Channel: docstore.WakeChannel, Payload: docstore.UserWakePayload(u, docstore.LaneConversation)},
		{Channel: docstore.WakeChannel, Payload: docstore.UserWakePayload(u, docstore.LaneConversation)}, // duplicate
		{Channel: watchChannel, Payload: prefix + "/events"},
		{Channel: watchChannel, Payload: prefix + "/events"}, // duplicate
	}
	b.emitWakes(ctx, batch)

	if execs := counter.notifyExecs.Load(); execs != 1 {
		t.Fatalf("emitWakes SQL round-trips = %d, want 1 (batched unnest)", execs)
	}

	want := map[string]int{
		docstore.TaskWakePayload(u, "r1"):                      0,
		docstore.TaskWakePayload(u, "r2"):                      0,
		docstore.UserWakePayload(u, docstore.LaneConversation): 0,
	}
	remaining := len(want)
	waitDeadline := time.After(3 * time.Second)
	for remaining > 0 {
		select {
		case ev := <-eventsCh:
			if c, ok := want[ev.Payload]; ok {
				if c == 0 {
					remaining--
				}
				want[ev.Payload] = c + 1
			}
		case <-waitDeadline:
			t.Fatalf("timed out waiting for batched wakes: got=%+v", want)
		}
	}

	// Intentional short negative-assertion window confirming deduplicated payloads are not delivered twice.
	time.Sleep(50 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-eventsCh:
			if _, ok := want[ev.Payload]; ok {
				want[ev.Payload]++
			}
		default:
			break drain
		}
	}
	for payload, count := range want {
		if count != 1 {
			t.Fatalf("payload %q delivered %d times, want 1 (deduplicated)", payload, count)
		}
	}

	cancelListen()
	<-listenDone
}

func TestPostgresListenWakesDedicatedListenURLAndReconnectMetrics(t *testing.T) {
	baseURL := os.Getenv("NORI_TEST_POSTGRES_URL")
	if baseURL == "" {
		t.Skip("NORI_TEST_POSTGRES_URL not set")
	}

	origInit, origMax := listenInitialBackoff, listenMaxBackoff
	listenInitialBackoff = 20 * time.Millisecond
	listenMaxBackoff = 100 * time.Millisecond
	t.Cleanup(func() {
		listenInitialBackoff = origInit
		listenMaxBackoff = origMax
	})

	appName := fmt.Sprintf("nori_listen_url_test_%d", time.Now().UnixNano())
	listenURL, err := appendQueryParam(baseURL, "application_name", appName)
	if err != nil {
		t.Fatalf("appendQueryParam: %v", err)
	}

	b, _, prefix := openTracedTestBackend(t, Options{ListenURL: listenURL})
	ctx := context.Background()

	handler := &captureLogHandler{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	eventsCh, cancelListen, listenDone := startWakeListener(t, ctx, b, 32)
	defer cancelListen()

	// Verify ListenWakes actually connected using Options.ListenURL (matching application_name).
	var pid int
	if err := b.pool.QueryRow(ctx, `
		SELECT pid
		FROM pg_stat_activity
		WHERE datname = current_database()
		  AND application_name = $1
		  AND query ILIKE 'LISTEN nori_wake%'
		LIMIT 1
	`, appName).Scan(&pid); err != nil || pid == 0 {
		t.Fatalf("ListenWakes did not connect using ListenURL (application_name=%q not found in pg_stat_activity, err=%v)", appName, err)
	}

	// Terminate that exact LISTEN backend to trigger real reconnect + resync + metrics + slog recovery.
	if _, err := b.pool.Exec(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatalf("pg_terminate_backend(%d): %v", pid, err)
	}

	// Wait for synthetic resync event on reconnect.
	resyncDeadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-eventsCh:
			if ev.Channel == docstore.WakeChannel && ev.Payload == docstore.WakePayloadResync {
				goto gotResync
			}
		case <-resyncDeadline:
			t.Fatalf("timed out waiting for synthetic resync event after LISTEN disconnect")
		}
	}
gotResync:

	stats := b.ListenStats()
	if stats.WakeErrors < 1 || stats.WakeReconnects < 1 {
		t.Fatalf("ListenStats after wake reconnect = %+v, want WakeErrors>=1 and WakeReconnects>=1", stats)
	}

	// Verify live notifications still arrive after reconnect.
	u := prefix + "after-reconnect"
	if err := b.WakeTask(ctx, u, "r-post-reconnect", time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatalf("WakeTask after reconnect: %v", err)
	}
	wantPayload := docstore.TaskWakePayload(u, "r-post-reconnect")
	liveDeadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-eventsCh:
			if ev.Payload == wantPayload {
				goto gotLive
			}
		case <-liveDeadline:
			t.Fatalf("timed out waiting for live wake %q after reconnect", wantPayload)
		}
	}
gotLive:

	var sawWarn, sawRecovered bool
	for _, rec := range handler.snapshot() {
		if rec.level == slog.LevelWarn && strings.Contains(rec.msg, "wake listener error") {
			sawWarn = true
		}
		if rec.level == slog.LevelInfo && strings.Contains(rec.msg, "wake listener recovered") {
			sawRecovered = true
		}
	}
	if !sawWarn || !sawRecovered {
		t.Fatalf("expected structured slog warn + recovery logs; sawWarn=%v sawRecovered=%v", sawWarn, sawRecovered)
	}

	cancelListen()
	if err := <-listenDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("ListenWakes exit err = %v, want context.Canceled", err)
	}
}

func TestPostgresPostCommitWakeWaitingTasksRollbackAndConcurrentMVCC(t *testing.T) {
	b, _, prefix := openTestBackendWithPrefix(t)
	ctx := context.Background()
	userID := prefix + "mvcc-u"
	docPath := fmt.Sprintf("owners/%s/conversation/state", userID)

	if err := b.Apply(ctx, docstore.Mutation{
		Kind:   "create",
		Path:   docPath,
		Fields: map[string]any{"routing_pending": true},
	}); err != nil {
		t.Fatalf("seed conversation doc: %v", err)
	}

	// Park task r-parked waiting on routing_pending.
	if err := b.WakeTask(ctx, userID, "r-parked", time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatalf("WakeTask r-parked: %v", err)
	}
	claimedParked, ok, err := b.ClaimTaskByID(ctx, userID, "r-parked", "w-init", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimTaskByID r-parked: ok=%v err=%v", ok, err)
	}
	if parked, err := b.ParkTask(ctx, userID, "r-parked", "w-init", claimedParked.WakeSeq, docstore.WaitRoutingPending); err != nil || !parked {
		t.Fatalf("ParkTask r-parked: parked=%v err=%v", parked, err)
	}

	// Start listener to verify rollback emits NO wakes and commit emits wakes for both tasks.
	eventsCh, cancelListen, listenDone := startWakeListener(t, ctx, b, 32)
	defer cancelListen()

	// 1. Rollback safety: tx.WakeWaitingTasks in an aborted SERIALIZABLE tx must NOT wake r-parked!
	rollbackErr := errors.New("abort before commit")
	err = b.RunTransaction(ctx, false, 1, func(tb docstore.TransactionBackend) error {
		if _, err := tb.Get(ctx, docPath); err != nil {
			return err
		}
		if err := tb.(docstore.WakeWriter).WakeWaitingTasks(ctx, userID, docstore.WaitRoutingPending); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback tx err = %v, want rollbackErr", err)
	}
	leases, err := b.ListTaskLeases(ctx, userID)
	if err != nil || len(leases) != 1 || leases[0].Runnable || leases[0].WaitingReason != docstore.WaitRoutingPending {
		t.Fatalf("rolled-back tx unexpectedly woke parked task: %+v err=%v", leases, err)
	}
	select {
	case ev := <-eventsCh:
		if ev.Payload == docstore.TaskWakePayload(userID, "r-parked") {
			t.Fatalf("rolled-back tx emitted wake event: %+v", ev)
		}
	case <-time.After(100 * time.Millisecond):
	}

	// 2. Concurrent MVCC insert visibility (reproduces and verifies fix for the 60s MVCC stall!):
	// Begin a SERIALIZABLE tx and take its snapshot (via Get), THEN insert & claim a brand-new
	// task r-concurrent in READ COMMITTED BEFORE the SERIALIZABLE tx calls WakeWaitingTasks & commits.
	// Because tx.WakeWaitingTasks flushes post-commit in READ COMMITTED, it MUST see r-concurrent
	// and bump its wake_seq so the worker's subsequent ParkTask(..., staleWakeSeq) returns false!
	var concurrentClaim docstore.TaskLease
	err = b.RunTransaction(ctx, false, 3, func(tb docstore.TransactionBackend) error {
		// Take SERIALIZABLE snapshot before r-concurrent exists.
		if _, err := tb.Get(ctx, docPath); err != nil {
			return err
		}
		// Insert and claim r-concurrent AFTER the SERIALIZABLE snapshot started!
		if concurrentClaim.RunID == "" {
			if err := b.WakeTask(ctx, userID, "r-concurrent", time.Now().UTC().Add(-time.Second)); err != nil {
				return fmt.Errorf("concurrent WakeTask: %w", err)
			}
			c, ok, err := b.ClaimTaskByID(ctx, userID, "r-concurrent", "w-concurrent", 30*time.Second)
			if err != nil || !ok {
				return fmt.Errorf("concurrent ClaimTaskByID: ok=%v err=%v", ok, err)
			}
			concurrentClaim = c
		}
		if err := tb.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   docPath,
			Fields: map[string]any{"routing_pending": false},
		}); err != nil {
			return err
		}
		// Call twice to verify deduplication in flushWakeWaitings as well.
		w := tb.(docstore.WakeWriter)
		if err := w.WakeWaitingTasks(ctx, userID, docstore.WaitRoutingPending); err != nil {
			return err
		}
		return w.WakeWaitingTasks(ctx, userID, docstore.WaitRoutingPending)
	})
	if err != nil {
		t.Fatalf("commit tx with concurrent task insert: %v", err)
	}

	// Verify r-parked was woken AND r-concurrent had its wake_seq bumped (so ParkTask fails!).
	leases, err = b.ListTaskLeases(ctx, userID)
	if err != nil || len(leases) != 2 {
		t.Fatalf("ListTaskLeases = (%+v, %v), want 2 rows", leases, err)
	}
	byRun := map[string]docstore.TaskLease{}
	for _, l := range leases {
		byRun[l.RunID] = l
	}
	if !byRun["r-parked"].Runnable || byRun["r-parked"].WaitingReason != "" {
		t.Fatalf("r-parked not woken: %+v", byRun["r-parked"])
	}
	if byRun["r-concurrent"].WakeSeq <= concurrentClaim.WakeSeq {
		t.Fatalf("MVCC STALL: r-concurrent wake_seq stayed %d (claim was %d); post-commit READ COMMITTED WakeWaitingTasks should have bumped it!",
			byRun["r-concurrent"].WakeSeq, concurrentClaim.WakeSeq)
	}

	parked, err := b.ParkTask(ctx, userID, "r-concurrent", "w-concurrent", concurrentClaim.WakeSeq, docstore.WaitRoutingPending)
	if err != nil || parked {
		t.Fatalf("ParkTask on r-concurrent with stale wake_seq=%d returned (%v, %v), want (false, nil)",
			concurrentClaim.WakeSeq, parked, err)
	}

	// Verify post-commit NOTIFY events were emitted for BOTH r-parked and r-concurrent.
	wantPayloads := map[string]bool{
		docstore.TaskWakePayload(userID, "r-parked"):     false,
		docstore.TaskWakePayload(userID, "r-concurrent"): false,
	}
	remaining := len(wantPayloads)
	waitDeadline := time.After(3 * time.Second)
	for remaining > 0 {
		select {
		case ev := <-eventsCh:
			if seen, ok := wantPayloads[ev.Payload]; ok && !seen {
				wantPayloads[ev.Payload] = true
				remaining--
			}
		case <-waitDeadline:
			t.Fatalf("timed out waiting for post-commit WakeWaitingTasks notifications: %+v", wantPayloads)
		}
	}

	cancelListen()
	<-listenDone
}

func openTestBackendWithPrefix(t *testing.T) (*backend, *sqlQueryCounter, string) {
	t.Helper()
	return openTracedTestBackend(t, Options{})
}

func TestPostgresQueueMetrics(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx := context.Background()

	// Ensure this test's isolated DB starts clean for exact aggregate assertions.
	var existingTasks, existingWakes int
	if err := b.pool.QueryRow(ctx, "SELECT (SELECT COUNT(*) FROM nori_task_leases), (SELECT COUNT(*) FROM nori_user_wakes)").Scan(&existingTasks, &existingWakes); err != nil {
		t.Fatalf("check existing queue rows: %v", err)
	}
	if existingTasks != 0 || existingWakes != 0 {
		t.Skipf("skipping exact QueueMetrics counts on non-empty DB (tasks=%d wakes=%d)", existingTasks, existingWakes)
	}

	// 1. Empty tables -> all zeros.
	emptyM, err := b.QueueMetrics(ctx)
	if err != nil {
		t.Fatalf("QueueMetrics empty: %v", err)
	}
	if emptyM != (docstore.QueueMetrics{}) {
		t.Fatalf("empty QueueMetrics = %+v, want all zeros", emptyM)
	}

	now := time.Now().UTC()
	u := prefix + "qm"

	// 2. Only future runnable rows -> positive runnable depth, 0 lag (never negative).
	if err := b.WakeTask(ctx, u, "r-future", now.Add(10*time.Minute)); err != nil {
		t.Fatalf("WakeTask future: %v", err)
	}
	if err := b.WakeUser(ctx, u+"-sf", docstore.LaneScheduler, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("WakeUser scheduler future: %v", err)
	}
	futureM, err := b.QueueMetrics(ctx)
	if err != nil {
		t.Fatalf("QueueMetrics future: %v", err)
	}
	if futureM.TaskRunnable != 1 || futureM.TaskOldestLagSec != 0 || futureM.SchedRunnable != 1 || futureM.SchedOldestLagSec != 0 {
		t.Fatalf("future-only QueueMetrics = %+v, want TaskRunnable=1 TaskLag=0 SchedRunnable=1 SchedLag=0", futureM)
	}

	// 3. Seed mixed task lease states:
	// - r-future: runnable unleased, future wake_at (+10m)
	// - r-due-1: runnable unleased, overdue by 30s
	// - r-due-2: runnable unleased, overdue by 10s
	// - r-leased: actively leased (wake_at -120s must NOT count toward TaskOldestLagSec!)
	// - r-parked-1, r-parked-2: parked (runnable=false)
	if err := b.WakeTask(ctx, u, "r-due-1", now.Add(-30*time.Second)); err != nil {
		t.Fatalf("WakeTask r-due-1: %v", err)
	}
	if err := b.WakeTask(ctx, u, "r-due-2", now.Add(-10*time.Second)); err != nil {
		t.Fatalf("WakeTask r-due-2: %v", err)
	}
	if err := b.WakeTask(ctx, u, "r-leased", now.Add(-120*time.Second)); err != nil {
		t.Fatalf("WakeTask r-leased: %v", err)
	}
	if _, ok, err := b.ClaimTaskByID(ctx, u, "r-leased", "w-metrics", 60*time.Second); err != nil || !ok {
		t.Fatalf("ClaimTaskByID r-leased: ok=%v err=%v", ok, err)
	}
	for _, rid := range []string{"r-parked-1", "r-parked-2"} {
		if err := b.WakeTask(ctx, u, rid, now.Add(-60*time.Second)); err != nil {
			t.Fatalf("WakeTask %s: %v", rid, err)
		}
		c, ok, err := b.ClaimTaskByID(ctx, u, rid, "w-park", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID %s: ok=%v err=%v", rid, ok, err)
		}
		if parked, err := b.ParkTask(ctx, u, rid, "w-park", c.WakeSeq, docstore.WaitQuestion); err != nil || !parked {
			t.Fatalf("ParkTask %s: parked=%v err=%v", rid, parked, err)
		}
	}

	// 4. Seed mixed user wakes:
	// - conversation: 1 overdue (-25s), 1 actively leased (-200s, must NOT skew lag), 1 parked (excluded)
	// - scheduler: 1 future (+10m, already added above), 1 overdue (-15s)
	if err := b.WakeUser(ctx, u+"-c-due", docstore.LaneConversation, now.Add(-25*time.Second)); err != nil {
		t.Fatalf("WakeUser conv due: %v", err)
	}
	if err := b.WakeUser(ctx, u+"-c-leased", docstore.LaneConversation, now.Add(-200*time.Second)); err != nil {
		t.Fatalf("WakeUser conv leased: %v", err)
	}
	cw, ok, err := b.ClaimUserWake(ctx, docstore.LaneConversation, "w-conv", 60*time.Second)
	if err != nil || !ok || cw.UserID != u+"-c-leased" {
		t.Fatalf("ClaimUserWake conv leased: uw=%+v ok=%v err=%v", cw, ok, err)
	}
	if err := b.WakeUser(ctx, u+"-c-parked", docstore.LaneConversation, now.Add(-300*time.Second)); err != nil {
		t.Fatalf("WakeUser conv parked: %v", err)
	}
	cp, ok, err := b.ClaimUserWake(ctx, docstore.LaneConversation, "w-conv-park", 60*time.Second)
	if err != nil || !ok || cp.UserID != u+"-c-parked" {
		t.Fatalf("ClaimUserWake conv parked: uw=%+v ok=%v err=%v", cp, ok, err)
	}
	if parked, err := b.ParkUserWake(ctx, u+"-c-parked", docstore.LaneConversation, "w-conv-park", cp.WakeSeq); err != nil || !parked {
		t.Fatalf("ParkUserWake conv parked: parked=%v err=%v", parked, err)
	}
	if err := b.WakeUser(ctx, u+"-s-due", docstore.LaneScheduler, now.Add(-15*time.Second)); err != nil {
		t.Fatalf("WakeUser sched due: %v", err)
	}

	m, err := b.QueueMetrics(ctx)
	if err != nil {
		t.Fatalf("QueueMetrics populated: %v", err)
	}
	if m.TaskRunnable != 3 || m.TaskLeased != 1 || m.TaskParked != 2 {
		t.Fatalf("task counts = (runnable=%d leased=%d parked=%d), want (3, 1, 2)", m.TaskRunnable, m.TaskLeased, m.TaskParked)
	}
	if m.TaskOldestLagSec < 25 || m.TaskOldestLagSec > 50 {
		t.Fatalf("TaskOldestLagSec = %.2f, want ~30s (in [25, 50])", m.TaskOldestLagSec)
	}
	if m.ConvRunnable != 2 {
		t.Fatalf("ConvRunnable = %d, want 2", m.ConvRunnable)
	}
	if m.ConvOldestLagSec < 20 || m.ConvOldestLagSec > 45 {
		t.Fatalf("ConvOldestLagSec = %.2f, want ~25s (in [20, 45])", m.ConvOldestLagSec)
	}
	if m.SchedRunnable != 2 {
		t.Fatalf("SchedRunnable = %d, want 2", m.SchedRunnable)
	}
	if m.SchedOldestLagSec < 10 || m.SchedOldestLagSec > 35 {
		t.Fatalf("SchedOldestLagSec = %.2f, want ~15s (in [10, 35])", m.SchedOldestLagSec)
	}
	t.Logf("MEASURED QueueMetrics: %+v", m)
}

func TestPostgresScopedLeaseValidation(t *testing.T) {
	ctx := context.Background()
	b := &backend{}
	if _, _, err := b.ClaimTaskForUser(ctx, "", "owner", time.Second); err == nil {
		t.Fatal("ClaimTaskForUser empty userID = nil, want error")
	}
	if _, _, err := b.ClaimTaskForUser(ctx, "u1", "", time.Second); err == nil {
		t.Fatal("ClaimTaskForUser empty owner = nil, want error")
	}
	if _, _, err := b.ClaimUserWakeForUser(ctx, "", docstore.LaneConversation, "owner", time.Second); err == nil {
		t.Fatal("ClaimUserWakeForUser empty userID = nil, want error")
	}
	if _, _, err := b.ClaimUserWakeForUser(ctx, "u1", "", "owner", time.Second); err == nil {
		t.Fatal("ClaimUserWakeForUser empty lane = nil, want error")
	}
	if _, _, err := b.ClaimUserWakeForUser(ctx, "u1", docstore.LaneConversation, "", time.Second); err == nil {
		t.Fatal("ClaimUserWakeForUser empty owner = nil, want error")
	}
}

func TestPostgresScopedLeaseClaimIsolation(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx := context.Background()
	u1 := prefix + "scoped-u1"
	u2 := prefix + "scoped-u2"
	now := time.Now().UTC()

	// u2 has an earlier wake than u1; ClaimTaskForUser(u1) must skip u2 and claim u1's earliest.
	if err := b.WakeTask(ctx, u2, "r-u2-early", now.Add(-120*time.Second)); err != nil {
		t.Fatalf("WakeTask u2: %v", err)
	}
	if err := b.WakeTask(ctx, u1, "r-u1-late", now.Add(-10*time.Second)); err != nil {
		t.Fatalf("WakeTask u1 late: %v", err)
	}
	if err := b.WakeTask(ctx, u1, "r-u1-early", now.Add(-30*time.Second)); err != nil {
		t.Fatalf("WakeTask u1 early: %v", err)
	}
	if err := b.WakeTask(ctx, u1, "r-u1-future", now.Add(5*time.Minute)); err != nil {
		t.Fatalf("WakeTask u1 future: %v", err)
	}

	c1, ok, err := b.ClaimTaskForUser(ctx, u1, "w1", 30*time.Second)
	if err != nil || !ok || c1.UserID != u1 || c1.RunID != "r-u1-early" {
		t.Fatalf("ClaimTaskForUser(u1) #1 = (%+v, %v, %v), want %s/r-u1-early", c1, ok, err, u1)
	}
	// Mutual exclusion against ClaimTaskByID on already-leased row.
	if _, ok, err := b.ClaimTaskByID(ctx, u1, "r-u1-early", "w2", 30*time.Second); err != nil || ok {
		t.Fatalf("ClaimTaskByID on leased u1 row = (%v, %v), want false", ok, err)
	}
	c2, ok, err := b.ClaimTaskForUser(ctx, u1, "w1", 30*time.Second)
	if err != nil || !ok || c2.UserID != u1 || c2.RunID != "r-u1-late" {
		t.Fatalf("ClaimTaskForUser(u1) #2 = (%+v, %v, %v), want %s/r-u1-late", c2, ok, err, u1)
	}
	if c3, ok, err := b.ClaimTaskForUser(ctx, u1, "w1", 30*time.Second); err != nil || ok {
		t.Fatalf("ClaimTaskForUser(u1) #3 = (%+v, %v, %v), want ok=false", c3, ok, err)
	}
	// u2 remains claimable for u2.
	cu2, ok, err := b.ClaimTaskForUser(ctx, u2, "w2", 30*time.Second)
	if err != nil || !ok || cu2.UserID != u2 || cu2.RunID != "r-u2-early" {
		t.Fatalf("ClaimTaskForUser(u2) = (%+v, %v, %v)", cu2, ok, err)
	}

	// User wakes: u2 due earlier, u1 conv due, u1 sched future.
	if err := b.WakeUser(ctx, u2, docstore.LaneConversation, now.Add(-120*time.Second)); err != nil {
		t.Fatalf("WakeUser u2 conv: %v", err)
	}
	if err := b.WakeUser(ctx, u2, docstore.LaneScheduler, now.Add(-120*time.Second)); err != nil {
		t.Fatalf("WakeUser u2 sched: %v", err)
	}
	if err := b.WakeUser(ctx, u1, docstore.LaneConversation, now.Add(-10*time.Second)); err != nil {
		t.Fatalf("WakeUser u1 conv: %v", err)
	}
	if err := b.WakeUser(ctx, u1, docstore.LaneScheduler, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("WakeUser u1 sched: %v", err)
	}

	uw1, ok, err := b.ClaimUserWakeForUser(ctx, u1, docstore.LaneConversation, "w1", 30*time.Second)
	if err != nil || !ok || uw1.UserID != u1 || uw1.Lane != docstore.LaneConversation {
		t.Fatalf("ClaimUserWakeForUser(u1, conv) = (%+v, %v, %v)", uw1, ok, err)
	}
	if uwDup, ok, err := b.ClaimUserWakeForUser(ctx, u1, docstore.LaneConversation, "w2", 30*time.Second); err != nil || ok {
		t.Fatalf("ClaimUserWakeForUser(u1, conv) while leased = (%+v, %v, %v), want ok=false", uwDup, ok, err)
	}
	if uwSched, ok, err := b.ClaimUserWakeForUser(ctx, u1, docstore.LaneScheduler, "w1", 30*time.Second); err != nil || ok {
		t.Fatalf("ClaimUserWakeForUser(u1, sched future) = (%+v, %v, %v), want ok=false", uwSched, ok, err)
	}
	uw2, ok, err := b.ClaimUserWakeForUser(ctx, u2, docstore.LaneScheduler, "w2", 30*time.Second)
	if err != nil || !ok || uw2.UserID != u2 || uw2.Lane != docstore.LaneScheduler {
		t.Fatalf("ClaimUserWakeForUser(u2, sched) = (%+v, %v, %v)", uw2, ok, err)
	}
}
