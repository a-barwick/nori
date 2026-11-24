package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/app"
	"nori/internal/conversation"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/users"
	"nori/internal/worker"
)

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// TestPostgresLoadConcurrencyAndWakeCancel benchmarks concurrent multi-user outer turns,
// multi-step inner tasks, and wake-driven cancellations against a real isolated Postgres DB.
// Gated behind NORI_POSTGRES_LOAD_TEST_URL so normal `go test ./...` runs skip it.
func TestPostgresLoadConcurrencyAndWakeCancel(t *testing.T) {
	pgURL := strings.TrimSpace(os.Getenv("NORI_POSTGRES_LOAD_TEST_URL"))
	if pgURL == "" {
		t.Skip("set NORI_POSTGRES_LOAD_TEST_URL to an isolated local Postgres DB to run load benchmark")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	const (
		numUsers      = 25
		completeTasks = 80
		cancelTasks   = 20
		totalTasks    = completeTasks + cancelTasks
	)

	// Track per-step timings and cancel signals in the deterministic loopback model stub.
	var (
		stepMu          sync.Mutex
		stepLatencies   []time.Duration
		callSeq         atomic.Int64
		cancelSeenMu    sync.Mutex
		cancelStartedAt = map[string]time.Time{}
		cancelReadyCh   = make(chan string, cancelTasks*2)
		cancelBlocksMu  sync.Mutex
		cancelBlocks    = map[string]chan struct{}{}
	)

	// Loopback NORI_MODEL_TEST_URL stub speaking /v1/chat/completions and /v1/responses.
	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		modelName, _ := body["model"].(string)

		switch r.URL.Path {
		case "/v1/chat/completions":
			// Fast outer-loop response: dispatches tasks for user prompts, acknowledges completion events.
			msgs, _ := body["messages"].([]any)
			rawMsgs, _ := json.Marshal(msgs)
			rawStr := string(rawMsgs)
			rf, _ := body["response_format"].(map[string]any)
			js, _ := rf["json_schema"].(map[string]any)
			schemaName, _ := js["name"].(string)

			content := "Done."
			if schemaName == "nori_response" {
				// Find user messages asking for load tasks if not a terminal notification.
				actions := []map[string]any{}
				if !strings.Contains(rawStr, "Task result:") && !strings.Contains(rawStr, "Stopped by") {
					for _, m := range msgs {
						mm, _ := m.(map[string]any)
						if mm["role"] != "user" {
							continue
						}
						txt, _ := mm["content"].(string)
						if strings.Contains(txt, "LOAD_TASK:") {
							actions = append(actions, map[string]any{
								"name": "dispatch",
								"arguments": map[string]any{
									"request": txt, "for": nil, "question_id": nil,
									"attachments": []any{}, "revises": nil,
								},
							})
						}
					}
				}
				payload, _ := json.Marshal(map[string]any{"actions": actions, "text": "On it."})
				content = string(payload)
			} else if schemaName == "nori_dispatch_verify" || strings.Contains(schemaName, "verify") || strings.Contains(schemaName, "guard") {
				// Provenance guard call if schema matches.
				payload, _ := json.Marshal(map[string]any{"actions": []any{}, "text": "ok"})
				content = string(payload)
			}
			// Simulate realistic light model latency (5ms).
			time.Sleep(5 * time.Millisecond)
			if stream, _ := body["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream")
				chunks := []map[string]any{
					{"choices": []any{map[string]any{"delta": map[string]any{"content": content}, "finish_reason": nil}}},
					{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}},
					{"choices": []any{}, "model": modelName, "service_tier": "default", "usage": map[string]any{"prompt_tokens": 50, "completion_tokens": 15, "total_tokens": 65}},
				}
				for _, ch := range chunks {
					b, _ := json.Marshal(ch)
					_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
				}
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":        modelName,
				"service_tier": "default",
				"usage":        map[string]any{"prompt_tokens": 50, "completion_tokens": 15, "total_tokens": 65},
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": content},
					"finish_reason": "stop",
				}},
			})

		case "/v1/responses":
			items, _ := body["input"].([]any)
			rawInput, _ := json.Marshal(items)
			inputStr := string(rawInput)

			// Check if this is a cancellable task: signal the harness when step 1 enters model
			// and wait until request context is cancelled (by wake-driven Stop!).
			if strings.Contains(inputStr, "CANCEL_TASK_TAG:") {
				idx := strings.Index(inputStr, "CANCEL_TASK_TAG:")
				tag := inputStr[idx : idx+len("CANCEL_TASK_TAG:")+36]
				cancelBlocksMu.Lock()
				ch, exists := cancelBlocks[tag]
				if !exists {
					ch = make(chan struct{})
					cancelBlocks[tag] = ch
					cancelReadyCh <- tag
				}
				cancelBlocksMu.Unlock()

				select {
				case <-r.Context().Done():
					return
				case <-ch:
				case <-time.After(15 * time.Second):
				}
			} else {
				// Normal task: 2-step inner loop (step 1 calls built-in memory_read, step 2 completes).
				time.Sleep(5 * time.Millisecond)
			}

			var outItem map[string]any
			id := fmt.Sprintf("call_load_%d", callSeq.Add(1))
			if strings.Contains(inputStr, `"type":"function_call_output"`) {
				args, _ := json.Marshal(map[string]any{
					"answer":   "Verified memories and completed load task.",
					"evidence": []string{"memory_read checked user memory store"},
					"unmet":    []string{},
				})
				outItem = map[string]any{
					"type":      "function_call",
					"name":      "complete",
					"arguments": string(args),
					"call_id":   id,
				}
			} else {
				outItem = map[string]any{
					"type":      "function_call",
					"name":      "memory_read",
					"arguments": `{}`,
					"call_id":   id,
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":        modelName,
				"service_tier": "default",
				"status":       "completed",
				"output":       []any{outItem},
				"usage":        map[string]any{"input_tokens": 60, "output_tokens": 20, "total_tokens": 80},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer modelSrv.Close()

	// Open 3 independent Postgres backends (1 API + 2 Worker pools) to match real split-role deployment.
	artifactDir := t.TempDir()
	localArtifacts, err := localfs.Open(artifactDir)
	if err != nil {
		t.Fatalf("localfs.Open: %v", err)
	}
	t.Cleanup(func() { _ = localArtifacts.Close() })
	openSvc := func(role string) (*app.Service, docstore.Backend) {
		b, err := postgres.OpenBackend(ctx, pgURL)
		if err != nil {
			t.Fatalf("OpenBackend(%s): %v", role, err)
		}
		t.Cleanup(func() { _ = b.Close() })
		storage := platform.NewStorage(b, func(u users.User) state.ArtifactStore {
			return localArtifacts
		})
		cfg := settings.Settings{
			Cloud:         "local",
			Role:          role,
			TaskBudgetUSD: 1.0,
			Features:      settings.Features{DisableJev: true},
			Store:         settings.Store{Backend: "postgres", PostgresURL: pgURL, LocalArtifactDir: artifactDir},
			Models: settings.Models{
				OpenAIKey: "load-placeholder",
				Outer:     "gpt-6.1-sol",
				Inner:     "gpt-6.1-terra",
				TestURL:   modelSrv.URL,
			},
		}
		svc := app.OpenServiceWithStorage(storage, nil, cfg)
		t.Cleanup(svc.Close)
		return svc, b
	}

	apiSvc, apiBackend := openSvc("api")
	w1Svc, w1Backend := openSvc("worker")
	w2Svc, w2Backend := openSvc("worker")

	// Start 2 real worker pools with LISTEN nori_wake.
	poolCtx, stopPools := context.WithCancel(ctx)
	var poolWG sync.WaitGroup
	startPool := func(name string, svc *app.Service) {
		p, err := worker.New(svc, worker.Options{
			Owner:                   name,
			TaskConcurrency:         40,
			ConversationConcurrency: 20,
			SchedulerConcurrency:    4,
		})
		if err != nil {
			t.Fatalf("worker.New(%s): %v", name, err)
		}
		poolWG.Add(1)
		go func() {
			defer poolWG.Done()
			if err := p.Run(poolCtx); err != nil {
				t.Errorf("pool %s error: %v", name, err)
			}
		}()
	}
	startPool("load-worker-1", w1Svc)
	startPool("load-worker-2", w2Svc)

	// Separate pgxpool strictly for sampling pg_stat_activity & verifying final quiescence.
	obsCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		t.Fatalf("obs ParseConfig: %v", err)
	}
	obsCfg.MaxConns = 4
	obsPool, err := pgxpool.NewWithConfig(ctx, obsCfg)
	if err != nil {
		t.Fatalf("obs pgxpool: %v", err)
	}
	defer obsPool.Close()

	// Provision 25 isolated test users.
	testUsers := make([]users.User, numUsers)
	for i := 0; i < numUsers; i++ {
		u := users.User{
			ID:          uuid.NewString(),
			Email:       fmt.Sprintf("load-%02d-%s@example.test", i, uuid.NewString()[:6]),
			DisplayName: fmt.Sprintf("Load User %d", i),
			TimeZone:    "UTC",
		}
		if err := apiSvc.Users.Create(ctx, u); err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}
		testUsers[i] = u
	}

	// Sample Postgres active & total connections + container CPU% in background during load.
	var (
		maxTotalConns  atomic.Int64
		maxActiveConns atomic.Int64
		cpuMu          sync.Mutex
		maxCPUPct      float64
		lastMemUsage   string
		stopSampler    = make(chan struct{})
		samplerDone    = make(chan struct{})
	)
	go func() {
		defer close(samplerDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		ticks := 0
		for {
			select {
			case <-stopSampler:
				return
			case <-ticker.C:
				ticks++
				var total, active int64
				err := obsPool.QueryRow(ctx, `
					SELECT count(*), count(*) FILTER (WHERE state = 'active')
					FROM pg_stat_activity
					WHERE datname = current_database() AND pid <> pg_backend_pid()
				`).Scan(&total, &active)
				if err == nil {
					for cur := maxTotalConns.Load(); total > cur && !maxTotalConns.CompareAndSwap(cur, total); cur = maxTotalConns.Load() {
					}
					for cur := maxActiveConns.Load(); active > cur && !maxActiveConns.CompareAndSwap(cur, active); cur = maxActiveConns.Load() {
					}
				}
				if ticks%5 == 1 {
					if out, dErr := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.CPUPerc}}|{{.MemUsage}}", "nori-local-postgres-1").Output(); dErr == nil {
						parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
						if len(parts) == 2 {
							if v, pErr := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(parts[0]), "%"), 64); pErr == nil {
								cpuMu.Lock()
								if v > maxCPUPct {
									maxCPUPct = v
								}
								lastMemUsage = strings.TrimSpace(parts[1])
								cpuMu.Unlock()
							}
						}
					}
				}
			}
		}
	}()

	// Snapshot baseline TxStats and scan counters before load starts.
	var baseSeqScans, baseIdxScans int64
	_ = obsPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(seq_scan), 0), COALESCE(SUM(idx_scan), 0)
		FROM pg_stat_user_tables
		WHERE relname = 'nori_records' OR relname LIKE 'nori_records_%'
	`).Scan(&baseSeqScans, &baseIdxScans)

	statsSum := func() postgres.TxStats {
		var out postgres.TxStats
		for _, b := range []docstore.Backend{apiBackend, w1Backend, w2Backend} {
			if st, ok := postgres.TxStatsOf(b); ok {
				out.Total += st.Total
				out.Attempts += st.Attempts
				out.Retries += st.Retries
				out.Exhausted += st.Exhausted
			}
		}
		return out
	}
	baseStats := statsSum()

	type taskMeta struct {
		user        users.User
		runID       string
		cancellable bool
		tag         string
		submitted   time.Time
	}
	tasks := make([]taskMeta, 0, totalTasks)
	tagToTask := make(map[string]taskMeta, cancelTasks)

	benchStart := time.Now()

	// Enqueue 80 completing tasks + 20 cancellable tasks concurrently across 25 users.
	// Also enqueue 25 concurrent outer-loop conversation turns simultaneously so both
	// outer conversation lanes and inner task pools stress Postgres at once!
	var enqueueWG sync.WaitGroup
	var tasksMu sync.Mutex
	var failedStarts atomic.Int64
	var failedCancelStarts atomic.Int64
	for i := 0; i < totalTasks; i++ {
		u := testUsers[i%numUsers]
		isCancel := i >= completeTasks
		runID := uuid.NewString()
		tag := ""
		reqText := fmt.Sprintf("LOAD_TASK: run %d for %s", i, u.DisplayName)
		if isCancel {
			tag = "CANCEL_TASK_TAG:" + runID
			reqText = fmt.Sprintf("LOAD_TASK: %s", tag)
		}
		meta := taskMeta{user: u, runID: runID, cancellable: isCancel, tag: tag}
		tasksMu.Lock()
		tasks = append(tasks, meta)
		if isCancel {
			tagToTask[tag] = meta
		}
		tasksMu.Unlock()

		enqueueWG.Add(1)
		go func(m taskMeta, text string) {
			defer enqueueWG.Done()
			userApp, release, err := apiSvc.AcquireByID(ctx, m.user.ID)
			if err != nil {
				failedStarts.Add(1)
				if m.cancellable {
					failedCancelStarts.Add(1)
				}
				t.Errorf("apiSvc.Open(%s): %v", m.user.ID, err)
				return
			}
			defer release()
			now := time.Now().UTC()
			cmd := state.Command{
				ID:        m.runID,
				RunID:     m.runID,
				Kind:      state.CommandStart,
				Text:      text,
				CreatedAt: now,
			}
			if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil); err != nil {
				failedStarts.Add(1)
				if m.cancellable {
					failedCancelStarts.Add(1)
				}
				t.Errorf("AcceptCommandBatch start %s: %v", m.runID, err)
				return
			}
			tasksMu.Lock()
			for idx := range tasks {
				if tasks[idx].runID == m.runID {
					tasks[idx].submitted = time.Now()
					break
				}
			}
			tasksMu.Unlock()
		}(meta, reqText)
	}

	// Also submit 25 concurrent outer-loop conversation messages (1 per user) to exercise
	// ConversationControl + nori_user_wakes + outer turns under the same Postgres load.
	for i := 0; i < numUsers; i++ {
		u := testUsers[i]
		enqueueWG.Add(1)
		go func(u users.User) {
			defer enqueueWG.Done()
			userApp, release, err := apiSvc.AcquireByID(ctx, u.ID)
			if err != nil {
				t.Errorf("outer AcquireByID(%s): %v", u.ID, err)
				return
			}
			defer release()
			_, err = userApp.Runner.HandleInput(ctx, conversation.Input{
				ID:      uuid.NewString(),
				Message: "Quick status check for load test.",
			})
			if err != nil {
				t.Errorf("outer Accept(%s): %v", u.ID, err)
			}
		}(u)
	}
	enqueueWG.Wait()

	// Issue wake-driven Stop commands as soon as each cancellable task enters its model call.
	cancelStopCh := make(chan struct{})
	expectedCancels := cancelTasks - int(failedCancelStarts.Load())
	var cancelDoneWG sync.WaitGroup
	cancelDoneWG.Add(1)
	go func() {
		defer cancelDoneWG.Done()
		stopped := 0
		for stopped < expectedCancels {
			select {
			case <-ctx.Done():
				return
			case <-cancelStopCh:
				return
			case tag := <-cancelReadyCh:
				m := tagToTask[tag]
				stopped++
				go func(m taskMeta) {
					userApp, release, err := apiSvc.AcquireByID(ctx, m.user.ID)
					if err != nil {
						t.Errorf("cancel Open: %v", err)
						return
					}
					defer release()
					t0 := time.Now()
					cancelSeenMu.Lock()
					cancelStartedAt[m.runID] = t0
					cancelSeenMu.Unlock()
					stopCmd := state.Command{
						ID:        uuid.NewString(),
						RunID:     m.runID,
						Kind:      state.CommandStop,
						CreatedAt: time.Now().UTC(),
					}
					if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{stopCmd}, nil); err != nil {
						t.Errorf("AcceptCommandBatch stop %s: %v", m.runID, err)
					}
				}(m)
			}
		}
	}()

	// Wait for all admitted tasks to reach terminal status and measure latencies.
	expectedTasks := totalTasks - int(failedStarts.Load())
	var (
		taskLatencies   []time.Duration
		cancelLatencies []time.Duration
		completedCount  int
		cancelledCount  int
		failedCount     int
	)
	finished := make(map[string]bool, totalTasks)
	deadline := time.Now().Add(2 * time.Minute)
	for len(finished) < expectedTasks && time.Now().Before(deadline) {
		for _, m := range tasks {
			if finished[m.runID] {
				continue
			}
			userApp, release, err := apiSvc.AcquireByID(ctx, m.user.ID)
			if err != nil {
				continue
			}
			run, err := userApp.Store.GetRun(ctx, m.runID)
			if err == nil && run.Status != state.RunRunning {
				finished[m.runID] = true
				switch run.Status {
				case state.RunCompleted:
					completedCount++
					if run.FinishedAt != nil {
						taskLatencies = append(taskLatencies, run.FinishedAt.Sub(run.StartedAt))
					}
				case state.RunCancelled:
					cancelledCount++
					cancelSeenMu.Lock()
					t0, ok := cancelStartedAt[m.runID]
					cancelSeenMu.Unlock()
					if ok {
						cancelLatencies = append(cancelLatencies, time.Since(t0))
					}
				default:
					failedCount++
					t.Errorf("task %s ended with unexpected status=%s err=%q", m.runID, run.Status, run.Error)
				}
				// Collect recorded step durations from TaskSteps.
				if steps, sErr := userApp.Store.TaskSteps(ctx, m.runID); sErr == nil {
					stepMu.Lock()
					for _, st := range steps {
						if st.DurationMS > 0 {
							stepLatencies = append(stepLatencies, time.Duration(st.DurationMS)*time.Millisecond)
						}
					}
					stepMu.Unlock()
				}
			}
			release()
		}
		if len(finished) < expectedTasks {
			time.Sleep(25 * time.Millisecond)
		}
	}
	benchElapsed := time.Since(benchStart)
	close(cancelStopCh)
	cancelDoneWG.Wait()

	// Wait briefly for all outer notification turns and wakes to drain to full quiescence.
	var leaseRows, activeWakeRows int64
	qDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(qDeadline) {
		_ = obsPool.QueryRow(ctx, "SELECT count(*) FROM nori_task_leases").Scan(&leaseRows)
		_ = obsPool.QueryRow(ctx, "SELECT count(*) FROM nori_user_wakes WHERE runnable = true OR owner <> '' OR lease_until IS NOT NULL").Scan(&activeWakeRows)
		if leaseRows == 0 && activeWakeRows == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	close(stopSampler)
	<-samplerDone
	stopPools()
	poolWG.Wait()

	endStats := statsSum()
	deltaTotal := endStats.Total - baseStats.Total
	deltaAttempts := endStats.Attempts - baseStats.Attempts
	deltaRetries := endStats.Retries - baseStats.Retries
	deltaExhausted := endStats.Exhausted - baseStats.Exhausted
	retryRate := 0.0
	if deltaAttempts > 0 {
		retryRate = float64(deltaRetries) / float64(deltaAttempts) * 100.0
	}

	slices.Sort(stepLatencies)
	slices.Sort(taskLatencies)
	slices.Sort(cancelLatencies)

	t.Logf("=== WAVE 3 REAL POSTGRES LOAD BENCHMARK RESULTS ===")
	t.Logf("Workload: %d users, %d total tasks (%d completed, %d wake-cancelled, %d failed) + %d concurrent outer turns",
		numUsers, totalTasks, completedCount, cancelledCount, failedCount, numUsers)
	t.Logf("Wall time: %v | Task throughput: %.2f tasks/s | Serializable Tx throughput: %.2f tx/s",
		benchElapsed.Round(time.Millisecond), float64(len(finished))/benchElapsed.Seconds(), float64(deltaTotal)/benchElapsed.Seconds())
	t.Logf("Step latency (n=%d): p50=%v p95=%v p99=%v",
		len(stepLatencies), percentile(stepLatencies, 50), percentile(stepLatencies, 95), percentile(stepLatencies, 99))
	t.Logf("Task end-to-end latency (n=%d): p50=%v p95=%v p99=%v",
		len(taskLatencies), percentile(taskLatencies, 50), percentile(taskLatencies, 95), percentile(taskLatencies, 99))
	t.Logf("Wake-driven cancel latency (n=%d): p50=%v p95=%v p99=%v",
		len(cancelLatencies), percentile(cancelLatencies, 50), percentile(cancelLatencies, 95), percentile(cancelLatencies, 99))
	t.Logf("SERIALIZABLE (40001) stats: tx=%d attempts=%d retries=%d (%.2f%%) exhausted=%d",
		deltaTotal, deltaAttempts, deltaRetries, retryRate, deltaExhausted)
	t.Logf("Postgres peak connections: total=%d active=%d | Peak container CPU: %.2f%% | Mem: %s",
		maxTotalConns.Load(), maxActiveConns.Load(), maxCPUPct, lastMemUsage)
	t.Logf("Final quiescence: nori_task_leases=%d active_nori_user_wakes=%d", leaseRows, activeWakeRows)
	var seqScans, idxScans int64
	if err := obsPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(seq_scan), 0), COALESCE(SUM(idx_scan), 0)
		FROM pg_stat_user_tables
		WHERE relname = 'nori_records' OR relname LIKE 'nori_records_%'
	`).Scan(&seqScans, &idxScans); err == nil {
		t.Logf("nori_records delta scans: seq_scan=%d idx_scan=%d (total seq=%d idx=%d)", seqScans-baseSeqScans, idxScans-baseIdxScans, seqScans, idxScans)
	}
	if rows, err := obsPool.Query(ctx, `
		SELECT COALESCE(parent.relname, s.indexrelname) AS idx_name,
		       SUM(s.idx_scan)::bigint,
		       SUM(s.idx_tup_read)::bigint
		FROM pg_stat_user_indexes s
		LEFT JOIN pg_inherits inh ON inh.inhrelid = s.indexrelid
		LEFT JOIN pg_class parent ON parent.oid = inh.inhparent
		WHERE s.relname = 'nori_records' OR s.relname LIKE 'nori_records_%'
		GROUP BY 1
		HAVING SUM(s.idx_scan) > 0
		ORDER BY SUM(s.idx_scan) DESC
	`); err == nil {
		var parts []string
		for rows.Next() {
			var name string
			var scans, reads int64
			if rows.Scan(&name, &scans, &reads) == nil {
				parts = append(parts, fmt.Sprintf("%s=%d(%d)", name, scans, reads))
			}
		}
		rows.Close()
		t.Logf("nori_records index scans: %s", strings.Join(parts, ", "))
	}

	if completedCount != completeTasks || cancelledCount != cancelTasks || failedCount != 0 {
		t.Fatalf("unexpected outcomes: completed=%d/%d cancelled=%d/%d failed=%d",
			completedCount, completeTasks, cancelledCount, cancelTasks, failedCount)
	}
	if deltaExhausted != 0 {
		t.Fatalf("exhausted serializable transactions: %d", deltaExhausted)
	}
	if leaseRows != 0 || activeWakeRows != 0 {
		t.Fatalf("leaked rows after quiescence: nori_task_leases=%d active_nori_user_wakes=%d", leaseRows, activeWakeRows)
	}
}
