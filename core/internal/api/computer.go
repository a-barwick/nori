package api

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"nori/internal/billing"
	"nori/internal/computer"
)

const (
	desktopWSLimitExceededCode = "desktop_ws_limit_exceeded"
	defaultDesktopWSRetryAfter = 5

	// Distinct server-initiated WebSocket close codes in the 4000-4099 application range so
	// desktop clients stop auto-reconnecting and cannot hold Sprites awake indefinitely.
	DesktopWSCloseMaxLifetime    websocket.StatusCode = 4001
	DesktopWSCloseIdleSleeping   websocket.StatusCode = 4002
	DesktopWSCloseEntitlement    websocket.StatusCode = 4003
	DesktopWSCloseSessionRevoked websocket.StatusCode = 4004
)

func desktopCloseCodeForReason(reason computer.DesktopCloseReason) (websocket.StatusCode, string) {
	switch reason {
	case computer.DesktopCloseMaxLifetime:
		return DesktopWSCloseMaxLifetime, "max session lifetime reached"
	case computer.DesktopCloseIdleSleeping:
		return DesktopWSCloseIdleSleeping, "computer idle or sleeping"
	case computer.DesktopCloseEntitlement:
		return DesktopWSCloseEntitlement, "account access or hosted credit unavailable"
	default:
		return DesktopWSCloseSessionRevoked, "desktop session revoked"
	}
}

// ComputerAPI exposes /v1/computer/* management endpoints and the authenticated
// RFB WebSocket reverse proxy for live desktop viewing and user takeover.
type ComputerAPI struct {
	Manager        *computer.Manager
	UserID         string
	RateLimiter    *RateLimiter
	Billing        *billing.Service
	MaxWSLifetime  time.Duration // default computer.DefaultDesktopWSMaxLifetime (2h)
	WSPollInterval time.Duration // default computer.DefaultDesktopWSPollInterval (5s)

	wsMu      sync.Mutex
	wsCancels map[string]map[uint64]func(computer.DesktopCloseReason)
	wsNextID  uint64
}

// Register attaches the /v1/computer/* endpoints to mux.
func (c *ComputerAPI) Register(mux *http.ServeMux) {
	c.register(mux)
}

func (c *ComputerAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/computer", c.status)
	mux.HandleFunc("POST /v1/computer/wake", c.wake)
	mux.HandleFunc("POST /v1/computer/sleep", c.sleep)
	mux.HandleFunc("POST /v1/computer/reset", c.reset)
	mux.HandleFunc("GET /v1/computer/checkpoints", c.listCheckpoints)
	mux.HandleFunc("POST /v1/computer/checkpoints", optionalJSONBody(c.createCheckpoint))
	mux.HandleFunc("POST /v1/computer/restore", optionalJSONBody(c.restore))
	mux.HandleFunc("POST /v1/computer/sessions", optionalJSONBody(c.createSession))
	mux.HandleFunc("PATCH /v1/computer/sessions/{id}", jsonBody(c.updateSession))
	mux.HandleFunc("DELETE /v1/computer/sessions/{id}", c.endSession)
	mux.HandleFunc("GET /v1/computer/sessions/{id}/ws", c.proxyDesktopWS)
}

// CloseDesktopSessions immediately closes all active Desktop VNC WebSocket connections for userID
// registered on this handler and its underlying computer.Manager.
func (c *ComputerAPI) CloseDesktopSessions(userID string) int {
	return c.CloseDesktopSessionsWithReason(userID, computer.DesktopCloseSessionRevoked)
}

