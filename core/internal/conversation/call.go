package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/contextview"
	"nori/internal/homeai"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
)

func inputRole(in state.ConversationInput) string {
	if in.Introduction || in.Event != nil {
		return "system"
	}
	return "user"
}

func inputMessage(in state.ConversationInput) json.RawMessage {
	if in.Event != nil && in.Event.Reason == state.QuestionWork {
		data, _ := json.Marshal(in.Event)
		return modelMessage(map[string]string{
			"role":    "system",
			"content": prompts.Runtime("work-request", map[string]any{"Event": string(data)}),
		})
	}
	if in.Event != nil {
		data, _ := json.Marshal(in.Event)
		return modelMessage(map[string]string{
			"role":    "system",
			"content": prompts.Runtime("task-notification", map[string]any{"Event": string(data)}),
		})
	}
	return modelMessage(map[string]string{"role": inputRole(in), "content": inputText(in)})
}

func inputText(in state.ConversationInput) string {
	if len(in.Attachments) == 0 {
		return in.Text
	}
	data, _ := json.Marshal(in.Attachments)
	return strings.TrimSpace(in.Text + prompts.Runtime("input-attachments", nil) + string(data))
}

const (
	preparingReply  = "preparing_reply"
	requestingReply = "requesting_reply"
	proposedReply   = "proposed_reply"
	requestingPlan  = "requesting_plan"
	resolvedReply   = "resolved_reply"
)

