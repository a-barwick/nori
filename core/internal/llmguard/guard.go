package llmguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	ScopeOuter      = "outer"
	ScopeTask       = "task"
	ScopeCompaction = "compaction"
	ScopeExtract    = "extract"
	ScopeDream      = "dream"
	ScopeClassify   = "classify"
	ScopeReview     = "review"
)

var (
	ErrLiveCallBlockedInTest = errors.New("llmguard: non-loopback LLM request blocked during go test")
	ErrRateLimited           = errors.New("llmguard: LLM call rate limit exceeded")
	ErrConcurrencyLimited    = errors.New("llmguard: LLM in-flight concurrency limit exceeded")
	ErrDuplicateStorm        = errors.New("llmguard: repeated identical LLM request blocked (storm detected)")
	ErrCircuitOpen           = errors.New("llmguard: LLM circuit breaker open after repeated aborts or provider errors")
	ErrBudgetExceeded        = errors.New("llmguard: estimated LLM spend ceiling exceeded")
	ErrCallLimitExceeded     = errors.New("llmguard: total LLM call limit exceeded")
)

func IsGuardError(err error) bool {
	return errors.Is(err, ErrLiveCallBlockedInTest) ||
		errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrConcurrencyLimited) ||
		errors.Is(err, ErrDuplicateStorm) ||
		errors.Is(err, ErrCircuitOpen) ||
		errors.Is(err, ErrBudgetExceeded) ||
		errors.Is(err, ErrCallLimitExceeded)
}

type CallMeta struct {
	UserID          string
	Scope           string
	Provider        string
	Model           string
	MaxOutputTokens int
	BYOK            bool
}

type metaKey struct{}

func WithMeta(ctx context.Context, meta CallMeta) context.Context {
	cur := MetaFromContext(ctx)
	if meta.UserID != "" {
		cur.UserID = meta.UserID
	}
	if meta.Scope != "" {
		cur.Scope = meta.Scope
	}
	if meta.Provider != "" {
		cur.Provider = meta.Provider
	}
	if meta.Model != "" {
		cur.Model = meta.Model
	}
	if meta.MaxOutputTokens > 0 {
		cur.MaxOutputTokens = meta.MaxOutputTokens
	}
	if meta.BYOK {
		cur.BYOK = true
	}
	return context.WithValue(ctx, metaKey{}, cur)
}

func WithScope(ctx context.Context, scope string) context.Context {
	return WithMeta(ctx, CallMeta{Scope: scope})
}

func MetaFromContext(ctx context.Context) CallMeta {
	if ctx == nil {
		return CallMeta{}
	}
	m, _ := ctx.Value(metaKey{}).(CallMeta)
	return m
}

type ScopePolicy struct {
	MaxPerMinute int
	MaxPerHour   int
	MaxInFlight  int
}

type Config struct {
	Now func() time.Time

	MaxTimeout         time.Duration
	GlobalMaxPerMinute int
	GlobalMaxInFlight  int

	DuplicateWindow time.Duration
	DuplicateMax    int

	AbortBreakerThreshold         int
	ProviderErrorBreakerThreshold int
	BreakerBaseCooldown           time.Duration
	BreakerMaxCooldown            time.Duration

	MaxEstimatedUSD float64

	MaxTotalCalls int

	AllowLiveInTests bool

	Scopes map[string]ScopePolicy
}

func DefaultConfig() Config {
	return Config{
		MaxTimeout:                    3 * time.Minute,
		GlobalMaxPerMinute:            240,
		GlobalMaxInFlight:             64,
		DuplicateWindow:               5 * time.Minute,
		DuplicateMax:                  4,
		AbortBreakerThreshold:         3,
		ProviderErrorBreakerThreshold: 5,
		BreakerBaseCooldown:           30 * time.Second,
		BreakerMaxCooldown:            15 * time.Minute,
		Scopes: map[string]ScopePolicy{
			ScopeCompaction: {MaxPerMinute: 6, MaxPerHour: 60, MaxInFlight: 1},
			ScopeExtract:    {MaxPerMinute: 6, MaxPerHour: 60, MaxInFlight: 1},
			ScopeDream:      {MaxPerMinute: 4, MaxPerHour: 24, MaxInFlight: 1},
			ScopeOuter:      {MaxPerMinute: 30, MaxPerHour: 600, MaxInFlight: 8},
			ScopeClassify:   {MaxPerMinute: 30, MaxPerHour: 600, MaxInFlight: 8},
			ScopeReview:     {MaxPerMinute: 30, MaxPerHour: 600, MaxInFlight: 8},
			ScopeTask:       {MaxPerMinute: 60, MaxPerHour: 1200, MaxInFlight: 16},
		},
	}
}