// CloseDesktopSessionsWithReason immediately closes all active Desktop VNC WebSocket connections
// for userID with an explicit server-initiated close reason.
func (c *ComputerAPI) CloseDesktopSessionsWithReason(userID string, reason computer.DesktopCloseReason) int {
	if c == nil {
		return 0
	}
	closed := 0
	if c.Manager != nil {
		closed += c.Manager.CloseDesktopSessionsWithReason(userID, reason)
	}
	c.wsMu.Lock()
	subs := c.wsCancels[userID]
	delete(c.wsCancels, userID)
	cancels := make([]func(computer.DesktopCloseReason), 0, len(subs))
	for _, fn := range subs {
		cancels = append(cancels, fn)
	}
	c.wsMu.Unlock()
	for _, fn := range cancels {
		fn(reason)
	}
	if closed == 0 {
		closed = len(cancels)
	}
	return closed
}

func (c *ComputerAPI) registerLocalWS(userID string, closeFn func(computer.DesktopCloseReason)) func() {
	c.wsMu.Lock()
	c.wsNextID++
	id := c.wsNextID
	if c.wsCancels == nil {
		c.wsCancels = map[string]map[uint64]func(computer.DesktopCloseReason){}
	}
	if c.wsCancels[userID] == nil {
		c.wsCancels[userID] = map[uint64]func(computer.DesktopCloseReason){}
	}
	c.wsCancels[userID][id] = closeFn
	c.wsMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.wsMu.Lock()
			if subs := c.wsCancels[userID]; subs != nil {
				delete(subs, id)
				if len(subs) == 0 {
					delete(c.wsCancels, userID)
				}
			}
			c.wsMu.Unlock()
		})
	}
}

func (c *ComputerAPI) billingService() *billing.Service {
	if c == nil {
		return nil
	}
	if c.Billing != nil {
		return c.Billing
	}
	if c.Manager != nil {
		return c.Manager.Billing()
	}
	return nil
}

func (c *ComputerAPI) checkDesktopEntitlement(ctx context.Context) (billing.Entitlement, error) {
	if c.Manager != nil && c.Manager.Billing() != nil && (c.Billing == nil || c.Billing == c.Manager.Billing()) {
		return c.Manager.CheckDesktopAccess(ctx, c.UserID)
	}
	bsvc := c.billingService()
	if bsvc == nil {
		return billing.Entitlement{}, nil
	}
	ent, err := bsvc.CheckAccess(ctx, c.UserID)
	if err != nil {
		return ent, err
	}
	if c.Manager != nil && c.Manager.MeteringDisabled() {
		return ent, nil
	}
	bal, err := bsvc.Store.CreditBalance(ctx, c.UserID)
	if err != nil {
		return ent, err
	}
	if bal.AvailableMicros <= 0 {
		return ent, billing.ErrCreditExhausted
	}
	if bsvc.GlobalBudget.HostedAllMaxMicrosPerDay > 0 {
		used, gErr := bsvc.Store.GetGlobalBudget(ctx, time.Now().UTC(), billing.ScopeHostedAll)
		if gErr == nil && used.SpentMicros >= bsvc.GlobalBudget.HostedAllMaxMicrosPerDay {
			return ent, billing.ErrGlobalBudgetExceeded
		}
	}
	return ent, nil
}

func (c *ComputerAPI) maxDesktopWS(ent billing.Entitlement) int {
	if c.billingService() != nil && ent.Tier != "" {
		if n := ent.EffectivePolicy().MaxDesktopWS; n > 0 {
			return n
		}
	}
	return 1
}

func (c *ComputerAPI) wsMaxLifetime() time.Duration {
	if c != nil && c.MaxWSLifetime > 0 {
		return c.MaxWSLifetime
	}
	return computer.DefaultDesktopWSMaxLifetime
}

func (c *ComputerAPI) wsPollInterval() time.Duration {
	if c != nil && c.WSPollInterval > 0 {
		return c.WSPollInterval
	}
	return computer.DefaultDesktopWSPollInterval
}

// optionalJSONBody enforces JSON media type only when a non-empty request body is sent,
// allowing bodyless POSTs to match the OpenAPI optional requestBody contract.
func optionalJSONBody(next http.HandlerFunc) http.HandlerFunc {
	strict := jsonBody(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.ContentLength == 0 {
			next(w, r)
			return
		}
		strict(w, r)
	}
}