func (r *Runner) call(ctx context.Context, f state.ConversationFence, t *state.ConversationTurn) (callErr error) {
	ctx, span := conversationTracer.Start(ctx, "conversation.attempt", trace.WithAttributes(
		attribute.String("openinference.span.kind", "CHAIN"),
		attribute.String("session.id", observability.SessionID()),
		attribute.String("nori.turn.id", t.ID),
		attribute.String("nori.attempt.id", t.Attempt.ID),
		attribute.Int("nori.attempt.number", t.Attempt.Number),
	))
	started := r.now()
	historyMS, tasksMS, visibleMS, jevMS, promptMS, outerMS := int64(-1), int64(-1), int64(-1), int64(-1), int64(-1), int64(-1)
	defer func() {
		span.SetAttributes(
			attribute.Int64("nori.phase.history_ms", historyMS), attribute.Int64("nori.phase.tasks_ms", tasksMS),
			attribute.Int64("nori.phase.visible_ms", visibleMS), attribute.Int64("nori.phase.classifier_ms", jevMS),
			attribute.Int64("nori.phase.prompt_ms", promptMS), attribute.Int64("nori.phase.outer_ms", outerMS),
			attribute.String("nori.reply.status", t.Attempt.Status),
		)
		if callErr != nil && !errors.Is(ctx.Err(), context.Canceled) && !errors.Is(callErr, state.ErrConversationStale) && !errors.Is(callErr, state.ErrConversationBusy) {
			observability.RecordErrorCategory(ctx, "conversation.attempt", callErr)
		} else if t.Attempt.Status == "error" {
			span.SetStatus(codes.Error, "conversation reply failed")
		}
		slog.InfoContext(ctx, "conversation attempt phases",
			"attempt_id", t.Attempt.ID,
			"history_ms", historyMS,
			"tasks_ms", tasksMS,
			"visible_ms", visibleMS,
			"jev_ms", jevMS,
			"prompt_ms", promptMS,
			"outer_ms", outerMS,
			"total_ms", r.now().Sub(started).Milliseconds(),
		)
		span.End()
	}()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	watchErr := make(chan error, 1)
	go r.watchLease(callCtx, done, watchErr, f, cancel)
	defer close(done)
	historyReady := make(chan historyResult, 1)
	tasksReady := make(chan taskResult, 1)
	var work sync.WaitGroup
	defer func() { cancel(); work.Wait() }()
	defer func() {
		if callCtx.Err() == nil {
			return
		}
		if ctx.Err() != nil {
			callErr = ctx.Err()
			return
		}
		select {
		case err := <-watchErr:
			callErr = err
		default:
			callErr = nil
		}
	}()
	checkpoint := t.Attempt.ModelCheckpoint
	var reply Reply
	requests := workRequests(t.Inputs)
	fromUser := slices.ContainsFunc(t.Inputs, state.ConversationInput.IsUser)
	noDispatch := !fromUser && len(requests) == 0
	if checkpoint == nil {
		err := errors.New("an earlier attempt was interrupted before its reply was saved")
		return r.finishCall(ctx, callCtx, f, t, Reply{}, err, noDispatch, historyView{})
	}
	if checkpoint.Phase != preparingReply {
		var err error
		switch checkpoint.Phase {
		case requestingReply:
			err = errors.New("the model request was interrupted before its reply was saved")
		case requestingPlan:
			err = errors.New("the task plan was interrupted before it was saved")
		case proposedReply, resolvedReply:
			err = restoreReply(checkpoint.Message, checkpoint.Text, checkpoint.Structured, &reply)
		default:
			err = errors.New("saved model phase is invalid")
		}
		if err != nil {
			return r.finishCall(ctx, callCtx, f, t, Reply{}, err, noDispatch, historyView{})
		}
		observability.Event(ctx, "conversation.checkpoint_reused", attribute.String("nori.phase", checkpoint.Phase))
		if checkpoint.Phase == resolvedReply {
			memories, err := r.tasks.Store.Memories(callCtx)
			if err != nil {
				return err
			}
			state.SortCoreMemories(memories)
			return r.finishCall(ctx, callCtx, f, t, reply, nil, noDispatch, historyView{Memories: memories})
		}
	}
	r.runWork(&work, func() { tasksReady <- r.readAttemptTasks(callCtx) })
	contextFrom, e := r.contextStart(callCtx)
	if e != nil {
		return e
	}
	r.runWork(&work, func() { historyReady <- r.readAttemptHistory(callCtx, contextFrom) })
	recallReady := make(chan []recalledMemoryLog, 1)
	if fromUser && !isPureAckInputs(t.Inputs) {
		var queryParts []string
		for _, in := range t.Inputs {
			if in.IsUser() && strings.TrimSpace(in.Text) != "" {
				queryParts = append(queryParts, in.Text)
			}
		}
		queryText := strings.Join(queryParts, "\n")
		recallNow := r.now()
		r.runWork(&work, func() { recallReady <- r.recallMemoryLogs(callCtx, queryText, contextFrom, recallNow) })
	} else {
		recallReady <- nil
	}
	readStarted := r.now()
	visible, next, e := r.tasks.Store.ConversationMessages(callCtx, "")
	visibleMS = r.now().Sub(readStarted).Milliseconds()
	if e != nil {
		return e
	}
	if !contextFrom.IsZero() {
		visible = slices.DeleteFunc(visible, func(m Message) bool { return m.CreatedAt.Before(contextFrom) })
	}
	inputID := t.Inputs[0].ID
	for _, input := range t.Inputs {
		if input.IsUser() {
			inputID = input.ID
			break
		}
	}
	record := func(ctx context.Context, u state.Usage) error {
		u.InputID = inputID
		if err := r.tasks.Store.RecordUsage(ctx, u); err != nil {
			return err
		}
		stale, err := r.tasks.Store.CheckConversation(ctx, f)
		if err != nil {
			return err
		}
		if stale {
			return state.ErrConversationStale
		}
		return ctx.Err()
	}
	request := ReplyRequest{RecordUsage: record, NoDispatch: noDispatch, WorkRequests: requests}
	for _, input := range t.Inputs {
		if input.IsUser() {
			request.TelemetryInput = input.Text
		}
	}
	hintReady, hintErr := r.startHint(&work, callCtx, record, f, t, visible, inputID)
	if hintErr != nil {
		return hintErr
	}
	historyRead := <-historyReady
	historyMS = historyRead.durationMS
	if historyRead.err != nil {
		return historyRead.err
	}
	view := historyRead.view
	view.Truncated = view.Truncated || !contextFrom.IsZero()
	for _, memory := range userProvenanceMemories(view.Memories) {
		request.MemoryIDs = append(request.MemoryIDs, memory.ID)
	}
	promptStarted := r.now()
	messages, breakpoints := buildMessages(t, visible, view, next)
	promptMS = r.now().Sub(promptStarted).Milliseconds()
	taskRead := <-tasksReady
	tasksMS = taskRead.durationMS
	if taskRead.err != nil {
		return taskRead.err
	}
	taskContext, err := r.prepareAttemptTasks(callCtx, t, &request, taskRead.session, contextFrom)
	if err != nil {
		return err
	}

	classified := <-hintReady
	jevMS = classified.durationMS
	hint, classificationErr := classified.hint, classified.err
	if hint.Usage.ID != "" {
		attemptUsage(&t.Attempt, hint.Usage)
	}
	if classified.usageErr != nil {
		return classified.usageErr
	}
	if classificationErr != nil {
		slog.WarnContext(callCtx, "Jev advisory hint unavailable", "err", classificationErr)
		hint = Classification{Usage: hint.Usage, Available: false}
	}
	if saved := string(modelMessage(hint)); t.Attempt.Hint != saved || hint.Usage.ID != "" {
		t.Attempt.Hint = saved
		if e = r.checkpoint(ctx, f, *t); e != nil {
			return e
		}
	}
	if callCtx.Err() != nil {
		return callCtx.Err()
	}
	facts, clock := r.UserFacts, r.Clock
	if r.Profile != nil {
		facts, clock.Location = r.Profile(ctx)
	}
	if clock.Now == nil {
		clock.Now = r.Now
	}
	recalled := <-recallReady
	advisoryPayload := map[string]any{"user": facts, "task_context": taskContext, "now": clock.timeContext()}
	if hint.Available {
		advisoryPayload["classification_hint"] = hint
	}
	if len(recalled) > 0 {
		advisoryPayload["relevant_memory_logs"] = recalled
	}
	advice, _ := json.Marshal(advisoryPayload)
	advisory := modelMessage(map[string]string{"role": "system", "content": prompts.Runtime("call-advisory-context-for-the-current-inputs", nil) + string(advice)})
	for t.Attempt.ContextBytes+len(advisory) > state.OuterInputBytes && len(view.Turns) > 0 {
		view.Turns, view.Truncated = view.Turns[1:], true
		messages, breakpoints = buildMessages(t, visible, view, next)
	}
	messages = append(messages, advisory)
	t.Attempt.ContextBytes += len(advisory)
	outerStarted := r.now()
	defer func() { outerMS = r.now().Sub(outerStarted).Milliseconds() }()
	if checkpoint.Phase == preparingReply {
		if e = r.checkpointModel(ctx, f, t, requestingReply, Reply{}); e != nil {
			return e
		}
		request.Messages, request.CacheBreakpoints = messages, breakpoints
		if r.OnDelta != nil && len(request.WorkRequests) == 0 && !hasUntrustedTaskOutput(t, taskContext) {
			turnID := t.ID
			onDelta := r.OnDelta
			request.Stream = func(visible string) error {
				if visible != "" {
					onDelta(turnID, visible)
				}
				return nil
			}
		}
		var modelErr error
		turnModel := r.replyModelForTurn(callCtx, t)
		for {
			reply, modelErr = turnModel.Respond(callCtx, request)
			if reply.ServedModel != "" {
				t.Attempt.ServedModel = reply.ServedModel
			}
			if modelErr == nil && len(request.WorkRequests) == 0 {
				modelErr = validateReplyIDs(reply.Calls, request.TaskIDs, request.MemoryIDs)
			}
			reply.Usage.InputID = inputID
			attemptUsage(&t.Attempt, reply.Usage)
			if e = r.recordUsage(inputID, reply.Usage); e != nil {
				return e
			}
			if !errors.Is(modelErr, contextview.ErrOverflow) || t.Attempt.ContextReduced || len(view.Turns) == 0 || callCtx.Err() != nil {
				break
			}
			view.Turns = nil
			view.Truncated = true
			reducedMessages, reducedBreakpoints := buildMessages(t, visible, view, next)
			reduced := append(reducedMessages, advisory)
			beforeBytes, afterBytes := 0, 0
			for _, m := range request.Messages {
				beforeBytes += len(m)
			}
			for _, m := range reduced {
				afterBytes += len(m)
			}
			if afterBytes >= beforeBytes {
				break
			}
			t.Attempt.ContextReduced = true
			if r.OnDelta != nil {
				r.OnDelta(t.ID, "")
			}
			request.Messages, request.CacheBreakpoints = reduced, reducedBreakpoints
			if e = r.checkpointModel(ctx, f, t, requestingReply, Reply{}); e != nil {
				return e
			}
		}
		if modelErr != nil {
			if r.OnDelta != nil {
				r.OnDelta(t.ID, "")
			}
			return r.finishCall(ctx, callCtx, f, t, reply, modelErr, request.NoDispatch, view)
		}
		phase := resolvedReply
		if fromUser && hasUntrustedTaskOutput(t, taskContext) && slices.ContainsFunc(reply.Calls, sideEffectingCall) {
			phase = proposedReply
		}
		if e = r.checkpointModel(ctx, f, t, phase, reply); e != nil {
			return e
		}
	}
	if t.Attempt.ModelCheckpoint.Phase == proposedReply {
		plan, err := r.commandPlanMessages(callCtx, t, view, visible, taskContext)
		if err != nil {
			return r.finishCall(ctx, callCtx, f, t, reply, err, request.NoDispatch, view)
		}
		if e = r.checkpointModel(ctx, f, t, requestingPlan, Reply{}); e != nil {
			return e
		}
		reply, err = r.planUserCommands(callCtx, t, reply, request, plan, inputID)
		if err != nil {
			return r.finishCall(ctx, callCtx, f, t, reply, err, request.NoDispatch, view)
		}
		if e = r.checkpointModel(ctx, f, t, resolvedReply, reply); e != nil {
			return e
		}
	}
	return r.finishCall(ctx, callCtx, f, t, reply, nil, request.NoDispatch, view)
}

