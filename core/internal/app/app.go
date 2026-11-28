package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"nori/internal/agent"
	"nori/internal/api"
	"nori/internal/computer"
	"nori/internal/connections"
	computerconn "nori/internal/connectors/computer"
	"nori/internal/conversation"
	"nori/internal/extensions"
	"nori/internal/homeai"
	"nori/internal/llmguard"
	"nori/internal/modelprofiles"
	"nori/internal/permissions"
	"nori/internal/platform"
	"nori/internal/proactivity"
	"nori/internal/push"
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
	UserID          string
	Store           state.ConversationStore
	Leases          docstore.LeaseStore
	Features        settings.Features
	Runner          *conversation.Runner
	Tasks           *conversation.Tasks
	TaskActivities  *task.Activities
	Waker           wake.Waker
	WakeRepair      *wake.Repairer
	Model           string
	ModelProvider   string
	handler         http.Handler
	Assistant       *api.Assistant
	Deltas          *api.DeltaBroker
	Scheduler       *scheduler.Service
	Discovery       *proactivity.DiscoveryService
	ProactiveDrafts *task.ProactiveDraftService
	PushWorker      *push.OutboxWorker

	guardWakeRepair func(context.Context) (func(), error)
}

type openOptions struct {
	skipCollapseDuplicates bool
	home                   *homeai.Service
	computer               *computer.Manager
	pushStore              push.Store
	pushWorker             *push.OutboxWorker
}

type appModels struct {
	account          *modelprofiles.Account
	outer            conversation.ReplyModel
	defaultTaskModel agent.Model
	summarizer       agent.Model
}

func openStoreAndLeases(ctx context.Context, waker wake.Waker, cfg settings.Settings, user users.User, storage *platform.Storage) (state.ConversationStore, docstore.LeaseStore, wake.Waker, error) {
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
		return nil, nil, nil, err
	}
	if waker == nil {
		waker = platform.Waker(leases)
	}
	return store, leases, wake.ForUser(waker, user.ID), nil
}

func buildAppModels(cfg *settings.Settings, user users.User, store state.ConversationStore, services *platform.AssistantServices, home *homeai.Service) appModels {
	if cfg.Models.BYOKRequired {
		account := &modelprofiles.Account{Store: store, Keys: services.ModelKeys}
		cfg.Models.CloudDispatchClient = llmguard.NewCheckedClient(account.CheckCloudDispatch)
		if home != nil {
			account.HomeProfiles = func(ctx context.Context) ([]modelprofiles.Profile, error) {
				devices, err := home.List(ctx, user.ID)
				return homeProfiles(devices, cfg.HomeAI.WorkEnabled), err
			}
		}
		outer := &accountReplyModel{account: account, models: cfg.Models, userID: user.ID, home: home}
		defaultTaskModel := &accountTaskModel{account: account, models: cfg.Models, meter: store, userID: user.ID, home: home, homeWork: cfg.HomeAI.WorkEnabled}
		return appModels{account: account, outer: outer, defaultTaskModel: defaultTaskModel, summarizer: defaultTaskModel}
	}
	outer := platform.ReplyModelForUser(cfg.Models, user.ID)
	defaultTaskModel := platform.TaskModelForUser(cfg.Models, store, user.ID)
	summaryModels := cfg.Models
	if summaryModels.EffectiveInnerProvider() == "openrouter" && summaryModels.Inner == "" {
		summaryModels.Inner = settings.DefaultOpenRouterInnerModel
	}
	summarizer := platform.TaskModelForUser(summaryModels, store, user.ID)
	return appModels{outer: outer, defaultTaskModel: defaultTaskModel, summarizer: summarizer}
}

