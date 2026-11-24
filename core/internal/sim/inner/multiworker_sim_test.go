package inner_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/sim/inner"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
	"nori/internal/transcript"
	"nori/internal/users"
)

// crashSimStore wraps state.TaskStore to simulate hard worker crashes (dropping
// ReleaseTaskControl calls when crashed so the authoritative fence stays held
// until the 30s TaskLease expires) and to inject faults on specific store calls.
type crashSimStore struct {
	state.TaskStore
	mu                sync.Mutex
	crashedOwners     map[string]bool
	onStartCkpt       func(ctx context.Context, runID string, ckpt state.TaskModelCheckpoint)
	onSaveCkpt        func(ctx context.Context, runID string, ckpt state.TaskModelCheckpoint)
	onRenewControl    func(ctx context.Context, runID string, f state.TaskFence) error
	onSaveControl     func(ctx context.Context, f state.TaskFence, ctrl state.TaskControl)
	onAdvanceStep     func(ctx context.Context, f state.TaskFence, ctrl state.TaskControl)
	onReadHistory     func(ctx context.Context, runID, ref string) ([][]byte, error, bool)
	onProjectQuestion func(ctx context.Context, runID string, rev int64, q state.Question)
	onPublish         func(ctx context.Context, req state.PublishRequest)
	onFinishCleanup   func(ctx context.Context, runID string, failed string)
	onPutOp           func(ctx context.Context, runID string, op state.OperationRecord)
}

func newCrashSimStore(base state.TaskStore) *crashSimStore {
	return &crashSimStore{
		TaskStore:     base,
		crashedOwners: make(map[string]bool),
	}
}

func (s *crashSimStore) markCrashed(owner string) {
	s.mu.Lock()
	s.crashedOwners[owner] = true
	s.mu.Unlock()
}

func (s *crashSimStore) isCrashed(owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.crashedOwners[owner]
}

func (s *crashSimStore) StartTaskModelCheckpoint(ctx context.Context, runID string, ckpt state.TaskModelCheckpoint) error {
	err := s.TaskStore.StartTaskModelCheckpoint(ctx, runID, ckpt)
	if err == nil {
		s.mu.Lock()
		fn := s.onStartCkpt
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, runID, ckpt)
		}
	}
	return err
}

func (s *crashSimStore) SaveTaskModelCheckpoint(ctx context.Context, runID string, ckpt state.TaskModelCheckpoint, messages []byte) (string, error) {
	ref, err := s.TaskStore.SaveTaskModelCheckpoint(ctx, runID, ckpt, messages)
	if err == nil {
		s.mu.Lock()
		fn := s.onSaveCkpt
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, runID, ckpt)
		}
	}
	return ref, err
}

func (s *crashSimStore) PutOperation(ctx context.Context, runID string, op state.OperationRecord) error {
	err := s.TaskStore.PutOperation(ctx, runID, op)
	if err == nil {
		s.mu.Lock()
		fn := s.onPutOp
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, runID, op)
		}
	}
	return err
}

func (s *crashSimStore) RenewTaskControl(ctx context.Context, runID string, f state.TaskFence) error {
	if s.isCrashed(f.Owner) {
		return context.Canceled
	}
	s.mu.Lock()
	fn := s.onRenewControl
	s.mu.Unlock()
	if fn != nil {
		if err := fn(ctx, runID, f); err != nil {
			return err
		}
	}
	return s.TaskStore.RenewTaskControl(ctx, runID, f)
}

func (s *crashSimStore) AdvanceTaskStep(ctx context.Context, f state.TaskFence, ctrl state.TaskControl, spend *state.SpendUpdate) (state.TaskControl, state.Run, error) {
	if s.isCrashed(f.Owner) {
		return state.TaskControl{}, state.Run{}, context.Canceled
	}
	saved, run, err := s.TaskStore.AdvanceTaskStep(ctx, f, ctrl, spend)
	if err == nil {
		s.mu.Lock()
		fn := s.onAdvanceStep
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, f, saved)
		}
	}
	return saved, run, err
}

func (s *crashSimStore) SaveTaskControl(ctx context.Context, f state.TaskFence, ctrl state.TaskControl) (state.TaskControl, error) {
	if s.isCrashed(f.Owner) {
		return state.TaskControl{}, context.Canceled
	}
	saved, err := s.TaskStore.SaveTaskControl(ctx, f, ctrl)
	if err == nil {
		s.mu.Lock()
		fn := s.onSaveControl
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, f, saved)
		}
	}
	return saved, err
}

func (s *crashSimStore) ReadHistory(ctx context.Context, runID, ref string) ([][]byte, error) {
	s.mu.Lock()
	fn := s.onReadHistory
	s.mu.Unlock()
	if fn != nil {
		if blocks, err, handled := fn(ctx, runID, ref); handled {
			return blocks, err
		}
	}
	return s.TaskStore.ReadHistory(ctx, runID, ref)
}

func (s *crashSimStore) ProjectQuestion(ctx context.Context, runID string, revision int64, question state.Question) error {
	err := s.TaskStore.ProjectQuestion(ctx, runID, revision, question)
	if err == nil {
		s.mu.Lock()
		fn := s.onProjectQuestion
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, runID, revision, question)
		}
	}
	return err
}

func (s *crashSimStore) Publish(ctx context.Context, req state.PublishRequest) (state.Run, error) {
	res, err := s.TaskStore.Publish(ctx, req)
	if err == nil {
		s.mu.Lock()
		fn := s.onPublish
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, req)
		}
	}
	return res, err
}

func (s *crashSimStore) FinishCleanup(ctx context.Context, runID, failed string) error {
	err := s.TaskStore.FinishCleanup(ctx, runID, failed)
	if err == nil {
		s.mu.Lock()
		fn := s.onFinishCleanup
		s.mu.Unlock()
		if fn != nil {
			fn(ctx, runID, failed)
		}
	}
	return err
}

func (s *crashSimStore) ReleaseTaskControl(ctx context.Context, runID string, f state.TaskFence) error {
	if s.isCrashed(f.Owner) {
		// Hard crash: process died before releasing TaskControl!
		return nil
	}
	return s.TaskStore.ReleaseTaskControl(ctx, runID, f)
}

// crashSimLeases wraps docstore.LeaseStore to simulate hard crashes (dropping
// ReleaseTask when crashed so the discovery lease stays held until expiry) and
// to hook ParkTask / RenewTaskLeases for deterministic fault & race tests.
type crashSimLeases struct {
	docstore.LeaseStore
	mu           sync.Mutex
	crashed      map[string]bool
	onRenewLease func(ctx context.Context, owner string, keys []docstore.TaskKey, lease time.Duration) (int, error, bool)
	onBeforePark func(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string)
}

func newCrashSimLeases(base docstore.LeaseStore) *crashSimLeases {
	return &crashSimLeases{
		LeaseStore: base,
		crashed:    make(map[string]bool),
	}
}

func (l *crashSimLeases) markCrashed(owner string) {
	l.mu.Lock()
	l.crashed[owner] = true
	l.mu.Unlock()
}

func (l *crashSimLeases) isCrashed(owner string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.crashed[owner]
}

