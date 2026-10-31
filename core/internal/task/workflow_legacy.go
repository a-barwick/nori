package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/genai"
	"nori/internal/agent"
	"nori/internal/state"
)

const Signal = "task-command"
const PriorWorkflowName = "nori-task-v2"
const LegacyWorkflowName = "nori-task-v1"

func superseded(err error) bool {
	var application *temporal.ApplicationError
	return errors.As(err, &application) && application.Type() == "superseded"
}

// Workflow contains only deterministic state transitions. Provider outcomes and
// the current transcript survive worker restart in Temporal history.
func WorkflowV2(ctx workflow.Context, id string) (result state.Run, err error) {
	// Bound execution, not queue wait: durable tasks may wait for worker
	// capacity without failing merely because other tasks occupy every slot.
	options := workflow.ActivityOptions{StartToCloseTimeout: 240 * time.Second, HeartbeatTimeout: 10 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}}
	ctx = workflow.WithActivityOptions(ctx, options)
	quick := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 15 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	stopOptions := workflow.ActivityOptions{StartToCloseTimeout: 200 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}}
	stopCtx := workflow.WithActivityOptions(ctx, stopOptions)
	spending := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
	signals := workflow.GetSignalChannel(ctx, Signal)
	var activities *Activities
	var contents []*genai.Content
	var revision, incorporated int64
	spent, budget := 0.0, agent.BudgetUSD
	costUnknown := false
	computerRuns := []string{}
	stopped := true
	modelCalls, toolCalls := 0, 0
	request, note := "", ""
	ran := []string{}
	var question *state.Question
	questionPublished := false
	projectSpend := func(c workflow.Context) {
		if e := workflow.ExecuteActivity(c, activities.Spend, Spending{RunID: id, Spent: spent, Budget: budget, CostUnknown: costUnknown}).Get(c, nil); e != nil {
			workflow.GetLogger(ctx).Warn("Model spend projection pending; history retains budget", "run_id", id, "error", e)
		}
	}
	waitSignal := func() error { var cmd state.Command; signals.Receive(ctx, &cmd); return ctx.Err() }
	retryBoundary := func(e error) bool {
		if superseded(e) {
			return true
		}
		if applicationKind(e, "paused") {
			err = waitSignal()
			return err == nil
		}
		return false
	}
	// Terminal failures use the latest accepted revision, but never retry paid
	// work. A fresh correction is checked at each ordinary failure boundary below.
	defer func() {
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		cleanup = workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(quick))
		projectSpend(workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(spending)))
		stopConfirmed := stopped
		if !stopped {
			c := workflow.WithActivityOptions(cleanup, stopOptions)
			if e := workflow.ExecuteActivity(c, activities.Stop, computerRuns).Get(c, nil); e != nil {
				err = fmt.Errorf("computer stop could not be confirmed: %w", e)
			} else {
				stopConfirmed = true
			}
		}
		if stopConfirmed && result.Status != "" && result.Status != "running" && len(computerRuns) > 0 {
			c := workflow.WithActivityOptions(cleanup, stopOptions)
			if e := workflow.ExecuteActivity(c, activities.CloseBrowser, BrowserCleanup{RunID: id, Scope: computerRuns[0]}).Get(c, nil); e != nil {
				workflow.GetLogger(ctx).Error("Task browser cleanup remains pending", "run_id", id, "error", e)
			}
		}
	}()
	contextLoaded := false
	runLoop := func() {
		if !contextLoaded {
			if err = workflow.ExecuteActivity(quick, activities.Context, id).Get(ctx, &contents); err != nil {
				return
			}
			contextLoaded = true
		}
		for {
			var signal state.Command
			for signals.ReceiveAsync(&signal) {
			}
			var snap state.TaskSnapshot
			err = workflow.ExecuteActivity(quick, activities.Reconcile, id).Get(ctx, &snap)
			if retryBoundary(err) {
				continue
			}
			if err != nil {
				return
			}
			revision = snap.Run.Revision
			if costUnknown {
				err = state.ErrCostUnknown
				return
			}
			granted := false
			for _, cmd := range snap.Commands {
				if cmd.Position <= incorporated {
					continue
				}
				switch cmd.Kind {
				case "start", "steer", "input":
					if question != nil && question.Kind == "input" {
						contents = append(contents, toolResponse(question.CallID, "wait_for_input", map[string]any{"superseded": true, "reason": "Austin changed the task instructions."}))
					}
					question = nil
					if cmd.Kind == "start" {
						request = cmd.Text
					}
					contents = append(contents, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: cmd.Text}}})
				case "answer":
					if question != nil && question.ID == cmd.QuestionID && question.Kind == "input" {
						contents = append(contents, toolResponse(question.CallID, "wait_for_input", map[string]any{"answer": cmd.Text, "question_id": cmd.QuestionID}))
						question = nil
					}
				case "continue":
					if question != nil && question.ID == cmd.QuestionID && question.Kind == "budget" {
						budget += agent.BudgetUSD
						granted, question = true, nil
						contents = append(contents, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: fmt.Sprintf("Austin approved another $%.2f of model spend for this task. Continue the task.", agent.BudgetUSD)}}})
					}
				}
				incorporated = cmd.Position
			}
			if granted {
				projectSpend(spending)
			}
			if question == nil && spent >= budget {
				question = &state.Question{ID: fmt.Sprintf("%s-budget-p%d", id, incorporated), Kind: "budget", Text: budgetQuestion(spent, budget, modelCalls, ran, note, request), Revision: revision}
				questionPublished = false
			}
			if question != nil {
				if !questionPublished {
					err = workflow.ExecuteActivity(quick, activities.Ask, QuestionProjection{Step: Step{id, revision}, Question: *question}).Get(ctx, nil)
					if retryBoundary(err) {
						continue
					}
					if err != nil {
						return
					}
					questionPublished = true
				}
				if err = waitSignal(); err != nil {
					return
				}
				continue
			}
			err = workflow.ExecuteActivity(quick, activities.Project, Projection{Step: Step{id, revision}, Kind: "model", Text: "Thinking"}).Get(ctx, nil)
			if retryBoundary(err) {
				continue
			}
			if err != nil {
				return
			}
			var outcome ModelOutcome
			err = workflow.ExecuteActivity(ctx, activities.Model, ModelInput{Step: Step{id, revision}, Contents: contents}).Get(ctx, &outcome)
			if err != nil {
				var application *temporal.ApplicationError
				if !errors.As(err, &application) {
					costUnknown = true
				}
				if retryBoundary(err) {
					continue
				}
				return
			}
			modelCalls++
			decision := outcome.Decision
			spent += decision.Cost
			costUnknown = costUnknown || !decision.CostKnown
			projectSpend(spending)
			if text := modelText(decision.Content); text != "" {
				note = text
			}
			var current state.TaskSnapshot
			err = workflow.ExecuteActivity(quick, activities.Reconcile, id).Get(ctx, &current)
			if retryBoundary(err) {
				continue
			}
			if err != nil {
				return
			}
			if costUnknown {
				err = state.ErrCostUnknown
				return
			}
			if current.Run.Revision != revision || current.Run.Position != incorporated {
				continue
			}
			if outcome.Failure != "" {
				err = errors.New(outcome.Failure)
				return
			}
			if len(decision.Calls) == 0 {
				if !stopped {
					err = workflow.ExecuteActivity(stopCtx, activities.Stop, computerRuns).Get(stopCtx, nil)
					if err != nil {
						return
					}
					stopped = true
				}
				// A generated answer waits here for accepted user input to be routed.
				// It is reused after an unrelated turn, and discarded after a steer.
				for {
					err = workflow.ExecuteActivity(quick, activities.Publish, Publication{Step: Step{id, revision}, Status: "completed", Text: decision.Answer, CostUnknown: costUnknown}).Get(ctx, &result)
					if !applicationKind(err, "routing_pending") {
						break
					}
					if err = waitSignal(); err != nil {
						return
					}
				}
				if retryBoundary(err) {
					continue
				}
				return
			}
			contents = append(contents, decision.Content)
			if invalid := agent.ValidateCalls(decision.Calls, true); invalid != nil {
				parts := []*genai.Part{}
				for _, call := range decision.Calls {
					parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"rejected": true, "reason": invalid.Error() + ". No call in this batch ran; send a valid batch."}}})
				}
				contents = append(contents, &genai.Content{Role: "user", Parts: parts})
				continue
			}
			parts := []*genai.Part{}
			finishFailedBatch := func(failed *genai.FunctionCall, operation, reason string) {
				if failed != nil {
					parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: failed.ID, Name: failed.Name, Response: map[string]any{"outcome": "uncertain", "operation_id": operation, "error": reason, "replayed": false}}})
				}
				for _, skipped := range decision.Calls[len(parts):] {
					parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: skipped.ID, Name: skipped.Name, Response: map[string]any{"skipped": true, "reason": "The batch ended before this call ran."}}})
				}
				contents = append(contents, &genai.Content{Role: "user", Parts: parts})
			}
			revised := false
			for _, call := range decision.Calls {
				toolCalls++
				if call.Name == "wait_for_input" {
					text, _ := call.Args["question"].(string)
					question = &state.Question{ID: fmt.Sprintf("%s-question-m%d", id, modelCalls), Kind: "input", Text: text, CallID: call.ID, Revision: revision}
					questionPublished = false
					break
				}
				err = workflow.ExecuteActivity(quick, activities.Project, Projection{Step: Step{id, revision}, Kind: "tool", Text: call.Name}).Get(ctx, nil)
				if retryBoundary(err) {
					revised = true
					break
				}
				if err != nil {
					finishFailedBatch(nil, "", err.Error())
					return
				}
				stopped = false
				scope := computerRun(Step{id, revision})
				if len(computerRuns) == 0 || computerRuns[len(computerRuns)-1] != scope {
					computerRuns = append(computerRuns, scope)
				}
				operation := fmt.Sprintf("%s-m%d-t%d", id, modelCalls, toolCalls)
				var output map[string]any
				err = workflow.ExecuteActivity(ctx, activities.Tool, ToolInput{Step: Step{id, revision}, OperationID: operation, Call: call}).Get(ctx, &output)
				if retryBoundary(err) {
					revised = true
					break
				}
				if err != nil {
					toolErr := err
					var latest state.TaskSnapshot
					err = workflow.ExecuteActivity(quick, activities.Reconcile, id).Get(ctx, &latest)
					if err != nil {
						finishFailedBatch(call, operation, toolErr.Error())
						return
					}
					if latest.Run.Revision != revision {
						parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"outcome": "uncertain", "operation_id": operation, "error": toolErr.Error(), "replayed": false}}})
						revised = true
						break
					}
					err = toolErr
					finishFailedBatch(call, operation, toolErr.Error())
					return
				}
				ran = append(ran, call.Name)
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: output}})
			}
			if question != nil {
				continue
			}
			if revised {
				for _, skipped := range decision.Calls[len(parts):] {
					parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: skipped.ID, Name: skipped.Name, Response: map[string]any{"skipped": true, "reason": "Execution admission or accepted input changed before this call ran."}}})
				}
			}
			contents = append(contents, &genai.Content{Role: "user", Parts: parts})
		}
	}
	for {
		runLoop()
		if err == nil || result.Status == "completed" {
			return
		}
		terminalErr := err
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		cleanup = workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(quick))
		stopConfirmed := stopped
		if !stopped {
			c := workflow.WithActivityOptions(cleanup, stopOptions)
			if e := workflow.ExecuteActivity(c, activities.Stop, computerRuns).Get(c, nil); e != nil {
				terminalErr = fmt.Errorf("computer stop could not be confirmed: %w", e)
			} else {
				stopConfirmed, stopped = true, true
			}
		}
		status := "failed"
		if stopConfirmed && (temporal.IsCanceledError(terminalErr) || applicationKind(terminalErr, "cancelled")) {
			status = "cancelled"
		}
		publication := Publication{Step: Step{id, revision}, Status: status, Detail: terminalErr.Error(), StopConfirmed: stopConfirmed, CostUnknown: costUnknown, BypassRouting: costUnknown || !stopConfirmed || status == "cancelled"}
		var publishErr error
		for {
			publishErr = workflow.ExecuteActivity(cleanup, activities.Publish, publication).Get(cleanup, &result)
			if !applicationKind(publishErr, "routing_pending") {
				break
			}
			// Keep this failure candidate and its completed tool protocol while
			// user input is routed; never spend another model call to recreate it.
			if waitErr := waitSignal(); waitErr != nil {
				terminalErr = waitErr
				publication.Status = "cancelled"
				publication.Detail = waitErr.Error()
				publication.BypassRouting = true
			}
		}
		if superseded(publishErr) && !costUnknown && ctx.Err() == nil {
			// A correction won the terminal transaction. Keep the transcript,
			// observed effects and spend; let its accepted commands reconcile.
			err = nil
			continue
		}
		if publishErr != nil {
			err = fmt.Errorf("%w; terminal persistence failed: %v", terminalErr, publishErr)
		} else {
			err = terminalErr
		}
		return
	}
}

