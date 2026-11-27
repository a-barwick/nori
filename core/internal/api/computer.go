package api

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"nori/internal/computer"
)

// ComputerAPI exposes /v1/computer/* management endpoints and the authenticated
// RFB WebSocket reverse proxy for live desktop viewing and user takeover.
type ComputerAPI struct {
	Manager *computer.Manager
	UserID  string
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
	mux.HandleFunc("POST /v1/computer/checkpoints", optionalJSONBody(c.createCheckpoint))
	mux.HandleFunc("POST /v1/computer/restore", optionalJSONBody(c.restore))
	mux.HandleFunc("POST /v1/computer/sessions", optionalJSONBody(c.createSession))
	mux.HandleFunc("PATCH /v1/computer/sessions/{id}", jsonBody(c.updateSession))
	mux.HandleFunc("DELETE /v1/computer/sessions/{id}", c.endSession)
	mux.HandleFunc("GET /v1/computer/sessions/{id}/ws", c.proxyDesktopWS)
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
		writeComputerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, formatComputerStatus(view, true))
}

func (c *ComputerAPI) wake(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, computer.ErrDisabled)
		return
	}
	view, err := c.Manager.Wake(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, formatComputerStatus(view, true))
}

func (c *ComputerAPI) sleep(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, computer.ErrDisabled)
		return
	}
	if err := c.Manager.Sleep(r.Context(), c.UserID); err != nil {
		writeComputerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ComputerAPI) reset(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, computer.ErrDisabled)
		return
	}
	view, err := c.Manager.Reset(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, formatComputerStatus(view, true))
}

func (c *ComputerAPI) createCheckpoint(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, computer.ErrDisabled)
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
		writeComputerError(w, err)
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
		writeComputerError(w, computer.ErrDisabled)
		return
	}
	var req restoreComputerRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decode(w, r, &req) {
			return
		}
	}
	if err := c.Manager.Restore(r.Context(), c.UserID, strings.TrimSpace(req.CheckpointID)); err != nil {
		writeComputerError(w, err)
		return
	}
	view, err := c.Manager.Status(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, formatComputerStatus(view, true))
}

func (c *ComputerAPI) createSession(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, computer.ErrDisabled)
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
		writeComputerError(w, err)
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
		writeComputerError(w, computer.ErrDisabled)
		return
	}
	var req updateComputerSessionRequest
	if !decode(w, r, &req) {
		return
	}
	sess, err := c.Manager.SetSessionMode(r.Context(), c.UserID, r.PathValue("id"), computer.SessionMode(strings.TrimSpace(req.Mode)))
	if err != nil {
		writeComputerError(w, err)
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
		writeComputerError(w, computer.ErrDisabled)
		return
	}
	if err := c.Manager.EndSession(r.Context(), c.UserID, r.PathValue("id")); err != nil && !errors.Is(err, computer.ErrNotFound) {
		writeComputerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ComputerAPI) proxyDesktopWS(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Manager == nil || !c.Manager.Enabled() {
		writeComputerError(w, computer.ErrDisabled)
		return
	}
	sessionID := r.PathValue("id")
	rawToken := strings.TrimSpace(r.URL.Query().Get("token"))
	if rawToken == "" {
		rawToken = strings.TrimSpace(r.Header.Get("X-Nori-Computer-Token"))
	}
	sess, err := c.Manager.ValidateSession(r.Context(), c.UserID, sessionID, rawToken)
	if err != nil {
		writeComputerError(w, err)
		return
	}
	target, err := c.Manager.DesktopTarget(r.Context(), c.UserID)
	if err != nil {
		writeComputerError(w, err)
		return
	}

	dialCtx, dialCancel := context.WithTimeout(r.Context(), 15*time.Second)
	upstream, _, err := websocket.Dial(dialCtx, target.WSURL, &websocket.DialOptions{
		HTTPHeader:   target.Headers,
		Subprotocols: []string{"binary"},
	})
	dialCancel()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "Could not connect to your computer desktop.", Code: "computer_desktop_unavailable", Retryable: true})
		return
	}
	defer upstream.Close(websocket.StatusNormalClosure, "")
	upstream.SetReadLimit(16 << 20)

	clientConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"binary"},
		InsecureSkipVerify: true, // Protected by Nori auth + short-lived user-bound session token.
	})
	if err != nil {
		return
	}
	defer clientConn.Close(websocket.StatusNormalClosure, "")
	clientConn.SetReadLimit(4 << 20)

	proxyCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var currentMode atomic.Value
	currentMode.Store(sess.Mode)
	var lastTouch atomic.Int64
	lastTouch.Store(sess.LastActivityAt.Unix())
	idleCap := c.Manager.SessionIdleCap()

	// Subscribe synchronously so PATCH mode downgrades ("control" -> "view") and DELETE
	// revocations take effect IMMEDIATELY before the HTTP call returns (zero 300ms window).
	unsub := c.Manager.SubscribeSession(sessionID, func(ev computer.SessionEvent) {
		currentMode.Store(ev.Mode)
		if ev.Revoked {
			cancel()
		}
	})
	defer unsub()

	// Cross-replica poll fallback: enforces DB revocation, TTL expiry, and idle deadline.
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-proxyCtx.Done():
				return
			case <-ticker.C:
				cur, gErr := c.Manager.GetSession(proxyCtx, c.UserID, sessionID)
				now := time.Now().UTC()
				if gErr != nil || cur.EndedAt != nil || !cur.ExpiresAt.After(now) || now.Sub(cur.LastActivityAt) > idleCap {
					cancel()
					return
				}
				currentMode.Store(cur.Mode)
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
			mode, _ := currentMode.Load().(computer.SessionMode)
			filtered, _, hadHumanInput, parseErr := parser.Feed(mode, data)
			if parseErr != nil {
				return
			}
			if len(filtered) == 0 {
				continue
			}
			// Only real human input (KeyEvent / PointerEvent / ClientCutText) extends session idle TTL;
			// automatic FramebufferUpdateRequests from an unattended tab never renew the session.
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

func writeComputerError(w http.ResponseWriter, err error) {
	status, code, msg, retry := 502, "computer_error", "Could not complete computer action.", true
	switch {
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
	}
	writeJSON(w, status, apiError{Error: msg, Code: code, Retryable: retry})
}
