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
	"nori/internal/homeai"
	"nori/internal/llmguard"
	"nori/internal/modelprofiles"
	"nori/internal/permissions"
	"nori/internal/platform"
	"nori/internal/scheduler"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
	"nori/internal/usage"
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
	ModelProvider  string
	ModelProfiles  *modelprofiles.Registry
	CanPickModels  bool
	handler        http.Handler
	Assistant      *api.Assistant
	Deltas         *api.DeltaBroker
	Scheduler      *scheduler.Service

	guardWakeRepair func(context.Context) (func(), error)
}

type openOptions struct {
	skipCollapseDuplicates bool
	home                   *homeai.Service
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
	var profilesReg *modelprofiles.Registry
	var canPickModels bool
	model, provider := "", ""
	if !cfg.Models.BYOKRequired {
		model, provider = cfg.Models.Outer, cfg.Models.EffectiveOuterProvider()
		profilesReg, err = modelprofiles.NewRegistry(cfg)
		if err != nil {
			store.Close()
			return nil, false, err
		}
		canPickModels = profilesReg.CanSelect(user.ID, user.Email)
	}
	location, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		store.Close()
		return nil, false, fmt.Errorf("user time zone must be an IANA time zone")
	}
	userFacts := user.Facts()
	deltas := api.NewDeltaBroker()
	result := &App{
		UserID:        user.ID,
		Store:         store,
		Leases:        leases,
		Features:      cfg.Features,
		Waker:         waker,
		Model:         model,
		ModelProvider: provider,
		ModelProfiles: profilesReg,
		CanPickModels: canPickModels,
		Deltas:        deltas,
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
	var account *modelprofiles.Account
	var outer conversation.ReplyModel
	var summarizer, defaultTaskModel agent.Model
	if cfg.Models.BYOKRequired {
		if services.ModelKeys != nil {
			account = &modelprofiles.Account{Store: store, Keys: services.ModelKeys}
			cfg.Models.CloudDispatchClient = llmguard.NewCheckedClient(account.CheckCloudDispatch)
			if opts.home != nil {
				account.HomeProfiles = func(ctx context.Context) ([]modelprofiles.Profile, error) {
					devices, err := opts.home.List(ctx, user.ID)
					return homeProfiles(devices, cfg.HomeAI.WorkEnabled), err
				}
			}
		}
		outer = &accountReplyModel{account: account, models: cfg.Models, userID: user.ID, home: opts.home}
		defaultTaskModel = &accountTaskModel{account: account, models: cfg.Models, meter: store, userID: user.ID, home: opts.home, homeWork: cfg.HomeAI.WorkEnabled}
		summarizer = defaultTaskModel
	} else {
		outer = platform.ReplyModelForUser(cfg.Models, user.ID)
		defaultTaskModel = platform.TaskModelForUser(cfg.Models, store, user.ID)
		summaryModels := cfg.Models
		if summaryModels.EffectiveInnerProvider() == "openrouter" && summaryModels.Inner == "" {
			summaryModels.Inner = settings.DefaultOpenRouterInnerModel
		}
		summarizer = platform.TaskModelForUser(summaryModels, store, user.ID)
	}
	result.Assistant = &api.Assistant{
		Store: store, Connections: services.Connections, Permissions: services.Permissions, Tools: toolSource.Tools,
		Scheduler: services.Scheduler, UnattendedWrites: cfg.Connections.UnattendedWrites, MCP: services.MCP,
		ModelKeys: services.ModelKeys, Models: account, AccountBYOK: cfg.Models.BYOKRequired,
		Features:      AssistantFeatures(services.Connections, cfg.Features.DisableBackgroundTasks),
		NextScheduled: services.Scheduler.NextScheduled, InboxUnread: services.Scheduler.UnreadCount,
		NormalizeMemory: newMemoryNormalizer(summarizer, outer, store, cfg),
	}
	if opts.home != nil && account != nil {
		result.Assistant.HomeAI = &api.HomeAISettings{
			Service: opts.home, UserID: user.ID, Downloads: cfg.HomeAI.Downloads,
			Test: func(ctx context.Context, deviceID string) error {
				return testHomeAI(ctx, opts.home, account, cfg.Models, user.ID, deviceID)
			},
		}
		result.Assistant.Features["homeConnectors"] = true
	}
	result.WakeRepair = platform.WorkWakes(store, waker, !cfg.Features.DisableBackgroundTasks)
	if cfg.Features.DisableBackgroundTasks {
		return result, collapsed, nil
	}
	tasks := &conversation.Tasks{Store: store}
	var classifier conversation.IntentClassifier
	if !cfg.Features.DisableJev {
		classifier = platform.ClassifierForUser(cfg.Models, user.ID)
		if account != nil {
			classifier = accountClassifier{account: account, next: classifier}
		}
	}
	runner := conversation.New(outer, classifier, summarizer, tasks)
	runner.UserFacts = userFacts
	runner.Profile = platform.Profile(user, services.Connections)
	runner.Clock = conversation.Clock{Location: location}
	runner.OnDelta = deltas.Publish
	if cfg.Models.BYOKRequired {
		runner.AccountModel = outer.(*accountReplyModel).resolve
		runner.AccountSelection = account.CurrentSelection
		runner.HomeOnly = account.HomeOnly
	} else {
		runner.ModelForTurn = func(sel *state.ModelSelection) conversation.ReplyModel {
			p, reasoning, explicit, err := profilesReg.ResolveChat(sel)
			if !explicit {
				if profilesReg != nil {
					cat := profilesReg.Snapshot()
					if def, ok := cat.Get(cat.DefaultChatProfileID); ok && def.Backend == modelprofiles.BackendLocal {
						return platform.ReplyModelForProfile(cfg.Models, user.ID, def, def.DefaultReasoning)
					}
				}
				return nil
			}
			if err != nil {
				return &unavailableReplyModel{name: sel.ChatProfileID, err: err}
			}
			return platform.ReplyModelForProfile(cfg.Models, user.ID, p, reasoning)
		}
	}

	taskService := agent.New(store, defaultTaskModel)
	taskService.Capabilities = agent.Capabilities{ExaSearchKey: cfg.ExaSearchKey, SearchKey: cfg.SearchKey, Location: location}
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
			AutoReviewer: newAutoReviewer(outer, store, cfg),
		},
	}

	if cfg.Models.BYOKRequired {
		result.TaskActivities.AccountModel = defaultTaskModel.(*accountTaskModel).resolve
		if opts.home != nil && cfg.HomeAI.WorkEnabled {
			result.TaskActivities.HomeModelAvailability = defaultTaskModel.(*accountTaskModel).availability
		}
	} else {
		result.TaskActivities.ModelForRun = func(sel *state.ModelSelection) agent.Model {
			p, reasoning, explicit, err := profilesReg.ResolveTask(sel)
			if !explicit {
				if profilesReg != nil {
					cat := profilesReg.Snapshot()
					if def, ok := cat.Get(cat.DefaultTaskProfileID); ok && def.Backend == modelprofiles.BackendLocal {
						return platform.TaskModelForProfile(cfg.Models, store, user.ID, def, def.DefaultReasoning)
					}
				}
				return nil
			}
			if err != nil {
				return &unavailableTaskModel{name: sel.TaskProfileID, err: err}
			}
			return platform.TaskModelForProfile(cfg.Models, store, user.ID, p, reasoning)
		}
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
	return api.Deps{
		Model: a.Model, ModelProvider: a.ModelProvider, ModelProfiles: a.ModelProfiles, CanPickModels: a.CanPickModels,
		Runner: a.Runner, Tasks: a.Tasks, Waker: a.Waker, Ops: a, Features: a.Features, Assistant: a.Assistant, Deltas: a.Deltas,
	}
}

