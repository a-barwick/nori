package records_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

type wakeCall struct {
	kind   string
	userID string
	runID  string
	lane   string
	reason string
	wakeAt time.Time
}

type errRetryableFake struct{}

func (errRetryableFake) Error() string { return "injected retryable tx conflict" }

type fakeBackend struct {
	mu                 sync.Mutex
	docs               map[string][]byte
	seqs               map[string]int64
	wakes              []wakeCall
	failFirstTxCommits int
	failWritePath      string
}

func newFakeBackend() *fakeBackend {
	b := &fakeBackend{
		docs: map[string][]byte{
			"system/control": []byte(`{"paused":false}`),
		},
		seqs: map[string]int64{},
	}
	return b
}

func (b *fakeBackend) resetWakes() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wakes = nil
}

func (b *fakeBackend) committedWakes() []wakeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]wakeCall(nil), b.wakes...)
}

func (b *fakeBackend) Close() error { return nil }
func (b *fakeBackend) Watch(context.Context, docstore.QuerySpec, func([]docstore.Record) error) error {
	return nil
}

func (b *fakeBackend) DeleteBatch(_ context.Context, paths []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range paths {
		delete(b.docs, p)
	}
	return nil
}

func (b *fakeBackend) CurrentSequence(_ context.Context, path string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seqs[path], nil
}

func (b *fakeBackend) Get(_ context.Context, path string) (docstore.Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fakeGet(b.docs, path)
}

func (b *fakeBackend) Query(_ context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fakeQuery(b.docs, s)
}

func (b *fakeBackend) Apply(ctx context.Context, m docstore.Mutation) error {
	return b.RunTransaction(ctx, false, 5, func(tx docstore.TransactionBackend) error {
		return tx.Apply(ctx, m)
	})
}

func (b *fakeBackend) RunTransaction(_ context.Context, _ bool, attempts int, fn func(docstore.TransactionBackend) error) error {
	if attempts <= 0 {
		attempts = 5
	}
	for attempt := 0; attempt < attempts; attempt++ {
		b.mu.Lock()
		snapDocs := make(map[string][]byte, len(b.docs))
		for k, v := range b.docs {
			snapDocs[k] = append([]byte(nil), v...)
		}
		snapSeqs := make(map[string]int64, len(b.seqs))
		for k, v := range b.seqs {
			snapSeqs[k] = v
		}
		failPath := b.failWritePath
		b.mu.Unlock()

		tx := &fakeTx{
			docs:          snapDocs,
			seqs:          snapSeqs,
			failWritePath: failPath,
		}
		err := fn(tx)
		if err != nil {
			if errors.Is(err, errRetryableFake{}) {
				continue
			}
			return err
		}

		b.mu.Lock()
		if b.failFirstTxCommits > 0 {
			b.failFirstTxCommits--
			b.mu.Unlock()
			continue
		}
		b.docs = tx.docs
		b.seqs = tx.seqs
		b.wakes = append(b.wakes, tx.wakes...)
		b.mu.Unlock()
		return nil
	}
	return errors.New("transaction attempts exhausted")
}

type fakeTx struct {
	docs          map[string][]byte
	seqs          map[string]int64
	wakes         []wakeCall
	failWritePath string
}

var (
	_ docstore.TransactionBackend = (*fakeTx)(nil)
	_ docstore.Sequencer          = (*fakeTx)(nil)
	_ docstore.WakeWriter         = (*fakeTx)(nil)
)

func (t *fakeTx) Get(_ context.Context, path string) (docstore.Record, error) {
	return fakeGet(t.docs, path)
}

func (t *fakeTx) Query(_ context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	return fakeQuery(t.docs, s)
}

func (t *fakeTx) NextSequence(_ context.Context, path string, count int) (int64, error) {
	if count <= 0 {
		return 0, nil
	}
	start := t.seqs[path] + 1
	t.seqs[path] += int64(count)
	return start, nil
}

func (t *fakeTx) WakeTask(_ context.Context, userID, runID string, wakeAt time.Time) error {
	t.wakes = append(t.wakes, wakeCall{kind: "task", userID: userID, runID: runID, wakeAt: wakeAt})
	return nil
}

func (t *fakeTx) WakeWaitingTasks(_ context.Context, userID, reason string) error {
	t.wakes = append(t.wakes, wakeCall{kind: "waiting", userID: userID, reason: reason})
	return nil
}

func (t *fakeTx) WakeUser(_ context.Context, userID, lane string, wakeAt time.Time) error {
	t.wakes = append(t.wakes, wakeCall{kind: "user", userID: userID, lane: lane, wakeAt: wakeAt})
	return nil
}

func (t *fakeTx) NotifyPause(_ context.Context) error {
	t.wakes = append(t.wakes, wakeCall{kind: "pause"})
	return nil
}

