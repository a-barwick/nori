package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/app"
	"nori/internal/billing"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/users"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type roleProcess struct {
	name    string
	cmd     *exec.Cmd
	logs    *lockedBuffer
	done    chan error
	exited  atomic.Bool
	waitErr error
	once    sync.Once
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate loopback port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startNoriRole(t *testing.T, binPath, name string, env []string, args ...string) *roleProcess {
	t.Helper()
	logs := &lockedBuffer{}
	cmd := exec.Command(binPath, args...)
	cmd.Env = env
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	rp := &roleProcess{
		name: name,
		cmd:  cmd,
		logs: logs,
		done: make(chan error, 1),
	}
	go func() {
		err := cmd.Wait()
		rp.waitErr = err
		rp.exited.Store(true)
		rp.done <- err
		close(rp.done)
	}()
	t.Cleanup(func() {
		rp.stopGraceful(t)
	})
	return rp
}

func (p *roleProcess) killSIGKILL(t *testing.T) {
	t.Helper()
	if p.exited.Load() {
		t.Fatalf("%s already exited before SIGKILL: err=%v\nlogs:\n%s", p.name, p.waitErr, p.logs.String())
	}
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL %s: %v", p.name, err)
	}
	select {
	case err := <-p.done:
		if err == nil {
			t.Fatalf("%s exited with nil error after SIGKILL", p.name)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not exit within 10s after SIGKILL", p.name)
	}
}

func (p *roleProcess) stopGraceful(t *testing.T) {
	t.Helper()
	p.once.Do(func() {
		if p.exited.Load() {
			return
		}
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
}

func waitHTTPReady(t *testing.T, proc *roleProcess, url string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(25 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if proc.exited.Load() {
			t.Fatalf("%s exited before ready (%s): err=%v\nlogs:\n%s", proc.name, url, proc.waitErr, proc.logs.String())
		}
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not become ready at %s: %v\nlogs:\n%s", proc.name, url, lastErr, proc.logs.String())
}

func httpJSON(t *testing.T, method, url string, payload any, wantStatus int, out any) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, url, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, url, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, url, resp.StatusCode, wantStatus, string(respBytes))
	}
	if out != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			t.Fatalf("decode %s %s: %v (body=%s)", method, url, err, string(respBytes))
		}
	}
}