func (l *crashSimLeases) RenewTaskLeases(ctx context.Context, owner string, keys []docstore.TaskKey, lease time.Duration) (int, error) {
	if l.isCrashed(owner) {
		return 0, context.Canceled
	}
	l.mu.Lock()
	fn := l.onRenewLease
	l.mu.Unlock()
	if fn != nil {
		if n, err, handled := fn(ctx, owner, keys, lease); handled {
			return n, err
		}
	}
	return l.LeaseStore.RenewTaskLeases(ctx, owner, keys, lease)
}

func (l *crashSimLeases) ParkTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string) (bool, error) {
	l.mu.Lock()
	fn := l.onBeforePark
	l.mu.Unlock()
	if fn != nil {
		fn(ctx, userID, runID, owner, wakeSeq, reason)
	}
	return l.LeaseStore.ParkTask(ctx, userID, runID, owner, wakeSeq, reason)
}

func (l *crashSimLeases) ReleaseTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, nextWake time.Time) error {
	if l.isCrashed(owner) {
		// Hard crash: process died before releasing discovery lease!
		return nil
	}
	return l.LeaseStore.ReleaseTask(ctx, userID, runID, owner, wakeSeq, nextWake)
}

func (l *crashSimLeases) DeleteTaskLease(ctx context.Context, userID, runID string, wakeSeq int64) (bool, error) {
	// If any worker is marked crashed for this test scenario and called DeleteTaskLease on its way out, drop it!
	l.mu.Lock()
	drop := len(l.crashed) > 0 && l.crashed["drop-delete"]
	l.mu.Unlock()
	if drop {
		return false, context.Canceled
	}
	return l.LeaseStore.DeleteTaskLease(ctx, userID, runID, wakeSeq)
}

// runWithVirtualLeaseRenewal runs real task.Runner against leases and advances virtual
// lease renewals synchronously whenever Activities.Sleep advances the virtual clock.
func runWithVirtualLeaseRenewal(
	t *testing.T,
	ctx context.Context,
	h *wfHarness,
	store state.TaskStore,
	leases docstore.LeaseStore,
	runID string,
	owner string,
) (task.RunOutcome, error) {
	t.Helper()
	claim, ok, err := leases.ClaimTaskByID(ctx, h.user.ID, runID, owner, state.TaskLease)
	if err != nil {
		t.Fatalf("ClaimTaskByID(%s, %s): %v", runID, owner, err)
	}
	if !ok {
		t.Fatalf("ClaimTaskByID(%s, %s): lease not claimable", runID, owner)
	}

	var (
		mu         sync.Mutex
		tickCh     chan time.Time
		ackCh      chan struct{}
		lastRenewT = h.clock.Now()
	)
	act := *h.activities
	act.Store = store
	act.Sleep = func(sCtx context.Context, d time.Duration) error {
		if err := sCtx.Err(); err != nil {
			return err
		}
		if d > 0 {
			h.clock.Advance(d)
			now := h.clock.Now()
			mu.Lock()
			tCh, aCh := tickCh, ackCh
			due := tCh != nil && now.Sub(lastRenewT) >= 10*time.Second
			if due {
				lastRenewT = now
			}
			mu.Unlock()
			if due {
				select {
				case tCh <- now:
				case <-sCtx.Done():
					return sCtx.Err()
				}
				select {
				case <-aCh:
				case <-sCtx.Done():
					return sCtx.Err()
				}
			}
		}
		return sCtx.Err()
	}

	runner := &task.Runner{
		Activities: &act,
		Leases:     leases,
		UserID:     h.user.ID,
		Owner:      owner,
		Wakes:      h.wakes,
		Now:        h.clock.Now,
		NewTicker: func(time.Duration) (<-chan time.Time, func()) {
			tCh := make(chan time.Time)
			aCh := make(chan struct{}, 1)
			mu.Lock()
			tickCh = tCh
			ackCh = aCh
			lastRenewT = h.clock.Now()
			mu.Unlock()
			return tCh, func() {
				mu.Lock()
				if tickCh == tCh {
					tickCh = nil
					ackCh = nil
				}
				mu.Unlock()
			}
		},
		OnLeaseRenewed: func(_, _ string) {
			mu.Lock()
			aCh := ackCh
			mu.Unlock()
			if aCh != nil {
				select {
				case aCh <- struct{}{}:
				default:
				}
			}
		},
	}
	return runner.Run(users.WithID(ctx, h.user.ID), claim)
}

