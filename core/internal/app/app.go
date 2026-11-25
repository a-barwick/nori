package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"nori/internal/agent"
	"nori/internal/api"
	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/permissions"
	"nori/internal/platform"
	"nori/internal/scheduler"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
	"nori/internal/users"
	"nori/internal/wake"
)

type App struct {
	UserID         string
	Store          state.ConversationStore
	Leases         docstore.LeaseStore
	Features       settings.Features
	Runner         *conversation.Runner
	Tasks          *conversation.Tasks
	TaskActivities *task.Activities
	Waker          wake.Waker
	WakeRepair     *wake.Repairer
	Model          string
	handler        http.Handler
	Assistant      *api.Assistant
	Scheduler      *scheduler.Service
}

type openOptions struct {
	skipCollapseDuplicates bool
}

func Open(ctx context.Context, waker wake.Waker, cfg settings.Settings, user users.User) (*App, error) {
	opened, _, err := openWithResources(ctx, waker, cfg, user, nil, openOptions{})
	return opened, err
}

func openWithResources(ctx context.Context, waker wake.Waker, cfg settings.Settings, user users.User, storage *platform.Storage, opts openOptions) (*App, bool, error) {
	cfg = platform.ForUser(cfg, user)
	if err := validateStartup(cfg); err != nil {
		return nil, false, err
	}
	var store state.ConversationStore
	var leases docstore.LeaseStore
	var err error
	if storage != nil {
		store, err = storage.OpenStore(ctx, cfg, user)
		leases = storage.Leases()
	} else {
		store, err = platform.OpenStore(ctx, cfg, user)
	}
	if err != nil {
		return nil, false, err
	}
	if waker == nil {
		waker = platform.Waker(leases)
	}
	waker = wake.ForUser(waker, user.ID)
	model := cfg.Models.Outer
	outer := platform.ReplyModel(cfg.Models)
	location, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		store.Close()
		return nil, false, fmt.Errorf("user time zone must be an IANA time zone")
	}
	userFacts := user.Facts()
	result := &App{
		UserID:   user.ID,
		Store:    store,
		Leases:   leases,
		Features: cfg.Features,
		Waker:    waker,
		Model:    model,
	}
	services, err := platform.Assistant(cfg, user, store)
	if err != nil {
		store.Close()
		return nil, false, err
	}
	if services.Permissions != nil && services.Permissions.UserSends == "" {
		services.Permissions.UserSends = "ask"
	}
	var collapsed bool
	if !opts.skipCollapseDuplicates {
		// One-time repair of duplicate connections to one account, left by sign-ins made before they were merged.
		if removed, err := services.Connections.CollapseDuplicates(ctx); err != nil {
			slog.WarnContext(ctx, "connection duplicate repair failed", "user_id", user.ID, "err", err)
		} else {
			collapsed = true
			for kept, drop := range removed {
				slog.InfoContext(ctx, "combined duplicate connections", "user_id", user.ID, "kept_connection_id", kept, "dropped_connection_ids", drop)
			}
		}
	}
	toolSource := &agent.Service{Extensions: services.Tools}
	result.Scheduler = services.Scheduler
	result.Assistant = &api.Assistant{
		Store: store, Connections: services.Connections, Permissions: services.Permissions, Tools: toolSource.Tools,
		Scheduler: services.Scheduler, UnattendedWrites: cfg.Connections.UnattendedWrites, MCP: services.MCP,
		Features:      AssistantFeatures(services.Connections, cfg.Features.DisableBackgroundTasks),
		NextScheduled: services.Scheduler.NextScheduled, InboxUnread: services.Scheduler.UnreadCount,
	}
	result.WakeRepair = platform.WorkWakes(store, waker, !cfg.Features.DisableBackgroundTasks)
	if cfg.Features.DisableBackgroundTasks {
		return result, collapsed, nil
	}
	tasks := &conversation.Tasks{Store: store}
	summaryModels := cfg.Models
	if !summaryModels.UsesGateway() && !summaryModels.UsesOpenAIForTasks() {
		summaryModels.Inner = settings.DefaultOpenRouterInnerModel
	}
	summarizer := platform.TaskModel(summaryModels, store)
	var classifier conversation.IntentClassifier
	if !cfg.Features.DisableJev {
		classifier = conversation.NewJevClassifier(cfg.Models.JevKey)
	}
	runner := conversation.New(outer, classifier, summarizer, tasks)
	runner.UserFacts = userFacts
	runner.Profile = platform.Profile(user, services.Connections)
	runner.Clock = conversation.Clock{Location: location}

	taskService := agent.New(store, platform.TaskModel(cfg.Models, store))
	taskService.Capabilities = agent.Capabilities{SearchKey: cfg.SearchKey, Location: location}
	taskService.UserFacts = userFacts
	taskService.Extensions = services.Tools
	taskService.Profile = platform.Profile(user, services.Connections)
	taskService.Hidden = func(ctx context.Context, id string) bool {
		r, err := store.Resource(ctx, "library_overlay", id)
		if err != nil {
			return false
		}
		var overlay struct {
			DeletedAt *time.Time `json:"deleted_at"`
		}
		return json.Unmarshal(r.Body, &overlay) == nil && overlay.DeletedAt != nil
	}
	result.TaskActivities = &task.Activities{
		Agent:  taskService,
		Store:  store,
		UserID: user.ID,
		Executor: &task.Executor{
			Adapters:     services.Adapters,
			Connections:  services.Connections,
			Permissions:  services.Permissions,
			AutoReviewer: newAutoReviewer(outer, cfg),
		},
	}

	result.Runner, result.Tasks = runner, tasks
	result.Assistant.ConsolidateMemory = runner.ConsolidateNow
	result.Assistant.Answer = func(ctx context.Context, commandID, taskID, questionID, text string) error {
		_, err := tasks.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: taskID, Kind: state.CommandAnswer, QuestionID: questionID, Text: text}}, nil)
		return err
	}
	services.Scheduler.Arm = func(ctx context.Context, at time.Time) {
		if leases != nil {
			if err := leases.WakeUser(ctx, user.ID, docstore.LaneScheduler, at); err != nil {
				slog.WarnContext(ctx, "scheduler due-time wake arm failed; next pass will repair", "user_id", user.ID, "lane", docstore.LaneScheduler, "err", err)
			}
			return
		}
		if err := wake.At(ctx, waker, at); err != nil && !errors.Is(err, wake.ErrNotConfigured) {
			slog.WarnContext(ctx, "scheduler due-time wake arm failed; next pass will repair", "user_id", user.ID, "err", err)
		}
	}
	services.Scheduler.Accept = func(ctx context.Context, cmd state.Command) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{cmd}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
	result.Assistant.Resume = result.Resume
	result.Assistant.DeliverAnswers = result.deliverAnswers
	result.Assistant.ScheduleAnswerRetry = func(ctx context.Context) {
		if leases != nil {
			now := time.Now().UTC()
			if services.Scheduler != nil && services.Scheduler.Now != nil {
				now = services.Scheduler.Now().UTC()
			}
			_ = leases.WakeUser(ctx, user.ID, docstore.LaneScheduler, now.Add(2*time.Second))
		}
	}
	if result.Assistant.Connections != nil {
		// A completed sign-in resumes the tasks that were waiting for it.
		result.Assistant.Connections.OnConnected = result.Assistant.ResolveSignIns
	}
	if result.Assistant.MCP != nil {
		result.Assistant.MCP.OnSignedIn = result.Assistant.ResolveSignIns
	}
	result.Assistant.Continue = func(ctx context.Context, taskID, previous, request string, budget float64) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{{ID: taskID, RunID: taskID, Kind: state.CommandStart, Text: request, BudgetUSD: budget,
			Origin: &state.TaskOrigin{ConversationID: "assistant", ContinuesTaskID: previous}}}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
	result.Assistant.Revise = func(ctx context.Context, taskID, resultID, instruction string) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{{ID: taskID, RunID: taskID, Kind: state.CommandStart, Text: instruction, Revises: resultID}}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
	result.Assistant.Stop = func(ctx context.Context, commandID, taskID string) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: taskID, Kind: state.CommandStop}}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
	return result, collapsed, nil
}