func applicationKind(err error, kind string) bool {
	var application *temporal.ApplicationError
	return errors.As(err, &application) && application.Type() == kind
}
func toolResponse(id, name string, response map[string]any) *genai.Content {
	return &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: id, Name: name, Response: response}}}}
}

// modelText is the model's visible text beside its tool calls, used as its
// latest progress note.
func modelText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	texts := []string{}
	for _, part := range content.Parts {
		if part.Text != "" && !part.Thought {
			texts = append(texts, strings.TrimSpace(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(texts, " "))
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// budgetQuestion is built from recorded facts only: no model call is spent
// to summarize once the budget is used.
func budgetQuestion(spent, budget float64, modelCalls int, ran []string, note, request string) string {
	var b strings.Builder
	if budget <= agent.BudgetUSD {
		fmt.Fprintf(&b, "I reached this task's $%.2f model budget ($%.4f spent over %d model calls).", agent.BudgetUSD, spent, modelCalls)
	} else {
		fmt.Fprintf(&b, "I used the extra $%.2f you approved: $%.4f spent of $%.2f approved, over %d model calls.", agent.BudgetUSD, spent, budget, modelCalls)
	}
	if len(ran) == 0 {
		b.WriteString("\n\nDone so far: no tool calls ran.")
	} else {
		counts := map[string]int{}
		order := []string{}
		for _, name := range ran {
			if counts[name] == 0 {
				order = append(order, name)
			}
			counts[name]++
		}
		names := []string{}
		for _, name := range order {
			names = append(names, fmt.Sprintf("%s ×%d", name, counts[name]))
		}
		fmt.Fprintf(&b, "\n\nDone so far: %d tool calls (%s).", len(ran), strings.Join(names, ", "))
	}
	if note != "" {
		fmt.Fprintf(&b, " My latest note: %s", clip(note, 1500))
	}
	fmt.Fprintf(&b, "\n\nRemaining: the task isn't finished. Request: %s", clip(request, 1500))
	fmt.Fprintf(&b, "\n\nReply continue to grant another $%.2f, or Stop to end the task.", agent.BudgetUSD)
	return b.String()
}