func TestMultiWorkerCrashMidModelTakeover(t *testing.T) {
	t.Run("receipt_reuse_zero_duplicate_llm_call", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3001, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("mr-1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
			}
			return scriptedCall("mr-2", "complete", map[string]any{
				"answer":   "Recovered saved model checkpoint without duplicate call.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		run := h.startTaskRecord(ctx, "Crash right after SaveTaskModelCheckpoint")

		// Worker 1 hard-crashes immediately after SaveTaskModelCheckpoint commits (before AdvanceTaskStep).
		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashedOnce := false
		cStore.onSaveCkpt = func(_ context.Context, _ string, _ state.TaskModelCheckpoint) {
			if !crashedOnce {
				crashedOnce = true
				cStore.markCrashed("worker-1")
				cLeases.markCrashed("worker-1")
				w1Cancel()
			}
		}

		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashedOnce {
			t.Fatal("expected Worker 1 to crash right after SaveTaskModelCheckpoint")
		}
		if len(h.model.ModelCalls()) != 1 {
			t.Fatalf("expected 1 model call before crash, got %d", len(h.model.ModelCalls()))
		}
		ctrlBefore, err := h.store.GetTaskControl(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetTaskControl: %v", err)
		}
		if ctrlBefore.PendingModelID == "" {
			t.Fatal("expected PendingModelID persisted before Worker 1 crash")
		}

		// Before 30s lease expiry, Worker 2 MUST NOT steal the task.
		h.clock.Advance(15 * time.Second)
		if _, ok, err := cLeases.ClaimTaskByID(ctx, h.user.ID, run.ID, "worker-2", state.TaskLease); err != nil || ok {
			t.Fatalf("expected task leased until 30s expiry, got ok=%v err=%v", ok, err)
		}

		// After 30s lease expiry, Worker 2 takes over with Fence+1 and reuses the saved receipt!
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}

		// Total LLM calls MUST be 2 (turn 1 reused from receipt, turn 2 complete) — ZERO duplicate turn-1 call!
		if len(h.model.ModelCalls()) != 2 {
			t.Fatalf("expected exactly 2 total LLM calls (1 reused + 1 complete), got %d", len(h.model.ModelCalls()))
		}
		finalRun, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
			t.Fatalf("unexpected final run: %+v", finalRun)
		}
		if math.Abs(finalRun.Spent-0.20) > 1e-6 {
			t.Fatalf("expected spent=0.20 (no double charge), got %.6f", finalRun.Spent)
		}
		ctrlAfter, err := h.store.GetTaskControl(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetTaskControl after: %v", err)
		}
		if ctrlAfter.Fence <= ctrlBefore.Fence {
			t.Fatalf("expected Fence > %d after takeover, got %d", ctrlBefore.Fence, ctrlAfter.Fence)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("d053_retry_within_9m_after_mid_llm_crash", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3002, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		// Worker 1 crashes right after StartTaskModelCheckpoint (mid-LLM call, no saved response).
		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashedMidLLM := false
		cStore.onStartCkpt = func(_ context.Context, _ string, _ state.TaskModelCheckpoint) {
			if !crashedMidLLM {
				crashedMidLLM = true
				cStore.markCrashed("worker-1")
				cLeases.markCrashed("worker-1")
				w1Cancel()
			}
		}

		h.model.SetScript(func(mCtx context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			if mCtx.Err() != nil {
				return agent.Response{}, mCtx.Err(), true
			}
			return scriptedCall("d053-done", "complete", map[string]any{
				"answer":   "Recovered mid-LLM crash via D-053 retry within 9m.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		run := h.startTaskRecord(ctx, "Crash mid-LLM call before response saved")
		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashedMidLLM {
			t.Fatal("expected Worker 1 to crash right after StartTaskModelCheckpoint")
		}

		// Expire Worker 1's lease -> Worker 2 waits until RecoveryUntil (240s, renewing leases!),
		// sees Interrupted=true, retries within 9m D-053 deadline, and completes!
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone after D-053 retry, got outcome=%s err=%v", outcome, err)
		}

		finalRun, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
			t.Fatalf("expected RunCompleted after D-053 retry, got %+v", finalRun)
		}
		h.checkSafetyAndQuiescence(ctx)
	})
}

func TestMultiWorkerCrashMidToolTakeover(t *testing.T) {
	t.Run("safe_read_reruns_cleanly", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3003, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("sr-1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
			}
			return scriptedCall("sr-2", "complete", map[string]any{
				"answer":   "Safe read reran cleanly after worker crash.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		run := h.startTaskRecord(ctx, "Crash mid safe read")

		// Crash Worker 1 right inside Execute (after op.Attempt=1 is persisted, before SettleOperation!).
		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashed := false
		h.adapters.Register(extensions.AdapterNative, "sim", &composeHookAdapter{
			base: h.adapter,
			afterExecute: func(_ context.Context, _ invoke.Call, prepared invoke.Prepared, _ invoke.Result) {
				if prepared.Effect == extensions.EffectRead && !crashed {
					crashed = true
					cStore.markCrashed("worker-1")
					cLeases.markCrashed("worker-1")
					w1Cancel()
				}
			},
		})
		cStore.TaskStore = &dropSettleWhenCrashedStore{TaskStore: h.store, crashed: cStore}

		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashed {
			t.Fatal("expected Worker 1 to crash after safe read Execute")
		}

		// After 30s expiry, Worker 2 reruns safe read immediately and completes!
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}
		finalRun, _ := h.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
			t.Fatalf("expected RunCompleted, got %+v", finalRun)
		}
		ops, err := h.store.Operations(ctx, run.ID)
		if err != nil || len(ops) != 1 || ops[0].Outcome != "confirmed" || ops[0].Attempt != 2 {
			t.Fatalf("expected confirmed safe read with Attempt=2, got %+v (err=%v)", ops, err)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("interrupted_write_reconciles_without_double_execute", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3004, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{inner.ToolIDWrite},
			ConnectionIDs: []string{},
			ResourceIDs:   []string{"*"},
			ActionClasses: []string{"unknown"},
			Title:         "Allow sim_write for crash reconcile test",
		}); err != nil {
			t.Fatalf("SaveRule: %v", err)
		}

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("iw-1", inner.ToolNameWrite, map[string]any{
					"target": "doc-1", "value": "committed-once", "resource_id": "doc-1",
				}, 0.10), nil, true
			}
			return scriptedCall("iw-2", "complete", map[string]any{
				"answer":   "Interrupted write reconciled without double execution.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		// Wrap sim adapter to crash Worker 1 right AFTER side-effecting Execute returns (before SettleOperation!).
		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashedAfterExec := false
		h.adapters.Register(extensions.AdapterNative, "sim", &composeHookAdapter{
			base: h.adapter,
			afterExecute: func(_ context.Context, _ invoke.Call, prepared invoke.Prepared, _ invoke.Result) {
				if prepared.Effect != extensions.EffectRead && !crashedAfterExec {
					crashedAfterExec = true
					cStore.markCrashed("worker-1")
					cLeases.markCrashed("worker-1")
					w1Cancel()
				}
			},
		})
		// Prevent Worker 1's receiptContext from settling the write after crash!
		cStore.TaskStore = &dropSettleWhenCrashedStore{TaskStore: h.store, crashed: cStore}

		run := h.startTaskRecord(ctx, "Crash right after write side effect applied")
		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashedAfterExec {
			t.Fatal("expected Worker 1 to crash right after Execute")
		}

		// Worker 2 takes over after 30s expiry, waits for RecoveryUntil, reconciles, and completes!
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone after reconcile, got outcome=%s err=%v", outcome, err)
		}

		// Write MUST have executed EXACTLY ONCE!
		if len(h.adapter.Executed()) != 1 {
			t.Fatalf("expected exactly 1 write execution (zero duplicates), got %d: %+v", len(h.adapter.Executed()), h.adapter.Executed())
		}
		if h.adapter.ReconcileCount() < 1 {
			t.Fatalf("expected Reconcile to recover interrupted write, count=%d", h.adapter.ReconcileCount())
		}
		finalRun, _ := h.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
			t.Fatalf("expected RunCompleted, got %+v", finalRun)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("interrupted_write_reconcile_miss_settles_uncertain_and_asks", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3005, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{inner.ToolIDWrite},
			ConnectionIDs: []string{},
			ResourceIDs:   []string{"*"},
			ActionClasses: []string{"unknown"},
			Title:         "Allow sim_write for uncertain crash test",
		}); err != nil {
			t.Fatalf("SaveRule: %v", err)
		}
		h.adapter.QueueForcedOutcome(inner.ForcedOutcome{Outcome: "uncertain", ReconcileMiss: true})

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("iu-1", inner.ToolNameWrite, map[string]any{
					"target": "doc-2", "value": "uncertain-once", "resource_id": "doc-2",
				}, 0.10), nil, true
			}
			return scriptedCall("iu-2", "complete", map[string]any{
				"answer":   "User verified uncertain write; completed cleanly.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashedAfterExec := false
		h.adapters.Register(extensions.AdapterNative, "sim", &composeHookAdapter{
			base: h.adapter,
			afterExecute: func(_ context.Context, _ invoke.Call, prepared invoke.Prepared, _ invoke.Result) {
				if prepared.Effect != extensions.EffectRead && !crashedAfterExec {
					crashedAfterExec = true
					cStore.markCrashed("worker-1")
					cLeases.markCrashed("worker-1")
					w1Cancel()
				}
			},
		})
		cStore.TaskStore = &dropSettleWhenCrashedStore{TaskStore: h.store, crashed: cStore}

		run := h.startTaskRecord(ctx, "Crash mid-write with unreconcilable receipt")
		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashedAfterExec {
			t.Fatal("expected Worker 1 to crash right after Execute")
		}

		// Worker 2 takes over after 30s; provider reconcile misses -> settles uncertain -> parks on WaitQuestion!
		h.adapter.QueueForcedOutcome(inner.ForcedOutcome{ReconcileMiss: true})
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeParked {
			t.Fatalf("worker-2: expected OutcomeParked on uncertain question, got outcome=%s err=%v", outcome, err)
		}
		if len(h.adapter.Executed()) != 1 {
			t.Fatalf("expected write executed ONCE (never retried), got %d", len(h.adapter.Executed()))
		}
		midRun, _ := h.store.GetRun(ctx, run.ID)
		if midRun.Question == nil || len(midRun.Question.Clears) != 1 {
			t.Fatalf("expected uncertain question with Clears, got %+v", midRun.Question)
		}

		// User answers -> clears uncertain op -> Worker 3 completes cleanly!
		h.acceptCommands(ctx, state.Command{
			ID:         h.ids.Next(),
			RunID:      run.ID,
			Kind:       state.CommandAnswer,
			QuestionID: midRun.Question.ID,
			Text:       "Verified externally, please finish.",
		})
		outcome, err = runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-3")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-3: expected OutcomeDone after answer, got outcome=%s err=%v", outcome, err)
		}
		if len(h.adapter.Executed()) != 1 {
			t.Fatalf("expected still 1 write execution after completion, got %d", len(h.adapter.Executed()))
		}
		h.checkSafetyAndQuiescence(ctx)
	})
}