func (r *Runner) checkpointModel(ctx context.Context, f state.ConversationFence, t *state.ConversationTurn, phase string, reply Reply) error {
	t.Attempt.ModelCheckpoint = &state.ConversationModelCheckpoint{
		Phase: phase, Message: string(reply.Message), Text: reply.Text, Structured: reply.Structured,
	}
	return r.checkpoint(ctx, f, *t)
}

func classifyModelFailure(modelErr error, fromUser bool) state.MessageError {
	suffix := ""
	if fromUser {
		suffix = " Your message is saved — tap Retry to try again."
	}
	if errors.Is(modelErr, contextview.ErrOverflow) {
		msg := "This conversation grew too long for one reply."
		if fromUser {
			msg = "This conversation grew too long for one reply. Try sending a shorter follow-up."
		}
		return state.MessageError{
			Code:      "context_too_long",
			Message:   msg,
			Retryable: false,
		}
	}
	if errors.Is(modelErr, modelkeys.ErrMissingAPIKey) || errors.Is(modelErr, modelkeys.ErrModelAccess) {
		return state.MessageError{Code: modelkeys.AccessFailureCode, Message: modelErr.Error(), Retryable: fromUser}
	}
	switch {
	case errors.Is(modelErr, modelprofiles.ErrHomeOnly):
		return state.MessageError{Code: "home_only", Message: "Choose a home model in More → Models to continue.", Retryable: fromUser}
	case errors.Is(modelErr, homeai.ErrOffline):
		return state.MessageError{Code: "home_offline", Message: "Your Home AI Server is offline." + suffix, Retryable: fromUser}
	case errors.Is(modelErr, homeai.ErrOllamaUnavailable):
		return state.MessageError{Code: "home_ollama_unavailable", Message: "Start Ollama on your home server." + suffix, Retryable: fromUser}
	case errors.Is(modelErr, homeai.ErrBusy):
		return state.MessageError{Code: "home_busy", Message: "Your Home AI Server is busy." + suffix, Retryable: fromUser}
	case errors.Is(modelErr, homeai.ErrInterrupted):
		return state.MessageError{Code: "home_interrupted", Message: "The connection to your Home AI Server was interrupted." + suffix, Retryable: fromUser}
	case errors.Is(modelErr, homeai.ErrRevoked), errors.Is(modelErr, homeai.ErrDeviceNotFound), errors.Is(modelErr, homeai.ErrModelUnavailable), errors.Is(modelErr, homeai.ErrModelChanged):
		return state.MessageError{Code: "home_model_unavailable", Message: "Choose an available home model in More → Models.", Retryable: false}
	}
	var statusErr interface{ HTTPStatusCode() int }
	if errors.As(modelErr, &statusErr) {
		code := statusErr.HTTPStatusCode()
		switch {
		case code == 429:
			msg := "Nori is handling too many requests right now."
			if fromUser {
				msg = "Nori is handling too many requests right now. Your message is saved — tap Retry in a moment."
			}
			return state.MessageError{
				Code:      "rate_limited",
				Message:   msg,
				Retryable: fromUser,
			}
		case code >= 500 && code <= 599:
			return state.MessageError{
				Code:      "provider_overloaded",
				Message:   "Nori's model service is temporarily busy." + suffix,
				Retryable: fromUser,
			}
		case code == 401 || code == 403:
			return state.MessageError{
				Code:      "provider_unavailable",
				Message:   "Nori's model service isn't available right now.",
				Retryable: false,
			}
		}
	}
	if modelErr != nil {
		lower := strings.ToLower(modelErr.Error())
		if strings.Contains(lower, "interrupted") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "timeout") {
			return state.MessageError{
				Code:      "provider_unavailable",
				Message:   "Couldn't reach Nori's model service right now." + suffix,
				Retryable: fromUser,
			}
		}
	}
	return state.MessageError{
		Code:      "reply_failed",
		Message:   "Nori couldn't finish replying right now." + suffix,
		Retryable: fromUser,
	}
}