func normalizeConfig(cfg Config) Config {
	def := DefaultConfig()
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = def.MaxTimeout
	}
	if cfg.GlobalMaxPerMinute <= 0 {
		cfg.GlobalMaxPerMinute = def.GlobalMaxPerMinute
	}
	if cfg.GlobalMaxInFlight <= 0 {
		cfg.GlobalMaxInFlight = def.GlobalMaxInFlight
	}
	if cfg.DuplicateWindow <= 0 {
		cfg.DuplicateWindow = def.DuplicateWindow
	}
	if cfg.DuplicateMax <= 0 {
		cfg.DuplicateMax = def.DuplicateMax
	}
	if cfg.AbortBreakerThreshold <= 0 {
		cfg.AbortBreakerThreshold = def.AbortBreakerThreshold
	}
	if cfg.ProviderErrorBreakerThreshold <= 0 {
		cfg.ProviderErrorBreakerThreshold = def.ProviderErrorBreakerThreshold
	}
	if cfg.BreakerBaseCooldown <= 0 {
		cfg.BreakerBaseCooldown = def.BreakerBaseCooldown
	}
	if cfg.BreakerMaxCooldown <= 0 {
		cfg.BreakerMaxCooldown = def.BreakerMaxCooldown
	}
	mergedScopes := make(map[string]ScopePolicy, len(def.Scopes)+len(cfg.Scopes))
	for k, v := range def.Scopes {
		mergedScopes[k] = v
	}
	for k, v := range cfg.Scopes {
		mergedScopes[k] = v
	}
	cfg.Scopes = mergedScopes
	return cfg
}

type Snapshot struct {
	TotalAdmitted        int64
	TotalBlocked         int64
	BlockedLiveTest      int64
	BlockedRate          int64
	BlockedConcurrency   int64
	BlockedDuplicate     int64
	BlockedCircuit       int64
	BlockedBudget        int64
	BlockedCallLimit     int64
	UnknownAborts        int64
	EstimatedUnknownUSD  float64
	InFlightEstimatedUSD float64
	RecordedActualUSD    float64
}

type scopeKey struct {
	user  string
	scope string
}

type scopeState struct {
	inFlight    int
	minuteCalls []time.Time
	hourCalls   []time.Time
}

type circuitKey struct {
	user     string
	scope    string
	identity [32]byte
}

type circuitState struct {
	consecutiveAborts int
	consecutiveErrors int
	breakerTrips      int
	circuitOpenUntil  time.Time
	circuitReason     string
	accessErr         error
}

type dupKey struct {
	user     string
	scope    string
	endpoint string
	bodyHash string
}

type Guard struct {
	cfg    Config
	parent *Guard

	mu             sync.Mutex
	globalInFlight int
	globalMinute   []time.Time
	scopes         map[scopeKey]*scopeState
	circuits       map[circuitKey]*circuitState
	duplicates     map[dupKey][]time.Time
	stats          Snapshot
}

const maxUserGuards = 4096

type userGuardEntry struct {
	guard    *Guard
	lastUsed time.Time
}

var (
	defaultOnce  sync.Once
	defaultGuard *Guard

	userGuardsMu sync.Mutex
	userGuards   = map[string]*userGuardEntry{}
)

func Default() *Guard {
	defaultOnce.Do(func() {
		defaultGuard = New(DefaultConfig())
	})
	return defaultGuard
}