type dropSettleWhenCrashedStore struct {
	state.TaskStore
	crashed *crashSimStore
}

func (s *dropSettleWhenCrashedStore) SettleOperation(ctx context.Context, runID string, op state.OperationRecord) error {
	// While worker-1 is marked crashed and no worker-2 has acquired the fence yet, simulate hard process kill before settle.
	if ctrl, err := s.TaskStore.GetTaskControl(ctx, runID); err == nil && s.crashed.isCrashed(ctrl.Owner) {
		return context.Canceled
	}
	return s.TaskStore.SettleOperation(ctx, runID, op)
}

func TestMultiWorkerStaleFenceRejectedAndNoDoubleSpend(t *testing.T) {
	ctx := context.Background()
	h := newWFHarness(t, 3006, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

	turn := 0
	h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		turn++
		if turn == 1 {
			return scriptedCall("sf-1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
		}
		return scriptedCall("sf-2", "complete", map[string]any{
			"answer":   "Worker 2 completed with fresh fence.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.10), nil, true
	})

	run := h.startTaskRecord(ctx, "Verify stale fence cannot overwrite progress or double-count spend")

	// Worker 1 acquires authoritative TaskControl (Fence = 1) and stalls past 30s expiry.
	ctrl1, fence1, err := h.store.AcquireTaskControl(ctx, run.ID, "stale-worker-1")
	if err != nil {
		t.Fatalf("AcquireTaskControl worker-1: %v", err)
	}
	if fence1.Fence != 1 {
		t.Fatalf("expected initial fence=1, got %d", fence1.Fence)
	}

	// Expire Worker 1's lease -> Worker 2 takes over and completes the entire task.
	h.clock.Advance(state.TaskLease + time.Second)
	outcome, err := h.runTask(ctx, run.ID, "worker-2", nil)
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("worker-2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
	}

	// Stale Worker 1 wakes up and tries SaveTaskControl and AdvanceTaskStep with expired Fence=1!
	ctrl1.Phase = state.TaskPhaseModel
	ctrl1.Models = 99
	if _, err := h.store.SaveTaskControl(ctx, fence1, ctrl1); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("expected SaveTaskControl with stale fence to fail with ErrTaskBusy, got %v", err)
	}
	if _, _, err := h.store.AdvanceTaskStep(ctx, fence1, ctrl1, &state.SpendUpdate{
		RunID:  run.ID,
		Spent:  0.99,
		Budget: 2.0,
	}); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("expected AdvanceTaskStep with stale fence to fail with ErrTaskBusy, got %v", err)
	}

	// Verify Worker 2's committed progress and exact spend were completely preserved.
	finalRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if finalRun.Status != state.RunCompleted || math.Abs(finalRun.Spent-0.20) > 1e-6 {
		t.Fatalf("expected RunCompleted with Spent=0.20, got status=%s spent=%.6f", finalRun.Status, finalRun.Spent)
	}
	finalCtrl, err := h.store.GetTaskControl(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetTaskControl: %v", err)
	}
	if finalCtrl.Phase != state.TaskPhaseDone || finalCtrl.Fence <= fence1.Fence || finalCtrl.Models == 99 {
		t.Fatalf("stale worker corrupted TaskControl: %+v", finalCtrl)
	}
	h.checkSafetyAndQuiescence(ctx)
}

func TestMultiWorkerLeaseRenewalFailureImmediateSelfStop(t *testing.T) {
	cases := []struct {
		name           string
		failControl    bool
		failLeaseZero  bool
		wantRunOutcome task.RunOutcome
	}{
		{name: "renew_task_control_error", failControl: true, wantRunOutcome: task.OutcomeBackoff},
		{name: "renew_task_lease_returns_zero", failLeaseZero: true, wantRunOutcome: task.OutcomeBackoff},
	}

	for idx, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newWFHarness(t, int64(3010+idx), wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
			cStore := newCrashSimStore(h.store)
			cLeases := newCrashSimLeases(h.backend)

			if tc.failControl {
				cStore.onRenewControl = func(_ context.Context, _ string, _ state.TaskFence) error {
					return errors.New("injected postgres renewal timeout")
				}
			}
			if tc.failLeaseZero {
				cLeases.onRenewLease = func(_ context.Context, _ string, _ []docstore.TaskKey, _ time.Duration) (int, error, bool) {
					return 0, nil, true
				}
			}

			inModel := make(chan struct{}, 1)
			var blockFirst atomic.Bool
			blockFirst.Store(true)
			h.model.SetScript(func(mCtx context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
				if blockFirst.CompareAndSwap(true, false) {
					select {
					case inModel <- struct{}{}:
					default:
					}
					<-mCtx.Done()
					return agent.Response{NotSent: true}, mCtx.Err(), true
				}
				return scriptedCall("ok-done", "complete", map[string]any{
					"answer":   "Worker 2 completed after Worker 1 self-stopped on renewal failure.",
					"evidence": []any{},
					"unmet":    []any{},
				}, 0.10), nil, true
			})

			run := h.startTaskRecord(ctx, "Verify watchTaskLease immediate self-stop")
			claim, ok, err := cLeases.ClaimTaskByID(ctx, h.user.ID, run.ID, "worker-1", state.TaskLease)
			if err != nil || !ok {
				t.Fatalf("ClaimTaskByID worker-1: ok=%v err=%v", ok, err)
			}

			renewTick := make(chan time.Time, 1)
			act := *h.activities
			act.Store = cStore
			runner := &task.Runner{
				Activities: &act,
				Leases:     cLeases,
				UserID:     h.user.ID,
				Owner:      "worker-1",
				Wakes:      h.wakes,
				Now:        h.clock.Now,
				NewTicker: func(time.Duration) (<-chan time.Time, func()) {
					return renewTick, func() {}
				},
			}

			type runRes struct {
				outcome task.RunOutcome
				err     error
			}
			doneCh := make(chan runRes, 1)
			go func() {
				out, rErr := runner.Run(users.WithID(ctx, h.user.ID), claim)
				doneCh <- runRes{outcome: out, err: rErr}
			}()

			select {
			case <-inModel:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for Worker 1 to enter ModelRef")
			}

			// Fire 10s lease renewal tick -> MUST immediately cancel in-flight ModelRef!
			renewTick <- h.clock.Now().Add(10 * time.Second)

			select {
			case res := <-doneCh:
				if res.outcome != tc.wantRunOutcome && res.outcome != task.OutcomeBusy {
					t.Fatalf("expected self-stop outcome %s/busy, got outcome=%s err=%v", tc.wantRunOutcome, res.outcome, res.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("watchTaskLease failed to cancel in-flight ModelRef immediately on renewal failure")
			}

			// Clear fault, advance past short backoff + RecoveryUntil, and let Worker 2 finish cleanly!
			cStore.onRenewControl = nil
			cLeases.onRenewLease = nil
			h.clock.Advance(5 * time.Minute)
			outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
			if err != nil || outcome != task.OutcomeDone {
				t.Fatalf("worker-2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
			}
			finalRun, _ := h.store.GetRun(ctx, run.ID)
			if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
				t.Fatalf("expected RunCompleted, got %+v", finalRun)
			}
			h.checkSafetyAndQuiescence(ctx)
		})
	}
}

