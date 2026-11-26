package inner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/sim/env"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

var (
	ErrInjectedModelFault = errors.New("sim/inner: injected inner model provider error")
)

type ModelConfig struct {
	CostPerCall            float64
	ProviderErrProb        float64
	OverflowErrProb        float64
	UnknownCostProb        float64
	MaxStepsBeforeComplete int
}

func DefaultModelConfig() ModelConfig {
	return ModelConfig{
		CostPerCall:            0.15,
		ProviderErrProb:        0.04,
		OverflowErrProb:        0.03,
		UnknownCostProb:        0.05,
		MaxStepsBeforeComplete: 3,
	}
}

type ModelCallRecord struct {
	UserID       string
	RunID        string
	Revision     int64
	SpentBefore  float64
	OthersBefore float64
	Budget       float64
	Cost         float64
	CostKnown    bool
	ToolEmitted  string
	Err          string
	At           time.Time
}

type SpendSnapshot struct {
	Revision     int64
	SpentBefore  float64
	OthersBefore float64
	Budget       float64
}

type ModelScriptFunc func(ctx context.Context, runID string, req agent.Request) (resp agent.Response, err error, handled bool)

type FakeInnerModel struct {
	mu            sync.Mutex
	clock         *env.Clock
	rng           *rand.Rand
	cfg           ModelConfig
	faultsEnabled bool
	readSpend     func(ctx context.Context, userID, runID string) (SpendSnapshot, bool)
	script        ModelScriptFunc

	calls       []ModelCallRecord
	nextCallSeq int64
	forcedErrs  []error
}

var _ agent.Model = (*FakeInnerModel)(nil)

func NewFakeInnerModel(
	clock *env.Clock,
	cfg ModelConfig,
	rng *rand.Rand,
	readSpend func(ctx context.Context, userID, runID string) (SpendSnapshot, bool),
) *FakeInnerModel {
	if clock == nil {
		clock = env.NewClock(time.Time{})
	}
	if rng == nil {
		rng = rand.New(rand.NewPCG(2, 2))
	}
	if cfg.CostPerCall <= 0 {
		cfg.CostPerCall = 0.15
	}
	if cfg.MaxStepsBeforeComplete <= 0 {
		cfg.MaxStepsBeforeComplete = 3
	}
	return &FakeInnerModel{
		clock:         clock,
		rng:           rng,
		cfg:           cfg,
		faultsEnabled: true,
		readSpend:     readSpend,
	}
}

func (m *FakeInnerModel) Provider() string { return "sim" }

func (m *FakeInnerModel) Name() string { return "sim-inner-v1" }

func (m *FakeInnerModel) SetFaultsEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.faultsEnabled = enabled
}

func (m *FakeInnerModel) FaultsEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.faultsEnabled
}

func (m *FakeInnerModel) SetConfig(cfg ModelConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.CostPerCall <= 0 {
		cfg.CostPerCall = 0.15
	}
	if cfg.MaxStepsBeforeComplete <= 0 {
		cfg.MaxStepsBeforeComplete = 3
	}
	m.cfg = cfg
}

func (m *FakeInnerModel) SetScript(fn ModelScriptFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.script = fn
}

func (m *FakeInnerModel) QueueForcedError(errs ...error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forcedErrs = append(m.forcedErrs, errs...)
}

func (m *FakeInnerModel) ModelCalls() []ModelCallRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ModelCallRecord, len(m.calls))
	copy(out, m.calls)
	return out
}