func (r *Runner) modelFailureText(ctx context.Context, inputs []state.ConversationInput, failure state.MessageError) string {
	text := failure.Message
	seen := map[string]bool{}
	for _, input := range inputs {
		if input.Event == nil || seen[input.Event.RunID] {
			continue
		}
		id := input.Event.RunID
		seen[id] = true
		run, err := r.tasks.Store.GetRun(ctx, id)
		if err != nil {
			text += "\n\nTask " + id + prompts.Runtime("task-notification-saved", nil)
			continue
		}
		heading := "Task " + id + ": " + string(run.Status) + "."
		output := ""
		switch run.Status {
		case "completed", "failed", "cancelled":
			output = run.Result
			if run.Error != "" {
				output = strings.TrimSpace(output + "\n" + run.Error)
			}
		case "running":
			if run.CancelRequested {
				heading = "Task " + id + prompts.Runtime("task-stop-requested", nil)
			} else if question := run.Question; question != nil && question.Revision == run.Revision {
				if question.ResolvedBy != "" {
					heading = "Task " + id + prompts.Runtime("task-answer-accepted", nil)
				} else {
					heading = "Task " + id + prompts.Runtime("task-answer-needed", nil)
					output = question.Text
				}
			}
		}
		text += "\n\n" + heading
		if output != "" {
			text += prompts.Runtime("saved-task-output", nil) + strings.ReplaceAll(output, "\n", "\n> ")
		}
	}
	return text
}