func ForUser(userID string) *Guard {
	if userID == "" {
		return Default()
	}
	now := time.Now()
	userGuardsMu.Lock()
	defer userGuardsMu.Unlock()
	if entry, ok := userGuards[userID]; ok {
		entry.lastUsed = now
		return entry.guard
	}
	if len(userGuards) >= maxUserGuards {
		var oldestKey string
		var oldestTime time.Time
		for k, e := range userGuards {
			if oldestKey == "" || e.lastUsed.Before(oldestTime) {
				oldestKey = k
				oldestTime = e.lastUsed
			}
		}
		if oldestKey != "" {
			delete(userGuards, oldestKey)
		}
	}
	g := New(DefaultConfig())
	g.parent = Default()
	userGuards[userID] = &userGuardEntry{guard: g, lastUsed: now}
	return g
}
func New(cfg Config) *Guard {
	return &Guard{
		cfg:        normalizeConfig(cfg),
		scopes:     map[scopeKey]*scopeState{},
		circuits:   map[circuitKey]*circuitState{},
		duplicates: map[dupKey][]time.Time{},
	}
}

func (g *Guard) now() time.Time {
	if g.cfg.Now != nil {
		return g.cfg.Now()
	}
	return time.Now()
}

func (g *Guard) policyFor(scope string) ScopePolicy {
	if p, ok := g.cfg.Scopes[scope]; ok {
		return p
	}
	return ScopePolicy{MaxPerMinute: 30, MaxPerHour: 600, MaxInFlight: 8}
}

func (g *Guard) ClampTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	if timeout > g.cfg.MaxTimeout {
		return g.cfg.MaxTimeout
	}
	return timeout
}

func (g *Guard) Client(timeout time.Duration, defaults CallMeta) *http.Client {
	return &http.Client{
		Timeout:       g.ClampTimeout(timeout),
		Transport:     g.Transport(http.DefaultTransport, defaults),
		CheckRedirect: refuseRedirects,
	}
}

func (g *Guard) WrapClient(base *http.Client, timeout time.Duration, defaults CallMeta) *http.Client {
	if base == nil {
		return g.Client(timeout, defaults)
	}
	baseRT := base.Transport
	if baseRT == nil {
		baseRT = http.DefaultTransport
	}
	for {
		gt, ok := baseRT.(*guardedTransport)
		if !ok {
			break
		}
		baseRT = gt.base
	}
	effectiveTimeout := base.Timeout
	if timeout > 0 && (effectiveTimeout <= 0 || timeout < effectiveTimeout) {
		effectiveTimeout = timeout
	}
	cloned := *base
	cloned.Timeout = g.ClampTimeout(effectiveTimeout)
	cloned.Transport = g.Transport(baseRT, defaults)
	cloned.CheckRedirect = refuseRedirects
	return &cloned
}

func (g *Guard) Transport(base http.RoundTripper, defaults CallMeta) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &guardedTransport{guard: g, base: base, defaults: defaults}
}

func (g *Guard) RecordActualCost(usd float64) {
	if usd <= 0 {
		return
	}
	g.mu.Lock()
	g.stats.RecordedActualUSD += usd
	g.mu.Unlock()
	if g.parent != nil {
		g.parent.RecordActualCost(usd)
	}
}

func (g *Guard) RecordUnknownCost(usd float64) {
	if usd <= 0 {
		return
	}
	g.mu.Lock()
	g.stats.EstimatedUnknownUSD += usd
	g.mu.Unlock()
	if g.parent != nil {
		g.parent.RecordUnknownCost(usd)
	}
}
func (g *Guard) SetMaxEstimatedUSD(usd float64) {
	if usd < 0 {
		usd = 0
	}
	g.mu.Lock()
	g.cfg.MaxEstimatedUSD = usd
	g.mu.Unlock()
}

func (g *Guard) SetMaxTotalCalls(maxCalls int) {
	if maxCalls < 0 {
		maxCalls = 0
	}
	g.mu.Lock()
	g.cfg.MaxTotalCalls = maxCalls
	g.mu.Unlock()
}

func (g *Guard) Stats() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

func refuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

type guardedTransport struct {
	guard    *Guard
	base     http.RoundTripper
	defaults CallMeta
}

