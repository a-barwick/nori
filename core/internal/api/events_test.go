package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/secret"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestGoogleDrivePickerOversizedBodyReturns413(t *testing.T) {
	store := state.Compose(records.NewShared(memstore.New(), users.User{
		ID:       "11111111-1111-4111-8111-111111111111",
		Email:    "picker@example.com",
		TimeZone: "UTC",
	}, 1), memstore.NewArtifacts())
	assistant := &Assistant{
		Store:       store,
		Connections: connections.New(store, secret.Unavailable{}, "https://nori.test/callback"),
	}
	mux := http.NewServeMux()
	assistant.register(mux)

	oversized := `{"return_to":"/` + strings.Repeat("a", 5000) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/connections/conn_1/google-drive/picker", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestBatchSupersedesDeltasOnlyForMessagesAndResets(t *testing.T) {
	if batchSupersedesDeltas([]state.LogEvent{{Kind: state.EventTask}, {Kind: state.EventReadPosition}, {Kind: state.EventMemory}}) {
		t.Fatal("unrelated events should not drain assistant delta stream")
	}
	if !batchSupersedesDeltas([]state.LogEvent{{Kind: state.EventTask}, {Kind: state.EventMessage}}) {
		t.Fatal("EventMessage batch should drain stale assistant deltas")
	}
	if !batchSupersedesDeltas([]state.LogEvent{{Kind: state.EventDataReset}}) {
		t.Fatal("EventDataReset batch should drain stale assistant deltas")
	}
}

type failingEventPositionStore struct {
	state.ConversationStore
	err error
}

func (f failingEventPositionStore) EventPosition(context.Context) (int64, error) {
	return 0, f.err
}

func TestEventsErrorBeforeHeadersReturnsJSON(t *testing.T) {
	base := state.Compose(records.NewShared(memstore.New(), users.User{
		ID:       "11111111-1111-4111-8111-111111111111",
		Email:    "events@example.com",
		TimeZone: "UTC",
	}, 1), memstore.NewArtifacts())
	tasks := &conversation.Tasks{Store: failingEventPositionStore{
		ConversationStore: base,
		err:               errors.New("db offline"),
	}}
	h := Handler(Deps{Tasks: tasks})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/events", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json (not text/event-stream)", ct)
	}
}
