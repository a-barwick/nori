// Package gateway implements a local loopback LLM gateway backed by llmguard.
// It holds upstream provider API keys in a single process, authenticates local
// callers (Go nori-sim and Python persona-sim responder/judge/preflight) with
// a short-lived bearer token, and enforces a unified real-time --max-usd and
// --max-calls kill switch across all callers.
package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"nori/internal/llmguard"
	"nori/internal/usage"
)

const (
	defaultOpenAIBaseURL     = "https://api.openai.com/v1"
	defaultAnthropicBaseURL  = "https://api.anthropic.com/v1"
	defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	anthropicAPIVersion      = "2023-06-01"
	maxRequestBytes          = 8 << 20
	maxResponseBytes         = 8 << 20
)

// Config configures the local loopback LLM gateway.
type Config struct {
	Token             string
	OpenAIKey         string
	AnthropicKey      string
	OpenRouterKey     string
	OpenAIBaseURL     string
	AnthropicBaseURL  string
	OpenRouterBaseURL string
	MaxUSD            float64
	MaxCalls          int
	Guard             *llmguard.Guard
	Transport         http.RoundTripper
}

type ModelStats struct {
	Provider     string  `json:"provider"`
	Calls        int64   `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type StatsSnapshot struct {
	TotalAdmitted        int64                 `json:"total_admitted"`
	TotalBlocked         int64                 `json:"total_blocked"`
	BlockedBudget        int64                 `json:"blocked_budget"`
	BlockedCallLimit     int64                 `json:"blocked_call_limit"`
	UnknownAborts        int64                 `json:"unknown_aborts"`
	RecordedActualUSD    float64               `json:"recorded_actual_usd"`
	EstimatedUnknownUSD  float64               `json:"estimated_unknown_usd"`
	InFlightEstimatedUSD float64               `json:"in_flight_estimated_usd"`
	TotalExposureUSD     float64               `json:"total_exposure_usd"`
	MaxUSD               float64               `json:"max_usd"`
	MaxCalls             int                   `json:"max_calls"`
	BudgetExceeded       bool                  `json:"budget_exceeded"`
	ByModel              map[string]ModelStats `json:"by_model"`
}

// Server proxies OpenAI/OpenRouter/Anthropic LLM requests through a single llmguard.Guard.
type Server struct {
	cfg   Config
	guard *llmguard.Guard

	mu      sync.Mutex
	byModel map[string]ModelStats
}

func New(cfg Config) (*Server, error) {
	cfg.Token = strings.TrimSpace(cfg.Token)
	if cfg.Token == "" {
		return nil, errors.New("gateway token must be non-empty")
	}
	if cfg.OpenAIBaseURL == "" {
		cfg.OpenAIBaseURL = defaultOpenAIBaseURL
	}
	if cfg.AnthropicBaseURL == "" {
		cfg.AnthropicBaseURL = defaultAnthropicBaseURL
	}
	if cfg.OpenRouterBaseURL == "" {
		cfg.OpenRouterBaseURL = defaultOpenRouterBaseURL
	}
	g := cfg.Guard
	if g == nil {
		gc := llmguard.DefaultConfig()
		gc.GlobalMaxPerMinute = 600
		gc.GlobalMaxInFlight = 64
		gc.MaxEstimatedUSD = cfg.MaxUSD
		gc.MaxTotalCalls = cfg.MaxCalls
		gc.Scopes = map[string]llmguard.ScopePolicy{
			llmguard.ScopeOuter:      {MaxPerMinute: 300, MaxPerHour: 6000, MaxInFlight: 32},
			llmguard.ScopeTask:       {MaxPerMinute: 300, MaxPerHour: 6000, MaxInFlight: 32},
			llmguard.ScopeCompaction: {MaxPerMinute: 120, MaxPerHour: 2400, MaxInFlight: 16},
			llmguard.ScopeExtract:    {MaxPerMinute: 120, MaxPerHour: 2400, MaxInFlight: 16},
			llmguard.ScopeDream:      {MaxPerMinute: 60, MaxPerHour: 1200, MaxInFlight: 8},
			llmguard.ScopeClassify:   {MaxPerMinute: 300, MaxPerHour: 6000, MaxInFlight: 32},
			llmguard.ScopeReview:     {MaxPerMinute: 300, MaxPerHour: 6000, MaxInFlight: 32},
		}
		g = llmguard.New(gc)
	}
	return &Server{
		cfg:     cfg,
		guard:   g,
		byModel: map[string]ModelStats{},
	}, nil
}

func (s *Server) Stats() StatsSnapshot {
	gs := s.guard.Stats()
	exposure := gs.RecordedActualUSD + gs.EstimatedUnknownUSD + gs.InFlightEstimatedUSD
	confirmedExposure := gs.RecordedActualUSD + gs.EstimatedUnknownUSD
	exceeded := gs.BlockedBudget > 0 || gs.BlockedCallLimit > 0 ||
		(s.cfg.MaxUSD > 0 && confirmedExposure > s.cfg.MaxUSD)

	s.mu.Lock()
	byModel := make(map[string]ModelStats, len(s.byModel))
	for k, v := range s.byModel {
		byModel[k] = v
	}
	s.mu.Unlock()

	return StatsSnapshot{
		TotalAdmitted:        gs.TotalAdmitted,
		TotalBlocked:         gs.TotalBlocked,
		BlockedBudget:        gs.BlockedBudget,
		BlockedCallLimit:     gs.BlockedCallLimit,
		UnknownAborts:        gs.UnknownAborts,
		RecordedActualUSD:    gs.RecordedActualUSD,
		EstimatedUnknownUSD:  gs.EstimatedUnknownUSD,
		InFlightEstimatedUSD: gs.InFlightEstimatedUSD,
		TotalExposureUSD:     exposure,
		MaxUSD:               s.cfg.MaxUSD,
		MaxCalls:             s.cfg.MaxCalls,
		BudgetExceeded:       exceeded,
		ByModel:              byModel,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/stats", s.handleStats)
	mux.HandleFunc("/v1/chat/completions", s.handleProxy)
	mux.HandleFunc("/v1/responses", s.handleProxy)
	mux.HandleFunc("/v1/messages", s.handleProxy)
	return mux
}

func (s *Server) authorized(r *http.Request) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(auth, "Bearer ") {
		tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.Token)) == 1 {
			return true
		}
	}
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.Token)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid gateway token")
		return
	}
	writeJSON(w, http.StatusOK, s.Stats())
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid gateway token")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}
	if len(body) > maxRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds gateway limit")
		return
	}

	var metaReq struct {
		Model               string `json:"model"`
		MaxTokens           int    `json:"max_tokens"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		MaxOutputTokens     int    `json:"max_output_tokens"`
		Stream              bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &metaReq); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "request body must be valid JSON")
		return
	}
	maxOut := metaReq.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = metaReq.MaxCompletionTokens
	}
	if maxOut <= 0 {
		maxOut = metaReq.MaxTokens
	}

	provider, upstreamURL, headers, err := s.resolveUpstream(r.URL.Path, r.Header.Get("X-Nori-Provider"), metaReq.Model)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "provider_unconfigured", err.Error())
		return
	}

	scope := normalizeGatewayScope(r.Header.Get("X-Nori-Scope"), r.URL.Path)
	userID := "gateway"

	reqCtx := llmguard.WithMeta(r.Context(), llmguard.CallMeta{
		UserID:          userID,
		Scope:           scope,
		Provider:        provider,
		Model:           metaReq.Model,
		MaxOutputTokens: maxOut,
	})

	upReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid upstream request")
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		upReq.Header.Set(k, v)
	}

	client := s.upstreamClient(llmguard.CallMeta{
		UserID:          userID,
		Scope:           scope,
		Provider:        provider,
		Model:           metaReq.Model,
		MaxOutputTokens: maxOut,
	})

	res, err := client.Do(upReq)
	if err != nil {
		if errors.Is(err, llmguard.ErrBudgetExceeded) || errors.Is(err, llmguard.ErrCallLimitExceeded) {
			slog.WarnContext(r.Context(), "nori-gateway killed request: local budget exceeded",
				"model", metaReq.Model, "provider", provider, "err", err)
			writeError(w, http.StatusTooManyRequests, "local_budget_exceeded", err.Error())
			return
		}
		if llmguard.IsGuardError(err) {
			writeError(w, http.StatusTooManyRequests, "llmguard_rejected", err.Error())
			return
		}
		if errors.Is(err, context.Canceled) {
			writeError(w, 499, "client_canceled", "client canceled request")
			return
		}
		writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("upstream %s request failed", provider))
		return
	}
	defer res.Body.Close()

	if metaReq.Stream && res.StatusCode == http.StatusOK && strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		s.proxyStream(w, res, provider, metaReq.Model, len(body), maxOut)
		return
	}

	respBytes, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		s.guard.RecordUnknownCost(llmguard.EstimateWorstCaseCallUSD(len(body), maxOut))
		writeError(w, http.StatusBadGateway, "upstream_read_error", "failed reading upstream response")
		return
	}
	if len(respBytes) > maxResponseBytes {
		s.guard.RecordUnknownCost(llmguard.EstimateWorstCaseCallUSD(len(body), maxOut))
		writeError(w, http.StatusBadGateway, "upstream_response_too_large", "upstream response exceeded gateway limit")
		return
	}

	if res.StatusCode == http.StatusOK {
		respBytes = s.accountAndInjectCost(provider, metaReq.Model, respBytes, len(body), maxOut)
	}

	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(respBytes)
}

