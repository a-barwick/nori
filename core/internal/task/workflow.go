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
const WorkflowName = "nori-task-v1"

func superseded(err error) bool {
	var application *temporal.ApplicationError
	return errors.As(err, &application) && application.Type() == "superseded"
}

// Workflow contains only deterministic state transitions. Every external read,
// model request, computer call and projection write is an independent activity.
func Workflow(ctx workflow.Context, id string) (result state.Run, err error) {
	options := workflow.ActivityOptions{StartToCloseTimeout: 240 * time.Second, ScheduleToCloseTimeout: 260 * time.Second, HeartbeatTimeout: 10 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}}
	ctx = workflow.WithActivityOptions(ctx, options)
	// Read/write activities are short and do not heartbeat.
	quick := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 15 * time.Second, ScheduleToCloseTimeout: 25 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	// Stop may wait for an idle shutdown to reach TERMINATED. It performs no
	// heartbeats; ordinary projection activities keep their shorter limits.
	stopOptions := workflow.ActivityOptions{StartToCloseTimeout: 200 * time.Second, ScheduleToCloseTimeout: 240 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}}
	stopCtx := workflow.WithActivityOptions(ctx, stopOptions)
	signals := workflow.GetSignalChannel(ctx, Signal)
	var activities *Activities
	var contents []*genai.Content
	var revision, incorporated int64
	usedComputer := false
	computerRuns := []string{}
	var finalText string
	// These values are replayed from ModelOutcome history; Firestore is a
	// recoverable cumulative projection, never the budget authority.
	spent, budget := 0.0, agent.BudgetUSD
	costUnknown := false
	spending := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second, ScheduleToCloseTimeout: 15 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
	projectSpend := func(spendCtx workflow.Context) {
		if spendErr := workflow.ExecuteActivity(spendCtx, activities.Spend, Spending{RunID: id, Spent: spent, Budget: budget, CostUnknown: costUnknown}).Get(spendCtx, nil); spendErr != nil {
			workflow.GetLogger(ctx).Warn("Model spend projection pending; workflow budget retained", "run_id", id, "error", spendErr)
		}
	}
	defer func() {
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		cleanup = workflow.WithActivityOptions(cleanup, workflow.ActivityOptions{StartToCloseTimeout: 25 * time.Second, ScheduleToCloseTimeout: 35 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		projectSpend(workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(spending)))
		stopConfirmed := !usedComputer
		if usedComputer {
			stopCleanup := workflow.WithActivityOptions(cleanup, stopOptions)
			if stopErr := workflow.ExecuteActivity(stopCleanup, activities.Stop, computerRuns).Get(stopCleanup, nil); stopErr != nil {
				err = fmt.Errorf("computer stop could not be confirmed: %w", stopErr)
				finalText = ""
			} else {
				stopConfirmed = true
			}
		}
		// Successful publication happens in the body so a newly accepted correction
		// can return to reconciliation rather than publish an obsolete answer.
		if err != nil {
			status := "failed"
			var application *temporal.ApplicationError
			if temporal.IsCanceledError(err) || (errors.As(err, &application) && application.Type() == "cancelled") {
				status = "cancelled"
			}
			publishErr := workflow.ExecuteActivity(cleanup, activities.Publish, Publication{Step: Step{id, revision}, Status: status, Detail: err.Error(), StopConfirmed: stopConfirmed, CostUnknown: costUnknown}).Get(cleanup, &result)
			if publishErr != nil {
				err = fmt.Errorf("%w; terminal persistence failed: %v", err, publishErr)
			} else if result.Status == "cancelled" {
				err = temporal.NewCanceledError("Cancelled by Austin.")
			}

		}
	}()
	if err = workflow.ExecuteActivity(quick, activities.Context, id).Get(ctx, &contents); err != nil {
		return
	}
	// A paused boundary waits for a command or explicit resume signal without
	// timers or model calls. A new pinned worker release owns this history.
	retryBoundary := func(boundaryErr error) bool {
		if superseded(boundaryErr) {
			return true
		}
		var application *temporal.ApplicationError
		if errors.As(boundaryErr, &application) && application.Type() == "paused" {
			var command state.Command
			signals.Receive(ctx, &command)
			return ctx.Err() == nil
		}
		return false
	}
	// User waits have no expiry and wake only for accepted commands/resume.
	await := func() error {
		needsSignal := true
		for {
			if needsSignal {
				var command state.Command
				signals.Receive(ctx, &command)
			}
			needsSignal = true
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var current state.TaskSnapshot
			e := workflow.ExecuteActivity(quick, activities.Reconcile, id).Get(ctx, &current)
			if retryBoundary(e) {
				needsSignal = false
				continue
			}
			if e != nil {
				return e
			}
			if current.Run.Revision != revision {
				return nil
			}
		}
	}
	modelCalls, toolCalls := 0, 0
	// Budget accounting uses each model call's reported usage. Only an explicit
	// continue command answering the budget question raises it.
	asked := false
	request, note := "", ""
	ran := []string{}
	for {
		// Signals are wakeups; Firestore command IDs/positions are authoritative and
		// deduplicated here even after relay retries, restart or out-of-order arrival.
		var command state.Command
		for signals.ReceiveAsync(&command) {
		}
		var snap state.TaskSnapshot
		err = workflow.ExecuteActivity(quick, activities.Reconcile, id).Get(ctx, &snap)
		if retryBoundary(err) {
			continue
		}
		if err != nil {
			return
		}
		// Reconciliation gives Stop precedence. A correction cannot make an
		// unpriced provider request free or authorize another model call.
		if costUnknown {
			err = state.ErrCostUnknown
			return
		}
		revision = snap.Run.Revision
		granted := false
		for _, cmd := range snap.Commands {
			if cmd.Position > incorporated {
				switch {
				case cmd.Kind == "cancel":
				case cmd.Kind == "continue" && asked:
					asked, granted = false, true
					budget += agent.BudgetUSD
					contents = append(contents, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: fmt.Sprintf("Austin approved another $%.2f of model spend for this task. Continue the task.", agent.BudgetUSD)}}})
				default:
					if cmd.Kind == "start" {
						request = cmd.Text
					}
					contents = append(contents, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: cmd.Text}}})
				}
				incorporated = cmd.Position
			}
		}
		if granted {
			projectSpend(spending)
		}
		if spent >= budget {
			// The durable wait, Stop, pause and publication need no model call,
			// so they keep working after the budget is spent.
			err = workflow.ExecuteActivity(quick, activities.Project, Projection{Step: Step{id, revision}, Kind: "waiting", Text: budgetQuestion(spent, budget, modelCalls, ran, note, request)}).Get(ctx, nil)
			if retryBoundary(err) {
				continue
			}
			if err != nil {
				return
			}
			asked = true
			if err = await(); err != nil {
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
			// Model application errors originate only in its pre-I/O guard. A
			// Temporal timeout/crash/cancellation may hide a billed response.
			var application *temporal.ApplicationError
			if !errors.As(err, &application) {
				costUnknown = true
				if !temporal.IsCanceledError(err) {
					err = fmt.Errorf("%w Model activity outcome unavailable: %v", state.ErrCostUnknown, err)
				}
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
		if current.Run.Revision != revision {
			continue
		}
		// Account the response first, then honor accepted cancellation/correction
		// before a rejection from the now-obsolete model turn can fail the task.
		if outcome.Failure != "" {
			err = errors.New(outcome.Failure)
			return
		}
		if len(decision.Calls) == 0 {
			finalText = decision.Answer
			if usedComputer {
				if err = workflow.ExecuteActivity(stopCtx, activities.Stop, computerRuns).Get(stopCtx, nil); err != nil {
					return
				}
				usedComputer = false
			}
			err = workflow.ExecuteActivity(quick, activities.Publish, Publication{Step: Step{id, revision}, Status: "completed", Text: finalText, CostUnknown: costUnknown}).Get(ctx, &result)
			if retryBoundary(err) {
				finalText = ""
				continue
			}
			return
		}
		contents = append(contents, decision.Content)
		// Validate the complete batch before any call has an effect. An invalid
		// batch is answered call by call and returned to the model, not failed.
		if invalid := agent.ValidateCalls(decision.Calls, true); invalid != nil {
			parts := []*genai.Part{}
			for _, call := range decision.Calls {
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"rejected": true, "reason": invalid.Error() + ". No call in this batch ran; send a valid batch."}}})
			}
			contents = append(contents, &genai.Content{Role: "user", Parts: parts})
			continue
		}
		parts := []*genai.Part{}
		revised := false
		for _, call := range decision.Calls {
			toolCalls++
			if call.Name == "wait_for_input" {
				question, _ := call.Args["question"].(string)
				err = workflow.ExecuteActivity(quick, activities.Project, Projection{Step: Step{id, revision}, Kind: "waiting", Text: question}).Get(ctx, nil)
				if retryBoundary(err) {
					revised = true
					break
				}
				if err != nil {
					return
				}
				if err = await(); err != nil {
					return
				}
				// Complete the protocol pair before incorporating Austin's next input.
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"resumed": true}}})
				break
			}
			err = workflow.ExecuteActivity(quick, activities.Project, Projection{Step: Step{id, revision}, Kind: "tool", Text: call.Name}).Get(ctx, nil)
			if retryBoundary(err) {
				revised = true
				break
			}
			if err != nil {
				return
			}
			usedComputer = true
			computerID := computerRun(Step{id, revision})
			if len(computerRuns) == 0 || computerRuns[len(computerRuns)-1] != computerID {
				computerRuns = append(computerRuns, computerID)
			}
			operation := fmt.Sprintf("%s-m%d-t%d", id, modelCalls, toolCalls)
			var output map[string]any
			err = workflow.ExecuteActivity(ctx, activities.Tool, ToolInput{Step: Step{id, revision}, OperationID: operation, Call: call}).Get(ctx, &output)
			if retryBoundary(err) {
				revised = true
				break
			}
			if err != nil {
				return
			}
			ran = append(ran, call.Name)
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: output}})
		}
		if revised {
			// Preserve observed results from earlier calls in this batch. Pair
			// every remaining call with an explicit skipped result so the next
			// model turn sees both real effects and the accepted correction.
			for _, skipped := range decision.Calls[len(parts):] {
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: skipped.ID, Name: skipped.Name, Response: map[string]any{"skipped": true, "reason": "Execution admission or accepted input changed before this call ran."}}})
			}
			contents = append(contents, &genai.Content{Role: "user", Parts: parts})
			continue
		}
		contents = append(contents, &genai.Content{Role: "user", Parts: parts})
	}
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