func (m *FakeInnerModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{NotSent: true}, err
	}

	userID := simUserID(ctx)
	runID := usage.Attempt(ctx, "", "").InputID
	var spend SnapSpend
	if m.readSpend != nil && runID != "" {
		if snap, ok := m.readSpend(ctx, userID, runID); ok {
			spend.snap = snap
			spend.ok = true
		}
	}

	m.mu.Lock()
	now := m.clock.Now()
	cost := m.cfg.CostPerCall
	script := m.script

	if len(m.forcedErrs) > 0 {
		err := m.forcedErrs[0]
		m.forcedErrs = m.forcedErrs[1:]
		resp := agent.Response{Cost: cost, CostKnown: true}
		if errors.Is(err, contextview.ErrOverflow) {
			resp = agent.Response{NotSent: true, Cost: 0, CostKnown: true}
		}
		m.recordCallLocked(userID, runID, spend, resp, "", err, now)
		m.mu.Unlock()
		return resp, err
	}

	if m.faultsEnabled {
		roll := m.rng.Float64()
		if roll < m.cfg.OverflowErrProb {
			resp := agent.Response{NotSent: true, Cost: 0, CostKnown: true}
			m.recordCallLocked(userID, runID, spend, resp, "", contextview.ErrOverflow, now)
			m.mu.Unlock()
			return resp, contextview.ErrOverflow
		}
		roll -= m.cfg.OverflowErrProb
		if roll < m.cfg.ProviderErrProb {
			resp := agent.Response{Cost: cost, CostKnown: true}
			m.recordCallLocked(userID, runID, spend, resp, "", ErrInjectedModelFault, now)
			m.mu.Unlock()
			return resp, ErrInjectedModelFault
		}
	}
	m.mu.Unlock()

	if script != nil {
		if resp, err, handled := script(ctx, runID, req); handled {
			m.mu.Lock()
			toolName := ""
			if len(resp.Calls) > 0 {
				toolName = resp.Calls[0].Name
			}
			m.recordCallLocked(userID, runID, spend, resp, toolName, err, now)
			m.mu.Unlock()
			return resp, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	call := m.decideCallLocked(req)
	msg := transcript.Message{
		Role: transcript.RoleModel,
		Parts: []transcript.Part{
			{ToolCall: &call},
		},
	}
	resp := agent.Response{
		Message: &msg,
		Calls:   []transcript.ToolCall{call},
		Usage: &agent.Usage{
			InputTokens:  120,
			OutputTokens: 48,
			TotalTokens:  168,
		},
		Cost:      cost,
		CostKnown: true,
	}
	if m.faultsEnabled && m.cfg.UnknownCostProb > 0 && m.rng.Float64() < m.cfg.UnknownCostProb {
		resp.Cost = 0
		resp.CostKnown = false
		resp.NotSent = false
	}

	m.recordCallLocked(userID, runID, spend, resp, call.Name, nil, now)
	return resp, nil
}

func (m *FakeInnerModel) Inspect(ctx context.Context, _ agent.InspectRequest) (agent.Inspection, error) {
	if err := ctx.Err(); err != nil {
		return agent.Inspection{}, err
	}
	return agent.Inspection{
		Observation: "simulated image inspection",
		Sent:        true,
		Cost:        0.01,
		CostKnown:   true,
	}, nil
}

type SnapSpend struct {
	snap SpendSnapshot
	ok   bool
}

func (m *FakeInnerModel) recordCallLocked(userID, runID string, spend SnapSpend, resp agent.Response, toolName string, err error, now time.Time) {
	rec := ModelCallRecord{
		UserID:      userID,
		RunID:       runID,
		Cost:        resp.Cost,
		CostKnown:   resp.CostKnown,
		ToolEmitted: toolName,
		At:          now,
	}
	if spend.ok {
		rec.Revision = spend.snap.Revision
		rec.SpentBefore = spend.snap.SpentBefore
		rec.OthersBefore = spend.snap.OthersBefore
		rec.Budget = spend.snap.Budget
	}
	if err != nil {
		rec.Err = err.Error()
	}
	m.calls = append(m.calls, rec)
}

func (m *FakeInnerModel) nextCallIDLocked() string {
	m.nextCallSeq++
	return fmt.Sprintf("sim-call-%d", m.nextCallSeq)
}

func (m *FakeInnerModel) decideCallLocked(req agent.Request) transcript.ToolCall {
	msgs := req.Messages
	if len(msgs) > 0 && isClockMessage(msgs[len(msgs)-1]) {
		msgs = msgs[:len(msgs)-1]
	}

	offered := make(map[string]bool, len(req.Tools))
	for _, t := range req.Tools {
		offered[t.Name] = true
	}

	var (
		modelTurns         int
		evidence           []any
		unmet              []any
		seenRequestWork    bool
		seenRequestInput   bool
		seenTool           = make(map[string]bool)
		lastApprovalCall   *transcript.ToolCall
		callsByID          = make(map[string]transcript.ToolCall)
		lastUserText       string
		lastToolResultName string
		lastToolOutput     map[string]any
		omittedOpIDs       []string
		savedOpIDs         []string
		seenMessageIDs     []string
		seenLogTargets     []struct{ date, entryID string }
		archiveHistoryRef  string
		archiveBefore      string
	)

	for _, msg := range msgs {
		if msg.Role == transcript.RoleModel {
			modelTurns++
		}
		for _, part := range msg.Parts {
			if part.ToolCall != nil {
				c := *part.ToolCall
				callsByID[c.ID] = c
				seenTool[c.Name] = true
				if c.Name == "request_work" {
					seenRequestWork = true
				}
				if c.Name == "request_input" {
					seenRequestInput = true
				}
			}
			if part.ToolResult != nil {
				lastToolResultName = part.ToolResult.Name
				lastToolOutput = part.ToolResult.Output
				lastUserText = ""
				outcome, _ := part.ToolResult.Output["outcome"].(string)
				opID, _ := part.ToolResult.Output["operation_id"].(string)
				if opID != "" {
					if omitted, _ := part.ToolResult.Output["omitted"].(bool); omitted {
						omittedOpIDs = append(omittedOpIDs, opID)
					} else {
						savedOpIDs = append(savedOpIDs, opID)
					}
				}
				if outcome == "confirmed" && opID != "" {
					evidence = append(evidence, opID)
				}
				if outcome == "denied" {
					unmet = append(unmet, fmt.Sprintf("%s was denied", part.ToolResult.Name))
				}
				if outcome == "needs_interaction" {
					if kind, _ := part.ToolResult.Output["interaction_kind"].(string); kind == "approval" {
						if prior, ok := callsByID[part.ToolResult.CallID]; ok {
							cp := prior
							cp.Args = cloneMap(prior.Args)
							lastApprovalCall = &cp
						}
					}
				}
				switch part.ToolResult.Name {
				case "conversation_find":
					if msgsOut, ok := part.ToolResult.Output["messages"].([]any); ok {
						for _, raw := range msgsOut {
							if row, ok := raw.(map[string]any); ok {
								if mid, _ := row["message_id"].(string); state.ValidEvidenceID(mid) {
									seenMessageIDs = append(seenMessageIDs, mid)
								}
							}
						}
					}
				case "memory_read":
					if summary, ok := part.ToolResult.Output["conversation_summary"].(map[string]any); ok {
						if items, ok := summary["items"].([]any); ok {
							for _, raw := range items {
								if row, ok := raw.(map[string]any); ok {
									if iid, _ := row["input_id"].(string); state.ValidEvidenceID(iid) {
										seenMessageIDs = append(seenMessageIDs, iid)
									}
								}
							}
						}
					}
				case "memory_search":
					if results, ok := part.ToolResult.Output["results"].([]any); ok {
						for _, raw := range results {
							if row, ok := raw.(map[string]any); ok {
								if d, _ := row["log_date"].(string); state.ValidMemoryDate(d) {
									eid, _ := row["entry_id"].(string)
									seenLogTargets = append(seenLogTargets, struct{ date, entryID string }{date: d, entryID: eid})
								}
							}
						}
					}
				}
			}
			if msg.Role == transcript.RoleUser && part.Text != "" {
				lastUserText = part.Text
				lastToolResultName = ""
				lastToolOutput = nil
				if ref, before, ops := parseProjectedArchiveNotice(part.Text); ref != "" && before != "" {
					archiveHistoryRef = ref
					archiveBefore = before
					savedOpIDs = append(savedOpIDs, ops...)
				}
			}
		}
	}

	if strings.HasPrefix(lastUserText, "The user approved this exact action; retry the same call unchanged:") && lastApprovalCall != nil {
		return transcript.ToolCall{
			ID:   m.nextCallIDLocked(),
			Name: lastApprovalCall.Name,
			Args: cloneMap(lastApprovalCall.Args),
		}
	}

	if strings.HasPrefix(lastUserText, "The user declined this action.") {
		unmet = append(unmet, "The user declined the requested action")
		return m.completeCallLocked("Stopped without executing the declined action.", evidence, unmet)
	}

	if lastToolResultName == "request_work" && lastToolOutput != nil {
		outcome, _ := lastToolOutput["outcome"].(string)
		if outcome != "confirmed" {
			unmet = append(unmet, "Delegated subtask did not fully succeed")
		}
		return m.completeCallLocked("Completed task incorporating subtask results.", evidence, unmet)
	}

	if modelTurns >= m.cfg.MaxStepsBeforeComplete {
		return m.completeCallLocked("Completed simulated task steps.", evidence, unmet)
	}

	type candidate struct {
		weight int
		build  func() transcript.ToolCall
	}
	var candidates []candidate

	if offered[ToolNameRead] {
		candidates = append(candidates, candidate{
			weight: 30,
			build: func() transcript.ToolCall {
				key := fmt.Sprintf("key-%d", m.rng.IntN(4)+1)
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: ToolNameRead,
					Args: map[string]any{"key": key},
				}
			},
		})
	}
	if offered[ToolNameWrite] {
		candidates = append(candidates, candidate{
			weight: 28,
			build: func() transcript.ToolCall {
				target := fmt.Sprintf("key-%d", m.rng.IntN(4)+1)
				val := fmt.Sprintf("val-%d", m.rng.IntN(100))
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: ToolNameWrite,
					Args: map[string]any{
						"target":      target,
						"value":       val,
						"resource_id": target,
					},
				}
			},
		})
	}
	if offered[ToolNameSend] {
		candidates = append(candidates, candidate{
			weight: 16,
			build: func() transcript.ToolCall {
				recipients := []string{"alice@example.com", "bob@example.com", "ops@example.com"}
				to := recipients[m.rng.IntN(len(recipients))]
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: ToolNameSend,
					Args: map[string]any{
						"to":   to,
						"body": fmt.Sprintf("sim notice #%d", m.rng.IntN(100)),
					},
				}
			},
		})
	}
	if offered["result_link"] {
		candidates = append(candidates, candidate{
			weight: 10,
			build: func() transcript.ToolCall {
				n := m.rng.IntN(100) + 1
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "result_link",
					Args: map[string]any{
						"url":   fmt.Sprintf("https://example.com/sim/result/%d", n),
						"title": fmt.Sprintf("Sim Result %d", n),
					},
				}
			},
		})
	}
	if offered["request_input"] && !seenRequestInput && modelTurns > 0 {
		candidates = append(candidates, candidate{
			weight: 10,
			build: func() transcript.ToolCall {
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "request_input",
					Args: map[string]any{
						"question": fmt.Sprintf("Confirm detail #%d before continuing?", m.rng.IntN(50)+1),
					},
				}
			},
		})
	}
	if offered["request_work"] && !seenRequestWork {
		candidates = append(candidates, candidate{
			weight: 14,
			build: func() transcript.ToolCall {
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "request_work",
					Args: map[string]any{
						"request": fmt.Sprintf("Gather simulated subtask data #%d", m.rng.IntN(50)+1),
						"why":     "Need delegated work before completing parent task",
					},
				}
			},
		})
	}
	if offered["memory_read"] && !seenTool["memory_read"] {
		candidates = append(candidates, candidate{
			weight: 5,
			build: func() transcript.ToolCall {
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "memory_read",
					Args: map[string]any{},
				}
			},
		})
	}
	if offered["memory_search"] && !seenTool["memory_search"] {
		candidates = append(candidates, candidate{
			weight: 6,
			build: func() transcript.ToolCall {
				queries := []string{"preference", "project", "request", "sim", "task"}
				layers := []string{"", "core", "log"}
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "memory_search",
					Args: map[string]any{
						"query": queries[m.rng.IntN(len(queries))],
						"layer": layers[m.rng.IntN(len(layers))],
						"limit": "5",
					},
				}
			},
		})
	}
	if offered["memory_get"] && !seenTool["memory_get"] {
		candidates = append(candidates, candidate{
			weight: 5,
			build: func() transcript.ToolCall {
				if len(seenLogTargets) > 0 {
					tgt := seenLogTargets[m.rng.IntN(len(seenLogTargets))]
					args := map[string]any{"target": "log", "date": tgt.date}
					if tgt.entryID != "" {
						args["entry_id"] = tgt.entryID
					}
					return transcript.ToolCall{
						ID:   m.nextCallIDLocked(),
						Name: "memory_get",
						Args: args,
					}
				}
				if m.rng.IntN(2) == 0 {
					return transcript.ToolCall{
						ID:   m.nextCallIDLocked(),
						Name: "memory_get",
						Args: map[string]any{"target": "core"},
					}
				}
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "memory_get",
					Args: map[string]any{
						"target": "log",
						"date":   m.clock.Now().UTC().Format("2006-01-02"),
					},
				}
			},
		})
	}
	if offered["conversation_find"] && !seenTool["conversation_find"] {
		candidates = append(candidates, candidate{
			weight: 4,
			build: func() transcript.ToolCall {
				queries := []string{"request", "e2e", "sim", "user"}
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "conversation_find",
					Args: map[string]any{
						"query": queries[m.rng.IntN(len(queries))],
					},
				}
			},
		})
	}
	if offered["conversation_read"] && !seenTool["conversation_read"] && len(seenMessageIDs) > 0 {
		candidates = append(candidates, candidate{
			weight: 8,
			build: func() transcript.ToolCall {
				mid := seenMessageIDs[m.rng.IntN(len(seenMessageIDs))]
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "conversation_read",
					Args: map[string]any{"message_id": mid},
				}
			},
		})
	}
	if offered["operation_read"] && !seenTool["operation_read"] && (len(omittedOpIDs) > 0 || len(savedOpIDs) > 0) {
		pool := omittedOpIDs
		weight := 10
		if len(pool) == 0 {
			pool = savedOpIDs
			weight = 4
		}
		candidates = append(candidates, candidate{
			weight: weight,
			build: func() transcript.ToolCall {
				opID := pool[m.rng.IntN(len(pool))]
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "operation_read",
					Args: map[string]any{"operation_id": opID},
				}
			},
		})
	}
	if offered["task_history_read"] && !seenTool["task_history_read"] && archiveHistoryRef != "" && archiveBefore != "" {
		candidates = append(candidates, candidate{
			weight: 10,
			build: func() transcript.ToolCall {
				return transcript.ToolCall{
					ID:   m.nextCallIDLocked(),
					Name: "task_history_read",
					Args: map[string]any{
						"history_ref": archiveHistoryRef,
						"before":      archiveBefore,
						"offset":      "0",
					},
				}
			},
		})
	}
	if modelTurns > 0 {
		candidates = append(candidates, candidate{
			weight: 22,
			build: func() transcript.ToolCall {
				return m.completeCallLocked("Finished simulated task.", evidence, unmet)
			},
		})
	}

	if len(candidates) == 0 {
		return m.completeCallLocked("Finished simulated task.", evidence, unmet)
	}
	totalWeight := 0
	for _, c := range candidates {
		totalWeight += c.weight
	}
	pick := m.rng.IntN(totalWeight)
	for _, c := range candidates {
		if pick < c.weight {
			return c.build()
		}
		pick -= c.weight
	}
	return candidates[len(candidates)-1].build()
}

