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
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/netguard"
	"nori/internal/state"
	"nori/internal/usage"
)

const (
	ScopeOuter      = "outer"
	ScopeTask       = "task"
	ScopeCompaction = "compaction"
	ScopeExtract    = "extract"
	ScopeDream      = "dream"
	ScopeClassify   = "classify"
	ScopeReview     = "review"
	ScopeSearch     = "search"

	defaultSpendWindow   = 24 * time.Hour
	exaSearchEstimateUSD = 0.013
)

var (
	ErrLiveCallBlockedInTest = errors.New("llmguard: non-loopback LLM request blocked during go test")
	ErrRateLimited           = errors.New("llmguard: LLM call rate limit exceeded")
	ErrConcurrencyLimited    = errors.New("llmguard: LLM in-flight concurrency limit exceeded")
	ErrDuplicateStorm        = errors.New("llmguard: repeated identical LLM request blocked (storm detected)")
	ErrCircuitOpen           = errors.New("llmguard: LLM circuit breaker open after repeated aborts or provider errors")
	ErrBudgetExceeded        = errors.New("llmguard: estimated LLM spend ceiling exceeded")
	ErrCallLimitExceeded     = errors.New("llmguard: total LLM call limit exceeded")
	ErrDispatchBlocked       = errors.New("llmguard: provider dispatch blocked")
)

func IsGuardError(err error) bool {
	return errors.Is(err, ErrLiveCallBlockedInTest) ||
		errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrConcurrencyLimited) ||
		errors.Is(err, ErrDuplicateStorm) ||
		errors.Is(err, ErrCircuitOpen) ||
		errors.Is(err, ErrBudgetExceeded) ||
		errors.Is(err, ErrCallLimitExceeded) || errors.Is(err, ErrDispatchBlocked)
}

type CallMeta struct {
	UserID          string
	Scope           string
	Provider        string
	Model           string
	Billing         string
	MaxOutputTokens int
	BYOK            bool
}

func (m CallMeta) isHosted() bool {
	if m.Provider == ProviderHome || m.Billing == state.UsageBillingLocal {
		return false
	}
	if m.BYOK || m.Billing == state.UsageBillingBYOK {
		return false
	}
	return true
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
	if meta.Billing != "" {
		cur.Billing = meta.Billing
	}
	if meta.MaxOutputTokens > 0 {
		cur.MaxOutputTokens = meta.MaxOutputTokens
	}
	if meta.BYOK {
		cur.BYOK = true
	}
	return context.WithValue(ctx, metaKey{}, cur)
}

type CostObserver func(costUSD float64, costKnown bool, sent bool)

type costObserverKey struct{}

// WithCostObserver attaches an observer to ctx that receives cost notifications
// from guarded model calls executed with ctx. If an observer is already attached,
// both observers are invoked.
func WithCostObserver(ctx context.Context, fn CostObserver) context.Context {
	if fn == nil {
		return ctx
	}
	if prev, ok := ctx.Value(costObserverKey{}).(CostObserver); ok && prev != nil {
		combined := func(costUSD float64, costKnown bool, sent bool) {
			prev(costUSD, costKnown, sent)
			fn(costUSD, costKnown, sent)
		}
		return context.WithValue(ctx, costObserverKey{}, CostObserver(combined))
	}
	return context.WithValue(ctx, costObserverKey{}, fn)
}