func (t *fakeTx) Apply(_ context.Context, m docstore.Mutation) error {
	if t.failWritePath != "" && m.Path == t.failWritePath {
		return errors.New("injected write failure")
	}
	if m.Kind == "delete" {
		delete(t.docs, m.Path)
		return nil
	}
	if m.Kind == "create" {
		if _, exists := t.docs[m.Path]; exists {
			return docstore.ErrExists
		}
	}
	fields := m.Fields
	if m.Kind == "update" || m.Merge {
		old, err := fakeGet(t.docs, m.Path)
		if errors.Is(err, docstore.ErrNotFound) && m.Kind == "set" {
			old.Fields = map[string]any{}
		} else if err != nil {
			return err
		}
		fields = old.Fields
		if m.Merge {
			fakeMerge(fields, m.Fields)
		}
		for _, ch := range m.Updates {
			parts := strings.Split(ch.Path, ".")
			parent := fields
			for _, k := range parts[:len(parts)-1] {
				child, ok := parent[k].(map[string]any)
				if !ok {
					child = map[string]any{}
					parent[k] = child
				}
				parent = child
			}
			key := parts[len(parts)-1]
			if docstore.IsDelete(ch.Value) {
				delete(parent, key)
				continue
			}
			if union, ok := ch.Value.(docstore.Union); ok {
				prior, _ := parent[key].([]any)
				for _, val := range union {
					enc, err := docstore.Value(val)
					if err != nil {
						return err
					}
					enc = docstore.JSONValue(enc)
					raw, _ := json.Marshal(enc)
					dup := false
					for _, prev := range prior {
						pRaw, _ := json.Marshal(docstore.JSONValue(prev))
						if bytes.Equal(raw, pRaw) {
							dup = true
							break
						}
					}
					if !dup {
						prior = append(prior, enc)
					}
				}
				parent[key] = prior
				continue
			}
			val, err := docstore.Value(ch.Value)
			if err != nil {
				return err
			}
			parent[key] = val
		}
	}
	raw, err := json.Marshal(docstore.JSONValue(fields))
	if err != nil {
		return err
	}
	t.docs[m.Path] = raw
	return nil
}

func fakeMerge(dst, src map[string]any) {
	for k, v := range src {
		if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
			existing, _ := dst[k].(map[string]any)
			if existing == nil {
				existing = map[string]any{}
			}
			fakeMerge(existing, nested)
			dst[k] = existing
		} else {
			dst[k] = v
		}
	}
}

func fakeDecode(raw []byte) (map[string]any, error) {
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	docstore.NormalizeNumbers(m)
	return m, nil
}

func fakeGet(docs map[string][]byte, path string) (docstore.Record, error) {
	raw, ok := docs[path]
	if !ok {
		return docstore.Record{}, docstore.ErrNotFound
	}
	fields, err := fakeDecode(raw)
	if err != nil {
		return docstore.Record{}, err
	}
	return docstore.Record{Path: path, Fields: fields}, nil
}

