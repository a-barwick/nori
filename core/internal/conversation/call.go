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

// inputText adds what the outer model may know about attached files. It never sees their contents. File names came from
// the user's device, so they're quoted as data.
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
	// The lease watcher also covers setup reads, not just provider latency.
	// History and task state can load while Jev classifies the visible context.
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
			// Newer input cancelled setup or provider I/O. Let the turn owner
			// incorporate the queued batch before retrying.
			callErr = nil
		}
	}()
	checkpoint := t.Attempt.ModelCheckpoint
	var reply Reply
	requests := workRequests(t.Inputs)
	fromUser := slices.ContainsFunc(t.Inputs, state.ConversationInput.IsUser)
	noDispatch := !fromUser && len(requests) == 0
	if checkpoint == nil {
		// An unfinished legacy attempt may already have sent a paid request.
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
	if fromUser {
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
	// Provider callbacks only write the usage ledger. The turn owner alone
	// mutates/checkpoints Attempt, including while Jev and setup overlap.
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
	for _, memory := range view.Memories {
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
	advisoryPayload := map[string]any{"user": facts, "classification_hint": hint, "task_context": taskContext, "now": clock.timeContext()}
	if len(recalled) > 0 {
		advisoryPayload["relevant_memory_logs"] = recalled
	}
	advice, _ := json.Marshal(advisoryPayload)
	advisory := modelMessage(map[string]string{"role": "system", "content": prompts.Runtime("call-advisory-context-for-the-current-inputs", nil) + string(advice)})
	// The oldest recent turns give way first when the whole input would pass its budget. Compaction folds them into
	// the summary.
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
		var modelErr error
		for {
			reply, modelErr = r.model.Respond(callCtx, request)
			if modelErr == nil && len(request.WorkRequests) == 0 {
				// The reply schema carries no ID enums (they would defeat prompt caching), so check IDs here.
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
			request.Messages, request.CacheBreakpoints = reduced, reducedBreakpoints
			if e = r.checkpointModel(ctx, f, t, requestingReply, Reply{}); e != nil {
				return e
			}
		}
		if modelErr != nil {
			return r.finishCall(ctx, callCtx, f, t, reply, modelErr, request.NoDispatch, view)
		}
		phase := resolvedReply
		if fromUser && (slices.ContainsFunc(reply.Calls, plannedCommand) || (hasUntrustedTaskOutput(t, taskContext) && slices.ContainsFunc(reply.Calls, sideEffectingCall))) {
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

func (r *Runner) modelFailureText(ctx context.Context, inputs []state.ConversationInput, modelErr error) string {
	text := prompts.Runtime("reply-failed", nil) + modelErr.Error()
	for _, input := range inputs {
		if input.IsUser() {
			text += prompts.Runtime("reply-retry", nil)
			break
		}
	}
	seen := map[string]bool{}
	for _, input := range inputs {
		if input.Event == nil || seen[input.Event.RunID] {
			continue
		}
		id := input.Event.RunID
		seen[id] = true
		// Re-read after the failed call: a panel answer or newer task state may
		// have superseded this event while the model request was in flight.
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

// buildMessages also returns prompt-cache breakpoints (message indices): the static system prompt, the end of the
// leading memory/summary block, and the ends of the last two history turns. A request writes its latest four
// breakpoints, so each turn writes the prefix through its newest history turn and the next turn, whose
// second-to-last history turn ends at the same position, can read it back.
func buildMessages(t *state.ConversationTurn, visible []Message, view historyView, next string) ([]json.RawMessage, []int) {
	messages := []json.RawMessage{modelMessage(map[string]string{"role": "system", "content": prompts.Conversation})}
	messages = append(messages, memoryMessage(view.Memories))
	if view.Summary != nil {
		messages = append(messages, summaryMessage(*view.Summary))
	}
	breakpoints := []int{0, len(messages) - 1}
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
	// Merge older visible records around complete protocol blocks. Appending all
	// records after history makes old conversation look like new input.
	recordIndex := len(records) - 1
	appendRecord := func() {
		m := records[recordIndex]
		messages = append(messages, modelMessage(map[string]string{"role": m.Role, "content": m.Text}))
		recordIndex--
	}
	for _, turn := range history {
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
	t.Attempt.ContextTruncated = truncated
	t.Attempt.ContextTurns = len(history)
	for _, in := range t.Inputs {
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
	if modelErr != nil {
		if callCtx.Err() != nil {
			return callCtx.Err()
		}
		failureText := r.modelFailureText(callCtx, t.Inputs, modelErr)
		stale, err := r.tasks.Store.RenewConversation(ctx, f)
		if err != nil {
			return err
		}
		if stale {
			return r.checkpoint(ctx, f, *t)
		}
		// Retain rejected provider messages in private attempt records for diagnosis.
		// They must never become conversation history or accepted commands.
		t.Attempt.Reply = string(reply.Message)
		t.Attempt.StructuredReply = reply.Structured
		t.Attempt.ProtocolInvalid = len(reply.Message) > 0
		r.attemptEnd(&t.Attempt, "error", modelErr.Error())
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
		if requests := workRequests(t.Inputs); err == nil && len(requests) > 0 && !workCommandsAllowed(commands, requests) {
			err = fmt.Errorf("a work request's turn can only settle that request")
		}
		failure := prompts.Runtime("tool-calls-invalid", nil)
		if err != nil {
			r.attemptEnd(&t.Attempt, "error", failure)
			t.Attempt.ProtocolInvalid = true
			t.Attempt.Text = failure
		} else {
			t.Attempt.Reply = string(reply.Message)
			t.Attempt.Text = reply.Text
			// Only a turn carrying the user's own message can show a result or hand files to a task. Task output reaches
			// the model too, and it mustn't be able to get a file into another task.
			fromUser := slices.ContainsFunc(t.Inputs, func(input state.ConversationInput) bool { return input.IsUser() })
			if fromUser {
				t.Attempt.ShowResults = shows
			}
			if fromUser && len(remembered) > 0 {
				t.Attempt.Memory, _ = planMemory(t.Attempt.ID, remembered, view.Memories)
			}
			// A message's files go with it when it reaches one task. When it reaches several, each dispatch names its own.
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
					// A revision must link to a result that exists, and the task is told which one.
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
		// Classification is advisory. A cutoff before its result leaves this
		// fallback usable without resending a potentially billed request.
		t.Attempt.Hint = string(modelMessage(Classification{}))
		if err := r.checkpoint(callCtx, f, *t); err != nil {
			return nil, err
		}
		// Exclude this batch and any subsequently queued inputs from Jev's history.
		classificationHistory := classifyHistory(visible, t)
		inputs := append([]state.ConversationInput(nil), t.Inputs...)
		r.runWork(work, func() {
			classifyStarted := r.now()
			hint, err := r.classifier.Classify(callCtx, ClassifyRequest{Inputs: inputs, History: classificationHistory, RecordUsage: record})
			durationMS := r.now().Sub(classifyStarted).Milliseconds()
			// Preserve observed usage even if setup failed or the turn was superseded.
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
	// Context maintenance is part of preparing a turn, not dependent on finding
	// idle time. A committed extractive view survives a failed or interrupted refinement.
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
			r.compactAfter = r.now().Add(compactionPause)
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
		// A retained history cutoff still exposes work that can act on the user's behalf.
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
