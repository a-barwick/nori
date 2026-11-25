// Package llmguard provides the single guarded HTTP transport boundary for all
// outbound LLM provider requests across Nori. Every model client (outer chat,
// inner task/compaction/extraction/dream, intent classifier) routes through
// Guard so rate limits, concurrency caps, duplicate-payload storm detection,
// consecutive-abort circuit breakers, timeout ceilings, and test-mode network
// locks are enforced fundamentally in one place rather than per caller.
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
)

// IsGuardError reports whether err was returned by llmguard admission control
// before any upstream provider request was sent.
func IsGuardError(err error) bool {
	return errors.Is(err, ErrLiveCallBlockedInTest) ||
		errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrConcurrencyLimited) ||
		errors.Is(err, ErrDuplicateStorm) ||
		errors.Is(err, ErrCircuitOpen) ||
		errors.Is(err, ErrBudgetExceeded)
}

// CallMeta tags an outbound LLM request with its caller scope and identity.
type CallMeta struct {
	UserID          string
	Scope           string
	Provider        string
	Model           string
	MaxOutputTokens int
}

type metaKey struct{}

// WithMeta attaches or merges CallMeta onto ctx. Non-empty fields in meta
// override existing context values.
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
	return context.WithValue(ctx, metaKey{}, cur)
}

// WithScope sets the LLM call scope on ctx.
func WithScope(ctx context.Context, scope string) context.Context {
	return WithMeta(ctx, CallMeta{Scope: scope})
}

// WithUser sets the user ID on ctx.
func WithUser(ctx context.Context, userID string) context.Context {
	return WithMeta(ctx, CallMeta{UserID: userID})
}

// MetaFromContext returns any CallMeta stored on ctx.
func MetaFromContext(ctx context.Context) CallMeta {
	if ctx == nil {
		return CallMeta{}
	}
	m, _ := ctx.Value(metaKey{}).(CallMeta)
	return m
}

// ScopePolicy bounds request rate and concurrency for a single (user, scope).
type ScopePolicy struct {
	MaxPerMinute int
	MaxPerHour   int
	MaxInFlight  int
}

// Config controls Guard enforcement thresholds. Zero values use safe defaults.
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

	// MaxEstimatedUSD optionally caps cumulative estimated + recorded USD spend
	// across this Guard (including conservative worst-case estimates for aborted
	// / unknown-status calls). Zero disables the process USD cap.
	MaxEstimatedUSD float64

	// AllowLiveInTests disables the automatic refusal of non-loopback hosts when
	// testing.Testing() is true. Leave false except in explicit opt-in live tests.
	AllowLiveInTests bool

	Scopes map[string]ScopePolicy
}

// DefaultConfig returns production-safe guard limits.
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

// Snapshot exposes read-only guard telemetry for diagnostics and tests.
type Snapshot struct {
	TotalAdmitted       int64
	TotalBlocked        int64
	BlockedLiveTest     int64
	BlockedRate         int64
	BlockedConcurrency  int64
	BlockedDuplicate    int64
	BlockedCircuit      int64
	BlockedBudget       int64
	UnknownAborts       int64
	EstimatedUnknownUSD float64
	RecordedActualUSD   float64
}

type scopeKey struct {
	user  string
	scope string
}

type scopeState struct {
	inFlight          int
	minuteCalls       []time.Time
	hourCalls         []time.Time
	consecutiveAborts int
	consecutiveErrors int
	breakerTrips      int
	circuitOpenUntil  time.Time
	circuitReason     string
}

type dupKey struct {
	user     string
	scope    string
	endpoint string
	bodyHash string
}

// Guard enforces admission limits, storm/loop protection, and circuit breakers
// across all LLM requests routed through it.
type Guard struct {
	cfg Config

	mu             sync.Mutex
	globalInFlight int
	globalMinute   []time.Time
	scopes         map[scopeKey]*scopeState
	duplicates     map[dupKey][]time.Time
	stats          Snapshot
}

var (
	defaultOnce  sync.Once
	defaultGuard *Guard
)

// Default returns the process-wide shared Guard used by platform-wired clients.
func Default() *Guard {
	defaultOnce.Do(func() {
		defaultGuard = New(DefaultConfig())
	})
	return defaultGuard
}

