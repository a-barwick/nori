package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"nori/internal/app"
	"nori/internal/httpserver"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/postgres"
	"nori/internal/users"
	"nori/internal/worker"
)

func validateSimSafety(s settings.Settings, allowDefaultDB bool) error {
	if s.Cloud != "local" {
		return fmt.Errorf("nori-sim requires NORI_CLOUD=local (got %q)", s.Cloud)
	}
	u, err := url.Parse(s.Store.PostgresURL)
	if err != nil {
		return fmt.Errorf("invalid NORI_POSTGRES_URL: %w", err)
	}
	host := u.Hostname()
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("nori-sim requires a loopback Postgres host (got %q)", host)
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if !allowDefaultDB && (dbName == "" || dbName == "nori" || dbName == "postgres") {
		return fmt.Errorf("nori-sim refuses to run against database %q; use an ephemeral DB (or pass --allow-default-db explicitly)", dbName)
	}
	return nil
}

func manageEphemeralDB(ctx context.Context, rawURL, action string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("ephemeral DB management requires a loopback Postgres host (got %q)", host)
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if !strings.HasPrefix(dbName, "nori_sim_") {
		return fmt.Errorf("refusing to %s non-ephemeral database %q (must start with nori_sim_)", action, dbName)
	}
	adminURL := *u
	adminURL.Path = "/postgres"
	conn, err := pgx.Connect(ctx, adminURL.String())
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	ident := pgx.Identifier{dbName}.Sanitize()
	switch action {
	case "create":
		_, err = conn.Exec(ctx, "CREATE DATABASE "+ident)
	case "drop":
		_, err = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	return err
}

func main() {
	startTimeFlag := flag.String("start-time", "", "Initial virtual clock RFC3339 timestamp (defaults to now UTC)")
	allowDefaultDB := flag.Bool("allow-default-db", false, "Allow running against default local database (for workbench seeding)")
	createDBFlag := flag.String("create-db", "", "Create a loopback ephemeral nori_sim_* database at this Postgres URL and exit")
	dropDBFlag := flag.String("drop-db", "", "Drop a loopback ephemeral nori_sim_* database at this Postgres URL and exit")
	userIDFlag := flag.String("user-id", "", "Optional user ID to scope worker.Pool and virtual-clock wake sweeps")
	flag.Parse()

	scopedUserID := strings.TrimSpace(*userIDFlag)
	if scopedUserID != "" && !users.ValidID(scopedUserID) {
		slog.Error("invalid --user-id", "user_id", scopedUserID)
		os.Exit(2)
	}

	if *createDBFlag != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := manageEphemeralDB(ctx, *createDBFlag, "create"); err != nil {
			slog.Error("create-db failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *dropDBFlag != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := manageEphemeralDB(ctx, *dropDBFlag, "drop"); err != nil {
			slog.Error("drop-db failed", "err", err)
			os.Exit(1)
		}
		return
	}

	s, err := settings.LoadDefaultCloud("local")
	if err != nil {
		slog.Error("failed to load settings", "err", err)
		os.Exit(1)
	}
	if err := validateSimSafety(s, *allowDefaultDB); err != nil {
		slog.Error("safety guard rejected startup", "err", err)
		os.Exit(2)
	}

	startTime := time.Now().UTC()
	if *startTimeFlag != "" {
		parsed, err := time.Parse(time.RFC3339, *startTimeFlag)
		if err != nil {
			slog.Error("invalid --start-time", "err", err)
			os.Exit(2)
		}
		startTime = parsed.UTC()
	}
	clock := newSimClock(startTime)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTelemetry := observability.Init(ctx, "nori-sim", s)
	defer observability.Shutdown(stopTelemetry)

	rawBackend, err := postgres.OpenBackendWithOptions(ctx, s.Store.PostgresURL, postgres.Options{
		ListenURL:         s.Store.PostgresListenURL,
		MaxConns:          s.Store.PostgresMaxConns,
		MinConns:          s.Store.PostgresMinConns,
		MaxConnLifetime:   s.Store.PostgresMaxConnLifetime,
		MaxConnIdleTime:   s.Store.PostgresMaxConnIdleTime,
		HealthCheckPeriod: s.Store.PostgresHealthCheckPeriod,
		StatementTimeout:  s.Store.PostgresStatementTimeout,
	})
	if err != nil {
		observability.Fatal(err)
	}

	simBackend := newSimClockBackend(rawBackend, clock)
	storage := platform.NewStorage(simBackend, nil)
	storage.Now = clock.Now

	svc := app.OpenServiceWithStorage(storage, platform.Waker(simBackend), s)
	svc.Now = clock.Now
	svc.IdleTTL = 365 * 24 * time.Hour
	svc.ConfigureApp = func(_ users.User, a *app.App) {
		if a.Runner != nil {
			a.Runner.Now = clock.Now
			a.Runner.Clock.Now = clock.Now
		}
		if a.Tasks != nil {
			a.Tasks.Now = clock.Now
		}
		if a.Scheduler != nil {
			a.Scheduler.Now = clock.Now
		}
		if a.WakeRepair != nil {
			a.WakeRepair.Now = clock.Now
		}
		if a.Assistant != nil {
			if a.Assistant.Permissions != nil {
				a.Assistant.Permissions.Now = clock.Now
			}
			if a.Assistant.Connections != nil {
				a.Assistant.Connections.Now = clock.Now
			}
			if a.Assistant.MCP != nil {
				a.Assistant.MCP.Now = clock.Now
			}
		}
		if a.TaskActivities != nil {
			a.TaskActivities.Now = clock.Now
			if a.TaskActivities.Agent != nil {
				a.TaskActivities.Agent.Now = clock.Now
			}
			if a.TaskActivities.Executor != nil {
				a.TaskActivities.Executor.Now = clock.Now
			}
		}
	}
	defer svc.Close()

	var pool *worker.Pool
	if !s.Features.DisableBackgroundTasks {
		workerOwner := fmt.Sprintf("nori-sim-%s", uuid.NewString()[:8])
		pool, err = worker.New(svc, worker.Options{
			Owner:                   workerOwner,
			UserID:                  scopedUserID,
			TaskConcurrency:         16,
			ConversationConcurrency: 8,
			SchedulerConcurrency:    8,
			PollInterval:            150 * time.Millisecond,
			RepairInterval:          5 * time.Second,
			Now:                     clock.Now,
		})
		if err != nil {
			observability.Fatal(err)
		}
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	recordErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		cancelRun()
	}

	if pool != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := pool.Run(runCtx); err != nil {
				recordErr(err)
			}
		}()
	}

	simMux := buildSimHandler(svc, simBackend, clock, scopedUserID)
	addr := net.JoinHostPort("127.0.0.1", cmp.Or(s.Port, "8080"))
	server := httpserver.New(ctx, addr, simMux)
	slog.InfoContext(runCtx, "nori-sim listening",
		"addr", server.Addr,
		"model", s.Models.Outer,
		"sim_now", clock.Now().Format(time.RFC3339),
	)
	if err := httpserver.Listen(runCtx, server, 5*time.Second); err != nil {
		recordErr(err)
	}
	cancelRun()
	wg.Wait()

	if firstErr != nil {
		observability.Shutdown(stopTelemetry)
		observability.Fatal(firstErr)
	}
}

func writeSimJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func buildSimHandler(svc *app.Service, backend *simClockBackend, clock *simClock, scopedUserID string) http.Handler {
	baseHandler := svc.Handler(true)
	tracker := newSimInvariantTracker()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /_sim/clock", func(w http.ResponseWriter, r *http.Request) {
		writeSimJSON(w, http.StatusOK, map[string]any{
			"sim_now": clock.Now().Format(time.RFC3339),
		})
	})

	mux.HandleFunc("POST /_sim/clock/set", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Time  string `json:"time"`
			Force bool   `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeSimJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		target, err := time.Parse(time.RFC3339, req.Time)
		if err != nil {
			writeSimJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid RFC3339 time: " + err.Error()})
			return
		}
		cur := clock.Now()
		updated := cur
		if req.Force || target.After(cur) {
			updated = clock.Set(target)
		}
		if err := wakeDueForAllUsers(r.Context(), svc, backend, updated, scopedUserID); err != nil {
			writeSimJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeSimJSON(w, http.StatusOK, map[string]any{
			"sim_now": updated.Format(time.RFC3339),
		})
	})

	mux.HandleFunc("POST /_sim/clock/advance", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Seconds float64 `json:"seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeSimJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if req.Seconds < 0 {
			writeSimJSON(w, http.StatusBadRequest, map[string]string{"error": "seconds must be >= 0"})
			return
		}
		updated := clock.Advance(time.Duration(req.Seconds * float64(time.Second)))
		if err := wakeDueForAllUsers(r.Context(), svc, backend, updated, scopedUserID); err != nil {
			writeSimJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeSimJSON(w, http.StatusOK, map[string]any{
			"sim_now": updated.Format(time.RFC3339),
		})
	})

	mux.HandleFunc("POST /_sim/drain", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID         string `json:"user_id"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.UserID == "" {
			writeSimJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
			return
		}
		timeout := 120 * time.Second
		if req.TimeoutSeconds > 0 {
			timeout = time.Duration(req.TimeoutSeconds) * time.Second
		}
		drainCtx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		res, err := drainUserToQuiescence(drainCtx, svc, backend, clock, req.UserID)
		if err != nil {
			writeSimJSON(w, http.StatusGatewayTimeout, map[string]any{
				"error": err.Error(),
				"state": res,
			})
			return
		}
		writeSimJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("GET /_sim/users/{id}/state", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		row, err := svc.Users.Get(r.Context(), userID)
		if err != nil {
			writeSimJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		userApp, release, err := svc.Acquire(r.Context(), row)
		if err != nil {
			writeSimJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		defer release()

		snap, err := inspectUserStateWithTracker(r.Context(), row, userApp, backend, clock.Now(), tracker)
		if err != nil {
			writeSimJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeSimJSON(w, http.StatusOK, snap)
	})

	mux.HandleFunc("POST /_sim/users/{id}/context_from", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		row, err := svc.Users.Get(r.Context(), userID)
		if err != nil {
			writeSimJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		var req struct {
			ContextFrom string `json:"context_from"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeSimJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		target := clock.Now().UTC().Truncate(time.Microsecond)
		if req.ContextFrom != "" {
			parsed, err := time.Parse(time.RFC3339Nano, req.ContextFrom)
			if err != nil {
				writeSimJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid RFC3339 context_from: " + err.Error()})
				return
			}
			target = parsed.UTC().Truncate(time.Microsecond)
		}
		client := docstore.New(backend)
		ref := client.Collection("owners").Doc(row.DataOwner()).Collection("conversation").Doc("control")
		err = client.RunTransaction(r.Context(), func(_ context.Context, tx *docstore.Transaction) error {
			var c state.ConversationControl
			doc, err := tx.Get(ref)
			if err == nil {
				if err := doc.DataTo(&c); err != nil {
					return err
				}
			} else if !errors.Is(err, docstore.ErrNotFound) {
				return err
			}
			c.ContextFrom = target
			return tx.Set(ref, c)
		})
		if err != nil {
			writeSimJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeSimJSON(w, http.StatusOK, map[string]any{
			"context_from": target.Format(time.RFC3339Nano),
		})
	})

	mux.Handle("/", baseHandler)
	return mux
}