func (a *App) Close() {
	a.Store.Close()
}

func (a *App) apiDeps() api.Deps {
	return api.Deps{Model: a.Model, Runner: a.Runner, Tasks: a.Tasks, Waker: a.Waker, Ops: a, Features: a.Features, Assistant: a.Assistant}
}

// Resume repairs this user's discovery leases/wakes (including StopPaused while globally paused)
// and runs a scheduler pass so any due automation work whose wake was lost executes cleanly.
func (a *App) Resume(ctx context.Context) error {
	return errors.Join(a.RepairLeases(ctx), a.Schedule(ctx))
}

// RepairLeases reconciles this user's nori_task_leases and nori_user_wakes rows against
// authoritative nori_records truth.
func (a *App) RepairLeases(ctx context.Context) error {
	var failures []error
	control, err := a.Store.Control(ctx)
	if err != nil {
		return err
	}
	openRuns, err := a.Store.OpenTasks(ctx)
	if err != nil {
		failures = append(failures, err)
	}
	cleanupRuns, err := a.Store.CleanupTasks(ctx)
	if err != nil {
		failures = append(failures, err)
	}
	needLease := make(map[string]state.Run, len(openRuns)+len(cleanupRuns))
	for _, r := range openRuns {
		needLease[r.ID] = r
	}
	for _, r := range cleanupRuns {
		needLease[r.ID] = r
	}

	conv, _, convErr := a.Store.ConversationStatus(ctx)
	if convErr != nil {
		failures = append(failures, convErr)
	}

	var existingLeases map[string]docstore.TaskLease
	if a.Leases != nil {
		list, err := a.Leases.ListTaskLeases(ctx, a.UserID)
		if err != nil {
			failures = append(failures, err)
		} else {
			existingLeases = make(map[string]docstore.TaskLease, len(list))
			for _, l := range list {
				existingLeases[l.RunID] = l
			}
		}
	}

	// 1. Reconcile every run that needs execution or cleanup (in deterministic sorted order).
	needIDs := make([]string, 0, len(needLease))
	for runID := range needLease {
		needIDs = append(needIDs, runID)
	}
	slices.Sort(needIDs)
	for _, runID := range needIDs {
		run := needLease[runID]
		if control.Paused && (run.CancelRequested || run.Finishing != nil || (run.Status != state.RunRunning && run.CleanupPending)) {
			stopped, err := task.StopPaused(ctx, a.Store, runID)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if stopped.Status != state.RunRunning && !stopped.CleanupPending {
				if a.Leases != nil {
					if _, err := a.Leases.DeleteTaskLease(ctx, a.UserID, runID, 0); err != nil {
						failures = append(failures, err)
					}
				}
				continue
			}
		}
		if a.Leases == nil {
			continue
		}
		lease, exists := existingLeases[runID]
		if !exists {
			if _, err := a.Leases.EnsureTaskLease(ctx, a.UserID, runID, time.Time{}); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if !lease.Runnable && !control.Paused {
			shouldWake := false
			switch lease.WaitingReason {
			case docstore.WaitPaused:
				shouldWake = true
			case docstore.WaitRoutingPending:
				if convErr == nil && !conv.RoutingPending() {
					shouldWake = true
				}
			case docstore.WaitQuestion:
				waitingOnSubtasks := run.Question != nil && run.Question.Kind == state.QuestionWork && len(state.RunningSubtasks(run)) > 0
				if run.Question == nil || run.CancelRequested || (run.Question.ResolvedBy != "" && !waitingOnSubtasks) {
					shouldWake = true
				}
			case docstore.WaitFinishing:
				if !hasActiveOrUncleanChild(run, needLease) {
					shouldWake = true
				}
			default:
				shouldWake = true
			}
			if shouldWake {
				if err := a.Leases.WakeTask(ctx, a.UserID, runID, time.Time{}); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}

	// 2. Delete orphaned task leases whose runs are missing or terminal & clean.
	if a.Leases != nil {
		existingIDs := make([]string, 0, len(existingLeases))
		for runID := range existingLeases {
			existingIDs = append(existingIDs, runID)
		}
		slices.Sort(existingIDs)
		for _, runID := range existingIDs {
			if _, needed := needLease[runID]; needed {
				continue
			}
			run, err := a.Store.GetRun(ctx, runID)
			if errors.Is(err, state.ErrNotFound) {
				if _, delErr := a.Leases.DeleteTaskLease(ctx, a.UserID, runID, 0); delErr != nil {
					failures = append(failures, delErr)
				}
				continue
			}
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if run.Status != state.RunRunning && !run.CleanupPending {
				if snap, sErr := a.Store.Reconcile(ctx, runID); sErr != nil {
					failures = append(failures, sErr)
					continue
				} else {
					var delCmdErr error
					for _, cmd := range snap.Commands {
						if !cmd.Delivered {
							if dErr := a.Store.Delivered(ctx, cmd.ID); dErr != nil {
								delCmdErr = errors.Join(delCmdErr, dErr)
							}
						}
					}
					if delCmdErr != nil {
						failures = append(failures, delCmdErr)
						continue
					}
				}
				if _, delErr := a.Leases.DeleteTaskLease(ctx, a.UserID, runID, 0); delErr != nil {
					failures = append(failures, delErr)
				}
			}
		}
	}

	// 2b. Reconcile any residual undelivered commands left behind if StopPaused or task.Runner
	// failed mid-delivery after persisting terminal state and deleting the lease.
	after := ""
	for {
		page, err := a.Store.Pending(ctx, after)
		if err != nil {
			failures = append(failures, err)
			break
		}
		for _, cmd := range page.Commands {
			if _, active := needLease[cmd.RunID]; active {
				continue
			}
			run, err := a.Store.GetRun(ctx, cmd.RunID)
			if errors.Is(err, state.ErrNotFound) || (err == nil && run.Status != state.RunRunning && !run.CleanupPending) {
				if dErr := a.Store.Delivered(ctx, cmd.ID); dErr != nil {
					failures = append(failures, dErr)
				}
			} else if err != nil {
				failures = append(failures, err)
			} else if a.Leases != nil {
				if _, eErr := a.Leases.EnsureTaskLease(ctx, a.UserID, cmd.RunID, time.Time{}); eErr != nil {
					failures = append(failures, eErr)
				}
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}

	if a.Leases != nil {
		// 3. Reconcile outer conversation & scheduler wakes without bumping wake_seq on active runnable rows.
		wakesByLane := map[string]docstore.UserWake{}
		if wakes, err := a.Leases.ListUserWakes(ctx, a.UserID); err != nil {
			failures = append(failures, err)
		} else {
			for _, w := range wakes {
				wakesByLane[w.Lane] = w
			}
		}
		if convErr == nil && (conv.Pending() || conv.Active != "") {
			w, ok := wakesByLane[docstore.LaneConversation]
			if !ok || !w.Runnable {
				if err := a.Leases.WakeUser(ctx, a.UserID, docstore.LaneConversation, time.Time{}); err != nil {
					failures = append(failures, err)
				}
			}
		} else if convErr == nil {
			if wakeAt, err := a.NextConversationWake(ctx); err == nil && !wakeAt.IsZero() {
				w, ok := wakesByLane[docstore.LaneConversation]
				if !ok || !w.Runnable || w.WakeAt.After(wakeAt) {
					if err := a.Leases.WakeUser(ctx, a.UserID, docstore.LaneConversation, wakeAt); err != nil {
						failures = append(failures, err)
					}
				}
			}
		}
		if nextDue := a.NextSchedulerWake(ctx); nextDue != nil {
			w, ok := wakesByLane[docstore.LaneScheduler]
			if !ok || !w.Runnable || w.WakeAt.After(*nextDue) {
				if err := a.Leases.WakeUser(ctx, a.UserID, docstore.LaneScheduler, *nextDue); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}

	return errors.Join(failures...)
}

// NextSchedulerWake returns the earliest time LaneScheduler needs to run again,
// covering enabled automation schedules, pending/unsettled occurrences, and any
// undelivered interaction answers waiting for their task to ask.
func (a *App) NextSchedulerWake(ctx context.Context) *time.Time {
	var earliest *time.Time
	if a.Scheduler != nil {
		earliest = a.Scheduler.NextWake(ctx)
	}
	if a.Assistant != nil && a.Assistant.Permissions != nil {
		if answers, _, err := a.Assistant.Permissions.UndeliveredAnswers(ctx); err == nil {
			for _, ans := range answers {
				if ans.Recheck {
					continue
				}
				if run, rErr := a.Store.GetRun(ctx, ans.TaskID); rErr == nil && run.Status == state.RunRunning {
					retryAt := time.Now().UTC().Add(2 * time.Second)
					if a.Scheduler != nil && a.Scheduler.Now != nil {
						retryAt = a.Scheduler.Now().UTC().Add(2 * time.Second)
					}
					if earliest == nil || retryAt.Before(*earliest) {
						earliest = &retryAt
					}
					break
				}
			}
		}
	}
	return earliest
}

func hasActiveOrUncleanChild(parent state.Run, activeRuns map[string]state.Run) bool {
	for _, link := range parent.Subtasks {
		if child, ok := activeRuns[link.ID]; ok {
			if child.Status == state.RunRunning || child.CleanupPending {
				return true
			}
		} else if link.Status == state.RunRunning {
			return true
		}
	}
	for _, other := range activeRuns {
		if other.ParentID == parent.ID && (other.Status == state.RunRunning || other.CleanupPending) {
			return true
		}
	}
	return false
}

func (a *App) RepairWake(ctx context.Context) error {
	if a.WakeRepair == nil {
		return nil
	}
	return a.WakeRepair.Repair(ctx)
}

// NextConversationWake returns the earliest deferred idle wake timestamp for
// out-of-band flush/compaction or AutoDream consolidation, or zero when idle.
func (a *App) NextConversationWake(ctx context.Context) (time.Time, error) {
	if a.Runner == nil {
		return time.Time{}, nil
	}
	return a.Runner.NextConversationWake(ctx)
}

// ProcessConversation drains pending outer conversation inputs for this user.
func (a *App) ProcessConversation(ctx context.Context) error {
	if a.Runner == nil {
		return nil
	}
	return a.Runner.Process(ctx)
}

func (a *App) Process(ctx context.Context) error {
	if a.Runner == nil {
		return nil
	}
	return errors.Join(a.ProcessConversation(ctx), a.Schedule(ctx))
}

// Schedule turns due automation slots into tasks, settles finished occurrences and delivers results. Duplicate or
// lost passes are harmless: every step is keyed by a stable occurrence identity.
func (a *App) Schedule(ctx context.Context) error {
	if a.Scheduler == nil || a.Scheduler.Accept == nil {
		return nil
	}
	_, err := a.Scheduler.Tick(ctx)
	return errors.Join(err, a.deliverAnswers(ctx))
}

// deliverAnswers retries answer commands for interactions resolved before their task asked. An answer for a task
// that ended or moved to a newer instruction is retired: the newer revision supersedes it.
func (a *App) deliverAnswers(ctx context.Context) error {
	if a.Assistant == nil || a.Assistant.Permissions == nil || a.Assistant.Answer == nil {
		return nil
	}
	answers, ids, err := a.Assistant.Permissions.UndeliveredAnswers(ctx)
	if err != nil {
		return err
	}
	for i, answer := range answers {
		run, runErr := a.Store.GetRun(ctx, answer.TaskID)
		waiting := runErr == nil && run.Status == state.RunRunning && run.Question != nil && run.Question.ID == answer.Question && run.Question.ResolvedBy == ""
		if answer.Recheck && !waiting {
			// Recorded as delivered and the task is not waiting on it: nothing to repair.
			continue
		}
		err := a.Assistant.Answer(ctx, answer.CommandID, answer.TaskID, answer.Question, answer.Text)
		if errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy) {
			// Still running: the task has not asked yet or is mid-step, so try again on the next pass.
			if current, e := a.Store.GetRun(ctx, answer.TaskID); e == nil && current.Status == state.RunRunning && (current.Question == nil || current.Question.ID == answer.Question) {
				continue
			}
		}
		if (err == nil || errors.Is(err, state.ErrDuplicate) || errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy)) && !answer.Recheck {
			a.Assistant.Permissions.MarkDelivered(ctx, ids[i])
		}
	}
	return nil
}

func AssistantFeatures(conns *connections.Service, disableBackgroundTasks bool) map[string]bool {
	hasConnections := conns != nil && len(conns.Integrations) > 0
	return map[string]bool{
		"integrations": true,
		"connections":  hasConnections,
		"interactions": true,
		"permissions":  true,
		"preferences":  true,
		"work":         !disableBackgroundTasks,
		"operations":   true,
		"automations":  !disableBackgroundTasks,
		"goals":        true,
		"inbox":        true,
		"library":      true,
		"search":       true,
		"memory":       true,
		"mcp":          true,
	}
}

// NewOuterModelAutoReviewer wraps an outer conversation.ReplyModel behind deterministic
// action-boundary guardrails. Deterministic blocks/escalations always run first and never
// invoke the model; model errors fail safe to "ask".
func NewOuterModelAutoReviewer(model conversation.ReplyModel) permissions.AutoReviewer {
	if model == nil {
		return permissions.DeterministicAutoReviewer{}
	}
	return &permissions.ModelAutoReviewer{
		Review: func(ctx context.Context, prompt string) (string, error) {
			reviewCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			msg, err := json.Marshal(map[string]string{"role": "user", "content": prompt})
			if err != nil {
				return "", err
			}
			reply, err := model.Respond(reviewCtx, conversation.ReplyRequest{
				Messages:       []json.RawMessage{msg},
				NoDispatch:     true,
				TelemetryInput: "action-boundary auto-review",
			})
			if err != nil {
				return "", err
			}
			return reply.Text, nil
		},
	}
}

func newAutoReviewer(outer conversation.ReplyModel, cfg settings.Settings) permissions.AutoReviewer {
	if outer != nil && cfg.Models.TestURL == "" && (cfg.Models.UsesGateway() || cfg.Models.OpenAIKey != "") {
		return NewOuterModelAutoReviewer(outer)
	}
	return permissions.DeterministicAutoReviewer{}
}

var _ api.Operations = (*App)(nil)