func fakeField(rec docstore.Record, field string) any {
	if field == docstore.DocumentID {
		return rec.Path[strings.LastIndex(rec.Path, "/")+1:]
	}
	cur := any(rec.Fields)
	for _, part := range strings.Split(field, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func fakeCompare(a, b any) int {
	switch av := a.(type) {
	case string:
		bv, _ := b.(string)
		return cmp.Compare(av, bv)
	case int64:
		switch bv := b.(type) {
		case int64:
			return cmp.Compare(av, bv)
		case int:
			return cmp.Compare(av, int64(bv))
		case float64:
			return cmp.Compare(float64(av), bv)
		}
	case float64:
		switch bv := b.(type) {
		case float64:
			return cmp.Compare(av, bv)
		case int64:
			return cmp.Compare(av, float64(bv))
		}
	case bool:
		bv, _ := b.(bool)
		if av == bv {
			return 0
		}
		if !av && bv {
			return -1
		}
		return 1
	}
	return 0
}

func fakeMatches(rec docstore.Record, f docstore.Filter) bool {
	val := fakeField(rec, f.Field)
	if val == nil {
		return false
	}
	c := fakeCompare(val, f.Value)
	switch f.Op {
	case "==":
		return c == 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	default:
		return false
	}
}

func fakeQuery(docs map[string][]byte, s docstore.QuerySpec) ([]docstore.Record, error) {
	prefix := s.Collection + "/"
	var out []docstore.Record
	for path, raw := range docs {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		rest := path[len(prefix):]
		if strings.Contains(rest, "/") {
			continue
		}
		fields, err := fakeDecode(raw)
		if err != nil {
			return nil, err
		}
		rec := docstore.Record{Path: path, Fields: fields}
		ok := true
		for _, f := range s.Filters {
			if !fakeMatches(rec, f) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, rec)
		}
	}
	slices.SortFunc(out, func(a, b docstore.Record) int {
		for _, o := range s.Orders {
			c := fakeCompare(fakeField(a, o.Field), fakeField(b, o.Field))
			if c != 0 {
				if o.Direction == docstore.Desc {
					return -c
				}
				return c
			}
		}
		return cmp.Compare(a.Path, b.Path)
	})
	if s.Count > 0 && len(out) > s.Count {
		out = out[:s.Count]
	}
	return out, nil
}

func testUser() users.User {
	return users.User{
		ID:       users.LegacyID,
		Email:    "alpha@example.com",
		TimeZone: "UTC",
	}
}

func newTestDB(t *testing.T, clock *time.Time) (*fakeBackend, *records.Database) {
	t.Helper()
	fb := newFakeBackend()
	db := records.NewShared(fb, testUser(), 5.0)
	db.Now = func() time.Time { return *clock }
	return fb, db
}

func putRun(t *testing.T, fb *fakeBackend, user users.User, run state.Run) {
	t.Helper()
	enc, err := docstore.Encode(run)
	if err != nil {
		t.Fatalf("encode run: %v", err)
	}
	raw, err := json.Marshal(docstore.JSONValue(enc))
	if err != nil {
		t.Fatalf("marshal run: %v", err)
	}
	fb.mu.Lock()
	fb.docs["owners/"+user.DataOwner()+"/runs/"+run.ID] = raw
	fb.mu.Unlock()
}

func TestTaskControlFencingAndRenewal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	_, db := newTestDB(t, &now)

	if _, err := db.GetTaskControl(ctx, "run-1"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("GetTaskControl missing = %v, want ErrNotFound", err)
	}
	if err := db.RenewTaskControl(ctx, "run-1", state.TaskFence{Owner: "w1", Fence: 1}); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("RenewTaskControl missing = %v, want ErrTaskBusy", err)
	}
	if _, err := db.SaveTaskControl(ctx, state.TaskFence{Owner: "w1", Fence: 1}, state.TaskControl{RunID: "run-1"}); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("SaveTaskControl missing = %v, want ErrTaskBusy", err)
	}
	if err := db.ReleaseTaskControl(ctx, "run-1", state.TaskFence{Owner: "w1", Fence: 1}); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("ReleaseTaskControl missing = %v, want ErrTaskBusy", err)
	}

	ctrl, f1, err := db.AcquireTaskControl(ctx, "run-1", "worker-a")
	if err != nil {
		t.Fatalf("AcquireTaskControl: %v", err)
	}
	if ctrl.RunID != "run-1" || ctrl.Phase != state.TaskPhaseSync || f1.Owner != "worker-a" || f1.Fence != 1 {
		t.Fatalf("unexpected acquire result: ctrl=%+v fence=%+v", ctrl, f1)
	}
	if !ctrl.LeaseUntil.Equal(now.Add(state.TaskLease)) {
		t.Fatalf("LeaseUntil = %v, want %v", ctrl.LeaseUntil, now.Add(state.TaskLease))
	}

	now = now.Add(5 * time.Second)
	if _, _, err := db.AcquireTaskControl(ctx, "run-1", "worker-b"); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("competing acquire = %v, want ErrTaskBusy", err)
	}

	now = now.Add(10 * time.Second)
	if err := db.RenewTaskControl(ctx, "run-1", f1); err != nil {
		t.Fatalf("RenewTaskControl: %v", err)
	}
	renewedUntil := now.Add(state.TaskLease)

	stale := ctrl
	stale.Phase = state.TaskPhaseModel
	stale.Models = 3
	stale.PendingModelID = "run-1-m3-r0"
	stale.Owner = "spoofed"
	stale.Fence = 999
	saved, err := db.SaveTaskControl(ctx, f1, stale)
	if err != nil {
		t.Fatalf("SaveTaskControl: %v", err)
	}
	if saved.Owner != "worker-a" || saved.Fence != 1 || !saved.LeaseUntil.Equal(renewedUntil) {
		t.Fatalf("SaveTaskControl did not preserve authoritative lease: %+v (want LeaseUntil=%v)", saved, renewedUntil)
	}
	if saved.Phase != state.TaskPhaseModel || saved.Models != 3 || saved.PendingModelID != "run-1-m3-r0" {
		t.Fatalf("SaveTaskControl lost cursor fields: %+v", saved)
	}

	now = renewedUntil
	if err := db.RenewTaskControl(ctx, "run-1", f1); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("expired renew = %v, want ErrTaskBusy", err)
	}
	taken, f2, err := db.AcquireTaskControl(ctx, "run-1", "worker-b")
	if err != nil {
		t.Fatalf("takeover acquire: %v", err)
	}
	if f2.Owner != "worker-b" || f2.Fence != 2 || taken.Models != 3 || taken.PendingModelID != "run-1-m3-r0" {
		t.Fatalf("unexpected takeover: ctrl=%+v fence=%+v", taken, f2)
	}

	if _, err := db.SaveTaskControl(ctx, f1, stale); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("stale save = %v, want ErrTaskBusy", err)
	}

	now = now.Add(2 * time.Second)
	if err := db.ReleaseTaskControl(ctx, "run-1", f2); err != nil {
		t.Fatalf("ReleaseTaskControl: %v", err)
	}
	got, err := db.GetTaskControl(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetTaskControl after release: %v", err)
	}
	if got.Owner != "" || !got.LeaseUntil.IsZero() || got.Fence != 2 || got.Models != 3 {
		t.Fatalf("unexpected control after release: %+v", got)
	}

	_, f3, err := db.AcquireTaskControl(ctx, "run-1", "worker-c")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if f3.Owner != "worker-c" || f3.Fence != 3 {
		t.Fatalf("re-acquire fence = %+v, want worker-c/3", f3)
	}
}