// ObserveCallCost notifies any CostObserver attached to ctx of a model call's
// cost outcome.
func ObserveCallCost(ctx context.Context, costUSD float64, costKnown bool, sent bool) {
	if ctx == nil {
		return
	}
	if fn, ok := ctx.Value(costObserverKey{}).(CostObserver); ok && fn != nil {
		fn(costUSD, costKnown, sent)
	}
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
	SpendWindow     time.Duration

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
		SpendWindow:                   defaultSpendWindow,
		Scopes: map[string]ScopePolicy{
			ScopeCompaction: {MaxPerMinute: 6, MaxPerHour: 60, MaxInFlight: 1},
			ScopeExtract:    {MaxPerMinute: 6, MaxPerHour: 60, MaxInFlight: 1},
			ScopeDream:      {MaxPerMinute: 4, MaxPerHour: 24, MaxInFlight: 1},
			ScopeOuter:      {MaxPerMinute: 30, MaxPerHour: 600, MaxInFlight: 8},
			ScopeClassify:   {MaxPerMinute: 30, MaxPerHour: 600, MaxInFlight: 8},
			ScopeReview:     {MaxPerMinute: 30, MaxPerHour: 600, MaxInFlight: 8},
			ScopeTask:       {MaxPerMinute: 60, MaxPerHour: 1200, MaxInFlight: 16},
			ScopeSearch:     {MaxPerMinute: 30, MaxPerHour: 300, MaxInFlight: 8},
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
	if cfg.SpendWindow <= 0 {
		cfg.SpendWindow = def.SpendWindow
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

const (
	maxScopes   = 2048
	maxCircuits = 1024
)

type scopeState struct {
	inFlight    int
	minuteCalls []time.Time
	hourCalls   []time.Time
	lastUsed    time.Time
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
	halfOpenInFlight  bool
	circuitReason     string
	accessErr         error
	lastUsed          time.Time
}

type dupKey struct {
	user     string
	scope    string
	endpoint string
	bodyHash string
}

type spendEntry struct {
	at      time.Time
	usd     float64
	unknown bool
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
	spendEntries   []spendEntry
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

func (g *Guard) spendWindow() time.Duration {
	if g.cfg.SpendWindow > 0 {
		return g.cfg.SpendWindow
	}
	return defaultSpendWindow
}

func (g *Guard) pruneSpendLocked(now time.Time) {
	cutoff := now.Add(-g.spendWindow())
	keep := 0
	var actual, unknown float64
	for _, entry := range g.spendEntries {
		if !entry.at.Before(cutoff) {
			g.spendEntries[keep] = entry
			keep++
			if entry.unknown {
				unknown += entry.usd
			} else {
				actual += entry.usd
			}
		}
	}
	g.spendEntries = g.spendEntries[:keep]
	g.stats.RecordedActualUSD = actual
	g.stats.EstimatedUnknownUSD = unknown
}

func (g *Guard) recordSpendLocked(now time.Time, usd float64, unknown bool) {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		g.pruneSpendLocked(now)
		return
	}
	g.spendEntries = append(g.spendEntries, spendEntry{at: now, usd: usd, unknown: unknown})
	g.pruneSpendLocked(now)
}

func (g *Guard) RecordActualCost(usd float64) {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return
	}
	now := g.now()
	g.mu.Lock()
	g.recordSpendLocked(now, usd, false)
	g.mu.Unlock()
	if g.parent != nil {
		g.parent.RecordActualCost(usd)
	}
}

func (g *Guard) RecordUnknownCost(usd float64) {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return
	}
	now := g.now()
	g.mu.Lock()
	g.recordSpendLocked(now, usd, true)
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
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneSpendLocked(now)
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

func (t *guardedTransport) Unwrap() http.RoundTripper {
	return t.base
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

	if testing.Testing() && !t.guard.cfg.AllowLiveInTests && isRealNetworkTransport(t.base) && !netguard.IsLoopbackHost(req.URL.Hostname()) {
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

	mu     sync.Mutex
	cost   *float64
	failed error
}

func (b *meteredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.mu.Lock()
		b.failed = err
		b.mu.Unlock()
	}
	return n, err
}

func (b *meteredBody) setCost(usd float64) {
	b.mu.Lock()
	v := usd
	b.cost = &v
	b.mu.Unlock()
}

func (b *meteredBody) Close() error {
	err := b.ReadCloser.Close()
	b.mu.Lock()
	cost := b.cost
	failed := b.failed
	b.mu.Unlock()
	b.settle(b.status, cost, errors.Join(failed, err), nil)
	return err
}

type responseMeterKey struct{}

func RecordResponseCost(res *http.Response, usd float64) {
	if res == nil || res.Request == nil {
		return
	}
	if body, ok := res.Request.Context().Value(responseMeterKey{}).(*meteredBody); ok && usd >= 0 && !math.IsNaN(usd) && !math.IsInf(usd, 0) {
		body.setCost(usd)
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
	if override.Billing != "" {
		out.Billing = override.Billing
	}
	if override.MaxOutputTokens > 0 {
		out.MaxOutputTokens = override.MaxOutputTokens
	}
	if override.BYOK {
		out.BYOK = true
	}
	return out
}

var roundTripperType = reflect.TypeOf((*http.RoundTripper)(nil)).Elem()

func isRealNetworkTransport(rt http.RoundTripper) bool {
	return isRealNetworkTransportDepth(rt, 0)
}

func isRealNetworkTransportDepth(rt http.RoundTripper, depth int) bool {
	if depth > 16 {
		return false
	}
	if rt == nil {
		return true
	}
	if _, isHTTP := rt.(*http.Transport); isHTTP {
		return true
	}
	if unwrapper, ok := rt.(interface{ Unwrap() http.RoundTripper }); ok {
		return isRealNetworkTransportDepth(unwrapper.Unwrap(), depth+1)
	}
	val := reflect.ValueOf(rt)
	if val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return false
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return false
	}
	typ := val.Type()
	for i := range typ.NumField() {
		fieldType := typ.Field(i).Type
		if !fieldType.Implements(roundTripperType) {
			continue
		}
		fv := val.Field(i)
		if fv.Kind() == reflect.Interface && fv.IsNil() {
			continue
		}
		if fv.Kind() == reflect.Pointer && fv.IsNil() {
			continue
		}
		if fieldType == reflect.TypeOf((*http.Transport)(nil)) {
			return true
		}
		if fv.CanInterface() {
			if inner, ok := fv.Interface().(http.RoundTripper); ok && isRealNetworkTransportDepth(inner, depth+1) {
				return true
			}
		} else if fv.Kind() == reflect.Interface {
			innerVal := fv.Elem()
			if innerVal.IsValid() && innerVal.Type() == reflect.TypeOf((*http.Transport)(nil)) {
				return !innerVal.IsNil()
			}
			if inspectValueForRealTransport(innerVal, depth+1) {
				return true
			}
		} else if inspectValueForRealTransport(fv, depth+1) {
			return true
		}
	}
	return false
}

func inspectValueForRealTransport(val reflect.Value, depth int) bool {
	if depth > 16 || !val.IsValid() {
		return false
	}
	if val.Type() == reflect.TypeOf((*http.Transport)(nil)) {
		return !val.IsNil()
	}
	if val.Kind() == reflect.Interface {
		if val.IsNil() {
			return false
		}
		return inspectValueForRealTransport(val.Elem(), depth+1)
	}
	if val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return false
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return false
	}
	typ := val.Type()
	for i := range typ.NumField() {
		if !typ.Field(i).Type.Implements(roundTripperType) {
			continue
		}
		if inspectValueForRealTransport(val.Field(i), depth+1) {
			return true
		}
	}
	return false
}

type dispatchTransport struct {
	base  http.RoundTripper
	check func(context.Context) error
}

func (t dispatchTransport) Unwrap() http.RoundTripper {
	return t.base
}

func NewCheckedClient(check func(context.Context) error) *http.Client {
	return &http.Client{Transport: dispatchTransport{base: http.DefaultTransport, check: check}}
}

func (t dispatchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.check(req.Context()); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("%w: %w", ErrDispatchBlocked, err)
	}
	return t.base.RoundTrip(req)
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

const ProviderHome = "home"

func EstimateCallUSD(meta CallMeta, bodyBytes int) float64 {
	if meta.Scope == ScopeSearch || strings.EqualFold(meta.Provider, "exa") {
		return exaSearchEstimateUSD
	}
	return usage.EstimateCallUSD(meta.Provider, meta.Model, bodyBytes, meta.MaxOutputTokens)
}

func (g *Guard) admit(ctx context.Context, meta CallMeta, endpoint, bodyHash string, bodyBytes int, identity [32]byte) (func(int, *float64, error, error), error) {
	now := g.now()
	sk := scopeKey{user: meta.UserID, scope: meta.Scope}
	policy := g.policyFor(meta.Scope)

	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.scopes[sk]
	if st == nil {
		if len(g.scopes) >= maxScopes {
			g.evictScopesLocked(now.Add(-time.Hour))
		}
		st = &scopeState{}
		g.scopes[sk] = st
	}
	st.lastUsed = now

	ck := circuitKey{user: meta.UserID, scope: meta.Scope, identity: identity}
	cs := g.circuits[ck]
	if cs == nil {
		if len(g.circuits) >= maxCircuits {
			g.evictCircuitsLocked(now)
		}
		cs = &circuitState{}
		g.circuits[ck] = cs
	}
	cs.lastUsed = now

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
	if cs.halfOpenInFlight {
		g.stats.TotalBlocked++
		g.stats.BlockedCircuit++
		slog.WarnContext(ctx, "llmguard rejected call: circuit breaker half-open probe already in flight",
			"user_id", meta.UserID, "scope", meta.Scope, "reason", cs.circuitReason)
		if cs.accessErr != nil {
			return nil, fmt.Errorf("%w: %w", cs.accessErr, ErrCircuitOpen)
		}
		return nil, fmt.Errorf("%w (half-open probe in flight)", ErrCircuitOpen)
	}
	enteringHalfOpen := !cs.circuitOpenUntil.IsZero() && !now.Before(cs.circuitOpenUntil)

	if g.cfg.MaxTotalCalls > 0 && g.stats.TotalAdmitted >= int64(g.cfg.MaxTotalCalls) {
		g.stats.TotalBlocked++
		g.stats.BlockedCallLimit++
		slog.ErrorContext(ctx, "llmguard rejected call: total call limit exceeded",
			"user_id", meta.UserID, "scope", meta.Scope,
			"total_admitted", g.stats.TotalAdmitted, "max_calls", g.cfg.MaxTotalCalls)
		return nil, fmt.Errorf("%w (admitted %d >= limit %d)", ErrCallLimitExceeded, g.stats.TotalAdmitted, g.cfg.MaxTotalCalls)
	}

	hosted := meta.isHosted()
	estCallUSD := EstimateCallUSD(meta, bodyBytes)
	hostedEstUSD := 0.0
	if hosted {
		hostedEstUSD = estCallUSD
		g.pruneSpendLocked(now)
	}
	if g.cfg.MaxEstimatedUSD > 0 && hosted {
		projected := g.stats.RecordedActualUSD + g.stats.EstimatedUnknownUSD + g.stats.InFlightEstimatedUSD + hostedEstUSD
		if projected > g.cfg.MaxEstimatedUSD {
			g.stats.TotalBlocked++
			g.stats.BlockedBudget++
			slog.ErrorContext(ctx, "llmguard rejected call: estimated spend ceiling exceeded",
				"user_id", meta.UserID, "scope", meta.Scope, "billing", meta.Billing,
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

	if enteringHalfOpen {
		cs.halfOpenInFlight = true
		cs.circuitOpenUntil = time.Time{}
		cs.consecutiveAborts = 0
		cs.consecutiveErrors = 0
	}
	if hasDup {
		g.duplicates[dk] = append(dupRecent, now)
		if len(g.duplicates) > 2048 {
			g.gcDuplicatesLocked(dupCutoff)
		}
	}
	g.globalInFlight++
	st.inFlight++
	g.stats.InFlightEstimatedUSD += hostedEstUSD
	g.globalMinute = append(g.globalMinute, now)
	st.minuteCalls = append(st.minuteCalls, now)
	st.hourCalls = append(st.hourCalls, now)
	g.stats.TotalAdmitted++

	var once sync.Once
	settle := func(status int, actual *float64, resultErr, accessErr error) {
		once.Do(func() {
			finishedAt := g.now()
			g.mu.Lock()
			if g.globalInFlight > 0 {
				g.globalInFlight--
			}
			if st.inFlight > 0 {
				st.inFlight--
			}
			if enteringHalfOpen {
				cs.halfOpenInFlight = false
			}
			g.stats.InFlightEstimatedUSD = max(0, g.stats.InFlightEstimatedUSD-hostedEstUSD)
			if errors.Is(resultErr, ErrDispatchBlocked) {
				if g.stats.TotalAdmitted > 0 {
					g.stats.TotalAdmitted--
				}
				g.globalMinute = removeTimestamp(g.globalMinute, now)
				st.minuteCalls = removeTimestamp(st.minuteCalls, now)
				st.hourCalls = removeTimestamp(st.hourCalls, now)
				if hasDup {
					g.duplicates[dk] = removeTimestamp(g.duplicates[dk], now)
				}
				g.mu.Unlock()
				if parentSettle != nil {
					parentSettle(status, actual, resultErr)
				}
				return
			}
			if hosted {
				if actual != nil {
					g.recordSpendLocked(finishedAt, *actual, false)
				} else if resultErr != nil || (status >= 200 && status < 300) {
					g.recordSpendLocked(finishedAt, hostedEstUSD, true)
				}
			}
			g.observeLocked(ctx, finishedAt, cs, meta, status, resultErr, accessErr)
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

	hosted := meta.isHosted()
	hostedEstUSD := 0.0
	if hosted {
		hostedEstUSD = estCallUSD
		g.pruneSpendLocked(now)
	}
	if g.cfg.MaxEstimatedUSD > 0 && hosted {
		projected := g.stats.RecordedActualUSD + g.stats.EstimatedUnknownUSD + g.stats.InFlightEstimatedUSD + hostedEstUSD
		if projected > g.cfg.MaxEstimatedUSD {
			g.stats.TotalBlocked++
			g.stats.BlockedBudget++
			slog.ErrorContext(ctx, "llmguard rejected call: global estimated spend ceiling exceeded",
				"user_id", meta.UserID, "scope", meta.Scope, "billing", meta.Billing,
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
	g.stats.InFlightEstimatedUSD += hostedEstUSD
	g.globalMinute = append(g.globalMinute, now)
	g.stats.TotalAdmitted++

	var once sync.Once
	return func(status int, actual *float64, resultErr error) {
		once.Do(func() {
			finishedAt := g.now()
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.globalInFlight > 0 {
				g.globalInFlight--
			}
			g.stats.InFlightEstimatedUSD = max(0, g.stats.InFlightEstimatedUSD-hostedEstUSD)
			if errors.Is(resultErr, ErrDispatchBlocked) {
				if g.stats.TotalAdmitted > 0 {
					g.stats.TotalAdmitted--
				}
				g.globalMinute = removeTimestamp(g.globalMinute, now)
				return
			}
			if hosted {
				if actual != nil {
					g.recordSpendLocked(finishedAt, *actual, false)
				} else if resultErr != nil || (status >= 200 && status < 300) {
					g.recordSpendLocked(finishedAt, hostedEstUSD, true)
				}
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

func removeTimestamp(ts []time.Time, target time.Time) []time.Time {
	if i := slices.Index(ts, target); i >= 0 {
		return slices.Delete(ts, i, i+1)
	}
	return ts
}

func (g *Guard) evictScopesLocked(hourCutoff time.Time) {
	for k, st := range g.scopes {
		if st.inFlight > 0 {
			continue
		}
		st.hourCalls = pruneBefore(st.hourCalls, hourCutoff)
		if len(st.hourCalls) == 0 {
			delete(g.scopes, k)
		}
	}
	if len(g.scopes) < maxScopes {
		return
	}
	var oldestKey scopeKey
	var oldestTime time.Time
	found := false
	for k, st := range g.scopes {
		if st.inFlight > 0 {
			continue
		}
		if !found || st.lastUsed.Before(oldestTime) {
			oldestKey = k
			oldestTime = st.lastUsed
			found = true
		}
	}
	if found {
		delete(g.scopes, oldestKey)
	}
}

func (g *Guard) evictCircuitsLocked(now time.Time) {
	for k, existing := range g.circuits {
		if !existing.halfOpenInFlight && !now.Before(existing.circuitOpenUntil) {
			delete(g.circuits, k)
		}
	}
	if len(g.circuits) < maxCircuits {
		return
	}
	var oldestKey circuitKey
	var oldestTime time.Time
	found := false
	for k, existing := range g.circuits {
		if existing.halfOpenInFlight {
			continue
		}
		if !found || existing.lastUsed.Before(oldestTime) {
			oldestKey = k
			oldestTime = existing.lastUsed
			found = true
		}
	}
	if found {
		delete(g.circuits, oldestKey)
	}
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