// New creates an isolated Guard instance with normalized defaults.
func New(cfg Config) *Guard {
	return &Guard{
		cfg:        normalizeConfig(cfg),
		scopes:     map[scopeKey]*scopeState{},
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

// ClampTimeout enforces a positive per-call timeout no greater than MaxTimeout.
func (g *Guard) ClampTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	if timeout > g.cfg.MaxTimeout {
		return g.cfg.MaxTimeout
	}
	return timeout
}

// Client returns an http.Client whose transport is wrapped by this Guard,
// with redirects disabled and timeout clamped to MaxTimeout.
func (g *Guard) Client(timeout time.Duration, defaults CallMeta) *http.Client {
	return &http.Client{
		Timeout:       g.ClampTimeout(timeout),
		Transport:     g.Transport(http.DefaultTransport, defaults),
		CheckRedirect: refuseRedirects,
	}
}

// WrapClient clones base (or creates a new client when nil) and ensures every
// request passes through this Guard with clamped timeout and no redirects.
func (g *Guard) WrapClient(base *http.Client, timeout time.Duration, defaults CallMeta) *http.Client {
	if base == nil {
		return g.Client(timeout, defaults)
	}
	baseRT := base.Transport
	if baseRT == nil {
		baseRT = http.DefaultTransport
	}
	if gt, ok := baseRT.(*guardedTransport); ok && gt.guard == g {
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

// Transport wraps base so every RoundTrip enforces Guard policies.
func (g *Guard) Transport(base http.RoundTripper, defaults CallMeta) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &guardedTransport{guard: g, base: base, defaults: defaults}
}

// RecordActualCost adds verified provider USD spend to this Guard's budget accounting.
func (g *Guard) RecordActualCost(usd float64) {
	if usd <= 0 {
		return
	}
	g.mu.Lock()
	g.stats.RecordedActualUSD += usd
	g.mu.Unlock()
}

// Stats returns a point-in-time snapshot of admission, block, and abort counters.
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

	release, err := t.guard.admit(req.Context(), meta, endpoint, bodyHash, bodyBytes)
	if err != nil {
		return nil, err
	}

	res, rtErr := t.base.RoundTrip(req)
	t.guard.observe(req.Context(), meta, bodyBytes, res, rtErr)
	release()
	return res, rtErr
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

func estimateWorstCaseCallUSD(bodyBytes, maxOutputTokens int) float64 {
	estInputTokens := max(bodyBytes/4, 256)
	estOutputTokens := maxOutputTokens
	if estOutputTokens <= 0 {
		estOutputTokens = 1024
	}
	// Conservative pricing envelope ($2/M input, $10/M output) so aborted calls
	// never appear as $0.00 against guard spend ceilings.
	return float64(estInputTokens)*2e-6 + float64(estOutputTokens)*10e-6
}

func (g *Guard) admit(ctx context.Context, meta CallMeta, endpoint, bodyHash string, bodyBytes int) (func(), error) {
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

	// 1. Circuit breaker check (consecutive aborts or provider errors).
	if !st.circuitOpenUntil.IsZero() && now.Before(st.circuitOpenUntil) {
		g.stats.TotalBlocked++
		g.stats.BlockedCircuit++
		remaining := st.circuitOpenUntil.Sub(now).Round(time.Second)
		slog.WarnContext(ctx, "llmguard rejected call: circuit breaker open",
			"user_id", meta.UserID, "scope", meta.Scope, "reason", st.circuitReason, "retry_in", remaining.String())
		return nil, fmt.Errorf("%w (%s, retry in %s)", ErrCircuitOpen, st.circuitReason, remaining)
	}

	// 2. Hard spend ceiling (includes conservative estimate for unknown/aborted calls).
	if g.cfg.MaxEstimatedUSD > 0 {
		projected := g.stats.RecordedActualUSD + g.stats.EstimatedUnknownUSD + estimateWorstCaseCallUSD(bodyBytes, meta.MaxOutputTokens)
		if projected > g.cfg.MaxEstimatedUSD {
			g.stats.TotalBlocked++
			g.stats.BlockedBudget++
			slog.ErrorContext(ctx, "llmguard rejected call: estimated spend ceiling exceeded",
				"user_id", meta.UserID, "scope", meta.Scope,
				"recorded_usd", g.stats.RecordedActualUSD, "estimated_unknown_usd", g.stats.EstimatedUnknownUSD,
				"limit_usd", g.cfg.MaxEstimatedUSD)
			return nil, fmt.Errorf("%w (projected $%.4f > limit $%.4f)", ErrBudgetExceeded, projected, g.cfg.MaxEstimatedUSD)
		}
	}

	// 3. In-flight concurrency cap (global and per user+scope).
	if g.globalInFlight >= g.cfg.GlobalMaxInFlight || (policy.MaxInFlight > 0 && st.inFlight >= policy.MaxInFlight) {
		g.stats.TotalBlocked++
		g.stats.BlockedConcurrency++
		slog.WarnContext(ctx, "llmguard rejected call: concurrency limit reached",
			"user_id", meta.UserID, "scope", meta.Scope, "scope_in_flight", st.inFlight, "global_in_flight", g.globalInFlight)
		return nil, fmt.Errorf("%w (scope=%s in_flight=%d)", ErrConcurrencyLimited, meta.Scope, st.inFlight)
	}

	// 4. Sliding-window rate limits (global + per user+scope minute & hour).
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

	// 5. Duplicate-payload storm detector.
	if bodyHash != "" && g.cfg.DuplicateMax > 0 {
		dk := dupKey{user: meta.UserID, scope: meta.Scope, endpoint: endpoint, bodyHash: bodyHash}
		dupCutoff := now.Add(-g.cfg.DuplicateWindow)
		recent := pruneBefore(g.duplicates[dk], dupCutoff)
		if len(recent) >= g.cfg.DuplicateMax {
			g.duplicates[dk] = recent
			g.stats.TotalBlocked++
			g.stats.BlockedDuplicate++
			slog.ErrorContext(ctx, "llmguard rejected call: identical payload storm detected",
				"user_id", meta.UserID, "scope", meta.Scope, "endpoint", endpoint,
				"repeats", len(recent), "window", g.cfg.DuplicateWindow.String())
			return nil, fmt.Errorf("%w (scope=%s repeated %d times in %s)", ErrDuplicateStorm, meta.Scope, len(recent), g.cfg.DuplicateWindow)
		}
		g.duplicates[dk] = append(recent, now)
		if len(g.duplicates) > 2048 {
			g.gcDuplicatesLocked(dupCutoff)
		}
	}

	// Admitted: reserve in-flight & rate-limit slots.
	g.globalInFlight++
	st.inFlight++
	g.globalMinute = append(g.globalMinute, now)
	st.minuteCalls = append(st.minuteCalls, now)
	st.hourCalls = append(st.hourCalls, now)
	g.stats.TotalAdmitted++

	var once sync.Once
	release := func() {
		once.Do(func() {
			g.mu.Lock()
			if g.globalInFlight > 0 {
				g.globalInFlight--
			}
			if st.inFlight > 0 {
				st.inFlight--
			}
			g.mu.Unlock()
		})
	}
	return release, nil
}

func (g *Guard) observe(ctx context.Context, meta CallMeta, bodyBytes int, res *http.Response, rtErr error) {
	now := g.now()
	sk := scopeKey{user: meta.UserID, scope: meta.Scope}

	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.scopes[sk]
	if st == nil {
		return
	}

	if rtErr != nil {
		// Transport error or client cancellation after bytes may have been sent.
		// Track worst-case unbilled exposure and trip the abort circuit breaker
		// if repeated aborts indicate a cancel/retry storm.
		st.consecutiveAborts++
		g.stats.UnknownAborts++
		g.stats.EstimatedUnknownUSD += estimateWorstCaseCallUSD(bodyBytes, meta.MaxOutputTokens)
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

	if res != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
		st.consecutiveAborts = 0
		st.consecutiveErrors = 0
		st.breakerTrips = 0
		st.circuitOpenUntil = time.Time{}
		st.circuitReason = ""
		return
	}

	if res != nil {
		st.consecutiveAborts = 0
		if res.StatusCode == http.StatusPaymentRequired {
			st.breakerTrips++
			cooldown := min(5*time.Minute, g.cfg.BreakerMaxCooldown)
			st.circuitOpenUntil = now.Add(cooldown)
			st.circuitReason = "provider returned HTTP 402"
			slog.ErrorContext(ctx, "llmguard circuit breaker opened on HTTP 402",
				"user_id", meta.UserID, "scope", meta.Scope, "cooldown", cooldown.String())
			return
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusUnauthorized ||
			res.StatusCode == http.StatusForbidden || res.StatusCode >= 500 {
			st.consecutiveErrors++
			if st.consecutiveErrors >= g.cfg.ProviderErrorBreakerThreshold {
				st.breakerTrips++
				cooldown := g.breakerCooldown(st.breakerTrips)
				st.circuitOpenUntil = now.Add(cooldown)
				st.circuitReason = fmt.Sprintf("%d consecutive provider HTTP %d errors", st.consecutiveErrors, res.StatusCode)
				slog.ErrorContext(ctx, "llmguard circuit breaker opened on provider errors",
					"user_id", meta.UserID, "scope", meta.Scope, "status", res.StatusCode,
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