func waitForRunStatus(t *testing.T, ctx context.Context, store state.TaskStore, runID string, want state.RunStatus, timeout time.Duration) state.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last state.Run
	for time.Now().Before(deadline) {
		run, err := store.GetRun(ctx, runID)
		if err == nil {
			last = run
			if run.Status == want {
				return run
			}
			if run.Status != state.RunRunning && run.Status != want {
				t.Fatalf("run %s reached unexpected status=%s (want %s) err=%q", runID, run.Status, want, run.Error)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for run %s status=%s (last=%s err=%q)", runID, want, last.Status, last.Error)
	return state.Run{}
}

func waitForRunQuestion(t *testing.T, ctx context.Context, store state.TaskStore, runID string, timeout time.Duration) state.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last state.Run
	for time.Now().Before(deadline) {
		run, err := store.GetRun(ctx, runID)
		if err == nil {
			last = run
			if run.Question != nil {
				return run
			}
			if run.Status != state.RunRunning {
				t.Fatalf("run %s reached terminal status=%s before question (err=%q)", runID, run.Status, run.Error)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for run %s question (last status=%s)", runID, last.Status)
	return state.Run{}
}

func waitForQuiescence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var leases, wakes int64
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM nori_task_leases WHERE user_id = $1", userID).Scan(&leases)
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM nori_user_wakes WHERE user_id = $1 AND ((runnable AND wake_at <= now()) OR owner <> '' OR lease_until IS NOT NULL)", userID).Scan(&wakes)
		if leases == 0 && wakes == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Postgres did not reach quiescence for user %s: nori_task_leases=%d active_nori_user_wakes=%d", userID, leases, wakes)
}

func expireDeadWorkerLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ownerShard, userID, runID string) {
	t.Helper()
	past := time.Now().UTC().Add(-2 * time.Second).Format("2006-01-02T15:04:05.000000000Z")

	var leaseOwner string
	var leaseUntil *time.Time
	err := pool.QueryRow(ctx, `
		SELECT owner, lease_until FROM nori_task_leases WHERE user_id = $1 AND run_id = $2
	`, userID, runID).Scan(&leaseOwner, &leaseUntil)
	if err != nil {
		t.Fatalf("inspect crashed nori_task_leases for %s: %v", runID, err)
	}
	if leaseOwner == "" || leaseUntil == nil || !leaseUntil.After(time.Now().UTC()) {
		t.Fatalf("expected active crashed task lease for %s after SIGKILL, got owner=%q lease_until=%v", runID, leaseOwner, leaseUntil)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE nori_task_leases
		SET lease_until = now() - interval '2 seconds', wake_at = now() - interval '2 seconds'
		WHERE user_id = $1 AND run_id = $2
	`, userID, runID); err != nil {
		t.Fatalf("expire nori_task_leases for %s: %v", runID, err)
	}

	ctrlCollection := ownerShard + "/task_controls"
	tag, err := pool.Exec(ctx, `
		UPDATE nori_records
		SET data = jsonb_set(data, '{lease_until}', to_jsonb($1::text), true)
		WHERE shard = $2 AND collection = $3 AND id = $4
	`, past, ownerShard, ctrlCollection, runID)
	if err != nil {
		t.Fatalf("expire TaskControl lease_until for %s: %v", runID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expected 1 TaskControl row for %s, updated %d", runID, tag.RowsAffected())
	}
}

func expireModelCheckpointRecoveryWindow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ownerShard, runID string) {
	t.Helper()
	past := time.Now().UTC().Add(-2 * time.Second).Format("2006-01-02T15:04:05.000000000Z")
	cpCollection := fmt.Sprintf("%s/runs/%s/model_checkpoints", ownerShard, runID)

	var count int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM nori_records
		WHERE shard = $1 AND collection = $2
		  AND (NOT (data ? 'outcome') OR data->>'outcome' = '' OR data->'outcome' = 'null'::jsonb)
	`, ownerShard, cpCollection).Scan(&count)
	if err != nil {
		t.Fatalf("query model_checkpoints for %s: %v", runID, err)
	}
	if count != 1 {
		t.Fatalf("expected 1 unsettled TaskModelCheckpoint for %s after mid-model SIGKILL, got %d", runID, count)
	}

	tag, err := pool.Exec(ctx, `
		UPDATE nori_records
		SET data = jsonb_set(data, '{recovery_until}', to_jsonb($1::text), true)
		WHERE shard = $2 AND collection = $3
	`, past, ownerShard, cpCollection)
	if err != nil {
		t.Fatalf("expire model_checkpoints recovery_until for %s: %v", runID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expected 1 model_checkpoints row updated for %s, got %d", runID, tag.RowsAffected())
	}
}

func verifyPendingOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ownerShard, runID string, expireRecovery bool) {
	t.Helper()
	opCollection := fmt.Sprintf("%s/runs/%s/operations", ownerShard, runID)
	var count int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM nori_records
		WHERE shard = $1 AND collection = $2
		  AND (data->>'outcome' = 'pending' OR NOT (data ? 'result_ref') OR data->>'result_ref' = '')
	`, ownerShard, opCollection).Scan(&count)
	if err != nil {
		t.Fatalf("query pending operations for %s: %v", runID, err)
	}
	if count < 1 {
		t.Fatalf("expected >=1 pending unsettled operation for %s after mid-tool SIGKILL, got %d", runID, count)
	}
	if !expireRecovery {
		return
	}
	past := time.Now().UTC().Add(-2 * time.Second).Format("2006-01-02T15:04:05.000000000Z")
	if _, err := pool.Exec(ctx, `
		UPDATE nori_records
		SET data = jsonb_set(data, '{recovery_until}', to_jsonb($1::text), true)
		WHERE shard = $2 AND collection = $3
	`, past, ownerShard, opCollection); err != nil {
		t.Fatalf("expire operations recovery_until for %s: %v", runID, err)
	}
}

func findToolBySuffix(tools []any, suffix string) string {
	for _, item := range tools {
		m, _ := item.(map[string]any)
		name, _ := m["name"].(string)
		if strings.HasSuffix(name, suffix) {
			return name
		}
	}
	return ""
}

func TestSplitRoleSIGKILLCrashRecovery(t *testing.T) {
	pgURL := strings.TrimSpace(os.Getenv("NORI_POSTGRES_CRASH_TEST_URL"))
	if pgURL == "" {
		t.Skip("set NORI_POSTGRES_CRASH_TEST_URL to an isolated Postgres DB to run split-role SIGKILL crash recovery tests")
	}
	parsedCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		t.Fatalf("invalid NORI_POSTGRES_CRASH_TEST_URL: %v", err)
	}
	if strings.EqualFold(strings.TrimSpace(parsedCfg.ConnConfig.Database), "nori") {
		t.Fatalf("refusing to run crash tests against default database %q; point NORI_POSTGRES_CRASH_TEST_URL to an isolated temporary database", parsedCfg.ConnConfig.Database)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "nori")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, "../../cmd/nori")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ../../cmd/nori failed: %v\n%s", err, string(out))
	}

	nonce := uuid.NewString()[:8]
	tagMidModel := "CRASH_MID_MODEL_" + nonce + ":"
	tagMidRead := "CRASH_MID_READ_" + nonce + ":"
	tagMidWrite := "CRASH_MID_WRITE_" + nonce + ":"
	tagAPIDispatch := "CRASH_API_DISPATCH_" + nonce + ":"

	var (
		scenario atomic.Value
		callSeq  atomic.Int64

		midModelCalls   atomic.Int64
		midModelEntered = make(chan struct{}, 2)

		armReadBlock   atomic.Bool
		taskReadCalls  atomic.Int64
		midReadEntered = make(chan struct{}, 2)

		armWriteBlock   atomic.Bool
		taskWriteCalls  atomic.Int64
		midWriteEntered = make(chan struct{}, 2)

		apiTaskCompleted atomic.Bool
		apiKillEntered   = make(chan struct{}, 2)
		apiKillRelease   = make(chan struct{})
		apiKillOnce      sync.Once
	)
	closeAPIKill := func() {
		apiKillOnce.Do(func() { close(apiKillRelease) })
	}

	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		modelName, _ := body["model"].(string)

		switch r.URL.Path {
		case "/v1/chat/completions":
			msgs, _ := body["messages"].([]any)
			rawMsgs, _ := json.Marshal(msgs)
			rawStr := string(rawMsgs)
			rf, _ := body["response_format"].(map[string]any)
			js, _ := rf["json_schema"].(map[string]any)
			schemaName, _ := js["name"].(string)

			content := "Done."
			if schemaName == "nori_response" {
				actions := []map[string]any{}
				if strings.Contains(rawStr, "Completed while API was SIGKILLed.") {
					content = "API crash recovery confirmed."
				} else if !apiTaskCompleted.Load() {
					for _, m := range msgs {
						mm, _ := m.(map[string]any)
						if mm["role"] != "user" {
							continue
						}
						txt, _ := json.Marshal(mm["content"])
						if strings.Contains(string(txt), tagAPIDispatch) {
							actions = append(actions, map[string]any{
								"name": "dispatch",
								"arguments": map[string]any{
									"request": tagAPIDispatch + " run task across API SIGKILL", "for": nil, "question_id": nil,
									"attachments": []any{}, "revises": nil,
								},
							})
							content = "Dispatching task."
							break
						}
					}
				}
				payload, _ := json.Marshal(map[string]any{"actions": actions, "text": content})
				content = string(payload)
			}
			if stream, _ := body["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream")
				chunks := []map[string]any{
					{"choices": []any{map[string]any{"delta": map[string]any{"content": content}, "finish_reason": nil}}},
					{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}},
					{"choices": []any{}, "model": modelName, "service_tier": "default", "usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 12, "total_tokens": 52}},
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
				"usage":        map[string]any{"prompt_tokens": 40, "completion_tokens": 12, "total_tokens": 52},
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": content},
					"finish_reason": "stop",
				}},
			})

		case "/v1/responses":
			items, _ := body["input"].([]any)
			tools, _ := body["tools"].([]any)
			rawInput, _ := json.Marshal(items)
			inputStr := string(rawInput)
			fnOutputs := strings.Count(inputStr, `"type":"function_call_output"`)
			active, _ := scenario.Load().(string)

			var outItem map[string]any
			callID := fmt.Sprintf("call_crash_%d", callSeq.Add(1))

			switch {
			case active == tagMidModel && strings.Contains(inputStr, tagMidModel):
				n := midModelCalls.Add(1)
				if n == 1 {
					select {
					case midModelEntered <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					return
				}
				if fnOutputs == 0 {
					outItem = map[string]any{
						"type": "function_call", "name": "memory_read", "arguments": `{}`, "call_id": callID,
					}
				} else {
					args, _ := json.Marshal(map[string]any{
						"answer":   "Recovered mid-model SIGKILL via D-053.",
						"evidence": []string{"memory_read succeeded after D-053 recovery"},
						"unmet":    []string{},
					})
					outItem = map[string]any{
						"type": "function_call", "name": "complete", "arguments": string(args), "call_id": callID,
					}
				}

			case active == tagMidRead && strings.Contains(inputStr, tagMidRead):
				switch fnOutputs {
				case 0:
					readTool := findToolBySuffix(tools, "resources_list")
					if readTool == "" {
						http.Error(w, "missing safe-read MCP resources_list tool", http.StatusUnprocessableEntity)
						return
					}
					outItem = map[string]any{
						"type": "function_call", "name": readTool, "arguments": `{}`, "call_id": callID,
					}
				case 1:
					outItem = map[string]any{
						"type": "function_call", "name": "memory_read", "arguments": `{}`, "call_id": callID,
					}
				default:
					args, _ := json.Marshal(map[string]any{
						"answer":   "Recovered mid-tool safe read SIGKILL.",
						"evidence": []string{"safe read re-executed and memory_read succeeded"},
						"unmet":    []string{},
					})
					outItem = map[string]any{
						"type": "function_call", "name": "complete", "arguments": string(args), "call_id": callID,
					}
				}

			case active == tagMidWrite && strings.Contains(inputStr, tagMidWrite):
				writeTool := findToolBySuffix(tools, "send_brief")
				if writeTool == "" {
					http.Error(w, "missing MCP send_brief tool", http.StatusUnprocessableEntity)
					return
				}
				if fnOutputs == 0 {
					args, _ := json.Marshal(map[string]any{"recipient": "ops@crash.test", "body": "crash check"})
					outItem = map[string]any{
						"type": "function_call", "name": writeTool, "arguments": string(args), "call_id": callID,
					}
				} else {
					args, _ := json.Marshal(map[string]any{
						"answer":   "Write settled.",
						"evidence": []string{},
						"unmet":    []string{},
					})
					outItem = map[string]any{
						"type": "function_call", "name": "complete", "arguments": string(args), "call_id": callID,
					}
				}

			case active == tagAPIDispatch && strings.Contains(inputStr, tagAPIDispatch):
				if fnOutputs == 0 {
					select {
					case apiKillEntered <- struct{}{}:
					default:
					}
					select {
					case <-apiKillRelease:
					case <-r.Context().Done():
						return
					}
					outItem = map[string]any{
						"type": "function_call", "name": "memory_read", "arguments": `{}`, "call_id": callID,
					}
				} else {
					apiTaskCompleted.Store(true)
					args, _ := json.Marshal(map[string]any{
						"answer":   "Completed while API was SIGKILLed.",
						"evidence": []string{"memory_read completed during API downtime"},
						"unmet":    []string{},
					})
					outItem = map[string]any{
						"type": "function_call", "name": "complete", "arguments": string(args), "call_id": callID,
					}
				}

			default:
				outItem = map[string]any{
					"type":    "message",
					"content": []any{map[string]any{"type": "output_text", "text": "Summary."}},
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":        modelName,
				"service_tier": "default",
				"status":       "completed",
				"output":       []any{outItem},
				"usage":        map[string]any{"input_tokens": 50, "output_tokens": 15, "total_tokens": 65},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		closeAPIKill()
		modelSrv.CloseClientConnections()
		modelSrv.Close()
	})

	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Allow", "POST, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var msg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reqID, hasID := msg["id"]
		if !hasID {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		method, _ := msg["method"].(string)
		params, _ := msg["params"].(map[string]any)

		replyResult := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": reqID, "result": result})
		}

		switch method {
		case "initialize":
			proto, _ := params["protocolVersion"].(string)
			if proto == "" {
				proto = "2025-06-18"
			}
			replyResult(map[string]any{
				"protocolVersion": proto,
				"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
				"serverInfo":      map[string]any{"name": "crash-stub", "version": "1.0.0"},
			})
		case "ping":
			replyResult(map[string]any{})
		case "tools/list":
			replyResult(map[string]any{
				"tools": []any{
					map[string]any{
						"name":        "send_brief",
						"title":       "Send crash test brief",
						"description": "External write tool used to verify uncertain-on-crash semantics.",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"recipient": map[string]any{"type": "string"},
								"body":      map[string]any{"type": "string"},
							},
							"required":             []string{"recipient", "body"},
							"additionalProperties": false,
						},
						"annotations": map[string]any{
							"readOnlyHint":    false,
							"destructiveHint": false,
							"idempotentHint":  false,
							"openWorldHint":   true,
						},
					},
				},
			})
		case "resources/list":
			if armReadBlock.CompareAndSwap(true, false) {
				taskReadCalls.Add(1)
				select {
				case midReadEntered <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return
			}
			if taskReadCalls.Load() > 0 {
				taskReadCalls.Add(1)
			}
			replyResult(map[string]any{
				"resources": []any{
					map[string]any{"uri": "nori://crash-test/report", "name": "Crash Test Report"},
				},
			})
		case "resources/templates/list":
			replyResult(map[string]any{"resourceTemplates": []any{}})
		case "tools/call":
			name, _ := params["name"].(string)
			if name != "send_brief" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": reqID,
					"error": map[string]any{"code": -32602, "message": "unknown tool " + name},
				})
				return
			}
			n := taskWriteCalls.Add(1)
			if n == 1 && armWriteBlock.CompareAndSwap(true, false) {
				select {
				case midWriteEntered <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return
			}
			replyResult(map[string]any{
				"content": []any{map[string]any{"type": "text", "text": `{"receipt":"rcpt_1"}`}},
				"isError": false,
			})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": reqID,
				"error": map[string]any{"code": -32601, "message": "method not found: " + method},
			})
		}
	}))
	t.Cleanup(func() {
		mcpSrv.CloseClientConnections()
		mcpSrv.Close()
	})

	backend, err := postgres.OpenBackend(ctx, pgURL)
	if err != nil {
		t.Fatalf("postgres.OpenBackend: %v", err)
	}
	defer backend.Close()

	artifactDir := filepath.Join(tmpDir, "artifacts")
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	localArtifacts, err := localfs.Open(artifactDir)
	if err != nil {
		t.Fatalf("localfs.Open: %v", err)
	}
	defer localArtifacts.Close()

	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore { return localArtifacts })
	svc := app.OpenServiceWithStorage(storage, nil, settings.Settings{
		Cloud:         "local",
		Role:          "api",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Store:         settings.Store{Backend: "postgres", PostgresURL: pgURL, LocalArtifactDir: artifactDir},
		Models: settings.Models{
			OpenAIKey: "crash-placeholder",
			Outer:     "gpt-6.1-sol",
			Inner:     "gpt-6.1-terra",
			TestURL:   modelSrv.URL,
		},
	})
	defer svc.Close()

	obsCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	obsCfg.MaxConns = 4
	obsPool, err := pgxpool.NewWithConfig(ctx, obsCfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	defer obsPool.Close()

	testUser := users.User{
		ID:          uuid.NewString(),
		Email:       fmt.Sprintf("crash-%s@example.test", uuid.NewString()[:8]),
		DisplayName: "Crash Recovery User",
		TimeZone:    "UTC",
	}
	if err := svc.Users.Create(ctx, testUser); err != nil {
		t.Fatalf("create user: %v", err)
	}
	// This fixture exercises a local model, which requires the BYOK entitlement.
	if _, err := storage.BillingStore().SetTier(ctx, testUser.ID, billing.TierBYOK, billing.SetTierOptions{}); err != nil {
		t.Fatalf("set crash fixture tier: %v", err)
	}
	ownerShard := "owners/" + testUser.DataOwner()
	userApp, releaseUser, err := svc.AcquireByID(ctx, testUser.ID)
	if err != nil {
		t.Fatalf("AcquireByID: %v", err)
	}
	defer releaseUser()

	baseEnv := func(extra ...string) []string {
		env := []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"TMPDIR=" + os.Getenv("TMPDIR"),
			"NORI_ENV_FILE=" + os.DevNull,
			"NORI_TASK_ENV_FILE=" + os.DevNull,
			"NORI_CLOUD=local",
			"NORI_STORE_BACKEND=postgres",
			"NORI_POSTGRES_URL=" + pgURL,
			"NORI_LOCAL_ARTIFACT_DIR=" + artifactDir,
			"NORI_BACKGROUND_TASKS_ENABLED=true",
			"NORI_JEV_ENABLED=false",
			"OPENAI_API_KEY=crash-test-placeholder",
			"NORI_AGENT_MODEL=gpt-6.1-sol",
			"MODEL_NAME=gpt-6.1-terra",
			"NORI_MODEL_TEST_URL=" + modelSrv.URL,
			"BIND_HOST=127.0.0.1",
		}
		return append(env, extra...)
	}

	apiPort := freeLoopbackPort(t)
	apiBase := fmt.Sprintf("http://127.0.0.1:%d", apiPort)
	apiProc := startNoriRole(t, binPath, "nori-api-1", baseEnv(fmt.Sprintf("PORT=%d", apiPort)), "--role=api")
	waitHTTPReady(t, apiProc, apiBase+"/readyz")

	mcpBase := fmt.Sprintf("%s/users/%s/v1/mcp", apiBase, testUser.ID)
	var createdMCP struct {
		Resource struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		} `json:"resource"`
	}
	httpJSON(t, http.MethodPost, mcpBase, map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"name":      "Crash Stub",
			"transport": "http",
			"endpoint":  mcpSrv.URL + "/mcp",
			"auth":      "none",
			"enabled":   true,
			"exposure":  map[string]any{"default": "direct"},
		},
	}, http.StatusCreated, &createdMCP)

	var connectedMCP struct {
		Resource struct {
			Observed struct {
				Status string `json:"status"`
			} `json:"observed"`
		} `json:"resource"`
	}
	httpJSON(t, http.MethodPost, fmt.Sprintf("%s/%s/actions", mcpBase, createdMCP.Resource.ID), map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": createdMCP.Resource.Revision,
		"value":             map[string]any{"action": "connect"},
	}, http.StatusOK, &connectedMCP)
	if connectedMCP.Resource.Observed.Status != "connected" {
		t.Fatalf("MCP stub failed to connect: status=%q", connectedMCP.Resource.Observed.Status)
	}

	var discoveredTools struct {
		Items []struct {
			Name       string `json:"name"`
			Definition struct {
				ID string `json:"id"`
			} `json:"definition"`
		} `json:"items"`
	}
	httpJSON(t, http.MethodGet, fmt.Sprintf("%s/%s/tools", mcpBase, createdMCP.Resource.ID), nil, http.StatusOK, &discoveredTools)
	var allowToolIDs []string
	for _, item := range discoveredTools.Items {
		if item.Name == "@resources_list" || item.Name == "send_brief" {
			allowToolIDs = append(allowToolIDs, item.Definition.ID)
		}
	}
	if len(allowToolIDs) != 2 {
		t.Fatalf("expected @resources_list and send_brief in MCP tools, got %+v", discoveredTools.Items)
	}
	httpJSON(t, http.MethodPost, fmt.Sprintf("%s/users/%s/v1/permissions", apiBase, testUser.ID), map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"effect":         "allow",
			"tool_ids":       allowToolIDs,
			"connection_ids": []string{},
			"resource_ids":   []string{},
			"action_classes": []string{"read", "unknown"},
			"title":          "Allow Crash Stub read and write tools",
		},
	}, http.StatusCreated, nil)

	startWorker := func(tb *testing.T, name string) *roleProcess {
		tb.Helper()
		opsPort := freeLoopbackPort(tb)
		addr := fmt.Sprintf("127.0.0.1:%d", opsPort)
		w := startNoriRole(tb, binPath, name, baseEnv(), "--role=worker", "--metrics-addr="+addr)
		waitHTTPReady(tb, w, "http://"+addr+"/readyz")
		return w
	}

	startTask := func(tb *testing.T, text string) string {
		tb.Helper()
		runID := uuid.NewString()
		cmd := state.Command{
			ID:        runID,
			RunID:     runID,
			Kind:      state.CommandStart,
			Text:      text,
			CreatedAt: time.Now().UTC(),
		}
		if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil); err != nil {
			tb.Fatalf("AcceptCommandBatch(%s): %v", runID, err)
		}
		return runID
	}

	t.Run("mid_model_sigkill_d053", func(t *testing.T) {
		scenario.Store(tagMidModel)
		worker1 := startWorker(t, "nori-worker-1")
		runID := startTask(t, tagMidModel+" verify D-053 recovery after SIGKILL")
		select {
		case <-midModelEntered:
		case <-time.After(15 * time.Second):
			t.Fatalf("worker-1 never reached model call\nlogs:\n%s", worker1.logs.String())
		}

		worker1.killSIGKILL(t)

		expireDeadWorkerLease(t, ctx, obsPool, ownerShard, testUser.ID, runID)
		expireModelCheckpointRecoveryWindow(t, ctx, obsPool, ownerShard, runID)

		worker2 := startWorker(t, "nori-worker-2")
		defer worker2.stopGraceful(t)

		run := waitForRunStatus(t, ctx, userApp.Store, runID, state.RunCompleted, 25*time.Second)
		if !strings.Contains(run.Result, "Recovered mid-model SIGKILL via D-053.") {
			t.Fatalf("unexpected mid-model recovered result: %q", run.Result)
		}
		if got := midModelCalls.Load(); got < 2 {
			t.Fatalf("expected >=2 model calls (initial + D-053 retry), got %d", got)
		}
		waitForQuiescence(t, ctx, obsPool, testUser.ID, 15*time.Second)
	})

	t.Run("mid_tool_safe_read_sigkill", func(t *testing.T) {
		scenario.Store(tagMidRead)
		worker2 := startWorker(t, "nori-worker-read-1")
		armReadBlock.Store(true)
		runID := startTask(t, tagMidRead+" verify safe read re-execution after SIGKILL")

		select {
		case <-midReadEntered:
		case <-time.After(15 * time.Second):
			t.Fatalf("worker never entered safe-read tool\nlogs:\n%s", worker2.logs.String())
		}

		worker2.killSIGKILL(t)

		expireDeadWorkerLease(t, ctx, obsPool, ownerShard, testUser.ID, runID)
		verifyPendingOperation(t, ctx, obsPool, ownerShard, runID, false)

		worker3 := startWorker(t, "nori-worker-read-2")
		defer worker3.stopGraceful(t)

		run := waitForRunStatus(t, ctx, userApp.Store, runID, state.RunCompleted, 25*time.Second)
		if !strings.Contains(run.Result, "Recovered mid-tool safe read SIGKILL.") {
			t.Fatalf("unexpected safe-read recovered result: %q", run.Result)
		}
		if got := taskReadCalls.Load(); got != 2 {
			t.Fatalf("expected safe-read tool to run twice (1 crashed + 1 re-executed), got %d", got)
		}
		waitForQuiescence(t, ctx, obsPool, testUser.ID, 15*time.Second)
	})

	t.Run("mid_tool_write_sigkill_uncertain_no_double_exec", func(t *testing.T) {
		scenario.Store(tagMidWrite)
		worker3 := startWorker(t, "nori-worker-write-1")
		armWriteBlock.Store(true)
		runID := startTask(t, tagMidWrite+" verify external write never double-executes after SIGKILL")

		select {
		case <-midWriteEntered:
		case <-time.After(15 * time.Second):
			t.Fatalf("worker never entered external write tool\nlogs:\n%s", worker3.logs.String())
		}

		worker3.killSIGKILL(t)

		expireDeadWorkerLease(t, ctx, obsPool, ownerShard, testUser.ID, runID)
		verifyPendingOperation(t, ctx, obsPool, ownerShard, runID, true)

		worker4 := startWorker(t, "nori-worker-write-2")
		defer worker4.stopGraceful(t)

		parked := waitForRunQuestion(t, ctx, userApp.Store, runID, 25*time.Second)
		if got := taskWriteCalls.Load(); got != 1 {
			t.Fatalf("external write was double-executed after SIGKILL: calls=%d want=1", got)
		}
		ops, err := userApp.Store.Operations(ctx, runID)
		if err != nil {
			t.Fatalf("Operations(%s): %v", runID, err)
		}
		foundUncertain := false
		for _, op := range ops {
			if op.Outcome == "uncertain" {
				foundUncertain = true
				break
			}
		}
		if !foundUncertain {
			t.Fatalf("expected unsettled write to be recorded as uncertain, got ops=%+v", ops)
		}
		if parked.Question == nil || len(parked.Question.Clears) == 0 {
			t.Fatalf("expected uncertain-action confirmation question with Clears, got %+v", parked.Question)
		}

		httpJSON(t, http.MethodPost, fmt.Sprintf("%s/users/%s/api/command", apiBase, testUser.ID), map[string]any{
			"id":     uuid.NewString(),
			"run_id": runID,
			"kind":   "stop",
		}, http.StatusAccepted, nil)
		waitForRunStatus(t, ctx, userApp.Store, runID, state.RunCancelled, 15*time.Second)
		waitForQuiescence(t, ctx, obsPool, testUser.ID, 15*time.Second)
	})

	t.Run("mid_task_api_sigkill", func(t *testing.T) {
		scenario.Store(tagAPIDispatch)
		worker4 := startWorker(t, "nori-worker-api-kill")
		defer worker4.stopGraceful(t)
		defer closeAPIKill()

		turnID := uuid.NewString()
		httpJSON(t, http.MethodPost, fmt.Sprintf("%s/users/%s/v1/conversations/assistant/messages", apiBase, testUser.ID), map[string]any{
			"id":   turnID,
			"text": tagAPIDispatch + " run task across API SIGKILL",
		}, http.StatusAccepted, nil)

		select {
		case <-apiKillEntered:
		case <-time.After(15 * time.Second):
			t.Fatalf("dispatched task did not enter model call\nworker logs:\n%s\napi logs:\n%s", worker4.logs.String(), apiProc.logs.String())
		}

		var dispatchedRunID string
		runs, err := userApp.Store.RecentRuns(ctx, 20)
		if err != nil {
			t.Fatalf("RecentRuns: %v", err)
		}
		for _, r := range runs {
			if r.Status == state.RunRunning && strings.Contains(r.Request, tagAPIDispatch) {
				dispatchedRunID = r.ID
				break
			}
		}
		if dispatchedRunID == "" {
			t.Fatalf("could not locate in-flight CRASH_API_DISPATCH run in %+v", runs)
		}

		apiProc.killSIGKILL(t)

		closeAPIKill()
		completed := waitForRunStatus(t, ctx, userApp.Store, dispatchedRunID, state.RunCompleted, 25*time.Second)
		if !strings.Contains(completed.Result, "Completed while API was SIGKILLed.") {
			t.Fatalf("unexpected completed result during API downtime: %q", completed.Result)
		}
		waitForQuiescence(t, ctx, obsPool, testUser.ID, 15*time.Second)

		apiPort2 := freeLoopbackPort(t)
		apiBase2 := fmt.Sprintf("http://127.0.0.1:%d", apiPort2)
		apiProc2 := startNoriRole(t, binPath, "nori-api-2", baseEnv(fmt.Sprintf("PORT=%d", apiPort2)), "--role=api")
		defer apiProc2.stopGraceful(t)
		waitHTTPReady(t, apiProc2, apiBase2+"/readyz")

		var readyBody map[string]any
		httpJSON(t, http.MethodGet, apiBase2+"/readyz", nil, http.StatusOK, &readyBody)
		if readyBody["status"] != "ok" {
			t.Fatalf("restarted API /readyz status = %v, want ok", readyBody["status"])
		}

		var snap state.Session
		httpJSON(t, http.MethodGet, fmt.Sprintf("%s/users/%s/v1/snapshot", apiBase2, testUser.ID), nil, http.StatusOK, &snap)
		foundInSnap := false
		for _, r := range snap.Runs {
			if r.ID == dispatchedRunID && r.Status == state.RunCompleted {
				foundInSnap = true
				break
			}
		}
		if !foundInSnap {
			t.Fatalf("restarted API /v1/snapshot missing completed run %s (runs=%+v)", dispatchedRunID, snap.Runs)
		}

		var detail struct {
			Item struct {
				TaskID string `json:"task_id"`
				Status string `json:"status"`
			} `json:"item"`
			Result string `json:"result"`
		}
		httpJSON(t, http.MethodGet, fmt.Sprintf("%s/users/%s/v1/work/%s", apiBase2, testUser.ID, dispatchedRunID), nil, http.StatusOK, &detail)
		if detail.Item.TaskID != dispatchedRunID || detail.Item.Status != "completed" {
			t.Fatalf("restarted API /v1/work/%s = %+v, want completed", dispatchedRunID, detail.Item)
		}
		if !strings.Contains(detail.Result, "Completed while API was SIGKILLed.") {
			t.Fatalf("restarted API /v1/work/%s result = %q", dispatchedRunID, detail.Result)
		}

		waitForQuiescence(t, ctx, obsPool, testUser.ID, 10*time.Second)
	})
}