func TestMultiWorkerWakeDrivenCancelAndLegacyDataOwner(t *testing.T) {
	modes := []struct {
		name string
		fire func(t *testing.T, ctx context.Context, h *wfHarness, runID string)
	}{
		{
			name: "stop",
			fire: func(t *testing.T, ctx context.Context, h *wfHarness, runID string) {
				h.acceptCommands(ctx, state.Command{
					ID:    h.ids.Next(),
					RunID: runID,
					Kind:  state.CommandStop,
				})
			},
		},
		{
			name: "pause",
			fire: func(t *testing.T, ctx context.Context, h *wfHarness, _ string) {
				if err := h.setPaused(ctx, true); err != nil {
					t.Errorf("setPaused(true): %v", err)
				}
			},
		},
		{
			name: "interrupting_steer",
			fire: func(t *testing.T, ctx context.Context, h *wfHarness, runID string) {
				h.acceptCommands(ctx, state.Command{
					ID:        h.ids.Next(),
					RunID:     runID,
					Kind:      state.CommandSteer,
					Text:      "Change course right now",
					Interrupt: true,
				})
			},
		},
	}

	for idx, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			// Explicitly exercise users.LegacyID ("owner") where user.ID != user.DataOwner()!
			h := newWFHarness(t, int64(3020+idx), wfHarnessOpts{
				budgetUSD:   2.0,
				costPerCall: 0.10,
				userID:      users.LegacyID,
			})
			if h.user.ID == h.user.DataOwner() {
				t.Fatalf("expected user.ID (%q) != user.DataOwner() (%q)", h.user.ID, h.user.DataOwner())
			}

			// Wire real memstore.Backend.ListenWakes -> WakeHub.Deliver so post-commit wakes drive bounded()!
			listenCtx, listenCancel := context.WithCancel(ctx)
			defer listenCancel()
			listenReady := make(chan struct{})
			go func() {
				close(listenReady)
				_ = h.backend.ListenWakes(listenCtx, h.wakes.Deliver)
			}()
			<-listenReady
			// Brief pause so ListenWakes registers its subscriber before firing commands.
			time.Sleep(10 * time.Millisecond)

			inModel := make(chan struct{}, 1)
			var blockFirst atomic.Bool
			blockFirst.Store(true)
			h.model.SetScript(func(mCtx context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
				if blockFirst.CompareAndSwap(true, false) {
					select {
					case inModel <- struct{}{}:
					default:
					}
					<-mCtx.Done()
					return agent.Response{NotSent: true}, mCtx.Err(), true
				}
				return scriptedCall("wake-done", "complete", map[string]any{
					"answer":   "Completed after wake-driven cancellation.",
					"evidence": []any{},
					"unmet":    []any{},
				}, 0.10), nil, true
			})

			run := h.startTaskRecord(ctx, "Legacy owner wake-driven cancel: "+tc.name)

			type runRes struct {
				outcome task.RunOutcome
				err     error
			}
			doneCh := make(chan runRes, 1)
			go func() {
				// Activities.NewTicker is a never-firing noopTicker in newWFHarness, proving
				// cancellation came purely from ListenWakes -> WakeHub -> bounded()!
				out, err := h.runTask(ctx, run.ID, "worker-1", nil)
				doneCh <- runRes{outcome: out, err: err}
			}()

			select {
			case <-inModel:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for ModelRef to start")
			}

			tc.fire(t, ctx, h, run.ID)

			var res runRes
			select {
			case res = <-doneCh:
			case <-time.After(2 * time.Second):
				t.Fatalf("wake-driven cancel (%s) did not interrupt ModelRef promptly", tc.name)
			}

			switch tc.name {
			case "stop":
				if res.err != nil || res.outcome != task.OutcomeDone {
					t.Fatalf("stop: expected OutcomeDone, got outcome=%s err=%v", res.outcome, res.err)
				}
				finalRun, _ := h.store.GetRun(ctx, run.ID)
				if finalRun.Status != state.RunCancelled || finalRun.CleanupPending {
					t.Fatalf("stop: expected RunCancelled & clean, got %+v", finalRun)
				}
			case "pause":
				if res.err != nil || res.outcome != task.OutcomeParked {
					t.Fatalf("pause: expected OutcomeParked, got outcome=%s err=%v", res.outcome, res.err)
				}
				if err := h.setPaused(ctx, false); err != nil {
					t.Fatalf("setPaused(false): %v", err)
				}
				out2, err := h.runTask(ctx, run.ID, "worker-2", nil)
				if err != nil || out2 != task.OutcomeDone {
					t.Fatalf("pause resume: expected OutcomeDone, got outcome=%s err=%v", out2, err)
				}
				finalRun, _ := h.store.GetRun(ctx, run.ID)
				if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
					t.Fatalf("pause resume: expected RunCompleted, got %+v", finalRun)
				}
			case "interrupting_steer":
				if res.err != nil || res.outcome != task.OutcomeDone {
					t.Fatalf("steer: expected OutcomeDone after rev-2 pivot, got outcome=%s err=%v", res.outcome, res.err)
				}
				finalRun, _ := h.store.GetRun(ctx, run.ID)
				if finalRun.Status != state.RunCompleted || finalRun.Revision != 2 {
					t.Fatalf("steer: expected RunCompleted at rev 2, got status=%s rev=%d", finalRun.Status, finalRun.Revision)
				}
			}

			listenCancel()
			h.checkSafetyAndQuiescence(ctx)
		})
	}
}

