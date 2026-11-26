package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

type stubReplyModel struct {
	mu sync.Mutex
	fn func(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error)
}

func (s *stubReplyModel) Name() string { return "stub-model" }
func (s *stubReplyModel) SetHandler(fn func(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
}
func (s *stubReplyModel) Respond(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error) {
	s.mu.Lock()
	fn := s.fn
	s.mu.Unlock()
	return fn(ctx, req)
}

func TestRetryEndpointsAndMessageViewErrorContract(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	var clockMu sync.Mutex
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	db := records.New(backend, users.User{ID: "22222222-2222-4222-8222-222222222222"}, 1.0)
	db.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(time.Second)
		return now
	}
	store := state.Compose(db, nil)
	tasks := &conversation.Tasks{Store: store}
	model := &stubReplyModel{
		fn: func(context.Context, conversation.ReplyRequest) (conversation.Reply, error) {
			return conversation.Reply{
				Usage: state.Usage{ID: "u-fail", Model: "stub-model"},
			}, errors.New("model request interrupted or unavailable")
		},
	}
	runner := conversation.New(model, nil, nil, tasks)
	runner.Now = db.Now
	runner.SyncWork = true

	h := Handler(Deps{Model: "stub-model", Runner: runner, Tasks: tasks})

	turnRec := doJSONReq(t, h, http.MethodPost, "/api/turn", map[string]any{
		"id":      uuid.NewString(),
		"message": "Check my schedule",
	})
	if turnRec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/turn status=%d body=%s", turnRec.Code, turnRec.Body.String())
	}
	if err := runner.Process(ctx); err != nil {
		t.Fatalf("Process failed turn: %v", err)
	}

	convRec := doJSONReq(t, h, http.MethodGet, "/v1/conversations/assistant", nil)
	if convRec.Code != http.StatusOK {
		t.Fatalf("GET /v1/conversations/assistant status=%d body=%s", convRec.Code, convRec.Body.String())
	}
	var convView conversationView
	if err := json.Unmarshal(convRec.Body.Bytes(), &convView); err != nil {
		t.Fatalf("decode conversationView: %v", err)
	}
	if len(convView.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(convView.Messages))
	}
	failedView := convView.Messages[1]
	if failedView.Status != state.MessageStatusFailed || failedView.Error == nil {
		t.Fatalf("expected failed messageView with error, got %+v", failedView)
	}
	if failedView.Error.Code != "provider_unavailable" || !failedView.Error.Retryable {
		t.Fatalf("unexpected error payload: %+v", failedView.Error)
	}
	if strings.Contains(failedView.Text, "HTTP") || strings.Contains(failedView.Text, "No new task commands") {
		t.Fatalf("leaked internal text: %q", failedView.Text)
	}

	retryRec := doJSONReq(t, h, http.MethodPost, "/api/retry", map[string]any{
		"turn_id":    failedView.TurnID,
		"message_id": failedView.ID,
	})
	if retryRec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/retry status=%d body=%s", retryRec.Code, retryRec.Body.String())
	}
	var sess conversation.Session
	if err := json.Unmarshal(retryRec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode retry Session: %v", err)
	}
	if !sess.Conversation.Pending {
		t.Fatalf("expected pending=true after POST /api/retry")
	}

	model.SetHandler(func(context.Context, conversation.ReplyRequest) (conversation.Reply, error) {
		return conversation.Reply{
			Usage:   state.Usage{ID: "u-ok", Model: "stub-model"},
			Message: json.RawMessage(`{"role":"assistant","content":"{\"actions\":[],\"text\":\"You are free all afternoon.\"}"}`),
			Text:    "You are free all afternoon.",
		}, nil
	})
	if err := runner.Process(ctx); err != nil {
		t.Fatalf("Process retry turn: %v", err)
	}

	v1RetryRec := doJSONReq(t, h, http.MethodPost, "/v1/conversations/assistant/retry", map[string]any{})
	if v1RetryRec.Code != http.StatusAccepted {
		t.Fatalf("POST /v1/conversations/assistant/retry status=%d body=%s", v1RetryRec.Code, v1RetryRec.Body.String())
	}
	var v1Body struct {
		Conversation conversationView `json:"conversation"`
	}
	if err := json.Unmarshal(v1RetryRec.Body.Bytes(), &v1Body); err != nil {
		t.Fatalf("decode v1 retry body: %v", err)
	}
	if v1Body.Conversation.Status == nil || v1Body.Conversation.Status.RoutingBlocked {
		t.Fatalf("expected routing_blocked=false after recovery, got %+v", v1Body.Conversation.Status)
	}
}
