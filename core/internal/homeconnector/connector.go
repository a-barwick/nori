package homeconnector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"nori/internal/homeaiwire"
)

var errAccessRejected = errors.New("connector access token rejected")
var errSessionRollover = errors.New("connector session rollover")

const heartbeatInterval = 30 * time.Second
const maxInferenceDuration = 5 * time.Minute

type Connector struct {
	config         Config
	ollama         *Ollama
	healthInterval time.Duration
	rolloverLead   time.Duration
}

func New(cfg Config) (*Connector, error) {
	validated, err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	return &Connector{config: validated, ollama: NewOllama(validated.OllamaURL), healthInterval: heartbeatInterval}, nil
}

func (c *Connector) Serve(ctx context.Context) error {
	if err := ensureStateDir(c.config.StateDir); err != nil {
		return err
	}
	defer c.updateStatus("stopped", false, nil)
	delay := time.Second
	for ctx.Err() == nil {
		cred, err := AccessToken(ctx, c.config)
		if err != nil {
			state := "connecting"
			if errors.Is(err, ErrPairRequired) {
				state = "unpaired"
			}
			c.updateStatus(state, false, nil)
		} else {
			started := time.Now()
			err = c.session(ctx, cred)
			if time.Since(started) > time.Minute {
				delay = time.Second
			}
			if errors.Is(err, errAccessRejected) || errors.Is(err, errSessionRollover) {
				_, err = ForceRefresh(ctx, c.config)
			}
			if errors.Is(err, ErrPairRequired) {
				c.updateStatus("unpaired", false, nil)
			} else {
				c.updateStatus("connecting", false, nil)
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		jitter := time.Duration(rand.Int64N(int64(delay/2 + 1)))
		timer := time.NewTimer(delay + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
	return nil
}

func (c *Connector) updateStatus(state string, available bool, models []homeaiwire.Model) {
	_ = writeStatus(c.config.StateDir, Status{State: state, Paired: state != "unpaired", OllamaAvailable: available, Models: models, UpdatedAt: time.Now().UTC()})
}

func (c *Connector) session(parent context.Context, credential Credential) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	u, _ := url.Parse(c.config.RelayURL)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/v1/connector/socket"
	dialCtx, dialCancel := context.WithTimeout(ctx, 20*time.Second)
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+credential.AccessToken)
	conn, response, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{
		HTTPClient:   relayHTTPClient(),
		HTTPHeader:   header,
		Subprotocols: []string{homeaiwire.Subprotocol},
	})
	dialCancel()
	if err != nil {
		if response != nil && response.StatusCode == http.StatusUnauthorized {
			return errAccessRejected
		}
		return err
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != homeaiwire.Subprotocol {
		return errors.New("relay did not negotiate connector protocol")
	}
	conn.SetReadLimit(maxRequestBytes + 64<<10)
	writer := &socketWriter{conn: conn, ctx: ctx}
	models, inventoryErr := c.ollama.Inventory(ctx)
	if err := writer.write(ctx, homeaiwire.Message{
		Type: "hello", Version: Version, Models: models, OllamaAvailable: inventoryErr == nil,
	}); err != nil {
		return err
	}
	c.updateStatus("online", inventoryErr == nil, models)
	messages := make(chan homeaiwire.Message, 8)
	readErrors := make(chan error, 1)
	go func() {
		for {
			var message homeaiwire.Message
			if err := wsjson.Read(ctx, conn, &message); err != nil {
				readErrors <- err
				return
			}
			select {
			case messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	healthErrors := make(chan error, 1)
	go c.monitorHealth(ctx, conn, writer, healthErrors)
	lead := c.rolloverLead
	if lead <= 0 {
		lead = homeaiwire.RefreshLead
	}
	expiresIn := time.Until(credential.Expiry.Add(-lead))
	if expiresIn < time.Second {
		expiresIn = time.Second
	}
	rollover := time.NewTimer(expiresIn)
	defer rollover.Stop()
	var rolloverC <-chan time.Time = rollover.C
	draining := false
	drained := false
	var active *activeRequest
	completed := make(chan string, 1)
	defer func() {
		if active != nil {
			active.cancel()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-rolloverC:
			if err := writer.write(ctx, homeaiwire.Message{Type: "draining"}); err != nil {
				return err
			}
			draining = true
			rolloverC = nil
		case err := <-readErrors:
			return err
		case requestID := <-completed:
			if active != nil && active.requestID == requestID {
				request := active
				active = nil
				request.cancel()
				if err := writer.write(ctx, homeaiwire.Message{Type: "ready", RequestID: request.requestID, AttemptID: request.attemptID}); err != nil {
					return err
				}
				if drained {
					_ = conn.Close(websocket.StatusNormalClosure, "session rollover")
					return errSessionRollover
				}
			}
		case err := <-healthErrors:
			return err
		case message := <-messages:
			switch message.Type {
			case "infer":
				if drained {
					return errors.New("relay dispatched after connector drained")
				}
				if message.RequestID == "" || message.AttemptID == "" || message.Deadline.IsZero() || time.Until(message.Deadline) <= 0 {
					if err := writer.failure(ctx, message, "invalid_request"); err != nil {
						return err
					}
					if err := writer.write(ctx, homeaiwire.Message{Type: "ready", RequestID: message.RequestID, AttemptID: message.AttemptID}); err != nil {
						return err
					}
					continue
				}
				if active != nil {
					if err := writer.failure(ctx, message, "busy"); err != nil {
						return err
					}
					continue
				}
				deadline := minTime(message.Deadline, time.Now().Add(maxInferenceDuration))
				requestCtx, requestCancel := context.WithDeadline(ctx, deadline)
				active = &activeRequest{requestID: message.RequestID, attemptID: message.AttemptID, cancel: requestCancel}
				go c.infer(requestCtx, writer, message, completed)
			case "cancel":
				if active != nil && active.requestID == message.RequestID && active.attemptID == message.AttemptID {
					active.cancel()
				}
			case "drained":
				if !draining {
					return errors.New("unexpected relay drain acknowledgement")
				}
				drained = true
				if active == nil {
					_ = conn.Close(websocket.StatusNormalClosure, "session rollover")
					return errSessionRollover
				}
			case "drain":
				drained = true
				if active == nil {
					_ = conn.Close(websocket.StatusNormalClosure, "session rollover")
					return errSessionRollover
				}
			default:
				return errors.New("unexpected relay message")
			}
		}
	}
}

func (c *Connector) monitorHealth(ctx context.Context, conn *websocket.Conn, writer *socketWriter, healthErrors chan<- error) {
	interval := c.healthInterval
	if interval <= 0 {
		interval = heartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
		err := conn.Ping(pingCtx)
		pingCancel()
		if err != nil {
			select {
			case healthErrors <- err:
			case <-ctx.Done():
			}
			return
		}
		models, inventoryErr := c.ollama.Inventory(ctx)
		if err := writer.write(ctx, homeaiwire.Message{
			Type: "health", Version: Version, Models: models, OllamaAvailable: inventoryErr == nil,
		}); err != nil {
			select {
			case healthErrors <- err:
			case <-ctx.Done():
			}
			return
		}
		c.updateStatus("online", inventoryErr == nil, models)
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

type activeRequest struct {
	requestID string
	attemptID string
	cancel    context.CancelFunc
}

type socketWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
	ctx  context.Context
}

func (w *socketWriter) write(ctx context.Context, message homeaiwire.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(w.ctx, 3*time.Second)
	defer cancel()
	return wsjson.Write(writeCtx, w.conn, message)
}

func (w *socketWriter) failure(ctx context.Context, request homeaiwire.Message, code string) error {
	return w.write(ctx, homeaiwire.Message{Type: "failed", RequestID: request.RequestID, AttemptID: request.AttemptID, Error: code})
}

type readResult struct {
	data []byte
	err  error
}

func (c *Connector) infer(ctx context.Context, writer *socketWriter, request homeaiwire.Message, completed chan<- string) {
	defer func() {
		if ctx.Err() != nil && writer.ctx.Err() == nil {
			code := "cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = "deadline_exceeded"
			}
			_ = writer.failure(writer.ctx, request, code)
		}
		completed <- request.RequestID
	}()
	if err := writer.write(ctx, homeaiwire.Message{Type: "accepted", RequestID: request.RequestID, AttemptID: request.AttemptID}); err != nil {
		return
	}
	resp, code := c.ollama.Open(ctx, request.Operation, request.Model, request.Digest, request.Payload)
	if code != "" {
		if ctx.Err() == nil {
			_ = writer.failure(ctx, request, code)
		}
		return
	}
	defer resp.Body.Close()
	if err := writer.write(ctx, homeaiwire.Message{
		Type: "headers", RequestID: request.RequestID, AttemptID: request.AttemptID,
		Status: resp.StatusCode, ContentType: strings.Split(resp.Header.Get("Content-Type"), ";")[0],
	}); err != nil {
		return
	}
	results := make(chan readResult, 1)
	go func() {
		for {
			buffer := make([]byte, 32<<10)
			n, err := resp.Body.Read(buffer)
			select {
			case results <- readResult{data: buffer[:n], err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var total int64
	var sequence int64
	buffered := make([]byte, 0, 32<<10)
	var flushTimer *time.Timer
	var flushC <-chan time.Time
	defer func() {
		if flushTimer != nil {
			flushTimer.Stop()
		}
	}()
	flush := func() error {
		if len(buffered) == 0 {
			return nil
		}
		sequence++
		err := writer.write(ctx, homeaiwire.Message{
			Type: "chunk", RequestID: request.RequestID, AttemptID: request.AttemptID,
			Sequence: sequence, Data: buffered,
		})
		buffered = buffered[:0]
		if flushTimer != nil {
			flushTimer.Stop()
		}
		flushC = nil
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-flushC:
			if err := flush(); err != nil {
				return
			}
		case result := <-results:
			total += int64(len(result.data))
			if total > maxOutputBytes {
				_ = writer.failure(ctx, request, "transport_error")
				return
			}
			for len(result.data) > 0 {
				space := cap(buffered) - len(buffered)
				count := min(space, len(result.data))
				buffered = append(buffered, result.data[:count]...)
				result.data = result.data[count:]
				if len(buffered) == cap(buffered) {
					if err := flush(); err != nil {
						return
					}
				}
			}
			if len(buffered) > 0 && flushC == nil {
				if flushTimer == nil {
					flushTimer = time.NewTimer(40 * time.Millisecond)
				} else {
					flushTimer.Reset(40 * time.Millisecond)
				}
				flushC = flushTimer.C
			}
			if result.err != nil {
				if err := flush(); err != nil {
					return
				}
				if result.err == io.EOF {
					_ = writer.write(ctx, homeaiwire.Message{Type: "complete", RequestID: request.RequestID, AttemptID: request.AttemptID, Sequence: sequence})
				} else if ctx.Err() == nil {
					_ = writer.failure(ctx, request, "transport_error")
				}
				return
			}
		}
	}
}

func (c *Connector) Doctor(ctx context.Context) (Status, error) {
	models, err := c.ollama.Inventory(ctx)
	status := Status{State: "ready", OllamaAvailable: err == nil, UpdatedAt: time.Now().UTC()}
	if err != nil {
		status.State = "ollama_unavailable"
		status.Message = "Check that Ollama is running on this computer."
		return status, err
	}
	status.Models = models
	status.Message = fmt.Sprintf("%d local models available", len(models))
	return status, nil
}

func IsMissingStatus(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