func TestAdvanceTaskStepAtomicSpendSubtaskRollupAndRetry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	fb, db := newTestDB(t, &now)
	u := testUser()

	putRun(t, fb, u, state.Run{
		ID:        "parent-1",
		Status:    state.RunRunning,
		Budget:    10.0,
		Spent:     0.10,
		StartedAt: now,
	})
	putRun(t, fb, u, state.Run{
		ID:        "child-1",
		ParentID:  "parent-1",
		Status:    state.RunRunning,
		Budget:    10.0,
		Spent:     0.0,
		StartedAt: now,
	})

	ctrl, f1, err := db.AcquireTaskControl(ctx, "child-1", "worker-a")
	if err != nil {
		t.Fatalf("AcquireTaskControl: %v", err)
	}

	now = now.Add(10 * time.Second)
	if err := db.RenewTaskControl(ctx, "child-1", f1); err != nil {
		t.Fatalf("RenewTaskControl: %v", err)
	}
	renewedUntil := now.Add(state.TaskLease)

	fb.failFirstTxCommits = 1

	next := ctrl
	next.Phase = state.TaskPhaseCalls
	next.Models = 1
	next.Ref = "ref-1"
	savedCtrl, updatedRun, err := db.AdvanceTaskStep(ctx, f1, next, &state.SpendUpdate{
		RunID:  "child-1",
		Spent:  0.25,
		Budget: 10.0,
	})
	if err != nil {
		t.Fatalf("AdvanceTaskStep (with retry): %v", err)
	}
	if savedCtrl.Phase != state.TaskPhaseCalls || savedCtrl.Models != 1 || !savedCtrl.LeaseUntil.Equal(renewedUntil) {
		t.Fatalf("unexpected savedCtrl: %+v", savedCtrl)
	}
	if updatedRun.Spent != 0.25 || updatedRun.Budget != 10.0 || updatedRun.CostUnknown {
		t.Fatalf("unexpected updatedRun: %+v", updatedRun)
	}
	parent, err := db.GetRun(ctx, "parent-1")
	if err != nil {
		t.Fatalf("GetRun parent: %v", err)
	}
	if parent.SubtaskSpent != 0.25 || parent.CostUnknown {
		t.Fatalf("parent SubtaskSpent = %v, CostUnknown = %v; want 0.25, false", parent.SubtaskSpent, parent.CostUnknown)
	}

	next2 := savedCtrl
	next2.Tools = 1
	next2.CallIndex = 1
	_, updatedRun2, err := db.AdvanceTaskStep(ctx, f1, next2, &state.SpendUpdate{
		RunID:       "child-1",
		Spent:       0.60,
		Budget:      10.0,
		CostUnknown: true,
	})
	if err != nil {
		t.Fatalf("AdvanceTaskStep 2: %v", err)
	}
	if updatedRun2.Spent != 0.60 || !updatedRun2.CostUnknown {
		t.Fatalf("updatedRun2 = %+v, want spent=0.60 unknown=true", updatedRun2)
	}
	parent, err = db.GetRun(ctx, "parent-1")
	if err != nil {
		t.Fatalf("GetRun parent 2: %v", err)
	}
	if parent.SubtaskSpent < 0.5999 || parent.SubtaskSpent > 0.6001 || !parent.CostUnknown {
		t.Fatalf("parent after step 2: SubtaskSpent=%v CostUnknown=%v, want 0.60/true", parent.SubtaskSpent, parent.CostUnknown)
	}

	fb.failWritePath = "owners/" + u.DataOwner() + "/task_controls/child-1"
	nextFail := savedCtrl
	nextFail.Tools = 99
	if _, _, err := db.AdvanceTaskStep(ctx, f1, nextFail, &state.SpendUpdate{RunID: "child-1", Spent: 4.00}); err == nil {
		t.Fatal("expected AdvanceTaskStep error on injected write failure")
	}
	fb.failWritePath = ""

	childAfterFail, _ := db.GetRun(ctx, "child-1")
	parentAfterFail, _ := db.GetRun(ctx, "parent-1")
	ctrlAfterFail, _ := db.GetTaskControl(ctx, "child-1")
	if childAfterFail.Spent != 0.60 || parentAfterFail.SubtaskSpent < 0.5999 || parentAfterFail.SubtaskSpent > 0.6001 || ctrlAfterFail.Tools != 1 {
		t.Fatalf("partial commit leaked after failure: child.Spent=%v parent.SubtaskSpent=%v ctrl.Tools=%d",
			childAfterFail.Spent, parentAfterFail.SubtaskSpent, ctrlAfterFail.Tools)
	}

	now = renewedUntil.Add(time.Second)
	nextStale := savedCtrl
	nextStale.Models = 50
	if _, _, err := db.AdvanceTaskStep(ctx, f1, nextStale, &state.SpendUpdate{RunID: "child-1", Spent: 9.99}); !errors.Is(err, state.ErrTaskBusy) {
		t.Fatalf("stale AdvanceTaskStep = %v, want ErrTaskBusy", err)
	}
	childAfterStale, _ := db.GetRun(ctx, "child-1")
	if childAfterStale.Spent != 0.60 {
		t.Fatalf("stale AdvanceTaskStep modified Run.Spent to %v", childAfterStale.Spent)
	}
}

