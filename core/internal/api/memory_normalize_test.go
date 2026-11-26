package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func newRealMemoryTestEnv(t *testing.T) (*records.Database, *Assistant, http.Handler) {
	t.Helper()
	backend := memstore.New()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	db := records.New(backend, users.User{ID: "11111111-1111-4111-8111-111111111111"}, 1.0)
	db.Now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	store := state.Compose(db, nil)
	assistant := &Assistant{Store: store}
	return db, assistant, newTestMemoryMux(assistant)
}

func TestCreateMemoryNormalizesDeduplicatesAndIdempotency(t *testing.T) {
	db, assistant, mux := newRealMemoryTestEnv(t)

	var calls atomic.Int32
	assistant.NormalizeMemory = func(_ context.Context, in conversation.ManualMemoryInput) (conversation.NormalizedMemory, error) {
		n := calls.Add(1)
		if n == 1 {
			return conversation.NormalizedMemory{
				Text:    "The user prefers aisle seats on flights.",
				About:   "the user",
				Section: "Preferences",
			}, nil
		}
		if len(in.Existing) != 1 {
			t.Fatalf("expected 1 existing memory on second call, got %d", len(in.Existing))
		}
		return conversation.NormalizedMemory{
			Text:       "The user prefers window seats on flights.",
			About:      "the user",
			Section:    "Preferences",
			ReplacesID: in.Existing[0].ID,
		}, nil
	}

	req1ID := uuid.NewString()
	body1 := map[string]any{
		"request_id":        req1ID,
		"expected_revision": 0,
		"value":             map[string]any{"text": "I like aisle seats when flying"},
	}
	rec1 := doJSONReq(t, mux, http.MethodPost, "/v1/memory", body1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create status=%d body=%s", rec1.Code, rec1.Body.String())
	}
	var receipt1 struct {
		Resource memoryFact `json:"resource"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &receipt1); err != nil {
		t.Fatalf("decode receipt1: %v", err)
	}
	if receipt1.Resource.Text != "The user prefers aisle seats on flights." {
		t.Fatalf("normalized text = %q", receipt1.Resource.Text)
	}
	if receipt1.Resource.Section != "Preferences" || receipt1.Resource.About != "the user" {
		t.Fatalf("section=%q about=%q", receipt1.Resource.Section, receipt1.Resource.About)
	}
	if receipt1.Resource.SourceText != "I like aisle seats when flying" {
		t.Fatalf("source_text = %q", receipt1.Resource.SourceText)
	}

	recRetry := doJSONReq(t, mux, http.MethodPost, "/v1/memory", body1)
	if recRetry.Code != http.StatusCreated {
		t.Fatalf("idempotent retry status=%d body=%s", recRetry.Code, recRetry.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("expected normalizer called once across idempotent retry, got %d", calls.Load())
	}

	recConflict := doJSONReq(t, mux, http.MethodPost, "/v1/memory", map[string]any{
		"request_id":        req1ID,
		"expected_revision": 0,
		"value":             map[string]any{"text": "Different memory text"},
	})
	if recConflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", recConflict.Code, recConflict.Body.String())
	}

	req2ID := uuid.NewString()
	rec2 := doJSONReq(t, mux, http.MethodPost, "/v1/memory", map[string]any{
		"request_id":        req2ID,
		"expected_revision": 0,
		"value":             map[string]any{"text": "actually I prefer window seats on flights"},
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("dedup update status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var receipt2 struct {
		Resource memoryFact `json:"resource"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &receipt2); err != nil {
		t.Fatalf("decode receipt2: %v", err)
	}
	if receipt2.Resource.ID != receipt1.Resource.ID {
		t.Fatalf("expected in-place update of %q, got new ID %q", receipt1.Resource.ID, receipt2.Resource.ID)
	}
	if receipt2.Resource.Text != "The user prefers window seats on flights." {
		t.Fatalf("updated text = %q", receipt2.Resource.Text)
	}

	mems, err := db.Memories(context.Background())
	if err != nil {
		t.Fatalf("Memories: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1 deduplicated memory, got %d: %+v", len(mems), mems)
	}
}

func TestCreateMemoryNormalizerFallbackAndExplicitOverride(t *testing.T) {
	_, assistant, mux := newRealMemoryTestEnv(t)

	assistant.NormalizeMemory = func(context.Context, conversation.ManualMemoryInput) (conversation.NormalizedMemory, error) {
		return conversation.NormalizedMemory{}, errors.New("model unavailable")
	}
	rec1 := doJSONReq(t, mux, http.MethodPost, "/v1/memory", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value":             map[string]any{"text": "Keep meetings under 30 minutes"},
	})
	if rec1.Code != http.StatusCreated {
		t.Fatalf("fallback create status=%d body=%s", rec1.Code, rec1.Body.String())
	}
	var r1 struct {
		Resource memoryFact `json:"resource"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &r1); err != nil {
		t.Fatalf("decode r1: %v", err)
	}
	if r1.Resource.Text != "Keep meetings under 30 minutes" || r1.Resource.About != "the user" {
		t.Fatalf("unexpected fallback resource: %+v", r1.Resource)
	}

	assistant.NormalizeMemory = func(context.Context, conversation.ManualMemoryInput) (conversation.NormalizedMemory, error) {
		return conversation.NormalizedMemory{
			Text:    "Alex is the design lead.",
			About:   "the user",
			Section: "Context",
		}, nil
	}
	rec2 := doJSONReq(t, mux, http.MethodPost, "/v1/memory", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"text":    "Alex leads design",
			"about":   "Alex",
			"section": "People",
		},
	})
	if rec2.Code != http.StatusCreated {
		t.Fatalf("explicit override create status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var r2 struct {
		Resource memoryFact `json:"resource"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &r2); err != nil {
		t.Fatalf("decode r2: %v", err)
	}
	if r2.Resource.Text != "Alex is the design lead." || r2.Resource.About != "Alex" || r2.Resource.Section != "People" {
		t.Fatalf("expected explicit about/section preserved, got %+v", r2.Resource)
	}
}