func toolResult(id string, value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return modelMessage(map[string]string{"role": "tool", "tool_call_id": id, "content": string(data)})
}

func commandRejected(err error) bool {
	return errors.Is(err, state.ErrBusy) || errors.Is(err, state.ErrPaused) || errors.Is(err, state.ErrDuplicate) ||
		errors.Is(err, state.ErrCancelled) || errors.Is(err, state.ErrNotFound) || errors.Is(err, state.ErrQuestionStale) ||
		errors.Is(err, state.ErrCommandInvalid)
}

func commandResult(receipt state.CommandReceipt) map[string]any {
	return map[string]any{
		"accepted": true, "command": receipt.Command, "run_id": receipt.Run.ID, "status": receipt.Run.Status,
		"instruction_revision": receipt.Run.Revision, "position": receipt.Run.Position, "question": receipt.Run.Question,
		"budget": receipt.Run.Budget,
	}
}

func (r *Runner) watchLease(callCtx context.Context, done <-chan struct{}, watchErr chan<- error, f state.ConversationFence, cancel context.CancelFunc) {
	lastRenew := r.now()
	r.runPeriodic(callCtx, done, 250*time.Millisecond, func() bool {
		stale, err := r.tasks.Store.CheckConversation(callCtx, f)
		if err == nil && r.now().Sub(lastRenew) >= 10*time.Second {
			stale, err = r.tasks.Store.RenewConversation(callCtx, f)
			lastRenew = r.now()
		}
		if err != nil {
			select {
			case watchErr <- err:
			default:
			}
			cancel()
			return false
		}
		if stale {
			cancel()
			return false
		}
		return true
	})
}

func classifyHistory(visible []Message, t *state.ConversationTurn) []Message {
	classificationHistory := []Message{}
	for _, message := range visible {
		if message.Status == state.MessageStatusFailed || message.Status == state.MessageStatusSuperseded {
			continue
		}
		if message.TurnID == t.ID || (message.Role == "user" && !message.CreatedAt.Before(t.Inputs[0].CreatedAt)) {
			continue
		}
		if message.RunID != "" {
			if message.ID == state.UserMessageID(message.RunID) {
				continue
			}
			message.Text = "[Task " + message.RunID + prompts.Runtime("task-record-suffix", nil) + message.Text
		}
		classificationHistory = append(classificationHistory, message)
	}
	return classificationHistory
}

type historyView struct {
	Memories  []state.Memory
	Summary   *state.ConversationSummary
	Turns     []state.ConversationTurn
	Truncated bool
}

const recordBytes = 4 << 10