func TestMultiWorkerWakeSeqRaceNoLostWake(t *testing.T) {
	t.Run("concurrent_answer_before_park_loops_without_parking", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3030, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cLeases := newCrashSimLeases(h.backend)

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("ws-q1", "request_input", map[string]any{"question": "Confirm environment?"}, 0.10), nil, true
			}
			return scriptedCall("ws-done", "complete", map[string]any{
				"answer":   "Handled concurrent answer without losing wake.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		run := h.startTaskRecord(ctx, "Race CommandAnswer against ParkTask(WaitQuestion)")

		racedOnce := false
		cLeases.onBeforePark = func(pCtx context.Context, _, runID, _ string, _ int64, reason string) {
			if reason == docstore.WaitQuestion && !racedOnce {
				racedOnce = true
				cur, err := h.store.GetRun(pCtx, runID)
				if err != nil || cur.Question == nil {
					t.Errorf("expected Question before ParkTask, got %+v err=%v", cur.Question, err)
					return
				}
				// Concurrent answer bumps wake_seq right BEFORE ParkTask executes!
				h.acceptCommands(pCtx, state.Command{
					ID:         h.ids.Next(),
					RunID:      runID,
					Kind:       state.CommandAnswer,
					QuestionID: cur.Question.ID,
					Text:       "Production environment confirmed.",
				})
			}
		}

		// Runner MUST NOT park — ParkTask returns parked=false, Runner calls RefreshTaskClaim and finishes in ONE Run()!
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, h.store, cLeases, run.ID, "worker-1")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("expected OutcomeDone in single Run without parking, got outcome=%s err=%v", outcome, err)
		}
		if !racedOnce {
			t.Fatal("expected concurrent answer race hook to fire before ParkTask")
		}
		finalRun, _ := h.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
			t.Fatalf("expected RunCompleted, got %+v", finalRun)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("concurrent_routing_clear_before_park_loops_without_parking", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3031, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cLeases := newCrashSimLeases(h.backend)

		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			return scriptedCall("ws-pub", "complete", map[string]any{
				"answer":   "Published immediately after concurrent routing clear.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		run := h.startTaskRecord(ctx, "Race PublishConversation against ParkTask(WaitRoutingPending)")
		if err := h.store.AcceptConversationInput(ctx, h.ids.Next(), "Quick user message", nil); err != nil {
			t.Fatalf("AcceptConversationInput: %v", err)
		}

		racedOnce := false
		cLeases.onBeforePark = func(pCtx context.Context, _, _, _ string, _ int64, reason string) {
			if reason == docstore.WaitRoutingPending && !racedOnce {
				racedOnce = true
				// Routing clears right BEFORE ParkTask -> atomically bumps wake_seq via WakeWaitingTasks!
				if !h.routeOneConversationTurn(pCtx, "Routed just before park.") {
					t.Errorf("expected routeOneConversationTurn to succeed")
				}
			}
		}

		// Runner MUST NOT park — loops immediately and publishes in ONE Run()!
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, h.store, cLeases, run.ID, "worker-1")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("expected OutcomeDone in single Run without parking, got outcome=%s err=%v", outcome, err)
		}
		if !racedOnce {
			t.Fatal("expected concurrent routing clear hook to fire before ParkTask")
		}
		finalRun, _ := h.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted || finalRun.PublicationPending || finalRun.CleanupPending {
			t.Fatalf("expected RunCompleted & clean, got %+v", finalRun)
		}
		h.checkSafetyAndQuiescence(ctx)
	})
}

type blockingReadAdapter struct {
	base       invoke.Adapter
	mu         sync.Mutex
	blockFirst bool
	started    chan struct{}
	calls      int
}

func (a *blockingReadAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	return a.base.Prepare(ctx, call)
}

func (a *blockingReadAdapter) Execute(ctx context.Context, call invoke.Call, prepared invoke.Prepared) invoke.Result {
	a.mu.Lock()
	a.calls++
	shouldBlock := a.blockFirst && prepared.Effect == extensions.EffectRead
	if shouldBlock {
		a.blockFirst = false
	}
	a.mu.Unlock()
	if shouldBlock {
		select {
		case a.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return invoke.Result{
			Outcome: "failed",
			Err:     ctx.Err(),
		}
	}
	return a.base.Execute(ctx, call, prepared)
}

func TestMultiWorkerLeaseLossDoesNotPoisonSafeReadOrCompose(t *testing.T) {
	t.Run("safe_read_lease_loss_reruns_cleanly", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3040, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		started := make(chan struct{}, 1)
		blocking := &blockingReadAdapter{base: h.adapter, blockFirst: true, started: started}
		h.adapters.Register(extensions.AdapterNative, "sim", blocking)

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("ll-r1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
			}
			return scriptedCall("ll-done", "complete", map[string]any{
				"answer":   "Safe read reran cleanly after lease loss without poisoned failed receipt.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		cStore.onRenewControl = func(_ context.Context, _ string, _ state.TaskFence) error {
			return errors.New("simulated lease renewal loss")
		}

		run := h.startTaskRecord(ctx, "Lease loss mid safe read")
		claim, ok, err := cLeases.ClaimTaskByID(ctx, h.user.ID, run.ID, "worker-1", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID worker-1: ok=%v err=%v", ok, err)
		}
		renewTick := make(chan time.Time, 1)
		act := *h.activities
		act.Store = cStore
		runner := &task.Runner{
			Activities: &act,
			Leases:     cLeases,
			UserID:     h.user.ID,
			Owner:      "worker-1",
			Wakes:      h.wakes,
			Now:        h.clock.Now,
			NewTicker:  func(time.Duration) (<-chan time.Time, func()) { return renewTick, func() {} },
		}

		doneCh := make(chan task.RunOutcome, 1)
		go func() {
			out, _ := runner.Run(users.WithID(ctx, h.user.ID), claim)
			doneCh <- out
		}()

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for safe read Execute to start")
		}
		// Trigger lease renewal failure -> cancels safe read ctx -> MUST NOT settle op as "failed"!
		renewTick <- h.clock.Now().Add(10 * time.Second)
		select {
		case out := <-doneCh:
			if out != task.OutcomeBackoff && out != task.OutcomeBusy {
				t.Fatalf("expected worker-1 backoff/busy on lease loss, got %s", out)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker-1 did not self-stop after lease renewal failure")
		}

		// Verify OperationRecord stayed "pending" (NOT poisoned as "failed")!
		opsMid, err := h.store.Operations(ctx, run.ID)
		if err != nil || len(opsMid) != 1 {
			t.Fatalf("expected 1 in-flight operation, got %+v (err=%v)", opsMid, err)
		}
		if opsMid[0].Outcome != "pending" {
			t.Fatalf("expected unsettled pending op after lease loss, got %q (%+v)", opsMid[0].Outcome, opsMid[0])
		}

		// Clear renewal fault; Worker 2 takes over and completes cleanly with Attempt=2, Outcome="confirmed"!
		cStore.onRenewControl = nil
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}
		opsFinal, _ := h.store.Operations(ctx, run.ID)
		if len(opsFinal) != 1 || opsFinal[0].Outcome != "confirmed" || opsFinal[0].Attempt != 2 {
			t.Fatalf("expected safe read confirmed on Attempt=2, got %+v", opsFinal)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("compose_lease_loss_does_not_settle_partial_or_failed", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3041, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		started := make(chan struct{}, 1)
		blocking := &blockingReadAdapter{base: h.adapter, blockFirst: true, started: started}
		h.adapters.Register(extensions.AdapterNative, "sim", blocking)

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("ll-c1", "compose", map[string]any{
					"script": `{"version":1,"concurrency":2,"steps":[{"id":"r1","tool":"sim_read","for_each":["key-1","key-2"],"as":"k","args":{"key":"$k"}}]}`,
				}, 0.10), nil, true
			}
			return scriptedCall("ll-cdone", "complete", map[string]any{
				"answer":   "Compose reran cleanly after lease loss.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		cStore.onRenewControl = func(_ context.Context, _ string, _ state.TaskFence) error {
			return errors.New("simulated lease loss during compose")
		}

		run := h.startTaskRecord(ctx, "Lease loss mid compose read")
		claim, ok, err := cLeases.ClaimTaskByID(ctx, h.user.ID, run.ID, "worker-1", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID worker-1: ok=%v err=%v", ok, err)
		}
		renewTick := make(chan time.Time, 1)
		act := *h.activities
		act.Store = cStore
		runner := &task.Runner{
			Activities: &act,
			Leases:     cLeases,
			UserID:     h.user.ID,
			Owner:      "worker-1",
			Wakes:      h.wakes,
			Now:        h.clock.Now,
			NewTicker:  func(time.Duration) (<-chan time.Time, func()) { return renewTick, func() {} },
		}

		doneCh := make(chan task.RunOutcome, 1)
		go func() {
			out, _ := runner.Run(users.WithID(ctx, h.user.ID), claim)
			doneCh <- out
		}()

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for compose safe read to start")
		}
		renewTick <- h.clock.Now().Add(10 * time.Second)
		select {
		case out := <-doneCh:
			if out != task.OutcomeBackoff && out != task.OutcomeBusy {
				t.Fatalf("expected worker-1 backoff/busy on compose lease loss, got %s", out)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker-1 did not self-stop after compose lease loss")
		}

		// Verify compose operation was NOT settled as "failed" or "partial"!
		opsMid, err := h.store.Operations(ctx, run.ID)
		if err != nil {
			t.Fatalf("Operations: %v", err)
		}
		for _, op := range opsMid {
			if op.Outcome == "failed" || op.Outcome == "partial" {
				t.Fatalf("compose lease loss poisoned operation %s as %q (%+v)", op.ID, op.Outcome, op)
			}
		}

		// Clear renewal fault; Worker 2 takes over and completes cleanly!
		cStore.onRenewControl = nil
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone after compose lease loss, got outcome=%s err=%v", outcome, err)
		}
		finalRun, _ := h.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
			t.Fatalf("expected RunCompleted, got %+v", finalRun)
		}
		h.checkSafetyAndQuiescence(ctx)
	})
}