func openWithResources(ctx context.Context, waker wake.Waker, cfg settings.Settings, user users.User, storage *platform.Storage, opts openOptions) (*App, bool, error) {
	cfg = platform.ForUser(cfg, user)
	if err := validateStartup(cfg); err != nil {
		return nil, false, err
	}
	store, leases, waker, err := openStoreAndLeases(ctx, waker, cfg, user, storage)
	if err != nil {
		return nil, false, err
	}
	location, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		store.Close()
		return nil, false, fmt.Errorf("user time zone must be an IANA time zone")
	}
	model, provider := "", ""
	if !cfg.Models.BYOKRequired {
		model, provider = cfg.Models.Outer, cfg.Models.EffectiveOuterProvider()
	}
	deltas := api.NewDeltaBroker()
	result := &App{
		UserID:        user.ID,
		Store:         store,
		Leases:        leases,
		Features:      cfg.Features,
		Waker:         waker,
		Model:         model,
		ModelProvider: provider,
		Deltas:        deltas,
	}
	var services *platform.AssistantServices
	if storage != nil {
		services, err = storage.Assistant(cfg, user, store)
	} else {
		services, err = platform.Assistant(cfg, user, store)
	}
	if err != nil {
		store.Close()
		return nil, false, err
	}
	if services.Permissions != nil && services.Permissions.UserSends == "" {
		services.Permissions.UserSends = "ask"
	}
	if opts.computer != nil && opts.computer.Enabled() {
		computerconn.Register(services.Adapters, computerconn.NewAdapter(opts.computer, user.ID, store, cfg.Cloud == "local"))
		services.Tools = extensions.Sources{services.Tools, computerconn.Source{Enabled: true}}
	}
	collapsed := collapseDuplicateConnections(ctx, user.ID, services.Connections, opts.skipCollapseDuplicates)

	toolSource := &agent.Service{Extensions: services.Tools}
	result.Scheduler = services.Scheduler
	result.Discovery = &proactivity.DiscoveryService{Store: store}
	result.PushWorker = opts.pushWorker
	wireSchedulerProactivityHooks(services.Scheduler, store, services.Connections, opts.pushWorker, user.ID)
	models := buildAppModels(&cfg, user, store, services, opts.home)

	result.Assistant = &api.Assistant{
		Store: store, Connections: services.Connections, Permissions: services.Permissions, Tools: toolSource.Tools,
		Scheduler: services.Scheduler, UnattendedWrites: cfg.Connections.UnattendedWrites, MCP: services.MCP,
		ModelKeys: services.ModelKeys, Models: models.account, AccountBYOK: cfg.Models.BYOKRequired,
		Features:      AssistantFeatures(services.Connections, cfg.Features.DisableBackgroundTasks),
		NextScheduled: services.Scheduler.NextScheduled, InboxUnread: services.Scheduler.UnreadCount,
		NormalizeMemory: newMemoryNormalizer(models.summarizer, models.outer, store, cfg),
		OnPreferencesSaved: func(ctx context.Context, prefs connections.Preferences) {
			if services.Scheduler != nil {
				if _, err := services.Scheduler.EnsureMorningBrief(ctx, prefs); err != nil {
					slog.WarnContext(ctx, "ensure morning brief after preferences save failed", "user_id", user.ID, "err", err)
				}
			}
		},
	}
	if opts.pushStore != nil {
		result.Assistant.PushDevices = &api.PushDevicesAPI{Store: opts.pushStore, UserID: user.ID}
	}
	if opts.home != nil && models.account != nil {
		result.Assistant.HomeAI = &api.HomeAISettings{
			Service: opts.home, UserID: user.ID, Downloads: cfg.HomeAI.Downloads,
			Test: func(ctx context.Context, deviceID string) error {
				return testHomeAI(ctx, opts.home, models.account, cfg.Models, user.ID, deviceID)
			},
		}
		result.Assistant.Features["homeConnectors"] = true
	}
	if opts.computer != nil && opts.computer.Enabled() {
		result.Assistant.Features["computer"] = true
		result.Assistant.Computer = &api.ComputerAPI{Manager: opts.computer, UserID: user.ID}
		compMgr := opts.computer
		uid := user.ID
		result.Assistant.OnLibraryDelete = func(ctx context.Context, resultIDs []string) {
			for _, rid := range resultIDs {
				_ = compMgr.PurgeLinkedResultFiles(ctx, uid, rid)
			}
		}
		result.Assistant.OnForget = func(ctx context.Context, tags []string) {
			for _, tag := range tags {
				_ = compMgr.PurgeLinkedEntityFiles(ctx, uid, tag)
			}
		}
	}
	result.WakeRepair = platform.WorkWakes(store, waker, !cfg.Features.DisableBackgroundTasks)
	if cfg.Features.DisableBackgroundTasks {
		return result, collapsed, nil
	}

	wireBackgroundRuntime(result, cfg, user, location, services, models, opts.home)
	return result, collapsed, nil
}

