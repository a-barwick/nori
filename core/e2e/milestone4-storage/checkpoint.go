package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	sdklog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/proto"
	"nori/internal/state"
	"nori/internal/task"
	"nori/internal/transcript"
)

// Wake signals carry no commands or authority. A task parked on a real durable
// input question can therefore cross the actual ContinueAsNew boundary without
// invoking a provider, tool, or production execution queue.
func checkpointBoundary(ctx context.Context, store state.ConversationStore, address string) (map[string]bool, error) {
	if !regexp.MustCompile(`^127\.0\.0\.1:\d+$`).MatchString(address) {
		return nil, errors.New("checkpoint fixture requires loopback Temporal")
	}
	id := uuid.NewString()
	accepted, err := store.Accept(ctx, state.Command{ID: id, RunID: id, Kind: "start", Text: "Wait for the exact release answer before doing any work."})
	if err != nil {
		return nil, err
	}
	if err = store.Incorporate(ctx, state.IncorporateRequest{RunID: id, Revision: accepted.Revision, Position: accepted.Position}); err != nil {
		return nil, err
	}
	question := state.Question{ID: id + "-checkpoint-question", Kind: "input", Text: "What is the release answer?", CallID: "checkpoint-request-input", Revision: accepted.Revision}
	if err = store.ProjectQuestion(ctx, id, accepted.Revision, question); err != nil {
		return nil, err
	}
	messages := []transcript.Message{
		transcript.UserText(strings.Repeat("PRIVATE_HISTORY_SENTINEL ", 4096)),
		{Role: transcript.RoleModel, Parts: []transcript.Part{{ToolCall: &transcript.ToolCall{
			ID: question.CallID, Name: "request_input", Args: map[string]any{"question": question.Text},
		}}}},
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	ref, err := store.AppendHistory(ctx, id, "", data)
	if err != nil {
		return nil, err
	}
	seed := task.Checkpoint{Ref: ref, Revision: accepted.Revision, Position: accepted.Position, Spent: .375, Budget: 1.5, Models: 7, Tools: 3, Checks: 2, Corrections: 1, Question: &question, QuestionPublished: true, Stopped: true, Request: accepted.Request, ReadOnly: true, ComputerRevisions: []int64{1, 2}}
	if err = store.Spend(ctx, state.SpendUpdate{RunID: id, Spent: seed.Spent, Budget: seed.Budget}); err != nil {
		return nil, err
	}
	before, err := store.Reconcile(ctx, id)
	if err != nil {
		return nil, err
	}
	c, err := client.Dial(client.Options{HostPort: address, Namespace: "default", Logger: sdklog.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	queue := "nori-m4-checkpoint-" + uuid.NewString()
	w := worker.New(c, queue, worker.Options{MaxConcurrentActivityExecutionSize: 1, MaxConcurrentWorkflowTaskExecutionSize: 2})
	w.RegisterWorkflowWithOptions(task.Workflow, workflow.RegisterOptions{Name: task.WorkflowName})
	w.RegisterActivity(&task.Activities{Store: store})
	if err = w.Start(); err != nil {
		return nil, err
	}
	defer w.Stop()
	workflowID := task.WorkflowID(id)
	execution, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: workflowID, TaskQueue: queue}, task.WorkflowName, id, seed)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.TerminateWorkflow(cleanup, workflowID, "", "isolated unpaid checkpoint fixture complete")
	}()
	readHistory := func(runID string) ([]*historypb.HistoryEvent, error) {
		iter := c.GetWorkflowHistory(ctx, workflowID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		events := []*historypb.HistoryEvent{}
		for iter.HasNext() {
			event, e := iter.Next()
			if e != nil {
				return nil, e
			}
			events = append(events, event)
		}
		return events, nil
	}
	completedSync := func(events []*historypb.HistoryEvent) int {
		scheduled := map[int64]bool{}
		count := 0
		for _, event := range events {
			if a := event.GetActivityTaskScheduledEventAttributes(); a != nil && a.ActivityType.Name == "SyncHistory" {
				scheduled[event.EventId] = true
			}
			if a := event.GetActivityTaskCompletedEventAttributes(); a != nil && scheduled[a.ScheduledEventId] {
				count++
			}
		}
		return count
	}
	waitFor := func(runID string, completed int) ([]*historypb.HistoryEvent, error) {
		for {
			events, e := readHistory(runID)
			if e != nil {
				return nil, e
			}
			if completedSync(events) >= completed {
				return events, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	original := execution.GetRunID()
	if _, err = waitFor(original, 1); err != nil {
		return nil, err
	}
	for i := 1; i <= 31; i++ {
		if err = c.SignalWorkflow(ctx, workflowID, original, task.Signal, state.Command{ID: uuid.NewString(), RunID: id, Kind: "resume"}); err != nil {
			return nil, err
		}
		if _, err = waitFor(original, i+1); err != nil {
			return nil, err
		}
	}
	if err = c.SignalWorkflow(ctx, workflowID, original, task.Signal, state.Command{ID: uuid.NewString(), RunID: id, Kind: "resume"}); err != nil {
		return nil, err
	}
	var oldHistory []*historypb.HistoryEvent
	var continued *historypb.WorkflowExecutionContinuedAsNewEventAttributes
	for continued == nil {
		oldHistory, err = readHistory(original)
		if err != nil {
			return nil, err
		}
		for _, event := range oldHistory {
			if a := event.GetWorkflowExecutionContinuedAsNewEventAttributes(); a != nil {
				continued = a
			}
		}
		if continued == nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	var checkpoint task.Checkpoint
	var resumedID string
	if err = converter.GetDefaultDataConverter().FromPayloads(continued.Input, &resumedID, &checkpoint); err != nil {
		return nil, err
	}
	successor := continued.NewExecutionRunId
	if successor == "" || successor == original {
		return nil, errors.New("ContinueAsNew did not create a successor execution")
	}
	if _, err = waitFor(successor, 1); err != nil {
		return nil, err
	}
	var newHistory []*historypb.HistoryEvent
	for i := 1; i <= 2; i++ {
		if err = c.SignalWorkflow(ctx, workflowID, successor, task.Signal, state.Command{ID: uuid.NewString(), RunID: id, Kind: "resume"}); err != nil {
			return nil, err
		}
		newHistory, err = waitFor(successor, i+1)
		if err != nil {
			return nil, err
		}
	}
	modelFree, small := true, true
	for _, events := range [][]*historypb.HistoryEvent{oldHistory, newHistory} {
		for _, event := range events {
			if a := event.GetActivityTaskScheduledEventAttributes(); a != nil && (strings.Contains(a.ActivityType.Name, "Model") || strings.Contains(a.ActivityType.Name, "Tool")) {
				modelFree = false
			}
			if proto.Size(event) >= 64<<10 {
				small = false
			}
		}
	}
	after, err := store.Reconcile(ctx, id)
	if err != nil {
		return nil, err
	}
	blocks, err := store.ReadHistory(ctx, id, checkpoint.Ref)
	if err != nil {
		return nil, err
	}
	report := map[string]bool{
		"real Temporal ContinueAsNew creates a successor without model or tool activities":  modelFree && completedSync(oldHistory) == 32 && completedSync(newHistory) >= 3,
		"ContinueAsNew preserves exact question reference counters accounting and controls": resumedID == id && reflect.DeepEqual(seed, checkpoint),
		"ContinueAsNew leaves current command receipts and open question unchanged":         reflect.DeepEqual(before.Commands, after.Commands) && reflect.DeepEqual(before.Run.Question, after.Run.Question) && after.Run.Revision == before.Run.Revision && after.Run.Position == before.Run.Position,
		"ContinueAsNew retains recorded spend and budget":                                   after.Run.Spent == seed.Spent && after.Run.Budget == seed.Budget,
		"large private history stays outside all Temporal event payloads":                   len(data) > 64<<10 && small && len(blocks) == 1 && bytes.Equal(blocks[0], data),
	}
	for name, passed := range report {
		if !passed {
			return nil, fmt.Errorf("checkpoint boundary failed: %s", name)
		}
	}
	return report, nil
}