func TestSameTxWakeHooks_CommandsAndTaskEvents(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	fb, db := newTestDB(t, &now)
	u := testUser()

	_, err := db.AcceptCommandBatch(ctx, []state.Command{
		{ID: "cmd-1", RunID: "run-1", Kind: state.CommandStart, Text: "do work"},
		{ID: "cmd-2", RunID: "run-1", Kind: state.CommandSteer, Text: "extra detail"},
	}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch start+steer: %v", err)
	}
	wakes := fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "task" || wakes[0].userID != u.ID || wakes[0].runID != "run-1" {
		t.Fatalf("start+steer wakes = %+v, want 1 task wake for user %q run-1", wakes, u.ID)
	}

	fb.resetWakes()
	_, err = db.AcceptCommandBatch(ctx, []state.Command{
		{ID: "cmd-hold-1", RunID: "run-1", Kind: state.CommandHold, Text: "wait for result"},
	}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch hold: %v", err)
	}
	if got := fb.committedWakes(); len(got) != 0 {
		t.Fatalf("CommandHold on running task emitted wakes: %+v, want none", got)
	}

	run, _ := db.GetRun(ctx, "run-1")
	if err := db.Incorporate(ctx, state.IncorporateRequest{
		RunID: "run-1", Revision: run.Revision, Position: state.TaskPosition(run),
	}); err != nil {
		t.Fatalf("Incorporate: %v", err)
	}
	fb.resetWakes()
	err = db.ProjectQuestion(ctx, "run-1", run.Revision, state.Question{
		ID: "q-1", Kind: "input", Text: "which region?", Revision: run.Revision,
	})
	if err != nil {
		t.Fatalf("ProjectQuestion: %v", err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "user" || wakes[0].userID != u.ID || wakes[0].lane != docstore.LaneConversation {
		t.Fatalf("ProjectQuestion wakes = %+v, want conversation wake for %q", wakes, u.ID)
	}

	if err := db.Admit(ctx, "run-1", run.Revision, "op-late-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	putRun(t, fb, u, func() state.Run {
		r, _ := db.GetRun(ctx, "run-1")
		r.CleanupPending = false
		return r
	}())

	fb.resetWakes()
	_, err = db.Publish(ctx, state.PublishRequest{
		RunID:    "run-1",
		Revision: run.Revision,
		Status:   state.RunCompleted,
		Text:     "all done",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "user" || wakes[0].userID != u.ID || wakes[0].lane != docstore.LaneConversation {
		t.Fatalf("Publish wakes = %+v, want conversation wake for %q", wakes, u.ID)
	}

	fb.resetWakes()
	_, err = db.AcceptCommandBatch(ctx, []state.Command{
		{ID: "cmd-hold-late", RunID: "run-1", Kind: state.CommandHold, Text: "late hold"},
	}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch late hold: %v", err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "user" || wakes[0].userID != u.ID || wakes[0].lane != docstore.LaneConversation {
		t.Fatalf("late CommandHold wakes = %+v, want conversation wake for %q", wakes, u.ID)
	}

	fb.resetWakes()
	ref, err := db.AppendHistory(ctx, "run-1", "", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	err = db.PutOperation(ctx, "run-1", state.OperationRecord{
		ID:          "op-late-1",
		CallID:      "call-1",
		Name:        "send_email",
		EffectClass: "sensitive_send",
		Outcome:     "confirmed",
		ResultRef:   ref,
		Revision:    run.Revision,
		Attempt:     1,
		Action:      &state.Action{Digest: "sha256:abc", Text: "Send email"},
	})
	if err != nil {
		t.Fatalf("PutOperation late write: %v", err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "user" || wakes[0].userID != u.ID || wakes[0].lane != docstore.LaneConversation {
		t.Fatalf("PutOperation late write wakes = %+v, want conversation wake for %q", wakes, u.ID)
	}
}

func TestSameTxWakeHooks_SubtasksAndCleanup(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	fb, db := newTestDB(t, &now)
	u := testUser()

	putRun(t, fb, u, state.Run{
		ID:        "parent-1",
		Status:    state.RunRunning,
		Revision:  1,
		StartedAt: now,
		Subtasks:  []state.SubtaskLink{{ID: "child-1", QuestionID: "q-sub", Status: state.RunRunning}},
	})
	putRun(t, fb, u, state.Run{
		ID:             "child-1",
		ParentID:       "parent-1",
		Status:         state.RunRunning,
		Revision:       1,
		CleanupPending: true,
		StartedAt:      now,
	})

	fb.resetWakes()
	_, err := db.Publish(ctx, state.PublishRequest{
		RunID:    "parent-1",
		Revision: 1,
		Status:   state.RunCancelled,
		Text:     "stopped",
	})
	if err != nil {
		t.Fatalf("Publish parent (finishing): %v", err)
	}
	wakes := fb.committedWakes()
	foundChildWake := false
	for _, w := range wakes {
		if w.kind == "task" && w.userID == u.ID && w.runID == "child-1" {
			foundChildWake = true
		}
	}
	if !foundChildWake {
		t.Fatalf("stopSubtasks did not wake child-1; wakes = %+v", wakes)
	}

	child, _ := db.GetRun(ctx, "child-1")
	_, err = db.Publish(ctx, state.PublishRequest{
		RunID:    "child-1",
		Revision: child.Revision,
		Status:   state.RunCancelled,
		Text:     "child stopped",
	})
	if err != nil {
		t.Fatalf("Publish child (finishing): %v", err)
	}

	fb.resetWakes()
	if err := db.FinishCleanup(ctx, "child-1", "temporary cleanup error"); err != nil {
		t.Fatalf("FinishCleanup with detail: %v", err)
	}
	if got := fb.committedWakes(); len(got) != 0 {
		t.Fatalf("FinishCleanup with error detail should not wake; got %+v", got)
	}

	fb.resetWakes()
	if err := db.FinishCleanup(ctx, "child-1", ""); err != nil {
		t.Fatalf("FinishCleanup confirmed: %v", err)
	}
	wakes = fb.committedWakes()
	var sawChild, sawParent, sawConv bool
	for _, w := range wakes {
		if w.kind == "task" && w.userID == u.ID && w.runID == "child-1" {
			sawChild = true
		}
		if w.kind == "task" && w.userID == u.ID && w.runID == "parent-1" {
			sawParent = true
		}
		if w.kind == "user" && w.userID == u.ID && w.lane == docstore.LaneConversation {
			sawConv = true
		}
	}
	if !sawChild || !sawParent || !sawConv {
		t.Fatalf("expected child, parent, and conversation wakes on FinishCleanup; got %+v", wakes)
	}
}

func TestSameTxWakeHooks_ChildPublishWakesRunningParent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	fb, db := newTestDB(t, &now)
	u := testUser()

	putRun(t, fb, u, state.Run{
		ID:        "parent-2",
		Status:    state.RunRunning,
		Revision:  1,
		StartedAt: now,
		Subtasks:  []state.SubtaskLink{{ID: "child-2", QuestionID: "q-sub-2", Status: state.RunRunning}},
	})
	putRun(t, fb, u, state.Run{
		ID:           "child-2",
		ParentID:     "parent-2",
		Status:       state.RunRunning,
		Revision:     1,
		Position:     1,
		Incorporated: 1,
		StartedAt:    now,
	})

	fb.resetWakes()
	_, err := db.Publish(ctx, state.PublishRequest{
		RunID:    "child-2",
		Revision: 1,
		Status:   state.RunCompleted,
		Text:     "subtask answer",
	})
	if err != nil {
		t.Fatalf("Publish child-2: %v", err)
	}
	wakes := fb.committedWakes()
	foundParent := false
	for _, w := range wakes {
		if w.kind == "task" && w.userID == u.ID && w.runID == "parent-2" {
			foundParent = true
		}
	}
	if !foundParent {
		t.Fatalf("child Publish did not wake parent-2; wakes = %+v", wakes)
	}
}

func TestSameTxWakeHooks_SubtaskSiblingsSkipRedundantParentWake(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	fb, db := newTestDB(t, &now)
	u := testUser()

	putRun(t, fb, u, state.Run{
		ID:        "parent-qw",
		Status:    state.RunRunning,
		Revision:  1,
		StartedAt: now,
		Question: &state.Question{
			ID:         "q-work-1",
			Kind:       state.QuestionWork,
			Text:       "Run two subtasks",
			Revision:   1,
			ResolvedBy: "cmd-spawn",
		},
		Subtasks: []state.SubtaskLink{
			{ID: "child-a", QuestionID: "q-work-1", Status: state.RunRunning},
			{ID: "child-b", QuestionID: "q-work-1", Status: state.RunRunning},
		},
	})
	putRun(t, fb, u, state.Run{
		ID:             "child-a",
		ParentID:       "parent-qw",
		Status:         state.RunRunning,
		Revision:       1,
		Position:       1,
		Incorporated:   1,
		CleanupPending: true,
		StartedAt:      now,
	})
	putRun(t, fb, u, state.Run{
		ID:           "child-b",
		ParentID:     "parent-qw",
		Status:       state.RunRunning,
		Revision:     1,
		Position:     1,
		Incorporated: 1,
		StartedAt:    now,
	})

	fb.resetWakes()
	if _, err := db.Publish(ctx, state.PublishRequest{
		RunID:    "child-a",
		Revision: 1,
		Status:   state.RunCompleted,
		Text:     "child-a done",
	}); err != nil {
		t.Fatalf("Publish child-a (finishing): %v", err)
	}
	if err := db.FinishCleanup(ctx, "child-a", ""); err != nil {
		t.Fatalf("FinishCleanup child-a: %v", err)
	}
	for _, w := range fb.committedWakes() {
		if w.kind == "task" && w.runID == "parent-qw" {
			t.Fatalf("intermediate child-a completion redundantly woke parent-qw while child-b still running: wakes=%+v", fb.committedWakes())
		}
	}

	fb.resetWakes()
	if _, err := db.Publish(ctx, state.PublishRequest{
		RunID:    "child-b",
		Revision: 1,
		Status:   state.RunCompleted,
		Text:     "child-b done",
	}); err != nil {
		t.Fatalf("Publish child-b: %v", err)
	}
	wokeParent := false
	for _, w := range fb.committedWakes() {
		if w.kind == "task" && w.userID == u.ID && w.runID == "parent-qw" {
			wokeParent = true
		}
	}
	if !wokeParent {
		t.Fatalf("final sibling child-b Publish did not wake parent-qw: wakes=%+v", fb.committedWakes())
	}

	finReq := state.PublishRequest{
		RunID:    "parent-fin2",
		Revision: 1,
		Status:   state.RunCancelled,
		Text:     "stopped",
	}
	putRun(t, fb, u, state.Run{
		ID:        "parent-fin2",
		Status:    state.RunRunning,
		Revision:  1,
		Finishing: &finReq,
		StartedAt: now,
		Subtasks: []state.SubtaskLink{
			{ID: "fin-c1", QuestionID: "q-fin", Status: state.RunRunning},
			{ID: "fin-c2", QuestionID: "q-fin", Status: state.RunRunning},
		},
	})
	putRun(t, fb, u, state.Run{
		ID: "fin-c1", ParentID: "parent-fin2", Status: state.RunRunning, Revision: 1, Position: 1, Incorporated: 1, StartedAt: now,
	})
	putRun(t, fb, u, state.Run{
		ID: "fin-c2", ParentID: "parent-fin2", Status: state.RunRunning, Revision: 1, Position: 1, Incorporated: 1, StartedAt: now,
	})

	fb.resetWakes()
	if _, err := db.Publish(ctx, state.PublishRequest{
		RunID: "fin-c1", Revision: 1, Status: state.RunCancelled, Text: "c1 cancelled",
	}); err != nil {
		t.Fatalf("Publish fin-c1: %v", err)
	}
	for _, w := range fb.committedWakes() {
		if w.kind == "task" && w.runID == "parent-fin2" {
			t.Fatalf("fin-c1 redundantly woke Finishing parent-fin2 while fin-c2 still running: wakes=%+v", fb.committedWakes())
		}
	}

	fb.resetWakes()
	if _, err := db.Publish(ctx, state.PublishRequest{
		RunID: "fin-c2", Revision: 1, Status: state.RunCancelled, Text: "c2 cancelled",
	}); err != nil {
		t.Fatalf("Publish fin-c2: %v", err)
	}
	wokeParent = false
	for _, w := range fb.committedWakes() {
		if w.kind == "task" && w.userID == u.ID && w.runID == "parent-fin2" {
			wokeParent = true
		}
	}
	if !wokeParent {
		t.Fatalf("final sibling fin-c2 did not wake Finishing parent-fin2: wakes=%+v", fb.committedWakes())
	}
	gotFinParent, err := db.GetRun(ctx, "parent-fin2")
	if err != nil || gotFinParent.Status != state.RunCancelled {
		t.Fatalf("expected parent-fin2 to finalize as cancelled, got %+v err=%v", gotFinParent, err)
	}

	finTermReq := state.PublishRequest{
		RunID: "parent-term-fin", Revision: 1, Status: state.RunCancelled, Text: "done",
	}
	putRun(t, fb, u, state.Run{
		ID: "parent-term-fin", Status: state.RunRunning, Revision: 1, Finishing: &finTermReq, StartedAt: now,
		Subtasks: []state.SubtaskLink{{ID: "term-child-1", QuestionID: "q-t1", Status: state.RunCancelled}},
	})
	putRun(t, fb, u, state.Run{
		ID: "term-child-1", ParentID: "parent-term-fin", Status: state.RunCancelled, Revision: 1, CleanupPending: true, StartedAt: now,
	})
	fb.resetWakes()
	if err := db.FinishCleanup(ctx, "term-child-1", ""); err != nil {
		t.Fatalf("FinishCleanup term-child-1: %v", err)
	}
	wokeParent = false
	for _, w := range fb.committedWakes() {
		if w.kind == "task" && w.userID == u.ID && w.runID == "parent-term-fin" {
			wokeParent = true
		}
	}
	if !wokeParent {
		t.Fatalf("terminal child FinishCleanup did not wake Finishing parent-term-fin: wakes=%+v", fb.committedWakes())
	}

	putRun(t, fb, u, state.Run{
		ID: "parent-already-done", Status: state.RunCompleted, Revision: 1, CleanupPending: false, StartedAt: now,
		Subtasks: []state.SubtaskLink{{ID: "term-child-2", QuestionID: "q-t2", Status: state.RunCompleted}},
	})
	putRun(t, fb, u, state.Run{
		ID: "term-child-2", ParentID: "parent-already-done", Status: state.RunCompleted, Revision: 1, CleanupPending: true, StartedAt: now,
	})
	fb.resetWakes()
	if err := db.FinishCleanup(ctx, "term-child-2", ""); err != nil {
		t.Fatalf("FinishCleanup term-child-2: %v", err)
	}
	for _, w := range fb.committedWakes() {
		if w.kind == "task" && w.runID == "parent-already-done" {
			t.Fatalf("terminal child FinishCleanup woke already-terminal clean parent: wakes=%+v", fb.committedWakes())
		}
	}
}

func convFence(c state.ConversationControl, turn state.ConversationTurn) state.ConversationFence {
	return state.ConversationFence{
		Owner:       c.Owner,
		Fence:       c.Fence,
		TurnID:      turn.ID,
		Through:     turn.Through,
		UserThrough: turn.UserThrough,
		Frozen:      turn.Frozen,
	}
}

func TestSameTxWakeHooks_ConversationAndUsers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	fb, db := newTestDB(t, &now)
	u := testUser()

	if err := db.EnsureConversationIntroduction(ctx, "hello"); err != nil {
		t.Fatalf("EnsureConversationIntroduction: %v", err)
	}
	wakes := fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "user" || wakes[0].userID != u.ID || wakes[0].lane != docstore.LaneConversation {
		t.Fatalf("EnsureConversationIntroduction wakes = %+v", wakes)
	}

	c, turn, err := db.AcquireConversation(ctx, "owner-1", "turn-intro")
	if err != nil || turn == nil {
		t.Fatalf("AcquireConversation intro: turn=%v err=%v", turn, err)
	}
	if err := db.PublishConversation(ctx, convFence(c, *turn), *turn, "welcome"); err != nil {
		t.Fatalf("PublishConversation intro: %v", err)
	}

	fb.resetWakes()
	if err := db.AcceptConversationInput(ctx, "in-1", "hello nori", nil); err != nil {
		t.Fatalf("AcceptConversationInput: %v", err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "user" || wakes[0].userID != u.ID || wakes[0].lane != docstore.LaneConversation {
		t.Fatalf("AcceptConversationInput wakes = %+v", wakes)
	}
	fb.resetWakes()
	if err := db.AcceptConversationInput(ctx, "in-1", "hello nori", nil); err != nil {
		t.Fatalf("AcceptConversationInput replay: %v", err)
	}
	if got := fb.committedWakes(); len(got) != 0 {
		t.Fatalf("AcceptConversationInput replay emitted wakes: %+v", got)
	}

	c, turn, err = db.AcquireConversation(ctx, "owner-1", "turn-1")
	if err != nil || turn == nil {
		t.Fatalf("AcquireConversation turn-1: turn=%v err=%v", turn, err)
	}
	fb.resetWakes()
	if err := db.ReleaseConversation(ctx, convFence(c, *turn)); err != nil {
		t.Fatalf("ReleaseConversation: %v", err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "user" || wakes[0].userID != u.ID || wakes[0].lane != docstore.LaneConversation {
		t.Fatalf("ReleaseConversation wakes = %+v", wakes)
	}

	c, turn, err = db.AcquireConversation(ctx, "owner-2", "turn-1")
	if err != nil || turn == nil {
		t.Fatalf("re-acquire turn-1: turn=%v err=%v", turn, err)
	}
	fb.resetWakes()
	if err := db.PublishConversation(ctx, convFence(c, *turn), *turn, "hi there"); err != nil {
		t.Fatalf("PublishConversation: %v", err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "waiting" || wakes[0].userID != u.ID || wakes[0].reason != docstore.WaitRoutingPending {
		t.Fatalf("PublishConversation wakes = %+v, want WakeWaitingTasks(routing_pending)", wakes)
	}

	if err := db.AcceptConversationInput(ctx, "in-2", "first", nil); err != nil {
		t.Fatalf("AcceptConversationInput in-2: %v", err)
	}
	_, turn, err = db.AcquireConversation(ctx, "owner-2", "turn-2")
	if err != nil || turn == nil {
		t.Fatalf("AcquireConversation turn-2: turn=%v err=%v", turn, err)
	}
	fb.resetWakes()
	if err := db.CancelConversation(ctx, turn.ID); err != nil {
		t.Fatalf("CancelConversation: %v", err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "waiting" || wakes[0].userID != u.ID || wakes[0].reason != docstore.WaitRoutingPending {
		t.Fatalf("CancelConversation wakes = %+v, want WakeWaitingTasks(routing_pending)", wakes)
	}

	if err := db.AcceptConversationInput(ctx, "in-3", "msg 3", nil); err != nil {
		t.Fatalf("AcceptConversationInput in-3: %v", err)
	}
	_, turn, err = db.AcquireConversation(ctx, "owner-3", "turn-3")
	if err != nil || turn == nil {
		t.Fatalf("AcquireConversation turn-3: turn=%v err=%v", turn, err)
	}
	if err := db.AcceptConversationInput(ctx, "in-4", "msg 4", nil); err != nil {
		t.Fatalf("AcceptConversationInput in-4: %v", err)
	}
	fb.resetWakes()
	if err := db.CancelConversation(ctx, turn.ID); err != nil {
		t.Fatalf("CancelConversation turn-3: %v", err)
	}
	wakes = fb.committedWakes()
	foundPendingWake := false
	for _, w := range wakes {
		if w.kind == "user" && w.userID == u.ID && w.lane == docstore.LaneConversation {
			foundPendingWake = true
		}
	}
	if !foundPendingWake {
		t.Fatalf("CancelConversation with queued input did not wake conversation lane; wakes = %+v", wakes)
	}

	usersStore := records.NewSharedUsers(fb)
	fb.resetWakes()
	if err := usersStore.SetPaused(ctx, true); err != nil {
		t.Fatalf("SetPaused(true): %v", err)
	}
	paused, err := usersStore.Paused(ctx)
	if err != nil || !paused {
		t.Fatalf("Paused() = %v, %v; want true, nil", paused, err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "pause" {
		t.Fatalf("SetPaused(true) wakes = %+v, want [pause]", wakes)
	}

	fb.resetWakes()
	if err := usersStore.SetPaused(ctx, false); err != nil {
		t.Fatalf("SetPaused(false): %v", err)
	}
	paused, err = usersStore.Paused(ctx)
	if err != nil || paused {
		t.Fatalf("Paused() = %v, %v; want false, nil", paused, err)
	}
	wakes = fb.committedWakes()
	if len(wakes) != 1 || wakes[0].kind != "waiting" || wakes[0].userID != "" || wakes[0].reason != docstore.WaitPaused {
		t.Fatalf("SetPaused(false) wakes = %+v, want [waiting(\"\"):paused]", wakes)
	}
}