func TestMultiWorkerAppendSkippedPermanentFailurePreservesPublishPhase(t *testing.T) {
	ctx := context.Background()
	// Model call costs 0.10 and call 0 (sim_paid_read) costs 0.15 -> spent reaches budget (0.25)
	// after call 0 without changing run.Revision, so call 1 enters appendSkipped (spending-limit)!
	h := newWFHarness(t, 3045, wfHarnessOpts{budgetUSD: 0.25, costPerCall: 0.10, paidCostUSD: 0.15})
	cStore := newCrashSimStore(h.store)
	cLeases := newCrashSimLeases(h.backend)

	c1 := transcript.ToolCall{ID: "as-1", Name: paidToolName, Args: map[string]any{"key": "key-1"}}
	c2 := transcript.ToolCall{ID: "as-2", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-2"}}
	h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		return agent.Response{
			Message:   &transcript.Message{Role: transcript.RoleModel, Parts: []transcript.Part{{ToolCall: &c1}, {ToolCall: &c2}}},
			Calls:     []transcript.ToolCall{c1, c2},
			Cost:      0.10,
			CostKnown: true,
		}, nil, true
	})

	run := h.startTaskRecord(ctx, "Verify appendSkipped permanent failure preserves TaskPhasePublish")

	// Corrupt the transcript read ONCE right when TaskPhaseCalls enters appendSkipped for call 1 ("as-2"),
	// triggering errTranscriptBlockInvalid (a permanent task error -> transitionFailure -> TaskPhasePublish)!
	var corruptNextRead atomic.Bool
	var injected atomic.Bool
	cStore.onReadHistory = func(_ context.Context, _ string, _ string) ([][]byte, error, bool) {
		if corruptNextRead.CompareAndSwap(true, false) {
			injected.Store(true)
			return [][]byte{[]byte("{corrupted-transcript-block")}, nil, true
		}
		return nil, nil, false
	}

	var sawPublishPhase bool
	var overwrotePublishPhase bool
	cStore.onAdvanceStep = func(_ context.Context, _ state.TaskFence, ctrl state.TaskControl) {
		if ctrl.Phase == state.TaskPhaseCalls && ctrl.CallIndex == 1 && !injected.Load() {
			corruptNextRead.Store(true)
		}
	}
	cStore.onSaveControl = func(_ context.Context, _ state.TaskFence, ctrl state.TaskControl) {
		if ctrl.Phase == state.TaskPhasePublish && ctrl.PendingPublication != nil {
			sawPublishPhase = true
		} else if sawPublishPhase && ctrl.Phase != state.TaskPhasePublish && ctrl.Phase != state.TaskPhaseCleanup && ctrl.Phase != state.TaskPhaseDone {
			overwrotePublishPhase = true
		}
	}

	outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-1")
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("expected OutcomeDone publishing permanent failure, got outcome=%s err=%v", outcome, err)
	}
	if !injected.Load() {
		t.Fatal("expected AppendToolResult permanent error hook to fire during appendSkipped")
	}
	if !sawPublishPhase {
		t.Fatal("expected transitionFailure to persist TaskPhasePublish")
	}
	if overwrotePublishPhase {
		t.Fatal("TaskPhaseCalls overwrote TaskPhasePublish after appendSkipped transitionFailure!")
	}
	finalRun, _ := h.store.GetRun(ctx, run.ID)
	if finalRun.Status != state.RunFailed || finalRun.CleanupPending {
		t.Fatalf("expected clean RunFailed after permanent appendSkipped error, got %+v", finalRun)
	}
	h.checkSafetyAndQuiescence(ctx)
}