func (m *FakeInnerModel) completeCallLocked(answer string, evidence, unmet []any) transcript.ToolCall {
	if evidence == nil {
		evidence = []any{}
	}
	if unmet == nil {
		unmet = []any{}
	}
	return transcript.ToolCall{
		ID:   m.nextCallIDLocked(),
		Name: "complete",
		Args: map[string]any{
			"answer":   answer,
			"evidence": evidence,
			"unmet":    unmet,
		},
	}
}

func isClockMessage(msg transcript.Message) bool {
	if msg.Role != transcript.RoleUser || len(msg.Parts) != 1 {
		return false
	}
	text := msg.Parts[0].Text
	return strings.HasPrefix(text, "Runtime note, not from the user: it is now ")
}

func parseProjectedArchiveNotice(text string) (historyRef, before string, opIDs []string) {
	idx := strings.Index(text, "{\"")
	if idx < 0 {
		return "", "", nil
	}
	var notice struct {
		HistoryRef       string `json:"history_ref"`
		Before           string `json:"before"`
		RecentOperations []struct {
			OperationID string `json:"operation_id"`
		} `json:"recent_operations"`
	}
	if err := json.Unmarshal([]byte(text[idx:]), &notice); err != nil {
		return "", "", nil
	}
	if !state.ValidHistoryRef(notice.HistoryRef) || notice.Before == "" {
		return "", "", nil
	}
	for _, op := range notice.RecentOperations {
		if op.OperationID != "" {
			opIDs = append(opIDs, op.OperationID)
		}
	}
	return notice.HistoryRef, notice.Before, opIDs
}

func (m *FakeInnerModel) Digest() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	h := sha256.New()
	writeHashString(h, "sim.inner.model.v1")
	writeHashUint64(h, uint64(len(m.calls)))
	for _, c := range m.calls {
		writeHashString(h, c.UserID)
		writeHashString(h, c.RunID)
		writeHashInt64(h, c.Revision)
		writeHashUint64(h, math.Float64bits(c.SpentBefore))
		writeHashUint64(h, math.Float64bits(c.OthersBefore))
		writeHashUint64(h, math.Float64bits(c.Budget))
		writeHashUint64(h, math.Float64bits(c.Cost))
		writeHashBool(h, c.CostKnown)
		writeHashString(h, c.ToolEmitted)
		writeHashString(h, c.Err)
		writeHashInt64(h, c.At.UTC().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}