func collapseDuplicateConnections(ctx context.Context, userID string, conns *connections.Service, skip bool) bool {
	if skip {
		return false
	}
	removed, err := conns.CollapseDuplicates(ctx)
	if err != nil {
		slog.WarnContext(ctx, "connection duplicate repair failed", "user_id", userID, "err", err)
		return false
	}
	for kept, drop := range removed {
		slog.InfoContext(ctx, "combined duplicate connections", "user_id", userID, "kept_connection_id", kept, "dropped_connection_ids", drop)
	}
	return true
}

func wireBackgroundRuntime(result *App, cfg settings.Settings, user users.User, location *time.Location, services *platform.AssistantServices, models appModels, home *homeai.Service) {
	store, leases, waker := result.Store, result.Leases, result.Waker
	userFacts := user.Facts()
	tasks := &conversation.Tasks{Store: store}

	var classifier conversation.IntentClassifier
	if !cfg.Features.DisableJev {
		classifier = platform.ClassifierForUser(cfg.Models, user.ID)
		if models.account != nil {
			classifier = accountClassifier{account: models.account, next: classifier}
		}
	}
	runner := conversation.New(models.outer, classifier, models.summarizer, tasks)
	runner.UserFacts = userFacts
	runner.Profile = platform.Profile(user, services.Connections)
	runner.Capabilities = buildCapabilitySummary(services, cfg, models)
	runner.ModelInfo = buildModelSummary(cfg, models)
	runner.Clock = conversation.Clock{Location: location}
	runner.OnDelta = result.Deltas.Publish
	if result.Assistant != nil {
		runner.OnForget = result.Assistant.OnForget
	}
	if result.Discovery != nil {
		discoverySvc := result.Discovery
		conns := services.Connections
		runner.DiscoveryHint = func(ctx context.Context, userMessage string) *proactivity.DiscoveryHint {
			ds, err := discoverySvc.Get(ctx)
			if err != nil {
				return nil
			}
			prefs, _, _ := conns.Preferences(ctx)
			records, _ := conns.List(ctx)
			hasCalendar, hasExternal := false, false
			for _, rec := range records {
				if rec.Status == connections.StatusConnected {
					hasExternal = true
					for _, cap := range rec.Capabilities {
						if cap == "calendar" || cap == "calendar.read" {
							hasCalendar = true
						}
					}
				}
			}
			return proactivity.NextDiscoveryHint(ds, proactivity.DiscoveryContext{
				Now:                time.Now().UTC(),
				Timezone:           prefs.Timezone,
				UserMessage:        userMessage,
				HasCalendar:        hasCalendar,
				HasExternalSources: hasExternal,
			})
		}
	}
	if cfg.Models.BYOKRequired {
		runner.AccountModel = models.outer.(*accountReplyModel).resolve
		runner.AccountSelection = models.account.CurrentSelection
		runner.HomeOnly = models.account.HomeOnly
	}

	taskService := agent.New(store, models.defaultTaskModel)
	taskService.Capabilities = agent.Capabilities{ExaSearchKey: cfg.ExaSearchKey, Location: location}
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
			AutoReviewer: newAutoReviewer(models.outer, store, cfg),
		},
	}
	result.ProactiveDrafts = &task.ProactiveDraftService{
		Store:       store,
		Permissions: services.Permissions,
		Executor:    result.TaskActivities.Executor,
	}
	if result.Assistant != nil {
		drafts := result.ProactiveDrafts
		result.Assistant.OnProactiveDraftResponse = func(ctx context.Context, interactionID, kind string) error {
			if kind == "approve" {
				_, err := drafts.ExecuteApprovedDraft(ctx, interactionID)
				return err
			}
			_, _, err := drafts.HandleDeclineOrSkip(ctx, interactionID, kind)
			return err
		}
	}
	if cfg.Models.BYOKRequired {
		result.TaskActivities.AccountModel = models.defaultTaskModel.(*accountTaskModel).resolve
		if home != nil && cfg.HomeAI.WorkEnabled {
			result.TaskActivities.HomeModelAvailability = models.defaultTaskModel.(*accountTaskModel).availability
		}
	}

	result.Runner, result.Tasks = runner, tasks
	wireAssistantCallbacks(result, user.ID, leases, waker, services, tasks, runner)
	wireConversationPushHook(runner, services.Connections, result.PushWorker, user.ID)
}