func (s *Server) upstreamClient(defaults llmguard.CallMeta) *http.Client {
	client := s.guard.Client(2*time.Minute, defaults)
	if s.cfg.Transport != nil {
		client.Transport = s.guard.Transport(s.cfg.Transport, defaults)
	}
	return client
}

func normalizeGatewayScope(raw, path string) string {
	switch strings.TrimSpace(raw) {
	case llmguard.ScopeOuter, llmguard.ScopeTask, llmguard.ScopeCompaction,
		llmguard.ScopeExtract, llmguard.ScopeDream, llmguard.ScopeClassify,
		llmguard.ScopeReview, "sim_responder", "sim_judge", "sim_preflight":
		return strings.TrimSpace(raw)
	default:
		if path == "/v1/responses" {
			return llmguard.ScopeTask
		}
		return llmguard.ScopeOuter
	}
}

func (s *Server) resolveUpstream(path, explicitProvider, model string) (provider, url string, headers map[string]string, err error) {
	p := strings.ToLower(strings.TrimSpace(explicitProvider))
	if p == "" {
		switch {
		case path == "/v1/messages":
			p = "anthropic"
		case strings.Contains(model, "/"):
			p = "openrouter"
		case strings.HasPrefix(strings.ToLower(model), "claude-"):
			// OpenAI-compatible endpoints (/v1/chat/completions, /v1/responses) cannot
			// be sent directly to api.anthropic.com; route via OpenRouter if available.
			if s.cfg.OpenRouterKey != "" {
				p = "openrouter"
			} else {
				return "", "", nil, errors.New("claude-* models on OpenAI-compatible paths require OpenRouter (e.g. anthropic/claude-*) or /v1/messages")
			}
		default:
			p = "openai"
		}
	}

	suffix := strings.TrimPrefix(path, "/v1")
	switch p {
	case "anthropic":
		if s.cfg.AnthropicKey == "" {
			return "", "", nil, errors.New("ANTHROPIC_TEST_API_KEY / ANTHROPIC_API_KEY is not configured in gateway")
		}
		return "anthropic", strings.TrimRight(s.cfg.AnthropicBaseURL, "/") + suffix, map[string]string{
			"x-api-key":         s.cfg.AnthropicKey,
			"anthropic-version": anthropicAPIVersion,
		}, nil
	case "openrouter":
		if s.cfg.OpenRouterKey == "" {
			return "", "", nil, errors.New("OPENROUTER_TEST_API_KEY / OPENROUTER_API_KEY is not configured in gateway")
		}
		return "openrouter", strings.TrimRight(s.cfg.OpenRouterBaseURL, "/") + suffix, map[string]string{
			"Authorization": "Bearer " + s.cfg.OpenRouterKey,
		}, nil
	default:
		if s.cfg.OpenAIKey == "" {
			return "", "", nil, errors.New("OPENAI_TEST_API_KEY / OPENAI_API_KEY is not configured in gateway")
		}
		return "openai", strings.TrimRight(s.cfg.OpenAIBaseURL, "/") + suffix, map[string]string{
			"Authorization": "Bearer " + s.cfg.OpenAIKey,
		}, nil
	}
}