func buildMessages(t *state.ConversationTurn, visible []Message, view historyView, next string) ([]json.RawMessage, []int) {
	messages := []json.RawMessage{modelMessage(map[string]string{"role": "system", "content": prompts.Conversation})}
	breakpoints := []int{0}
	turnEnds := []int{}
	history, truncated := view.Turns, view.Truncated
	if next != "" {
		truncated = true
	}
	var since time.Time
	if view.Summary != nil {
		since = view.Summary.Through
	}
	records := []Message{}
	size := 0
	for i := len(visible) - 1; i >= 0; i-- {
		m := visible[i]
		if m.TurnID != "" || !m.CreatedAt.After(since) {
			continue
		}
		if m.Status == state.MessageStatusFailed || m.Status == state.MessageStatusSuperseded {
			continue
		}
		if m.RunID != "" && m.ID == state.UserMessageID(m.RunID) {
			continue
		}
		if m.RunID != "" {
			m.Text = "[Task " + m.RunID + prompts.Runtime("task-record-suffix", nil) + m.Text
		}
		p := modelMessage(map[string]string{"role": m.Role, "content": m.Text})
		if size+len(p) > recordBytes {
			truncated = true
			break
		}
		size += len(p)
		records = append(records, m)
	}
	recordIndex := len(records) - 1
	appendRecord := func() {
		m := records[recordIndex]
		messages = append(messages, modelMessage(map[string]string{"role": m.Role, "content": m.Text}))
		recordIndex--
	}
	for _, turn := range history {
		if turn.Attempt.Status == "error" {
			continue
		}
		for recordIndex >= 0 && !records[recordIndex].CreatedAt.After(turn.StartedAt) {
			appendRecord()
		}
		for _, p := range turn.Protocol {
			messages = append(messages, json.RawMessage(p))
		}
		if len(turn.Protocol) > 0 {
			turnEnds = append(turnEnds, len(messages)-1)
		}
	}
	if n := len(turnEnds); n > 2 {
		turnEnds = turnEnds[n-2:]
	}
	breakpoints = append(breakpoints, turnEnds...)
	for recordIndex >= 0 {
		appendRecord()
	}
	messages = append(messages, memoryMessage(view.Memories))
	if view.Summary != nil {
		messages = append(messages, summaryMessage(*view.Summary))
	}
	t.Attempt.ContextTruncated = truncated
	t.Attempt.ContextTurns = len(history)
	for _, in := range t.Inputs {
		if in.Retry {
			continue
		}
		messages = append(messages, inputMessage(in))
	}
	for _, p := range t.Context {
		messages = append(messages, json.RawMessage(p))
	}
	t.Attempt.ContextBytes = 0
	for _, m := range messages {
		t.Attempt.ContextBytes += len(m)
	}
	return messages, breakpoints
}

