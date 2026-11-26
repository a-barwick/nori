package conversation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/contextview"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestTypedErrorEmissionAndRetryFlow(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	var clockMu sync.Mutex
	now := time.Date(2026, 11, 25, 7, 1, 16, 0, time.UTC)
	db := records.New(backend, users.User{ID: "33333333-3333-4333-8333-333333333333"}, 1.0)
	db.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(time.Second)
		return now
	}
	store := state.Compose(db, nil)
	model := &scriptedReplyModel{}
	runner := New(model, nil, nil, &Tasks{Store: store})
	runner.Now = db.Now
	runner.SyncWork = true

	model.SetHandler(func(context.Context, ReplyRequest) (Reply, error) {
		return Reply{}, providerHTTPError{status: 503, detail: "model provider returned HTTP 503"}
	})

	inputID := uuid.NewString()
	if _, err := runner.HandleInput(ctx, Input{ID: inputID, Message: "Can you remind me tomorrow at noon?"}); err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if err := runner.Process(ctx); err != nil {
		t.Fatalf("Process (failing turn): %v", err)
	}

	sess, err := runner.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !sess.Conversation.RoutingBlocked {
		t.Fatalf("expected RoutingBlocked=true after 503 failure")
	}
	if len(sess.Messages) != 2 {
		t.Fatalf("expected 2 messages (user + failed assistant), got %d", len(sess.Messages))
	}
	failedMsg := sess.Messages[1]
	if failedMsg.Role != "assistant" || failedMsg.Status != state.MessageStatusFailed {
		t.Fatalf("expected failed assistant message, got role=%q status=%q", failedMsg.Role, failedMsg.Status)
	}
	if failedMsg.Error == nil {
		t.Fatalf("expected structured Error on failed message")
	}
	if failedMsg.Error.Code != "provider_overloaded" || !failedMsg.Error.Retryable {
		t.Fatalf("unexpected Error metadata: %+v", failedMsg.Error)
	}
	for _, forbidden := range []string{"503", "HTTP", "provider returned", "No new task commands"} {
		if strings.Contains(failedMsg.Text, forbidden) || strings.Contains(failedMsg.Error.Message, forbidden) {
			t.Fatalf("failed message leaked forbidden substring %q: text=%q errMsg=%q", forbidden, failedMsg.Text, failedMsg.Error.Message)
		}
	}
	if len(sess.Traces) == 0 || !strings.Contains(sess.Traces[0].Error, "HTTP 503") {
		t.Fatalf("expected private trace to keep diagnostic error, got %+v", sess.Traces)
	}

	model.ResetRequests()
	model.SetHandler(func(_ context.Context, req ReplyRequest) (Reply, error) {
		joined := ""
		for _, raw := range req.Messages {
			joined += string(raw) + "\n"
		}
		if !strings.Contains(joined, "Can you remind me tomorrow at noon?") {
			t.Fatalf("expected retry turn to replay original user message, got:\n%s", joined)
		}
		if strings.Contains(joined, "temporarily busy") {
			t.Fatalf("failed error text must not be replayed into retry prompt:\n%s", joined)
		}
		return structuredTestReply("Sure, I can set that reminder.", nil, "retry-ok"), nil
	})

	retrySess, err := runner.Retry(ctx, failedMsg.TurnID, failedMsg.ID)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if !retrySess.Conversation.Pending {
		t.Fatalf("expected conversation pending immediately after Retry")
	}
	if _, err := runner.Retry(ctx, failedMsg.TurnID, failedMsg.ID); err != nil {
		t.Fatalf("second Retry: %v", err)
	}

	if err := runner.Process(ctx); err != nil {
		t.Fatalf("Process (retry turn): %v", err)
	}

	after, err := runner.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot after retry: %v", err)
	}
	if after.Conversation.RoutingBlocked || after.Conversation.Pending {
		t.Fatalf("expected clean idle state after retry, got %+v", after.Conversation)
	}
	userCount := 0
	var oldFailed, newReply *state.Message
	for i := range after.Messages {
		m := &after.Messages[i]
		if m.Role == "user" {
			userCount++
		}
		if m.ID == failedMsg.ID {
			oldFailed = m
		}
		if m.Role == "assistant" && m.ID != failedMsg.ID {
			newReply = m
		}
	}
	if userCount != 1 {
		t.Fatalf("expected exactly 1 user message (no duplicate), got %d", userCount)
	}
	if oldFailed == nil || oldFailed.Status != state.MessageStatusSuperseded {
		t.Fatalf("expected old failed message to be superseded, got %+v", oldFailed)
	}
	if newReply == nil || newReply.Status != "" || newReply.Error != nil || newReply.Text != "Sure, I can set that reminder." {
		t.Fatalf("unexpected recovered reply: %+v", newReply)
	}
}

func TestClassifyModelFailureVariants(t *testing.T) {
	f429 := classifyModelFailure(providerHTTPError{status: 429, detail: "HTTP 429"}, true)
	if f429.Code != "rate_limited" || !f429.Retryable || strings.Contains(f429.Message, "429") {
		t.Fatalf("unexpected 429 classification: %+v", f429)
	}

	fOverflow := classifyModelFailure(contextview.ErrOverflow, true)
	if fOverflow.Code != "context_too_long" || fOverflow.Retryable {
		t.Fatalf("unexpected overflow classification: %+v", fOverflow)
	}

	fNoUser := classifyModelFailure(providerHTTPError{status: 503, detail: "HTTP 503"}, false)
	if fNoUser.Code != "provider_overloaded" || fNoUser.Retryable {
		t.Fatalf("unexpected non-user 503 classification: %+v", fNoUser)
	}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"provider unavailable", fmt.Errorf("request failed: %w", ErrProviderUnavailable)},
		{"stream interrupted", fmt.Errorf("sse read: %w", ErrStreamInterrupted)},
		{"attempt interrupted", ErrAttemptInterrupted},
		{"deadline exceeded", fmt.Errorf("call timed out: %w", context.DeadlineExceeded)},
	} {
		got := classifyModelFailure(tc.err, true)
		if got.Code != "provider_unavailable" || !got.Retryable {
			t.Fatalf("classifyModelFailure(%s) = %+v, want provider_unavailable retryable", tc.name, got)
		}
	}
}

func TestRunnerBackoffFieldsRaceFree(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	r := &Runner{Now: func() time.Time { return now }}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				ts := now.Add(time.Duration(offset*10+j) * time.Second)
				r.setCompactAfter(ts)
				r.setExtractAfter(ts.Add(time.Second))
				r.setDreamAfter(ts.Add(2 * time.Second))
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = r.getCompactAfter()
				_ = r.getExtractAfter()
				_ = r.getDreamAfter()
			}
		}()
	}
	wg.Wait()
}