func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("llmguard: request URL is nil")
	}
	meta := mergeMeta(t.defaults, MetaFromContext(req.Context()))
	if meta.UserID == "" {
		meta.UserID = "default"
	}
	if meta.Scope == "" {
		meta.Scope = ScopeOuter
	}

	if testing.Testing() && !t.guard.cfg.AllowLiveInTests && isRealNetworkTransport(t.base) && !isLoopbackHost(req.URL.Hostname()) {
		t.guard.mu.Lock()
		t.guard.stats.TotalBlocked++
		t.guard.stats.BlockedLiveTest++
		t.guard.mu.Unlock()
		slog.ErrorContext(req.Context(), "llmguard blocked non-loopback LLM call in go test",
			"host", req.URL.Hostname(), "scope", meta.Scope, "model", meta.Model)
		return nil, fmt.Errorf("%w: host=%s scope=%s", ErrLiveCallBlockedInTest, req.URL.Hostname(), meta.Scope)
	}

	bodyHash, bodyBytes, err := hashAndRestoreBody(req)
	if err != nil {
		return nil, err
	}
	endpoint := req.URL.Host + req.URL.Path
	credential := req.Header.Get("Authorization")
	if credential == "" {
		credential = req.Header.Get("x-api-key")
	}
	if credential == "" {
		credential = req.Header.Get("x-goog-api-key")
	}
	identity := sha256.Sum256([]byte(strings.ToLower(req.URL.Host) + "\x00" + credential))

	settle, err := t.guard.admit(req.Context(), meta, endpoint, bodyHash, bodyBytes, identity)
	if err != nil {
		return nil, err
	}

	res, rtErr := t.base.RoundTrip(req)
	status := 0
	if res != nil {
		status = res.StatusCode
	}
	if rtErr == nil && res != nil && res.Body != nil && status >= 200 && status < 300 {
		body := &meteredBody{ReadCloser: res.Body, settle: settle, status: status}
		res.Body = body
		res.Request = req.WithContext(context.WithValue(req.Context(), responseMeterKey{}, body))
		return res, nil
	}
	var accessErr error
	if meta.BYOK && rtErr == nil && res != nil {
		accessErr = ModelAccessError(res, "provider")
	}
	settle(status, nil, rtErr, accessErr)
	return res, rtErr
}

type meteredBody struct {
	io.ReadCloser
	settle func(int, *float64, error, error)
	status int
	cost   *float64
	failed error
}

func (b *meteredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.failed = err
	}
	return n, err
}

func (b *meteredBody) Close() error {
	err := b.ReadCloser.Close()
	b.settle(b.status, b.cost, errors.Join(b.failed, err), nil)
	return err
}

type responseMeterKey struct{}

func RecordResponseCost(res *http.Response, usd float64) {
	if res.Request == nil {
		return
	}
	if body, ok := res.Request.Context().Value(responseMeterKey{}).(*meteredBody); ok && usd >= 0 && !math.IsNaN(usd) && !math.IsInf(usd, 0) {
		body.cost = &usd
	}
}

func mergeMeta(base, override CallMeta) CallMeta {
	out := base
	if override.UserID != "" {
		out.UserID = override.UserID
	}
	if override.Scope != "" {
		out.Scope = override.Scope
	}
	if override.Provider != "" {
		out.Provider = override.Provider
	}
	if override.Model != "" {
		out.Model = override.Model
	}
	if override.MaxOutputTokens > 0 {
		out.MaxOutputTokens = override.MaxOutputTokens
	}
	if override.BYOK {
		out.BYOK = true
	}
	return out
}

func isRealNetworkTransport(rt http.RoundTripper) bool {
	if rt == nil {
		return true
	}
	_, isHTTP := rt.(*http.Transport)
	return isHTTP
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func hashAndRestoreBody(req *http.Request) (string, int, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return "", 0, nil
	}
	data, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return "", 0, err
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	req.ContentLength = int64(len(data))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]), len(data), nil
}

func EstimateCallUSD(meta CallMeta, bodyBytes int) float64 {
	inputRate, outputRate := 2.5, 10.0
	model := strings.ToLower(meta.Model)
	switch {
	case strings.Contains(model, "gemini-4-carbon"):
		inputRate, outputRate = 5, 25
	case strings.Contains(model, "gemini-4-neon"):
		inputRate, outputRate = 3.75, 18.75
	case strings.Contains(model, "gemini-4-hydrogen"):
		inputRate, outputRate = 2.5, 10
	case strings.Contains(model, "opus-5.5"), strings.Contains(model, "opus-5-5"):
		inputRate, outputRate = 5, 20
	case strings.Contains(model, "opus"):
		inputRate, outputRate = 18.75, 75
	case strings.Contains(model, "claude"), meta.Provider == "anthropic":
		inputRate, outputRate = 3.75, 15
	case strings.Contains(model, "gpt-6.1-terra"):
		inputRate, outputRate = 2.5, 7.5
	case strings.Contains(model, "gpt-6"):
	case meta.Provider == "gateway", meta.Provider == "openrouter":
		inputRate, outputRate = 18.75, 75
	}
	output := meta.MaxOutputTokens
	if output <= 0 {
		output = 1024
	}
	return (float64(max(bodyBytes, 256))*inputRate + float64(output)*outputRate) / 1e6
}