func TestMultiWorkerPhaseBoundaryCrashes(t *testing.T) {
	t.Run("crash_mid_multi_tool_batch", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3050, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		c1 := transcript.ToolCall{ID: "mb-1", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-1"}}
		c2 := transcript.ToolCall{ID: "mb-2", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-2"}}
		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return agent.Response{
					Message:   &transcript.Message{Role: transcript.RoleModel, Parts: []transcript.Part{{ToolCall: &c1}, {ToolCall: &c2}}},
					Calls:     []transcript.ToolCall{c1, c2},
					Cost:      0.10,
					CostKnown: true,
				}, nil, true
			}
			return scriptedCall("mb-done", "complete", map[string]any{
				"answer":   "Resumed mid multi-tool batch at CallIndex=1 cleanly.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashed := false
		cStore.onAdvanceStep = func(_ context.Context, _ state.TaskFence, ctrl state.TaskControl) {
			if !crashed && ctrl.Phase == state.TaskPhaseCalls && ctrl.CallIndex == 1 {
				crashed = true
				cStore.markCrashed("worker-1")
				cLeases.markCrashed("worker-1")
				w1Cancel()
			}
		}

		run := h.startTaskRecord(ctx, "Crash between call 0 and call 1 in multi-tool batch")
		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashed {
			t.Fatal("expected Worker 1 to crash after committing CallIndex=1")
		}
		if len(h.adapter.Executed()) != 1 {
			t.Fatalf("expected exactly 1 tool executed before crash, got %d", len(h.adapter.Executed()))
		}

		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}
		// Total tool executions MUST be 2 (call 0 was NOT re-executed; call 1 ran once) and total model calls MUST be 2!
		if len(h.adapter.Executed()) != 2 {
			t.Fatalf("expected exactly 2 tool executions total (zero duplicate call 0), got %d", len(h.adapter.Executed()))
		}
		if len(h.model.ModelCalls()) != 2 {
			t.Fatalf("expected exactly 2 model calls total, got %d", len(h.model.ModelCalls()))
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("crash_post_incorporate_before_model", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3051, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			return scriptedCall("pi-done", "complete", map[string]any{
				"answer":   "Resumed post-Incorporate cleanly.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashed := false
		cStore.onSaveControl = func(_ context.Context, _ state.TaskFence, ctrl state.TaskControl) {
			if !crashed && ctrl.Phase == state.TaskPhaseModel {
				crashed = true
				cStore.markCrashed("worker-1")
				cLeases.markCrashed("worker-1")
				w1Cancel()
			}
		}

		run := h.startTaskRecord(ctx, "Crash right after sync -> model transition")
		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashed {
			t.Fatal("expected Worker 1 to crash right after sync -> model")
		}

		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}
		if len(h.model.ModelCalls()) != 1 {
			t.Fatalf("expected 1 model call total, got %d", len(h.model.ModelCalls()))
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("crash_post_ask_before_park", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3052, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("pa-q", "request_input", map[string]any{"question": "Confirm choice?"}, 0.10), nil, true
			}
			return scriptedCall("pa-done", "complete", map[string]any{
				"answer":   "Recovered post-Ask crash cleanly.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashed := false
		cStore.onProjectQuestion = func(_ context.Context, _ string, _ int64, _ state.Question) {
			if !crashed {
				crashed = true
				cStore.markCrashed("worker-1")
				cLeases.markCrashed("worker-1")
				w1Cancel()
			}
		}

		run := h.startTaskRecord(ctx, "Crash right after ProjectQuestion before ParkTask")
		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashed {
			t.Fatal("expected Worker 1 to crash right after ProjectQuestion")
		}

		// Worker 2 takes over after lease expiry -> idempotently re-checks Question and parks on WaitQuestion!
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeParked {
			t.Fatalf("worker-2: expected OutcomeParked, got outcome=%s err=%v", outcome, err)
		}

		midRun, _ := h.store.GetRun(ctx, run.ID)
		h.acceptCommands(ctx, state.Command{
			ID:         h.ids.Next(),
			RunID:      run.ID,
			Kind:       state.CommandAnswer,
			QuestionID: midRun.Question.ID,
			Text:       "Confirmed.",
		})
		outcome, err = runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-3")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-3: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("crash_post_publish_before_delete_lease", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3053, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			return scriptedCall("pp-done", "complete", map[string]any{
				"answer":   "Published before crash; lease cleaned up cleanly.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		w1Ctx, w1Cancel := context.WithCancel(ctx)
		defer w1Cancel()
		crashed := false
		cStore.onPublish = func(_ context.Context, _ state.PublishRequest) {
			if !crashed {
				crashed = true
				cStore.markCrashed("worker-1")
				cLeases.markCrashed("worker-1")
				cLeases.markCrashed("drop-delete")
				w1Cancel()
			}
		}

		run := h.startTaskRecord(ctx, "Crash right after clean Publish before DeleteTaskLease")
		_, _ = runWithVirtualLeaseRenewal(t, w1Ctx, h, cStore, cLeases, run.ID, "worker-1")
		if !crashed {
			t.Fatal("expected Worker 1 to crash right after Publish")
		}
		// Lease must still exist while run is already RunCompleted & clean!
		leasesBefore, _ := h.backend.ListTaskLeases(ctx, h.user.ID)
		if len(leasesBefore) != 1 {
			t.Fatalf("expected stale lease remaining after post-publish crash, got %+v", leasesBefore)
		}

		// Clear drop-delete and advance past lease expiry -> Worker 2 sees terminal clean run and deletes lease!
		cLeases.mu.Lock()
		delete(cLeases.crashed, "drop-delete")
		cLeases.mu.Unlock()
		h.clock.Advance(state.TaskLease + time.Second)
		outcome, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, run.ID, "worker-2")
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("worker-2: expected OutcomeDone cleaning up stale lease, got outcome=%s err=%v", outcome, err)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("crash_finishing_parent_post_child_cleanup", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 3054, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		cStore := newCrashSimStore(h.store)
		cLeases := newCrashSimLeases(h.backend)

		turn := 0
		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				// First perform a read so parent Admit() sets CleanupPending=true (requiring FinishCleanup on cancel).
				return scriptedCall("fp-read", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
			}
			if turn == 2 {
				return scriptedCall("fp-work", "request_work", map[string]any{
					"request": "Delegate child task",
					"why":     "Need subtask work",
				}, 0.10), nil, true
			}
			return scriptedCall("fp-child-done", "complete", map[string]any{
				"answer":   "Child finished cleanly.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		parent := h.startTaskRecord(ctx, "Parent task stopped while child runs")
		// Parent runs until parked on QuestionWork.
		out, err := runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, parent.ID, "worker-p1")
		if err != nil || out != task.OutcomeParked {
			t.Fatalf("parent worker-p1: expected OutcomeParked on QuestionWork, got %s err=%v", out, err)
		}
		// Spawn child linked to parent's QuestionWork.
		pSnap, _ := h.store.GetRun(ctx, parent.ID)
		childID := h.ids.Next()
		h.acceptCommands(ctx, state.Command{
			ID:         h.ids.Next(),
			RunID:      childID,
			Kind:       state.CommandStart,
			Text:       "Child task work",
			ParentID:   parent.ID,
			QuestionID: pSnap.Question.ID,
		})
		// Stop parent while child is still running -> parent transitions to Finishing, runs FinishCleanup,
		// and crashes right after FinishCleanup commits!
		h.acceptCommands(ctx, state.Command{
			ID:    h.ids.Next(),
			RunID: parent.ID,
			Kind:  state.CommandStop,
		})
		w2Ctx, w2Cancel := context.WithCancel(ctx)
		defer w2Cancel()
		crashedOnCleanup := false
		cStore.onFinishCleanup = func(_ context.Context, runID string, _ string) {
			if runID == parent.ID && !crashedOnCleanup {
				crashedOnCleanup = true
				cStore.markCrashed("worker-p2")
				cLeases.markCrashed("worker-p2")
				w2Cancel()
			}
		}
		_, _ = runWithVirtualLeaseRenewal(t, w2Ctx, h, cStore, cLeases, parent.ID, "worker-p2")
		if !crashedOnCleanup {
			t.Fatal("expected worker-p2 to crash right after parent FinishCleanup")
		}
		// Worker p2b takes over after lease expiry and parks cleanly on WaitFinishing while child runs!
		h.clock.Advance(state.TaskLease + time.Second)
		out, err = runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, parent.ID, "worker-p2b")
		if err != nil || out != task.OutcomeParked {
			t.Fatalf("parent worker-p2b: expected OutcomeParked on WaitFinishing, got %s err=%v", out, err)
		}

		// Run child to completion/cancellation -> wakes parent and triggers finishPending!
		out, err = runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, childID, "worker-c1")
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("child worker-c1: expected OutcomeDone, got %s err=%v", out, err)
		}

		// Simulate worker-p3 failing right at DeleteTaskLease (transient lease drop), then worker-p4 finishing cleanly!
		cLeases.markCrashed("drop-delete")
		_, _ = runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, parent.ID, "worker-p3")

		// Clear drop-delete and advance past backoff/lease expiry -> Worker p4 deletes parent lease cleanly!
		cLeases.mu.Lock()
		delete(cLeases.crashed, "drop-delete")
		cLeases.mu.Unlock()
		h.clock.Advance(state.TaskLease + time.Second)
		out, err = runWithVirtualLeaseRenewal(t, ctx, h, cStore, cLeases, parent.ID, "worker-p4")
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("parent worker-p4: expected OutcomeDone, got %s err=%v", out, err)
		}
		finalParent, _ := h.store.GetRun(ctx, parent.ID)
		if finalParent.Status != state.RunCancelled || finalParent.CleanupPending {
			t.Fatalf("expected final parent RunCancelled & clean, got %+v", finalParent)
		}
		h.checkSafetyAndQuiescence(ctx)
	})
}
