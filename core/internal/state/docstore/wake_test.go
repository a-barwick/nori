package docstore_test

import (
	"context"
	"testing"
	"time"

	"nori/internal/state/docstore"
)

func TestParseWakePayload(t *testing.T) {
	cases := []struct {
		payload string
		want    docstore.ParsedWake
	}{
		{
			payload: docstore.TaskWakePayload("user-1", "run-1"),
			want:    docstore.ParsedWake{Kind: docstore.WakeKindTask, UserID: "user-1", RunID: "run-1"},
		},
		{
			payload: docstore.UserTasksWakePayload("user-1"),
			want:    docstore.ParsedWake{Kind: docstore.WakeKindTask, UserID: "user-1", RunID: "*"},
		},
		{
			payload: docstore.WakePayloadAllTask,
			want:    docstore.ParsedWake{Kind: docstore.WakeKindTask},
		},
		{
			payload: docstore.UserWakePayload("user-1", docstore.LaneConversation),
			want:    docstore.ParsedWake{Kind: docstore.WakeKindConversation, UserID: "user-1"},
		},
		{
			payload: docstore.UserWakePayload("user-1", docstore.LaneScheduler),
			want:    docstore.ParsedWake{Kind: docstore.WakeKindScheduler, UserID: "user-1"},
		},
		{
			payload: docstore.WakePayloadPause,
			want:    docstore.ParsedWake{Kind: docstore.WakeKindPause},
		},
		{
			payload: docstore.WakePayloadResync,
			want:    docstore.ParsedWake{Kind: docstore.WakeKindResync},
		},
		{payload: "", want: docstore.ParsedWake{}},
		{payload: "t:", want: docstore.ParsedWake{}},
		{payload: "t:onlyuser", want: docstore.ParsedWake{}},
		{payload: "c:", want: docstore.ParsedWake{}},
		{payload: "s:", want: docstore.ParsedWake{}},
		{payload: "x:foo", want: docstore.ParsedWake{}},
	}
	for _, tc := range cases {
		got := docstore.ParseWakePayload(tc.payload)
		if got != tc.want {
			t.Errorf("ParseWakePayload(%q) = %+v, want %+v", tc.payload, got, tc.want)
		}
	}
}

type fakeWakeBackend struct {
	tx *fakeWakeTx
}

func (f *fakeWakeBackend) Get(context.Context, string) (docstore.Record, error) {
	return docstore.Record{}, docstore.ErrNotFound
}
func (f *fakeWakeBackend) Query(context.Context, docstore.QuerySpec) ([]docstore.Record, error) {
	return nil, nil
}
func (f *fakeWakeBackend) Apply(context.Context, docstore.Mutation) error { return nil }
func (f *fakeWakeBackend) RunTransaction(_ context.Context, _ bool, _ int, fn func(docstore.TransactionBackend) error) error {
	return fn(f.tx)
}
func (f *fakeWakeBackend) Watch(context.Context, docstore.QuerySpec, func([]docstore.Record) error) error {
	return nil
}
func (f *fakeWakeBackend) DeleteBatch(context.Context, []string) error { return nil }
func (f *fakeWakeBackend) Close() error                                { return nil }

type fakeWakeTx struct {
	calls []string
}

func (f *fakeWakeTx) Get(context.Context, string) (docstore.Record, error) {
	return docstore.Record{}, docstore.ErrNotFound
}
func (f *fakeWakeTx) Query(context.Context, docstore.QuerySpec) ([]docstore.Record, error) {
	return nil, nil
}
func (f *fakeWakeTx) Apply(context.Context, docstore.Mutation) error { return nil }
func (f *fakeWakeTx) WakeTask(_ context.Context, userID, runID string, _ time.Time) error {
	f.calls = append(f.calls, "task:"+userID+":"+runID)
	return nil
}
func (f *fakeWakeTx) WakeWaitingTasks(_ context.Context, userID, reason string) error {
	f.calls = append(f.calls, "wait:"+userID+":"+reason)
	return nil
}
func (f *fakeWakeTx) WakeUser(_ context.Context, userID, lane string, _ time.Time) error {
	f.calls = append(f.calls, "user:"+userID+":"+lane)
	return nil
}
func (f *fakeWakeTx) NotifyPause(context.Context) error {
	f.calls = append(f.calls, "pause")
	return nil
}

func TestTransactionWakeWriterDelegation(t *testing.T) {
	tx := &fakeWakeTx{}
	client := docstore.New(&fakeWakeBackend{tx: tx})
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	err := client.RunTransaction(context.Background(), func(_ context.Context, ttx *docstore.Transaction) error {
		if !ttx.HasWakeWriter() {
			t.Fatal("expected HasWakeWriter() == true")
		}
		if err := ttx.WakeTask("u1", "r1", now); err != nil {
			return err
		}
		if err := ttx.WakeWaitingTasks("u1", docstore.WaitRoutingPending); err != nil {
			return err
		}
		if err := ttx.WakeUser("u1", docstore.LaneConversation, now); err != nil {
			return err
		}
		return ttx.NotifyPause()
	})
	if err != nil {
		t.Fatalf("RunTransaction: %v", err)
	}
	want := []string{"task:u1:r1", "wait:u1:routing_pending", "user:u1:conversation", "pause"}
	if len(tx.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", tx.calls, want)
	}
	for i := range want {
		if tx.calls[i] != want[i] {
			t.Errorf("calls[%d] = %q, want %q", i, tx.calls[i], want[i])
		}
	}
}