func (r *Runner) finishCall(
	ctx, callCtx context.Context, f state.ConversationFence, t *state.ConversationTurn, reply Reply, modelErr error, noDispatch bool,
	view historyView,
) error {
	fromUser := slices.ContainsFunc(t.Inputs, state.ConversationInput.IsUser)
	if modelErr != nil {
		if callCtx.Err() != nil {
			return callCtx.Err()
		}
		failure := classifyModelFailure(modelErr, fromUser)
		failureText := r.modelFailureText(callCtx, t.Inputs, failure)
		stale, err := r.tasks.Store.RenewConversation(ctx, f)
		if err != nil {
			return err
		}
		if stale {
			return r.checkpoint(ctx, f, *t)
		}
		t.Attempt.Reply = string(reply.Message)
		t.Attempt.StructuredReply = reply.Structured
		t.Attempt.ProtocolInvalid = len(reply.Message) > 0
		r.attemptEnd(&t.Attempt, "error", modelErr.Error())
		t.Attempt.Failure = &failure
		t.Attempt.Text = failureText
	} else {
		t.Attempt.Reply = string(reply.Message)
		t.Attempt.StructuredReply = reply.Structured
		calls, shows, err := splitShows(reply.Calls)
		var remembered []memoryCall
		if err == nil {
			calls, remembered, err = splitMemory(calls)
		}
		var commands []state.Command
		if err == nil {
			commands, err = commandRequests(calls)
		}
		if noDispatch && len(reply.Calls) != 0 {
			err = fmt.Errorf("a turn without user input cannot authorize task commands")
		}
		if requests := workRequests(t.Inputs); err == nil && len(requests) > 0 && (len(remembered) > 0 || len(shows) > 0 || !workCommandsAllowed(commands, requests)) {
			err = fmt.Errorf("a work request's turn can only settle that request")
		}
		if err != nil {
			rawFailure := prompts.Runtime("tool-calls-invalid", nil)
			typedFailure := classifyModelFailure(err, fromUser)
			r.attemptEnd(&t.Attempt, "error", rawFailure)
			t.Attempt.Failure = &typedFailure
			t.Attempt.ProtocolInvalid = true
			t.Attempt.Text = typedFailure.Message
		} else {
			t.Attempt.Reply = string(reply.Message)
			t.Attempt.Text = reply.Text
			fromUser := slices.ContainsFunc(t.Inputs, func(input state.ConversationInput) bool { return input.IsUser() })
			if fromUser {
				t.Attempt.ShowResults = shows
			}
			if fromUser && len(remembered) > 0 {
				if source := state.SourceInput(t.Inputs); source.IsUser() && source.ID != "" {
					t.Attempt.Memory, _ = planMemory(t.Attempt.ID, remembered, userProvenanceMemories(view.Memories))
				}
			}
			receivers := 0
			for _, command := range commands {
				if command.Kind == state.CommandStart || command.Kind == state.CommandSteer || command.Kind == state.CommandAnswer {
					receivers++
				}
			}
			for i, call := range calls {
				id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(t.Attempt.ID+":"+call.ID)).String()
				command := commands[i]
				command.ID = id
				if !fromUser {
					command.Attachments, command.Revises = nil, ""
				}
				if command.Revises != "" {
					if _, err := r.tasks.Store.GetResult(ctx, command.Revises); errors.Is(err, state.ErrNotFound) {
						command.Revises = ""
					} else if err != nil {
						return err
					}
				}
				named := command.Attachments
				command.Attachments = nil
				for _, attachment := range named {
					upload, err := r.tasks.Store.GetUpload(ctx, attachment.ID)
					if errors.Is(err, state.ErrNotFound) {
						continue
					}
					if err != nil {
						return err
					}
					command.Attachments = append(command.Attachments, upload.Attachment)
				}
				if command.Kind == state.CommandStart {
					command.RunID = id
				}
				for _, input := range t.Inputs {
					if input.IsUser() {
						command.SourceInputIDs = append(command.SourceInputIDs, input.ID)
						if receivers == 1 && (command.Kind == state.CommandStart || command.Kind == state.CommandSteer || command.Kind == state.CommandAnswer) {
							command.Attachments = state.MergeAttachments(command.Attachments, input.Attachments)
						}
					}
				}
				if len(command.Attachments) > state.MaxAttachments*2 {
					command.Attachments = command.Attachments[:state.MaxAttachments*2]
				}
				t.Attempt.Actions = append(t.Attempt.Actions, state.ConversationAction{
					ID: id, Request: command.Text, Bytes: len(command.Text), Status: "pending", Command: command,
				})
				t.Attempt.Results = append(t.Attempt.Results, string(toolResult(call.ID, map[string]string{"error": "superseded before admission"})))
			}
		}
	}
	t.Attempt.ModelCheckpoint = nil
	return r.checkpoint(ctx, f, *t)
}

type hintResult struct {
	hint       Classification
	err        error
	usageErr   error
	durationMS int64
}

func (r *Runner) startHint(
	work *sync.WaitGroup, callCtx context.Context, record func(context.Context, state.Usage) error, f state.ConversationFence, t *state.ConversationTurn,
	visible []Message, inputID string,
) (<-chan hintResult, error) {
	hintReady := make(chan hintResult, 1)
	if r.classifier == nil || !slices.ContainsFunc(t.Inputs, state.ConversationInput.IsUser) {
		hintReady <- hintResult{durationMS: -1}
		return hintReady, nil
	}
	if t.Attempt.Hint != "" {
		var hint Classification
		if err := json.Unmarshal([]byte(t.Attempt.Hint), &hint); err != nil {
			return nil, fmt.Errorf("saved classification hint is invalid: %w", err)
		}
		hintReady <- hintResult{hint: hint, durationMS: -1}
	} else {
		t.Attempt.Hint = string(modelMessage(Classification{}))
		if err := r.checkpoint(callCtx, f, *t); err != nil {
			return nil, err
		}
		classificationHistory := classifyHistory(visible, t)
		inputs := make([]state.ConversationInput, 0, len(t.Inputs))
		for _, in := range t.Inputs {
			if !in.Retry {
				inputs = append(inputs, in)
			}
		}
		r.runWork(work, func() {
			classifyStarted := r.now()
			hint, err := r.classifier.Classify(callCtx, ClassifyRequest{Inputs: inputs, History: classificationHistory, RecordUsage: record})
			durationMS := r.now().Sub(classifyStarted).Milliseconds()
			var usageErr error
			if hint.Usage.ID != "" {
				hint.Usage.InputID = inputID
				usageErr = r.recordUsage(inputID, hint.Usage)
			} else {
				durationMS = -1
			}
			hintReady <- hintResult{hint, err, usageErr, durationMS}
		})
	}
	return hintReady, nil
}