func (s *Server) proxyStream(w http.ResponseWriter, res *http.Response, provider, model string, reqBytes, maxOut int) {
	w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
	w.WriteHeader(res.StatusCode)
	flusher, _ := w.(http.Flusher)

	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := res.Body.Read(tmp)
		if n > 0 {
			_, _ = w.Write(tmp[:n])
			if flusher != nil {
				flusher.Flush()
			}
			if buf.Len() < 512<<10 {
				buf.Write(tmp[:n])
			}
		}
		if err != nil {
			break
		}
	}
	// Best-effort cost accounting from final SSE usage chunk; fall back to worst-case estimate.
	costKnown := false
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" || !strings.Contains(data, `"usage"`) {
			continue
		}
		if cost, inTok, outTok, ok := extractAndCalculateCost(provider, model, []byte(data)); ok {
			s.guard.RecordActualCost(cost)
			s.recordModelStats(provider, model, inTok, outTok, cost)
			costKnown = true
			break
		}
	}
	if !costKnown {
		est := llmguard.EstimateWorstCaseCallUSD(reqBytes, maxOut)
		s.guard.RecordUnknownCost(est)
		s.recordModelStats(provider, model, 0, 0, est)
	}
}

func (s *Server) accountAndInjectCost(provider, reqModel string, respBytes []byte, reqBytes, maxOut int) []byte {
	cost, inTok, outTok, ok := extractAndCalculateCost(provider, reqModel, respBytes)
	if !ok {
		est := llmguard.EstimateWorstCaseCallUSD(reqBytes, maxOut)
		s.guard.RecordUnknownCost(est)
		s.recordModelStats(provider, reqModel, inTok, outTok, est)
		return respBytes
	}
	s.guard.RecordActualCost(cost)
	s.recordModelStats(provider, reqModel, inTok, outTok, cost)

	// Inject usage.cost so nori-sim in gateway mode records exact calculated cost.
	var doc map[string]any
	if json.Unmarshal(respBytes, &doc) == nil {
		if u, ok := doc["usage"].(map[string]any); ok {
			if _, hasCost := u["cost"]; !hasCost {
				u["cost"] = cost
				if patched, err := json.Marshal(doc); err == nil {
					return patched
				}
			}
		}
	}
	return respBytes
}