func (a *App) Resume(ctx context.Context) error {
	return errors.Join(a.RepairLeases(ctx), a.Schedule(ctx))
}

func (a *App) RepairLeases(ctx context.Context) error {
	var failures []error
	if a.Tasks != nil {
		if err := a.Tasks.CleanupUploads(ctx); err != nil {
			failures = append(failures, err)
		}
	}
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
	if a.guardWakeRepair != nil {
		done, err := a.guardWakeRepair(ctx)
		if err != nil {
			return err
		}
		defer done()
	}
	return a.WakeRepair.Repair(ctx)
}

func (a *App) NextConversationWake(ctx context.Context) (time.Time, error) {
	if a.Runner == nil {
		return time.Time{}, nil
	}
	return a.Runner.NextConversationWake(ctx)
}

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

func (a *App) Schedule(ctx context.Context) error {
	if a.Scheduler == nil || a.Scheduler.Accept == nil {
		return nil
	}
	_, err := a.Scheduler.Tick(ctx)
	return errors.Join(err, a.deliverAnswers(ctx))
}

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
			continue
		}
		err := a.Assistant.Answer(ctx, answer.CommandID, answer.TaskID, answer.Question, answer.Text)
		if errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy) {
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

func NewOuterModelAutoReviewer(model conversation.ReplyModel) permissions.AutoReviewer {
	return NewOuterModelAutoReviewerWithMeter(model, nil)
}

func NewOuterModelAutoReviewerWithMeter(model conversation.ReplyModel, meter interface {
	RecordUsage(context.Context, state.Usage) error
}) permissions.AutoReviewer {
	if model == nil {
		return permissions.DeterministicAutoReviewer{}
	}
	return &permissions.ModelAutoReviewer{
		Review: func(ctx context.Context, prompt string) (string, error) {
			reviewCtx, cancel := context.WithTimeout(llmguard.WithScope(ctx, llmguard.ScopeReview), 15*time.Second)
			defer cancel()
			msg, err := json.Marshal(map[string]string{"role": "user", "content": prompt})
			if err != nil {
				return "", err
			}
			var recordFn func(context.Context, state.Usage) error
			if meter != nil {
				recordFn = func(rctx context.Context, u state.Usage) error {
					if u.InputID == "" {
						u.InputID = "auto-review"
					}
					return meter.RecordUsage(rctx, u)
				}
			}
			reply, err := model.Respond(reviewCtx, conversation.ReplyRequest{
				Messages:       []json.RawMessage{msg},
				NoDispatch:     true,
				RecordUsage:    recordFn,
				TelemetryInput: "action-boundary auto-review",
			})
			if meter != nil && reply.Usage.ID != "" {
				if reply.Usage.InputID == "" {
					reply.Usage.InputID = "auto-review"
				}
				saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_ = meter.RecordUsage(saveCtx, reply.Usage)
				saveCancel()
			}
			if err != nil {
				return "", err
			}
			return reply.Text, nil
		},
	}
}

func newAutoReviewer(outer conversation.ReplyModel, meter interface {
	RecordUsage(context.Context, state.Usage) error
}, cfg settings.Settings) permissions.AutoReviewer {
	_, outerKey := cfg.Models.OuterCredential()
	if outer != nil && cfg.Models.TestURL == "" && (cfg.Models.BYOKRequired || cfg.Models.UsesGatewayOuter() || outerKey != "") {
		return NewOuterModelAutoReviewerWithMeter(outer, meter)
	}
	return permissions.DeterministicAutoReviewer{}
}

func newMemoryNormalizer(inner conversation.SummaryModel, outer conversation.ReplyModel, meter interface {
	RecordUsage(context.Context, state.Usage) error
}, cfg settings.Settings) func(context.Context, conversation.ManualMemoryInput) (conversation.NormalizedMemory, error) {
	if cfg.Models.TestURL != "" {
		return nil
	}
	_, innerKey := cfg.Models.TaskCredential()
	if inner != nil && (cfg.Models.BYOKRequired || cfg.Models.UsesGatewayInner() || innerKey != "") {
		return conversation.NewSummaryModelMemoryNormalizer(inner)
	}
	_, outerKey := cfg.Models.OuterCredential()
	if outer != nil && (cfg.Models.BYOKRequired || cfg.Models.UsesGatewayOuter() || outerKey != "") {
		return conversation.NewMemoryNormalizerWithMeter(outer, meter)
	}
	return nil
}

var _ api.Operations = (*App)(nil)

type accountReplyModel struct {
	account *modelprofiles.Account
	models  settings.Models
	userID  string
	home    *homeai.Service
}

func (m *accountReplyModel) Name() string { return "account" }

func (m *accountReplyModel) resolve(ctx context.Context, sel *state.ModelSelection) conversation.ReplyModel {
	p, effort, key, err := m.account.Resolve(ctx, modelprofiles.LaneChat, sel)
	if err != nil {
		return &unavailableReplyModel{name: p.Model, err: err}
	}
	if p.Backend == modelprofiles.BackendHome {
		return homeReplyModel(m.home, m.models, m.userID, p)
	}
	return platform.ReplyModelForBYOK(m.models, m.userID, p, effort, key)
}

func (m *accountReplyModel) Respond(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error) {
	return m.resolve(ctx, nil).Respond(ctx, req)
}

type accountTaskModel struct {
	account  *modelprofiles.Account
	models   settings.Models
	meter    usage.Meter
	userID   string
	home     *homeai.Service
	homeWork bool
}

func (m *accountTaskModel) Provider() string { return "account" }
func (m *accountTaskModel) Name() string     { return "account" }

func (m *accountTaskModel) resolve(ctx context.Context, sel *state.ModelSelection) agent.Model {
	p, effort, key, err := m.account.Resolve(ctx, modelprofiles.LaneTask, sel)
	if err != nil {
		return &unavailableTaskModel{name: p.Model, err: err}
	}
	if p.Backend == modelprofiles.BackendHome {
		if !m.homeWork {
			return &unavailableTaskModel{name: p.Model, err: modelprofiles.ErrHomeWorkUnavailable}
		}
		return homeTaskModel(m.home, m.models, m.meter, m.userID, p)
	}
	return platform.TaskModelForBYOK(m.models, m.meter, m.userID, p, effort, key)
}

func (m *accountTaskModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	return m.resolve(ctx, nil).Respond(ctx, req)
}

func (m *accountTaskModel) Inspect(ctx context.Context, req agent.InspectRequest) (agent.Inspection, error) {
	return m.resolve(ctx, nil).Inspect(ctx, req)
}