func (g *Guard) admit(ctx context.Context, meta CallMeta, endpoint, bodyHash string, bodyBytes int, identity [32]byte) (func(int, *float64, error, error), error) {
	now := g.now()
	sk := scopeKey{user: meta.UserID, scope: meta.Scope}
	policy := g.policyFor(meta.Scope)

	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.scopes[sk]
	if st == nil {
		st = &scopeState{}
		g.scopes[sk] = st
	}
	ck := circuitKey{user: meta.UserID, scope: meta.Scope, identity: identity}
	cs := g.circuits[ck]
	if cs == nil {
		if len(g.circuits) >= 1024 {
			for key, existing := range g.circuits {
				if !now.Before(existing.circuitOpenUntil) {
					delete(g.circuits, key)
				}
			}
		}
		cs = &circuitState{}
		g.circuits[ck] = cs
	}

	if !cs.circuitOpenUntil.IsZero() && now.Before(cs.circuitOpenUntil) {
		g.stats.TotalBlocked++
		g.stats.BlockedCircuit++
		remaining := cs.circuitOpenUntil.Sub(now).Round(time.Second)
		slog.WarnContext(ctx, "llmguard rejected call: circuit breaker open",
			"user_id", meta.UserID, "scope", meta.Scope, "reason", cs.circuitReason, "retry_in", remaining.String())
		if cs.accessErr != nil {
			return nil, fmt.Errorf("%w: %w", cs.accessErr, ErrCircuitOpen)
		}
		return nil, fmt.Errorf("%w (%s, retry in %s)", ErrCircuitOpen, cs.circuitReason, remaining)
	}

	if g.cfg.MaxTotalCalls > 0 && g.stats.TotalAdmitted >= int64(g.cfg.MaxTotalCalls) {
		g.stats.TotalBlocked++
		g.stats.BlockedCallLimit++
		slog.ErrorContext(ctx, "llmguard rejected call: total call limit exceeded",
			"user_id", meta.UserID, "scope", meta.Scope,
			"total_admitted", g.stats.TotalAdmitted, "max_calls", g.cfg.MaxTotalCalls)
		return nil, fmt.Errorf("%w (admitted %d >= limit %d)", ErrCallLimitExceeded, g.stats.TotalAdmitted, g.cfg.MaxTotalCalls)
	}

	estCallUSD := EstimateCallUSD(meta, bodyBytes)
	if g.cfg.MaxEstimatedUSD > 0 {
		projected := g.stats.RecordedActualUSD + g.stats.EstimatedUnknownUSD + g.stats.InFlightEstimatedUSD + estCallUSD
		if projected > g.cfg.MaxEstimatedUSD {
			g.stats.TotalBlocked++
			g.stats.BlockedBudget++
			slog.ErrorContext(ctx, "llmguard rejected call: estimated spend ceiling exceeded",
				"user_id", meta.UserID, "scope", meta.Scope,
				"recorded_usd", g.stats.RecordedActualUSD, "estimated_unknown_usd", g.stats.EstimatedUnknownUSD,
				"in_flight_estimated_usd", g.stats.InFlightEstimatedUSD, "limit_usd", g.cfg.MaxEstimatedUSD)
			return nil, fmt.Errorf("%w (projected $%.4f > limit $%.4f)", ErrBudgetExceeded, projected, g.cfg.MaxEstimatedUSD)
		}
	}

	if g.globalInFlight >= g.cfg.GlobalMaxInFlight || (policy.MaxInFlight > 0 && st.inFlight >= policy.MaxInFlight) {
		g.stats.TotalBlocked++
		g.stats.BlockedConcurrency++
		slog.WarnContext(ctx, "llmguard rejected call: concurrency limit reached",
			"user_id", meta.UserID, "scope", meta.Scope, "scope_in_flight", st.inFlight, "global_in_flight", g.globalInFlight)
		return nil, fmt.Errorf("%w (scope=%s in_flight=%d)", ErrConcurrencyLimited, meta.Scope, st.inFlight)
	}

	minuteCutoff := now.Add(-time.Minute)
	hourCutoff := now.Add(-time.Hour)
	g.globalMinute = pruneBefore(g.globalMinute, minuteCutoff)
	st.minuteCalls = pruneBefore(st.minuteCalls, minuteCutoff)
	st.hourCalls = pruneBefore(st.hourCalls, hourCutoff)

	if len(g.globalMinute) >= g.cfg.GlobalMaxPerMinute {
		g.stats.TotalBlocked++
		g.stats.BlockedRate++
		slog.ErrorContext(ctx, "llmguard rejected call: global per-minute rate limit exceeded",
			"user_id", meta.UserID, "scope", meta.Scope, "global_per_min", len(g.globalMinute))
		return nil, fmt.Errorf("%w (global %d/min)", ErrRateLimited, g.cfg.GlobalMaxPerMinute)
	}
	if policy.MaxPerMinute > 0 && len(st.minuteCalls) >= policy.MaxPerMinute {
		g.stats.TotalBlocked++
		g.stats.BlockedRate++
		slog.ErrorContext(ctx, "llmguard rejected call: scope per-minute rate limit exceeded",
			"user_id", meta.UserID, "scope", meta.Scope, "calls_per_min", len(st.minuteCalls), "limit", policy.MaxPerMinute)
		return nil, fmt.Errorf("%w (scope=%s %d/min)", ErrRateLimited, meta.Scope, policy.MaxPerMinute)
	}
	if policy.MaxPerHour > 0 && len(st.hourCalls) >= policy.MaxPerHour {
		g.stats.TotalBlocked++
		g.stats.BlockedRate++
		slog.ErrorContext(ctx, "llmguard rejected call: scope per-hour rate limit exceeded",
			"user_id", meta.UserID, "scope", meta.Scope, "calls_per_hour", len(st.hourCalls), "limit", policy.MaxPerHour)
		return nil, fmt.Errorf("%w (scope=%s %d/hr)", ErrRateLimited, meta.Scope, policy.MaxPerHour)
	}

	var (
		hasDup    bool
		dk        dupKey
		dupCutoff time.Time
		dupRecent []time.Time
	)
	if bodyHash != "" && g.cfg.DuplicateMax > 0 {
		hasDup = true
		dk = dupKey{user: meta.UserID, scope: meta.Scope, endpoint: endpoint, bodyHash: bodyHash}
		dupCutoff = now.Add(-g.cfg.DuplicateWindow)
		dupRecent = pruneBefore(g.duplicates[dk], dupCutoff)
		if len(dupRecent) >= g.cfg.DuplicateMax {
			g.duplicates[dk] = dupRecent
			g.stats.TotalBlocked++
			g.stats.BlockedDuplicate++
			slog.ErrorContext(ctx, "llmguard rejected call: identical payload storm detected",
				"user_id", meta.UserID, "scope", meta.Scope, "endpoint", endpoint,
				"repeats", len(dupRecent), "window", g.cfg.DuplicateWindow.String())
			return nil, fmt.Errorf("%w (scope=%s repeated %d times in %s)", ErrDuplicateStorm, meta.Scope, len(dupRecent), g.cfg.DuplicateWindow)
		}
	}

	var parentSettle func(int, *float64, error)
	if g.parent != nil {
		var err error
		parentSettle, err = g.parent.admitGlobal(ctx, meta, now, estCallUSD)
		if err != nil {
			g.stats.TotalBlocked++
			switch {
			case errors.Is(err, ErrBudgetExceeded):
				g.stats.BlockedBudget++
			case errors.Is(err, ErrCallLimitExceeded):
				g.stats.BlockedCallLimit++
			case errors.Is(err, ErrConcurrencyLimited):
				g.stats.BlockedConcurrency++
			case errors.Is(err, ErrRateLimited):
				g.stats.BlockedRate++
			}
			return nil, err
		}
	}

	if hasDup {
		g.duplicates[dk] = append(dupRecent, now)
		if len(g.duplicates) > 2048 {
			g.gcDuplicatesLocked(dupCutoff)
		}
	}
	g.globalInFlight++
	st.inFlight++
	g.stats.InFlightEstimatedUSD += estCallUSD
	g.globalMinute = append(g.globalMinute, now)
	st.minuteCalls = append(st.minuteCalls, now)
	st.hourCalls = append(st.hourCalls, now)
	g.stats.TotalAdmitted++

	var once sync.Once
	settle := func(status int, actual *float64, resultErr, accessErr error) {
		once.Do(func() {
			now := g.now()
			g.mu.Lock()
			if g.globalInFlight > 0 {
				g.globalInFlight--
			}
			if st.inFlight > 0 {
				st.inFlight--
			}
			g.stats.InFlightEstimatedUSD = max(0, g.stats.InFlightEstimatedUSD-estCallUSD)
			if actual != nil {
				g.stats.RecordedActualUSD += *actual
			} else if resultErr != nil || (status >= 200 && status < 300) {
				g.stats.EstimatedUnknownUSD += estCallUSD
			}
			g.observeLocked(ctx, now, cs, meta, status, resultErr, accessErr)
			g.mu.Unlock()
			if parentSettle != nil {
				parentSettle(status, actual, resultErr)
			}
		})
	}
	return settle, nil
}