type historyResult struct {
	view       historyView
	err        error
	durationMS int64
}
type taskResult struct {
	session    state.Session
	err        error
	durationMS int64
}

func (r *Runner) readAttemptHistory(ctx context.Context, from time.Time) historyResult {
	started := r.now()
	var view historyView
	result := func(err error) historyResult { return historyResult{view, err, r.now().Sub(started).Milliseconds()} }
	memories, err := r.tasks.Store.Memories(ctx)
	if err != nil {
		return result(err)
	}
	state.SortCoreMemories(memories)
	view.Memories = memories
	summary, err := r.tasks.Store.ConversationSummary(ctx)
	if err != nil {
		return result(err)
	}
	after := from
	if summary.Applies(from) {
		view.Summary, after = &summary, summary.Through
	}
	turns, err := r.tasks.Store.ConversationTurnsAfter(ctx, after)
	if err != nil {
		return result(err)
	}
	for {
		probe := state.NewHistoryWindow()
		for _, t := range turns {
			if !probe.Add(t) {
				break
			}
		}
		if !probe.Truncated {
			break
		}
		before := after
		maintenanceErr := r.advanceHotWindow(ctx)
		if ctx.Err() != nil {
			return result(ctx.Err())
		}
		summary, err = r.tasks.Store.ConversationSummary(ctx)
		if err != nil {
			return result(err)
		}
		if maintenanceErr != nil && !errors.Is(maintenanceErr, state.ErrConversationStale) {
			if !summary.Applies(from) || !summary.Through.After(before) {
				return result(maintenanceErr)
			}
			slog.WarnContext(ctx, "conversation indexing interrupted; using committed excerpts", "err", maintenanceErr)
			r.setPause(&r.compactAfter, r.now().Add(compactionPause))
		}
		if !summary.Applies(from) || !summary.Through.After(before) {
			break
		}
		view.Summary, after = &summary, summary.Through
		turns, err = r.tasks.Store.ConversationTurnsAfter(ctx, after)
		if err != nil {
			return result(err)
		}
	}
	window := state.NewHistoryWindow()
	for _, t := range turns {
		if !window.Add(t) {
			break
		}
	}
	view.Turns, view.Truncated = window.Chronological(), window.Truncated
	return result(nil)
}

func (r *Runner) readAttemptTasks(ctx context.Context) taskResult {
	started := r.now()
	snapshot, err := r.tasks.Store.ConversationProjection(ctx)
	return taskResult{snapshot, err, r.now().Sub(started).Milliseconds()}
}

func (r *Runner) prepareAttemptTasks(
	callCtx context.Context, t *state.ConversationTurn, request *ReplyRequest, snapshot state.Session, contextFrom time.Time,
) (TaskContext, error) {
	if !contextFrom.IsZero() {
		snapshot.Runs = slices.DeleteFunc(snapshot.Runs, func(run state.Run) bool {
			return run.StartedAt.Before(contextFrom) && (run.Status == state.RunCompleted || run.Status == state.RunFailed || run.Status == state.RunCancelled)
		})
	}
	eventRunIDs := []string{}
	for _, input := range t.Inputs {
		if input.Event != nil {
			eventRunIDs = append(eventRunIDs, input.Event.RunID)
		}
	}
	taskContext, err := r.tasks.ContextRecords(callCtx, snapshot.Runs, eventRunIDs, contextFrom)
	if err != nil {
		return TaskContext{}, err
	}
	t.Attempt.ContextTruncated = t.Attempt.ContextTruncated || taskContext.Truncated
	request.TaskIDs = nil
	for _, records := range [][]*TaskContextRecord{taskContext.ActiveTasks, taskContext.EventTasks, taskContext.RecentTasks} {
		for _, record := range records {
			request.TaskIDs = append(request.TaskIDs, record.ID)
		}
	}

	return taskContext, nil
}

func userProvenanceMemories(memories []state.Memory) []state.Memory {
	filtered := make([]state.Memory, 0, len(memories))
	for _, m := range memories {
		if m.EffectiveProvenance() == state.ProvenanceTaskOutcome {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered
}