type computerStatusResponse struct {
	Enabled              bool                      `json:"enabled"`
	ComputerID           string                    `json:"computer_id,omitempty"`
	Provider             string                    `json:"provider"`
	Status               string                    `json:"status"`
	BaselineCheckpointID string                    `json:"baseline_checkpoint_id,omitempty"`
	DiskUsedBytes        int64                     `json:"disk_used_bytes"`
	DiskMaxBytes         int64                     `json:"disk_max_bytes"`
	Checkpoints          []computer.CheckpointInfo `json:"checkpoints"`
	ActiveSessions       []computerSessionSummary  `json:"active_sessions"`
	LastActiveAt         *time.Time                `json:"last_active_at,omitempty"`
}

type computerSessionSummary struct {
	ID            string     `json:"id"`
	ComputerID    string     `json:"computer_id"`
	Mode          string     `json:"mode"`
	TaskID        string     `json:"task_id,omitempty"`
	InteractionID string     `json:"interaction_id,omitempty"`
	ExpiresAt     time.Time  `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	LastActivity  *time.Time `json:"last_activity_at,omitempty"`
}

type createComputerSessionRequest struct {
	Mode          string `json:"mode,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	InteractionID string `json:"interaction_id,omitempty"`
}

type createComputerSessionResponse struct {
	ID            string    `json:"id"`
	ComputerID    string    `json:"computer_id"`
	Mode          string    `json:"mode"`
	TaskID        string    `json:"task_id,omitempty"`
	InteractionID string    `json:"interaction_id,omitempty"`
	Token         string    `json:"token"`
	WSPath        string    `json:"ws_path"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type updateComputerSessionRequest struct {
	Mode string `json:"mode"`
}

type createCheckpointRequest struct {
	Label string `json:"label,omitempty"`
}

type restoreComputerRequest struct {
	CheckpointID string `json:"checkpoint_id,omitempty"`
}

func formatComputerStatus(view computer.StatusView, enabled bool) computerStatusResponse {
	ckpts := view.Checkpoints
	if ckpts == nil {
		ckpts = []computer.CheckpointInfo{}
	}
	sessions := make([]computerSessionSummary, 0, len(view.ActiveSessions))
	for _, s := range view.ActiveSessions {
		last := s.LastActivityAt
		sessions = append(sessions, computerSessionSummary{
			ID:            s.ID,
			ComputerID:    s.ComputerID,
			Mode:          string(s.Mode),
			TaskID:        s.TaskID,
			InteractionID: s.InteractionID,
			ExpiresAt:     s.ExpiresAt,
			CreatedAt:     s.CreatedAt,
			LastActivity:  &last,
		})
	}
	return computerStatusResponse{
		Enabled:              enabled,
		ComputerID:           view.ComputerID,
		Provider:             view.Provider,
		Status:               string(view.Status),
		BaselineCheckpointID: view.BaselineCheckpointID,
		DiskUsedBytes:        view.DiskUsedBytes,
		DiskMaxBytes:         view.DiskMaxBytes,
		Checkpoints:          ckpts,
		ActiveSessions:       sessions,
		LastActiveAt:         view.LastActiveAt,
	}
}

func (c *ComputerAPI) status(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeJSON(w, http.StatusOK, computerStatusResponse{
			Enabled:        false,
			Provider:       "disabled",
			Status:         string(computer.StatusNotProvisioned),
			Checkpoints:    []computer.CheckpointInfo{},
			ActiveSessions: []computerSessionSummary{},
		})
		return
	}
	view, err := c.Manager.Status(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, formatComputerStatus(view, true))
}

func (c *ComputerAPI) wake(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	view, err := c.Manager.Wake(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, formatComputerStatus(view, true))
}

func (c *ComputerAPI) sleep(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	if err := c.Manager.Sleep(r.Context(), c.UserID); err != nil {
		writeComputerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ComputerAPI) reset(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	view, err := c.Manager.Reset(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, formatComputerStatus(view, true))
}

func (c *ComputerAPI) listCheckpoints(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	checkpoints, err := c.Manager.ListCheckpoints(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	if checkpoints == nil {
		checkpoints = []computer.CheckpointInfo{}
	}
	writeJSON(w, http.StatusOK, checkpoints)
}

func (c *ComputerAPI) createCheckpoint(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	var req createCheckpointRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decode(w, r, &req) {
			return
		}
	}
	id, err := c.Manager.Checkpoint(r.Context(), c.UserID, strings.TrimSpace(req.Label))
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, computer.CheckpointInfo{
		ID:        id,
		Label:     strings.TrimSpace(req.Label),
		CreatedAt: time.Now().UTC(),
	})
}

func (c *ComputerAPI) restore(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	var req restoreComputerRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decode(w, r, &req) {
			return
		}
	}
	if err := c.Manager.Restore(r.Context(), c.UserID, strings.TrimSpace(req.CheckpointID)); err != nil {
		writeComputerError(w, r, err)
		return
	}
	view, err := c.Manager.Status(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, formatComputerStatus(view, true))
}

func (c *ComputerAPI) createSession(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	var req createComputerSessionRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decode(w, r, &req) {
			return
		}
	}
	mode := computer.SessionMode(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = computer.SessionModeView
	}
	created, err := c.Manager.CreateSession(r.Context(), c.UserID, mode, strings.TrimSpace(req.TaskID), strings.TrimSpace(req.InteractionID))
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, createComputerSessionResponse{
		ID:            created.Session.ID,
		ComputerID:    created.Session.ComputerID,
		Mode:          string(created.Session.Mode),
		TaskID:        created.Session.TaskID,
		InteractionID: created.Session.InteractionID,
		Token:         created.RawToken,
		WSPath:        "/v1/computer/sessions/" + created.Session.ID + "/ws",
		ExpiresAt:     created.Session.ExpiresAt,
	})
}

func (c *ComputerAPI) updateSession(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	var req updateComputerSessionRequest
	if !decode(w, r, &req) {
		return
	}
	sess, err := c.Manager.SetSessionMode(r.Context(), c.UserID, r.PathValue("id"), computer.SessionMode(strings.TrimSpace(req.Mode)))
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	last := sess.LastActivityAt
	writeJSON(w, http.StatusOK, computerSessionSummary{
		ID:            sess.ID,
		ComputerID:    sess.ComputerID,
		Mode:          string(sess.Mode),
		TaskID:        sess.TaskID,
		InteractionID: sess.InteractionID,
		ExpiresAt:     sess.ExpiresAt,
		CreatedAt:     sess.CreatedAt,
		LastActivity:  &last,
	})
}

func (c *ComputerAPI) endSession(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	if err := c.Manager.EndSession(r.Context(), c.UserID, r.PathValue("id")); err != nil && !errors.Is(err, computer.ErrNotFound) {
		writeComputerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ComputerAPI) proxyDesktopWS(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, r, computer.ErrDisabled)
		return
	}
	ent, err := c.checkDesktopEntitlement(r.Context())
	if err != nil {
		writeComputerError(w, r, err)
		return
	}

	releaseWS := func() {}
	if c.RateLimiter != nil {
		maxWS := c.maxDesktopWS(ent)
		var ok bool
		releaseWS, ok = c.RateLimiter.AcquireDesktopWS(c.UserID, maxWS)
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(defaultDesktopWSRetryAfter))
			writeJSON(w, http.StatusTooManyRequests, apiError{
				Error:     "Too many concurrent desktop sessions. Close an existing desktop tab to connect.",
				Code:      desktopWSLimitExceededCode,
				Retryable: true,
			})
			return
		}
	}
	defer releaseWS()

	sessionID := r.PathValue("id")
	rawToken := strings.TrimSpace(r.URL.Query().Get("token"))
	if rawToken == "" {
		rawToken = strings.TrimSpace(r.Header.Get("X-Nori-Computer-Token"))
	}
	// Atomically consume the single-use session token so replayed tokens cannot open a second socket.
	sess, err := c.Manager.ConsumeSession(r.Context(), c.UserID, sessionID, rawToken)
	if err != nil {
		writeComputerError(w, r, err)
		return
	}
	target, err := c.Manager.DesktopTarget(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, r, err)
		return
	}

	dialCtx, dialCancel := context.WithTimeout(r.Context(), 15*time.Second)
	upstream, _, err := websocket.Dial(dialCtx, target.WSURL, &websocket.DialOptions{
		HTTPHeader:   target.Headers,
		Subprotocols: []string{"binary"},
	})
	dialCancel()
	if err != nil {
		slog.WarnContext(r.Context(), "computer desktop upstream websocket dial failed", "session_id", sessionID, "err", err)
		writeJSON(w, http.StatusBadGateway, apiError{Error: "Could not connect to your computer desktop.", Code: "computer_desktop_unavailable", Retryable: true})
		return
	}
	defer upstream.Close(websocket.StatusNormalClosure, "")
	upstream.SetReadLimit(16 << 20)

	var clientConn *websocket.Conn
	clientReady := make(chan struct{})
	var closeOnce sync.Once
	var closeDone chan struct{}
	proxyCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// closeClient sends the WebSocket Close frame asynchronously BEFORE cancelling proxyCtx so:
	//  1. Callers like mgr.Sleep, EndSession, and HandleEntitlementChanged never block on a slow WS close handshake.
	//  2. coder/websocket delivers the exact 400x status code rather than aborting the read with 1006.
	closeClient := func(code websocket.StatusCode, reason string) {
		closeOnce.Do(func() {
			done := make(chan struct{})
			closeDone = done
			go func() {
				defer close(done)
				<-clientReady
				if clientConn != nil {
					_ = clientConn.Close(code, reason)
				}
				cancel()
			}()
		})
	}
	defer func() {
		closeOnce.Do(func() {
			if clientConn != nil {
				_ = clientConn.Close(websocket.StatusNormalClosure, "")
			}
			cancel()
		})
		if closeDone != nil {
			<-closeDone
		}
	}()
	// Register before sending the upgrade response so a sleep or revocation during
	// the handshake can queue a close frame for the accepted socket.
	onCloseReason := func(reason computer.DesktopCloseReason) {
		code, msg := desktopCloseCodeForReason(reason)
		closeClient(code, msg)
	}
	unregMgr := c.Manager.RegisterDesktopSessionWithReason(c.UserID, onCloseReason)
	defer unregMgr()
	unregLocal := c.registerLocalWS(c.UserID, onCloseReason)
	defer unregLocal()

	var currentMode atomic.Pointer[computer.SessionMode]
	currentMode.Store(&sess.Mode)
	var lastTouch atomic.Int64
	lastTouch.Store(sess.LastActivityAt.Unix())
	idleCap := c.Manager.SessionIdleCap()

	// Subscribe synchronously so PATCH mode downgrades ("control" -> "view") and DELETE
	// revocations take effect IMMEDIATELY before the HTTP call returns (zero wait window).
	unsub := c.Manager.SubscribeSession(sessionID, func(ev computer.SessionEvent) {
		currentMode.Store(&ev.Mode)
		if ev.Revoked {
			closeClient(DesktopWSCloseSessionRevoked, "desktop session revoked")
		}
	})
	defer unsub()

	clientConn, err = websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"binary"},
		InsecureSkipVerify: true, // Protected by Nori auth + single-use short-lived user-bound session token.
	})
	close(clientReady)
	if err != nil {
		slog.WarnContext(r.Context(), "computer desktop client websocket accept failed", "session_id", sessionID, "err", err)
		return
	}
	clientConn.SetReadLimit(4 << 20)

	// Enforce 2-hour hard maximum WebSocket session lifetime (close code 4001).
	lifetimeTimer := time.AfterFunc(c.wsMaxLifetime(), func() {
		closeClient(DesktopWSCloseMaxLifetime, "max session lifetime reached")
	})
	defer lifetimeTimer.Stop()

	// Cross-replica poll fallback (5s cadence): enforces live entitlement/suspension/credit checks (4003),
	// computer sleeping/idle timeout (4002), and DB revocation (4004).
	go func() {
		ticker := time.NewTicker(c.wsPollInterval())
		defer ticker.Stop()
		for {
			select {
			case <-proxyCtx.Done():
				return
			case <-ticker.C:
				if _, entErr := c.checkDesktopEntitlement(proxyCtx); entErr != nil {
					if proxyCtx.Err() == nil {
						closeClient(DesktopWSCloseEntitlement, "account access or hosted credit unavailable")
					}
					return
				}
				if compStatus, stErr := c.Manager.StoredComputerStatus(proxyCtx, c.UserID); stErr == nil &&
					(compStatus == computer.StatusSleeping || compStatus == computer.StatusDeleted || compStatus == computer.StatusDeleting) {
					closeClient(DesktopWSCloseIdleSleeping, "computer is sleeping")
					return
				}
				modeBeforeRead := currentMode.Load()
				cur, gErr := c.Manager.GetSession(proxyCtx, c.UserID, sessionID)
				if proxyCtx.Err() != nil {
					return
				}
				now := time.Now().UTC()
				if gErr != nil || cur.EndedAt != nil {
					closeClient(DesktopWSCloseSessionRevoked, "desktop session revoked")
					return
				}
				if !cur.ExpiresAt.After(now) || now.Sub(cur.LastActivityAt) > idleCap {
					closeClient(DesktopWSCloseIdleSleeping, "desktop session idle or expired")
					return
				}
				// A local PATCH or revocation may have arrived while the store read was in flight.
				currentMode.CompareAndSwap(modeBeforeRead, &cur.Mode)
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// Upstream (noVNC / x11vnc) -> Client
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			msgType, data, rErr := upstream.Read(proxyCtx)
			if rErr != nil {
				return
			}
			if wErr := clientConn.Write(proxyCtx, msgType, data); wErr != nil {
				return
			}
		}
	}()

	// Client -> Upstream (stateful RFB stream parser; drops RFB input frames when mode != "control")
	go func() {
		defer wg.Done()
		defer cancel()
		parser := newRFBClientParser()
		for {
			msgType, data, rErr := clientConn.Read(proxyCtx)
			if rErr != nil {
				return
			}
			mode := *currentMode.Load()
			filtered, _, hadHumanInput, parseErr := parser.Feed(mode, data)
			if parseErr != nil {
				return
			}
			if len(filtered) == 0 {
				continue
			}
			// Only real human input (KeyEvent / PointerEvent / ClientCutText) extends session idle TTL
			// and refreshes computer last_activity_at; unattended FramebufferUpdateRequests do not.
			if hadHumanInput && mode == computer.SessionModeControl {
				nowSec := time.Now().Unix()
				if nowSec-lastTouch.Load() >= 30 {
					lastTouch.Store(nowSec)
					_ = c.Manager.TouchSession(proxyCtx, sessionID)
				}
			}
			if wErr := upstream.Write(proxyCtx, msgType, filtered); wErr != nil {
				return
			}
		}
	}()

	wg.Wait()
}

const maxRFBClientBufferBytes = 256 << 10 // 256 KiB cap on buffered incomplete RFB client frames

// rfbClientParser is a bounded, stateful RFC 6143 client-to-server byte-stream parser.
// It handles arbitrary WebSocket chunk fragmentation and coalesced frames without ever
// forwarding partial or unvalidated frames, and drops all RFB input frames (type 4 KeyEvent,
// type 5 PointerEvent, type 6 ClientCutText, or unknown types) whenever mode != "control".
type rfbClientParser struct {
	stage int // 0=protocolVersion(12B), 1=securityType(1B), 2=clientInit(1B), 3=messages
	buf   []byte
}

func newRFBClientParser() *rfbClientParser {
	return &rfbClientParser{}
}

func (p *rfbClientParser) Feed(mode computer.SessionMode, chunk []byte) (out []byte, droppedAny bool, hadHumanInput bool, err error) {
	if len(chunk) == 0 {
		return nil, false, false, nil
	}
	if len(p.buf)+len(chunk) > maxRFBClientBufferBytes {
		return nil, true, false, errors.New("RFB client buffer limit exceeded")
	}
	p.buf = append(p.buf, chunk...)

	for len(p.buf) > 0 {
		switch p.stage {
		case 0: // ProtocolVersion: exact 12 bytes ("RFB xxx.yyy\n")
			if len(p.buf) < 12 {
				return out, droppedAny, hadHumanInput, nil
			}
			if !strings.HasPrefix(string(p.buf[:4]), "RFB ") || p.buf[11] != '\n' {
				return nil, true, false, errors.New("invalid RFB protocol version")
			}
			out = append(out, p.buf[:12]...)
			p.buf = p.buf[12:]
			p.stage = 1

		case 1: // SecurityType selection: exact 1 byte (only 1=None allowed for loopback x11vnc)
			secType := p.buf[0]
			if secType != 1 {
				return nil, true, false, errors.New("unsupported RFB security type")
			}
			out = append(out, secType)
			p.buf = p.buf[1:]
			p.stage = 2

		case 2: // ClientInit: exact 1 byte (shared flag)
			out = append(out, p.buf[0])
			p.buf = p.buf[1:]
			p.stage = 3

		case 3: // Framed RFB client-to-server messages
			msgType := p.buf[0]
			var msgLen int
			isInput := false
			switch msgType {
			case 0: // SetPixelFormat (20 bytes)
				msgLen = 20
			case 2: // SetEncodings (4 + 4*count bytes)
				if len(p.buf) < 4 {
					return out, droppedAny, hadHumanInput, nil
				}
				count := int(binary.BigEndian.Uint16(p.buf[2:4]))
				msgLen = 4 + count*4
			case 3: // FramebufferUpdateRequest (10 bytes)
				msgLen = 10
			case 4: // KeyEvent (8 bytes)
				msgLen = 8
				isInput = true
			case 5: // PointerEvent (6 bytes)
				msgLen = 6
				isInput = true
			case 6: // ClientCutText (8 + uint32 length bytes)
				if len(p.buf) < 8 {
					return out, droppedAny, hadHumanInput, nil
				}
				textLen := binary.BigEndian.Uint32(p.buf[4:8])
				if textLen > maxRFBClientBufferBytes-8 {
					return nil, true, false, errors.New("RFB ClientCutText exceeds maximum size")
				}
				msgLen = 8 + int(textLen)
				isInput = true
			default:
				// Unknown/extended client message: fail closed so stream framing never desynchronizes with x11vnc.
				return out, true, hadHumanInput, errors.New("unsupported RFB client message type")
			}
			if len(p.buf) < msgLen {
				// Incomplete frame: wait for remaining bytes instead of forwarding partial bytes.
				return out, droppedAny, hadHumanInput, nil
			}
			frame := p.buf[:msgLen]
			p.buf = p.buf[msgLen:]
			if isInput && mode != computer.SessionModeControl {
				droppedAny = true
				continue
			}
			if isInput {
				hadHumanInput = true
			}
			out = append(out, frame...)
		}
	}
	return out, droppedAny, hadHumanInput, nil
}

// FilterRFBClientMessage filters a post-handshake RFB client payload in a single call.
// Retained as a clean helper for unit tests; live WebSocket proxy uses stateful rfbClientParser.
func FilterRFBClientMessage(mode computer.SessionMode, payload []byte) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, false
	}
	p := &rfbClientParser{stage: 3}
	out, dropped, _, err := p.Feed(mode, payload)
	if err != nil {
		return out, true
	}
	return out, dropped
}

func writeComputerError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, msg, retry := 502, "computer_error", "Could not complete computer action.", true
	switch {
	case errors.Is(err, billing.ErrAccountSuspended):
		status, code, msg, retry = http.StatusForbidden, billing.CodeAccountSuspended, "Your account is currently suspended.", false
	case errors.Is(err, billing.ErrEntitlementMissing):
		status, code, msg, retry = http.StatusForbidden, billing.CodeEntitlementMissing, "Account billing entitlement is missing.", false
	case errors.Is(err, billing.ErrCreditExhausted):
		status, code, msg, retry = http.StatusPaymentRequired, billing.CodeCreditExhausted, "Hosted credit balance is exhausted.", false
	case errors.Is(err, billing.ErrSubscriptionInactive):
		status, code, msg, retry = http.StatusPaymentRequired, billing.CodeSubscriptionInactive, "Subscription is inactive.", false
	case errors.Is(err, billing.ErrGlobalBudgetExceeded):
		status, code, msg, retry = http.StatusTooManyRequests, billing.CodeGlobalBudgetExceeded, "Platform daily hosted compute budget has been reached. Try again later.", true
	case errors.Is(err, computer.ErrFleetCapReached):
		status, code, msg, retry = http.StatusServiceUnavailable, "computer_fleet_full", "Personal computer capacity is temporarily full. Try again shortly.", true
	case errors.Is(err, computer.ErrCheckpointLimitReached):
		status, code, msg, retry = http.StatusForbidden, "checkpoint_limit_reached", "You have reached the maximum number of checkpoints for your plan.", false
	case errors.Is(err, computer.ErrDisabled):
		status, code, msg, retry = http.StatusServiceUnavailable, "computer_disabled", "Personal computer is not enabled on this server.", false
	case errors.Is(err, computer.ErrNotFound), errors.Is(err, computer.ErrNotProvisioned):
		status, code, msg, retry = http.StatusNotFound, "computer_not_found", "Computer or session not found.", false
	case errors.Is(err, computer.ErrDeleting):
		status, code, msg, retry = http.StatusConflict, "computer_deleting", "Computer is being deleted.", false
	case errors.Is(err, computer.ErrUserInControl):
		status, code, msg, retry = http.StatusConflict, "computer_user_in_control", "You currently have control of the desktop.", false
	case errors.Is(err, computer.ErrInvalidMode):
		status, code, msg, retry = http.StatusBadRequest, "validation_failed", "Session mode must be view or control.", false
	case errors.Is(err, computer.ErrSessionExpired):
		status, code, msg, retry = http.StatusUnauthorized, "session_expired", "Desktop session has expired. Open Desktop again to reconnect.", false
	case errors.Is(err, computer.ErrSessionRevoked):
		status, code, msg, retry = http.StatusUnauthorized, "session_revoked", "Desktop session has ended.", false
	case errors.Is(err, computer.ErrSessionTokenUsed):
		status, code, msg, retry = http.StatusUnauthorized, "session_token_used", "Desktop session token has already been used. Open Desktop again to reconnect.", false
	}
	if status >= 500 && !errors.Is(err, computer.ErrDisabled) && !errors.Is(err, computer.ErrFleetCapReached) {
		ctx := context.Background()
		method, path := "", ""
		if r != nil {
			ctx = r.Context()
			method, path = r.Method, r.URL.Path
		}
		slog.WarnContext(ctx, "computer api request failed", "method", method, "path", path, "status", status, "code", code, "err", err)
	}
	writeJSON(w, status, apiError{Error: msg, Code: code, Retryable: retry})
}