func wakeDueForAllUsers(ctx context.Context, svc *app.Service, backend *simClockBackend, now time.Time, scopedUserID string) error {
	if scopedUserID != "" {
		if err := backend.WakeDueForUser(ctx, scopedUserID); err != nil {
			return err
		}
		row, err := svc.Users.Get(ctx, scopedUserID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) || errors.Is(err, docstore.ErrNotFound) {
				return nil
			}
			return err
		}
		userApp, release, err := svc.Acquire(ctx, row)
		if err != nil {
			return nil
		}
		defer release()
		_ = wakeDueForUser(ctx, userApp, backend, now)
		return nil
	}
	if err := backend.WakeDue(ctx); err != nil {
		return err
	}
	allUsers, err := svc.Users.List(ctx)
	if err != nil {
		return err
	}
	for _, row := range allUsers {
		userApp, release, err := svc.Acquire(ctx, row)
		if err != nil {
			continue
		}
		_ = wakeDueForUser(ctx, userApp, backend, now)
		release()
	}
	return nil
}

func wakeDueForUser(ctx context.Context, userApp *app.App, backend *simClockBackend, now time.Time) error {
	if convWake, err := userApp.NextConversationWake(ctx); err == nil && !convWake.IsZero() && !convWake.After(now) {
		if err := backend.WakeUser(ctx, userApp.UserID, docstore.LaneConversation, time.Time{}); err != nil {
			return err
		}
	}
	if schedWake := userApp.NextSchedulerWake(ctx); schedWake != nil && !schedWake.After(now) {
		if err := backend.WakeUser(ctx, userApp.UserID, docstore.LaneScheduler, time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

type drainResult struct {
	Quiescent           bool      `json:"quiescent"`
	SimNow              time.Time `json:"sim_now"`
	ConversationPending bool      `json:"conversation_pending"`
	ConversationActive  string    `json:"conversation_active"`
	OpenTasks           int       `json:"open_tasks"`
	ParkedOnInteraction int       `json:"parked_on_interaction"`
	CleanupTasks        int       `json:"cleanup_tasks"`
	PendingInteractions int       `json:"pending_interactions"`
	Rounds              int       `json:"rounds"`
}

func drainUserToQuiescence(ctx context.Context, svc *app.Service, backend *simClockBackend, clock *simClock, userID string) (drainResult, error) {
	row, err := svc.Users.Get(ctx, userID)
	if err != nil {
		return drainResult{}, fmt.Errorf("lookup user %s: %w", userID, err)
	}
	userApp, release, err := svc.Acquire(ctx, row)
	if err != nil {
		return drainResult{}, fmt.Errorf("acquire user app: %w", err)
	}
	defer release()

	_ = backend.WakeDueForUser(ctx, userID)
	_ = wakeDueForUser(ctx, userApp, backend, clock.Now())

	stableTicks := 0
	var last drainResult
	for round := 1; ; round++ {
		_ = backend.WakeDueForUser(ctx, userID)
		now := clock.Now()

		conv, activeTurn, err := userApp.Store.ConversationStatus(ctx)
		if err != nil {
			return last, err
		}
		if activeTurn != nil {
			if _, wait := state.RestartWait(conv, *activeTurn, now); wait > 0 && wait <= 5*time.Second {
				now = clock.AdvanceUntil(conv.LatestAt.Add(state.RestartQuiet + time.Millisecond))
				_ = backend.WakeDueForUser(ctx, userID)
			}
		}
		openTasks, err := userApp.Store.OpenTasks(ctx)
		if err != nil {
			return last, err
		}
		cleanupTasks, err := userApp.Store.CleanupTasks(ctx)
		if err != nil {
			return last, err
		}
		taskLeases, _ := backend.ListTaskLeases(ctx, userID)
		leaseByRun := make(map[string]docstore.TaskLease, len(taskLeases))
		parkedInteractive := 0
		activeRunnableTasks := 0
		for _, tl := range taskLeases {
			leaseByRun[tl.RunID] = tl
			if tl.Runnable {
				activeRunnableTasks++
			}
		}
		parkedIDs := make(map[string]bool)
		for _, run := range openTasks {
			tl, hasLease := leaseByRun[run.ID]
			isQuestion := (run.Question != nil && run.Question.ResolvedBy == "") ||
				(hasLease && !tl.Runnable && tl.WaitingReason == docstore.WaitQuestion)
			if isQuestion {
				parkedInteractive++
				parkedIDs[run.ID] = true
			}
		}
		nonParkedCleanup := 0
		for _, cr := range cleanupTasks {
			if !parkedIDs[cr.ID] {
				nonParkedCleanup++
			}
		}
		pendingInteractions := 0
		if userApp.Assistant != nil && userApp.Assistant.Permissions != nil {
			if list, _, pErr := userApp.Assistant.Permissions.Interactions(ctx, "pending", "", "", 50); pErr == nil {
				pendingInteractions = len(list)
			}
		}

		convDue := false
		if w, wErr := userApp.NextConversationWake(ctx); wErr == nil && !w.IsZero() && !w.After(now) {
			convDue = true
		}
		schedDue := false
		if sw := userApp.NextSchedulerWake(ctx); sw != nil && !sw.After(now) {
			schedDue = true
		}

		laneBusy := false
		convRunnable := false
		schedRunnable := false
		if wakes, wErr := backend.ListUserWakes(ctx, userID); wErr == nil {
			realNow := time.Now().UTC()
			for _, w := range wakes {
				if w.Owner != "" && (w.LeaseUntil == nil || w.LeaseUntil.After(realNow)) {
					laneBusy = true
				}
				if w.Lane == docstore.LaneConversation && w.Runnable && !w.WakeAt.After(realNow.Add(shortUserWakeThreshold)) {
					convRunnable = true
				}
				if w.Lane == docstore.LaneScheduler && w.Runnable && !w.WakeAt.After(realNow.Add(shortUserWakeThreshold)) {
					schedRunnable = true
				}
			}
		}
		if convDue && !laneBusy && !convRunnable {
			_ = backend.WakeUser(ctx, userID, docstore.LaneConversation, time.Time{})
		}
		if schedDue && !laneBusy && !schedRunnable {
			_ = backend.WakeUser(ctx, userID, docstore.LaneScheduler, time.Time{})
		}

		last = drainResult{
			SimNow:              now,
			ConversationPending: conv.Pending(),
			ConversationActive:  conv.Active,
			OpenTasks:           len(openTasks),
			ParkedOnInteraction: parkedInteractive,
			CleanupTasks:        len(cleanupTasks),
			PendingInteractions: pendingInteractions,
			Rounds:              round,
		}

		if round%40 == 0 {
			slog.InfoContext(ctx, "sim.drain.waiting",
				"round", round,
				"conv_pending", conv.Pending(),
				"conv_active", conv.Active,
				"open_tasks", len(openTasks),
				"parked", parkedInteractive,
				"runnable_tasks", activeRunnableTasks,
				"cleanup", len(cleanupTasks),
				"conv_due", convDue,
				"sched_due", schedDue,
				"lane_busy", laneBusy,
			)
		}

		nonParkedOpen := len(openTasks) - parkedInteractive
		if nonParkedOpen < 0 {
			nonParkedOpen = 0
		}
		isIdle := !conv.Pending() && conv.Active == "" &&
			activeRunnableTasks == 0 && nonParkedOpen == 0 &&
			nonParkedCleanup == 0 && !convDue && !schedDue && !laneBusy

		if isIdle {
			stableTicks++
			if stableTicks >= 2 {
				last.Quiescent = true
				return last, nil
			}
		} else {
			stableTicks = 0
		}

		select {
		case <-ctx.Done():
			return last, errors.Join(ctx.Err(), fmt.Errorf("drain timed out after %d rounds", round))
		case <-time.After(150 * time.Millisecond):
		}
	}
}