func (s *Server) recordModelStats(provider, model string, inTok, outTok int64, cost float64) {
	if model == "" {
		model = "unknown"
	}
	s.mu.Lock()
	st := s.byModel[model]
	if st.Provider == "" {
		st.Provider = provider
	}
	st.Calls++
	st.InputTokens += inTok
	st.OutputTokens += outTok
	st.CostUSD += cost
	s.byModel[model] = st
	s.mu.Unlock()
}

func extractAndCalculateCost(provider, fallbackModel string, raw []byte) (cost float64, inTok, outTok int64, ok bool) {
	var parsed struct {
		Model string `json:"model"`
		Tier  string `json:"service_tier"`
		Usage *struct {
			// Chat completions
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			// Responses / Anthropic
			InputTokens   *int64   `json:"input_tokens"`
			OutputTokens  *int64   `json:"output_tokens"`
			TotalTokens   *int64   `json:"total_tokens"`
			Cost          *float64 `json:"cost"`
			PromptDetails struct {
				Cached int64 `json:"cached_tokens"`
				Write  int64 `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
			InputDetails struct {
				Cached int64 `json:"cached_tokens"`
				Write  int64 `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &parsed) != nil || parsed.Usage == nil {
		return 0, 0, 0, false
	}
	u := parsed.Usage
	model := parsed.Model
	if model == "" {
		model = fallbackModel
	}
	tier := parsed.Tier
	if tier == "" {
		tier = "default"
	}

	inPtr := u.InputTokens
	outPtr := u.OutputTokens
	cached := u.InputDetails.Cached
	written := u.InputDetails.Write
	if inPtr == nil && u.PromptTokens != nil {
		inPtr = u.PromptTokens
		outPtr = u.CompletionTokens
		cached = u.PromptDetails.Cached
		written = u.PromptDetails.Write
	}
	if inPtr != nil {
		inTok = *inPtr
	}
	if outPtr != nil {
		outTok = *outPtr
	}

	if u.Cost != nil && *u.Cost > 0 {
		return *u.Cost, inTok, outTok, true
	}

	if provider == "anthropic" {
		if c, err := usage.AnthropicCost(model, inTok, outTok, u.CacheRead, u.CacheWrite); err == nil {
			return c, inTok + u.CacheRead + u.CacheWrite, outTok, true
		}
		return 0, inTok, outTok, false
	}

	totalPtr := u.TotalTokens
	if totalPtr == nil && inPtr != nil && outPtr != nil {
		t := *inPtr + *outPtr
		totalPtr = &t
	}
	if c, err := usage.SolCostWithWrites(model, tier, inPtr, outPtr, totalPtr, cached, written); err == nil {
		return c, inTok, outTok, true
	}
	if c, err := usage.TerraCostWithWrites(model, tier, inPtr, outPtr, totalPtr, cached, written); err == nil {
		return c, inTok, outTok, true
	}
	// Standard GPT-4.1 / 4.1-mini tariffs used by persona-sim responder and judge.
	low := strings.ToLower(model)
	switch {
	case strings.HasPrefix(low, "gpt-4.1-mini"):
		c := (float64(max(inTok-cached, 0))*0.40 + float64(cached)*0.10 + float64(outTok)*1.60) / 1e6
		return c, inTok, outTok, true
	case strings.HasPrefix(low, "gpt-4.1-nano"):
		c := (float64(max(inTok-cached, 0))*0.10 + float64(cached)*0.025 + float64(outTok)*0.40) / 1e6
		return c, inTok, outTok, true
	case strings.HasPrefix(low, "gpt-4.1"):
		c := (float64(max(inTok-cached, 0))*2.00 + float64(cached)*0.50 + float64(outTok)*8.00) / 1e6
		return c, inTok, outTok, true
	}
	return 0, inTok, outTok, false
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"type":    code,
			"code":    code,
			"message": message,
		},
	})
}