func (g *Guard) admitGlobal(ctx context.Context, meta CallMeta, now time.Time, estCallUSD float64) (func(int, *float64, error), error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cfg.MaxTotalCalls > 0 && g.stats.TotalAdmitted >= int64(g.cfg.MaxTotalCalls) {
		g.stats.TotalBlocked++
		g.stats.BlockedCallLimit++
		slog.ErrorContext(ctx, "llmguard rejected call: global total call limit exceeded",
			"user_id", meta.UserID, "scope", meta.Scope,
			"total_admitted", g.stats.TotalAdmitted, "max_calls", g.cfg.MaxTotalCalls)
		return nil, fmt.Errorf("%w (admitted %d >= limit %d)", ErrCallLimitExceeded, g.stats.TotalAdmitted, g.cfg.MaxTotalCalls)
	}

	if g.cfg.MaxEstimatedUSD > 0 {
		projected := g.stats.RecordedActualUSD + g.stats.EstimatedUnknownUSD + g.stats.InFlightEstimatedUSD + estCallUSD
		if projected > g.cfg.MaxEstimatedUSD {
			g.stats.TotalBlocked++
			g.stats.BlockedBudget++
			slog.ErrorContext(ctx, "llmguard rejected call: global estimated spend ceiling exceeded",
				"user_id", meta.UserID, "scope", meta.Scope,
				"recorded_usd", g.stats.RecordedActualUSD, "estimated_unknown_usd", g.stats.EstimatedUnknownUSD,
				"in_flight_estimated_usd", g.stats.InFlightEstimatedUSD, "limit_usd", g.cfg.MaxEstimatedUSD)
			return nil, fmt.Errorf("%w (projected $%.4f > limit $%.4f)", ErrBudgetExceeded, projected, g.cfg.MaxEstimatedUSD)
		}
	}

	if g.globalInFlight >= g.cfg.GlobalMaxInFlight {
		g.stats.TotalBlocked++
		g.stats.BlockedConcurrency++
		slog.WarnContext(ctx, "llmguard rejected call: global concurrency limit reached",
			"user_id", meta.UserID, "scope", meta.Scope, "global_in_flight", g.globalInFlight)
		return nil, fmt.Errorf("%w (global in_flight=%d)", ErrConcurrencyLimited, g.globalInFlight)
	}

	minuteCutoff := now.Add(-time.Minute)
	g.globalMinute = pruneBefore(g.globalMinute, minuteCutoff)
	if len(g.globalMinute) >= g.cfg.GlobalMaxPerMinute {
		g.stats.TotalBlocked++
		g.stats.BlockedRate++
		slog.ErrorContext(ctx, "llmguard rejected call: global per-minute rate limit exceeded",
			"user_id", meta.UserID, "scope", meta.Scope, "global_per_min", len(g.globalMinute))
		return nil, fmt.Errorf("%w (global %d/min)", ErrRateLimited, g.cfg.GlobalMaxPerMinute)
	}

	g.globalInFlight++
	g.stats.InFlightEstimatedUSD += estCallUSD
	g.globalMinute = append(g.globalMinute, now)
	g.stats.TotalAdmitted++

	var once sync.Once
	return func(status int, actual *float64, resultErr error) {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.globalInFlight > 0 {
				g.globalInFlight--
			}
			g.stats.InFlightEstimatedUSD = max(0, g.stats.InFlightEstimatedUSD-estCallUSD)
			if actual != nil {
				g.stats.RecordedActualUSD += *actual
			} else if resultErr != nil || (status >= 200 && status < 300) {
				g.stats.EstimatedUnknownUSD += estCallUSD
			}
			if resultErr != nil {
				g.stats.UnknownAborts++
			}
		})
	}, nil
}

