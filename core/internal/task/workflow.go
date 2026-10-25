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
	options := workflow.ActivityOptions{StartToCloseTimeout: 150 * time.Second, ScheduleToCloseTimeout: 170 * time.Second, HeartbeatTimeout: 10 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}}
	ctx = workflow.WithActivityOptions(ctx, options)
	// Read/write activities are short and do not heartbeat.
	quick := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 15 * time.Second, ScheduleToCloseTimeout: 25 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	signals := workflow.GetSignalChannel(ctx, Signal)
	var activities *Activities
	var contents []*genai.Content
	var revision, incorporated int64
	usedComputer := false
	computerRuns := []string{}
	var finalText string
	defer func() {
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		cleanup = workflow.WithActivityOptions(cleanup, workflow.ActivityOptions{StartToCloseTimeout: 25 * time.Second, ScheduleToCloseTimeout: 35 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
		stopConfirmed := !usedComputer
		if usedComputer {
			if stopErr := workflow.ExecuteActivity(cleanup, activities.Stop, computerRuns).Get(cleanup, nil); stopErr != nil {
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
			publishErr := workflow.ExecuteActivity(cleanup, activities.Publish, Publication{Step: Step{id, revision}, Status: status, Detail: err.Error(), StopConfirmed: stopConfirmed}).Get(cleanup, &result)
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
	// await blocks without polling until an accepted command changes the
	// revision; commands wake the task and one durable timer enforces the
	// admitted session deadline. Pause explicitly cancels it.
	await := func(deadline time.Time) error {
		for {
			var command state.Command
			selector := workflow.NewSelector(ctx)
			selector.AddReceive(signals, func(c workflow.ReceiveChannel, more bool) { c.Receive(ctx, &command) })
			timerCtx, cancelTimer := workflow.WithCancel(ctx)
			selector.AddFuture(workflow.NewTimer(timerCtx, deadline.Sub(workflow.Now(ctx))), func(workflow.Future) {})
			selector.Select(ctx)
			cancelTimer()
			var current state.TaskSnapshot
			e := workflow.ExecuteActivity(quick, activities.Reconcile, id).Get(ctx, &current)
			if superseded(e) {
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
	// continue command accepted while the budget is spent raises it.
	spent, budget := 0.0, agent.BudgetUSD
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
		if superseded(err) {
			continue
		}
		if err != nil {
			return
		}
		revision = snap.Run.Revision
		for _, cmd := range snap.Commands {
			if cmd.Position > incorporated {
				switch {
				case cmd.Kind == "cancel":
				case cmd.Kind == "continue" && spent >= budget:
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
		if spent >= budget {
			// The durable wait, Stop, pause and publication need no model call,
			// so they keep working after the budget is spent.
			err = workflow.ExecuteActivity(quick, activities.Project, Projection{Step: Step{id, revision}, Kind: "waiting", Text: budgetQuestion(spent, budget, modelCalls, ran, note, request)}).Get(ctx, nil)
			if superseded(err) {
				continue
			}
			if err != nil {
				return
			}
			if err = await(snap.Run.Deadline); err != nil {
				return
			}
			continue
		}
		err = workflow.ExecuteActivity(quick, activities.Project, Projection{Step: Step{id, revision}, Kind: "model", Text: "Thinking"}).Get(ctx, nil)
		if superseded(err) {
			continue
		}
		if err != nil {
			return
		}
		modelCalls++
		var decision agent.Decision
		err = workflow.ExecuteActivity(ctx, activities.Model, ModelInput{Step: Step{id, revision}, Contents: contents}).Get(ctx, &decision)
		if superseded(err) {
			continue
		}
		if err != nil {
			return
		}
		spent += decision.Cost
		if err = workflow.ExecuteActivity(quick, activities.Spend, Spending{RunID: id, Spent: spent, Budget: budget}).Get(ctx, nil); err != nil {
			return
		}
		if text := modelText(decision.Content); text != "" {
			note = text
		}
		var current state.TaskSnapshot
		err = workflow.ExecuteActivity(quick, activities.Reconcile, id).Get(ctx, &current)
		if superseded(err) {
			continue
		}
		if err != nil {
			return
		}
		if current.Run.Revision != revision {
			continue
		}
		if len(decision.Calls) == 0 {
			finalText = decision.Answer
			if usedComputer {
				if err = workflow.ExecuteActivity(quick, activities.Stop, computerRuns).Get(ctx, nil); err != nil {
					return
				}
				usedComputer = false
			}
			err = workflow.ExecuteActivity(quick, activities.Publish, Publication{Step: Step{id, revision}, Status: "completed", Text: finalText}).Get(ctx, &result)
			if superseded(err) {
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
				if superseded(err) {
					revised = true
					break
				}
				if err != nil {
					return
				}
				if err = await(snap.Run.Deadline); err != nil {
					return
				}
				// Complete the protocol pair before incorporating Austin's next input.
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"resumed": true}}})
				break
			}
			err = workflow.ExecuteActivity(quick, activities.Project, Projection{Step: Step{id, revision}, Kind: "tool", Text: call.Name}).Get(ctx, nil)
			if superseded(err) {
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
			if superseded(err) {
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
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: skipped.ID, Name: skipped.Name, Response: map[string]any{"skipped": true, "reason": "An accepted correction superseded this unexecuted call."}}})
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