func wireAssistantCallbacks(result *App, userID string, leases docstore.LeaseStore, waker wake.Waker, services *platform.AssistantServices, tasks *conversation.Tasks, runner *conversation.Runner) {
	result.Assistant.ConsolidateMemory = runner.ConsolidateNow
	result.Assistant.Answer = func(ctx context.Context, commandID, taskID, questionID, text string) error {
		_, err := tasks.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: taskID, Kind: state.CommandAnswer, QuestionID: questionID, Text: text}}, nil)
		return err
	}
	services.Scheduler.Arm = func(ctx context.Context, at time.Time) {
		if leases != nil {
			if err := leases.WakeUser(ctx, userID, docstore.LaneScheduler, at); err != nil {
				slog.WarnContext(ctx, "scheduler due-time wake arm failed; next pass will repair", "user_id", userID, "lane", docstore.LaneScheduler, "err", err)
			}
			return
		}
		if err := wake.At(ctx, waker, at); err != nil && !errors.Is(err, wake.ErrNotConfigured) {
			slog.WarnContext(ctx, "scheduler due-time wake arm failed; next pass will repair", "user_id", userID, "err", err)
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
			_ = leases.WakeUser(ctx, userID, docstore.LaneScheduler, now.Add(2*time.Second))
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
}

func (a *App) Close() {
	a.Store.Close()
}

func (a *App) apiDeps() api.Deps {
	return api.Deps{
		Model: a.Model, ModelProvider: a.ModelProvider,
		Runner: a.Runner, Tasks: a.Tasks, Waker: a.Waker, Ops: a, Features: a.Features, Assistant: a.Assistant, Deltas: a.Deltas,
	}
}

func (a *App) Resume(ctx context.Context) error {
	return errors.Join(a.RepairLeases(ctx), a.Schedule(ctx))
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
	var expireErr error
	if a.ProactiveDrafts != nil {
		now := time.Now().UTC()
		if a.Scheduler.Now != nil {
			now = a.Scheduler.Now().UTC()
		}
		_, expireErr = a.ProactiveDrafts.ExpirePendingDrafts(ctx, now)
	}
	_, err := a.Scheduler.Tick(ctx)
	var pushErr error
	if a.PushWorker != nil && a.PushWorker.Sender != nil {
		_, pushErr = a.PushWorker.ProcessPending(ctx, 20)
	}
	return errors.Join(expireErr, err, pushErr, a.deliverAnswers(ctx))
}

func wireSchedulerProactivityHooks(sched *scheduler.Service, store state.ConversationStore, conns *connections.Service, pushWorker *push.OutboxWorker, userID string) {
	if sched == nil || store == nil {
		return
	}
	if appender, ok := store.(interface {
		AppendProactiveMessage(context.Context, string, state.Message) (state.Message, error)
	}); ok {
		sched.AppendProactiveMessage = appender.AppendProactiveMessage
	}
	if enqueuer, ok := store.(interface {
		EnqueueProactiveTaskEvent(context.Context, state.TaskEvent) error
	}); ok {
		sched.EnqueueProactiveEvent = enqueuer.EnqueueProactiveTaskEvent
	}
	sched.ReadPosition = store.ReadPosition
	sched.LastUserTurnAt = func(ctx context.Context) (time.Time, error) {
		msgs, _, err := store.ConversationMessages(ctx, "")
		if err != nil {
			return time.Time{}, err
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				return msgs[i].CreatedAt, nil
			}
		}
		return time.Time{}, nil
	}
	sched.LastActivity = func(ctx context.Context) (time.Time, error) {
		var latest time.Time
		if rm, err := store.ReadPosition(ctx); err == nil && !rm.Through.IsZero() {
			latest = rm.Through
		}
		if sched.LastUserTurnAt != nil {
			if t, err := sched.LastUserTurnAt(ctx); err == nil && t.After(latest) {
				latest = t
			}
		}
		return latest, nil
	}
	if pushWorker != nil && userID != "" {
		sched.OnPush = func(ctx context.Context, d scheduler.Delivery) error {
			proactivity := connections.Preferences{}.EffectiveProactivity()
			if conns != nil {
				if prefs, _, err := conns.Preferences(ctx); err == nil {
					proactivity = prefs.EffectiveProactivity()
				}
			}
			var kind push.NotificationKind
			switch {
			case d.InteractionID != "":
				if !proactivity.IsPushUrgentAndApprovalsEnabled() {
					return nil
				}
				kind = push.KindApproval
			case d.AutomationID == scheduler.MorningBriefAutomationID:
				if !proactivity.IsPushMorningBriefEnabled() {
					return nil
				}
				kind = push.KindBrief
			case d.Outcome == scheduler.OutcomeAlert:
				if !proactivity.IsPushUrgentAndApprovalsEnabled() {
					return nil
				}
				kind = push.KindUrgent
			default:
				return nil
			}
			_, _, err := pushWorker.Enqueue(ctx, push.EnqueueRequest{
				DeliveryID:    d.ID,
				UserID:        userID,
				InteractionID: d.InteractionID,
				CollapseID:    d.ID,
				Kind:          kind,
			})
			return err
		}
	}
}

func wireConversationPushHook(runner *conversation.Runner, conns *connections.Service, pushWorker *push.OutboxWorker, userID string) {
	if runner == nil || pushWorker == nil || userID == "" {
		return
	}
	runner.OnAssistantReply = func(ctx context.Context, turnID string) {
		if turnID == "" {
			return
		}
		proactivity := connections.Preferences{}.EffectiveProactivity()
		if conns != nil {
			if prefs, _, err := conns.Preferences(ctx); err == nil {
				proactivity = prefs.EffectiveProactivity()
				qh := prefs.EffectiveQuietHours()
				if qh.IsEnabled() {
					now := time.Now().UTC()
					if runner.Now != nil {
						now = runner.Now()
					}
					if inQuiet, _ := scheduler.InQuietHours(now, qh, prefs.Timezone); inQuiet {
						return
					}
				}
			}
		}
		if !proactivity.IsPushChatsEnabled() {
			return
		}
		_, _, _ = pushWorker.Enqueue(ctx, push.EnqueueRequest{
			DeliveryID: "chat_" + turnID,
			UserID:     userID,
			CollapseID: "chat_assistant",
			Kind:       push.KindUpdate,
		})
	}
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