func (g *Guard) observeLocked(ctx context.Context, now time.Time, st *circuitState, meta CallMeta, status int, resultErr, accessErr error) {
	if resultErr != nil {
		st.consecutiveAborts++
		g.stats.UnknownAborts++
		if st.consecutiveAborts >= g.cfg.AbortBreakerThreshold {
			st.breakerTrips++
			cooldown := g.breakerCooldown(st.breakerTrips)
			st.circuitOpenUntil = now.Add(cooldown)
			st.circuitReason = fmt.Sprintf("%d consecutive aborted/unknown requests", st.consecutiveAborts)
			slog.ErrorContext(ctx, "llmguard circuit breaker opened on consecutive aborts",
				"user_id", meta.UserID, "scope", meta.Scope, "model", meta.Model,
				"consecutive_aborts", st.consecutiveAborts, "cooldown", cooldown.String(),
				"estimated_unknown_usd", g.stats.EstimatedUnknownUSD)
		}
		return
	}

	if status >= 200 && status < 300 {
		st.consecutiveAborts = 0
		st.consecutiveErrors = 0
		st.breakerTrips = 0
		st.circuitOpenUntil = time.Time{}
		st.circuitReason = ""
		st.accessErr = nil
		return
	}

	if status != 0 {
		st.consecutiveAborts = 0
		if status == http.StatusPaymentRequired {
			st.breakerTrips++
			cooldown := min(5*time.Minute, g.cfg.BreakerMaxCooldown)
			st.circuitOpenUntil = now.Add(cooldown)
			st.circuitReason = "provider returned HTTP 402"
			st.accessErr = accessErr
			slog.ErrorContext(ctx, "llmguard circuit breaker opened on HTTP 402",
				"user_id", meta.UserID, "scope", meta.Scope, "cooldown", cooldown.String())
			return
		}
		if status == http.StatusTooManyRequests || status == http.StatusUnauthorized ||
			status == http.StatusForbidden || status >= 500 {
			st.consecutiveErrors++
			if st.consecutiveErrors >= g.cfg.ProviderErrorBreakerThreshold {
				st.breakerTrips++
				cooldown := g.breakerCooldown(st.breakerTrips)
				st.circuitOpenUntil = now.Add(cooldown)
				st.circuitReason = fmt.Sprintf("%d consecutive provider HTTP %d errors", st.consecutiveErrors, status)
				st.accessErr = accessErr
				slog.ErrorContext(ctx, "llmguard circuit breaker opened on provider errors",
					"user_id", meta.UserID, "scope", meta.Scope, "status", status,
					"consecutive_errors", st.consecutiveErrors, "cooldown", cooldown.String())
			}
		}
	}
}

func (g *Guard) breakerCooldown(trips int) time.Duration {
	if trips <= 1 {
		return g.cfg.BreakerBaseCooldown
	}
	d := g.cfg.BreakerBaseCooldown
	for i := 1; i < trips; i++ {
		d *= 4
		if d >= g.cfg.BreakerMaxCooldown {
			return g.cfg.BreakerMaxCooldown
		}
	}
	return d
}

func pruneBefore(ts []time.Time, cutoff time.Time) []time.Time {
	keep := 0
	for _, t := range ts {
		if !t.Before(cutoff) {
			ts[keep] = t
			keep++
		}
	}
	return ts[:keep]
}

func (g *Guard) gcDuplicatesLocked(cutoff time.Time) {
	for k, ts := range g.duplicates {
		pruned := pruneBefore(ts, cutoff)
		if len(pruned) == 0 {
			delete(g.duplicates, k)
		} else {
			g.duplicates[k] = pruned
		}
	}
}
