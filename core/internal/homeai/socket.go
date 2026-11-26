package homeai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"nori/internal/homeaiwire"
)

type socketIdentity struct {
	deviceID     string
	userID       string
	generation   int64
	connectionID string
}

type nextAction struct {
	msg     *homeaiwire.Message
	waiting bool
}

func (s *Service) socket(w http.ResponseWriter, r *http.Request) {
	auth := strings.Fields(r.Header.Get("Authorization"))
	if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") || len(auth[1]) < 32 || len(auth[1]) > 200 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var id socketIdentity
	err := s.pool.QueryRow(r.Context(), `SELECT d.id,d.user_id,d.generation FROM nori_home_ai_devices d JOIN nori_home_ai_accounts a ON a.id=d.user_id WHERE d.access_hash=$1 AND d.access_expires_at>clock_timestamp() AND d.revoked_at IS NULL`, tokenHash(auth[1])).Scan(&id.deviceID, &id.userID, &id.generation)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		slog.WarnContext(r.Context(), "homeai socket auth lookup failed", "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{homeaiwire.Subprotocol}})
	if err != nil {
		slog.WarnContext(r.Context(), "homeai websocket accept failed", "device_id", id.deviceID, "err", err)
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != homeaiwire.Subprotocol {
		slog.WarnContext(r.Context(), "homeai websocket subprotocol mismatch", "device_id", id.deviceID, "subprotocol", conn.Subprotocol())
		_ = conn.Close(websocket.StatusPolicyViolation, "unsupported protocol")
		return
	}
	conn.SetReadLimit(128 << 10)
	id.connectionID = uuid.NewString()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if err = s.attach(ctx, id, auth[1]); err != nil {
		slog.WarnContext(ctx, "homeai device attach failed", "device_id", id.deviceID, "user_id", id.userID, "err", err)
		_ = conn.Close(websocket.StatusPolicyViolation, "device unavailable")
		return
	}
	slog.InfoContext(ctx, "homeai device connected", "device_id", id.deviceID, "user_id", id.userID, "connection_id", id.connectionID)
	defer func() {
		s.detach(id)
		slog.Info("homeai device disconnected", "device_id", id.deviceID, "user_id", id.userID, "connection_id", id.connectionID)
	}()
	wake, unsubscribe := s.subscribe("d:" + id.deviceID)
	defer unsubscribe()
	readerDone := make(chan error, 1)
	ready := make(chan homeaiwire.Message, 1)
	control := make(chan homeaiwire.Message, 1)
	go func() {
		for {
			var msg homeaiwire.Message
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				readerDone <- err
				return
			}
			if msg.Type == "draining" {
				if err := s.beginDrain(ctx, id); err != nil {
					readerDone <- err
					return
				}
				select {
				case control <- homeaiwire.Message{Type: "drained"}:
				case <-ctx.Done():
					return
				}
				continue
			}
			if msg.Type == "ready" {
				select {
				case ready <- msg:
				case <-ctx.Done():
					return
				}
				continue
			}
			if err := s.acceptMessage(ctx, id, msg); err != nil {
				readerDone <- err
				return
			}
		}
	}()
	poll := time.NewTicker(10 * time.Second)
	defer poll.Stop()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	activeRequest := ""
	activeAttempt := ""
	cancelSent := false
	var readyTimer *time.Timer
	var readyTimeout <-chan time.Time
	defer func() {
		if readyTimer != nil {
			readyTimer.Stop()
		}
	}()
	for {
		action, err := s.nextMessage(ctx, id, activeRequest, activeAttempt, cancelSent)
		if err != nil {
			slog.WarnContext(ctx, "homeai nextMessage failed", "device_id", id.deviceID, "connection_id", id.connectionID, "err", err)
			return
		}
		if action.waiting {
			if readyTimer == nil {
				readyTimer = time.NewTimer(45 * time.Second)
				readyTimeout = readyTimer.C
			}
		} else if action.msg != nil {
			request := action.msg
			if request.Type == "infer" {
				activeRequest = request.RequestID
				activeAttempt = request.AttemptID
			}
			if request.Type == "cancel" {
				cancelSent = true
				if readyTimer == nil {
					readyTimer = time.NewTimer(45 * time.Second)
					readyTimeout = readyTimer.C
				}
			}
			writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err = wsjson.Write(writeCtx, conn, request)
			stop()
			if err != nil {
				slog.WarnContext(ctx, "homeai websocket write failed", "device_id", id.deviceID, "msg_type", request.Type, "err", err)
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case readErr := <-readerDone:
			if readErr != nil && !errors.Is(readErr, context.Canceled) {
				slog.DebugContext(ctx, "homeai websocket reader closed", "device_id", id.deviceID, "err", readErr)
			}
			return
		case message := <-control:
			writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err = wsjson.Write(writeCtx, conn, message)
			stop()
			if err != nil {
				slog.WarnContext(ctx, "homeai control write failed", "device_id", id.deviceID, "msg_type", message.Type, "err", err)
				return
			}
		case <-readyTimeout:
			slog.WarnContext(ctx, "homeai device ready timeout exceeded", "device_id", id.deviceID, "request_id", activeRequest)
			return
		case readyMessage := <-ready:
			if activeRequest != "" && readyMessage.RequestID == activeRequest && readyMessage.AttemptID == activeAttempt {
				var status string
				err := s.pool.QueryRow(ctx, `SELECT status FROM nori_home_ai_requests WHERE id=$1 AND attempt_id=$2 AND device_id=$3 AND connection_id=$4`, activeRequest, activeAttempt, id.deviceID, id.connectionID).Scan(&status)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					slog.WarnContext(ctx, "homeai ready status check failed", "device_id", id.deviceID, "request_id", activeRequest, "err", err)
					return
				}
				if err == nil && status != "complete" && status != "failed" && status != "cancelled" {
					slog.WarnContext(ctx, "homeai premature ready received for non-terminal request", "device_id", id.deviceID, "request_id", activeRequest, "status", status)
					return
				}
				activeRequest = ""
				activeAttempt = ""
				cancelSent = false
				if readyTimer != nil {
					readyTimer.Stop()
					readyTimer = nil
					readyTimeout = nil
				}
			}
		case <-wake:
		case <-poll.C:
		case <-ping.C:
			pingCtx, stop := context.WithTimeout(ctx, 8*time.Second)
			err = conn.Ping(pingCtx)
			stop()
			if err != nil {
				slog.DebugContext(ctx, "homeai websocket ping failed", "device_id", id.deviceID, "err", err)
				return
			}
			if !s.touch(ctx, id) {
				slog.InfoContext(ctx, "homeai device touch rejected; closing connection", "device_id", id.deviceID)
				return
			}
		}
	}
}

func (s *Service) attach(ctx context.Context, id socketIdentity, token string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := tx.Exec(ctx, `UPDATE nori_home_ai_devices d SET connection_id=$1,last_seen=clock_timestamp(),ollama_available=false,draining=false WHERE d.id=$2 AND d.user_id=$3 AND d.generation=$4 AND d.revoked_at IS NULL AND d.access_hash=$5 AND d.access_expires_at>clock_timestamp() AND EXISTS(SELECT 1 FROM nori_home_ai_accounts WHERE id=d.user_id)`, id.connectionID, id.deviceID, id.userID, id.generation, tokenHash(token))
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return ErrRevoked
	}
	rows, err := tx.Query(ctx, `UPDATE nori_home_ai_requests SET status='failed',error_code='interrupted',payload=decode('','hex'),updated_at=clock_timestamp() WHERE device_id=$1 AND status IN ('dispatched','running') RETURNING id`, id.deviceID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var requestID string
		if err = rows.Scan(&requestID); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, requestID)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, requestID := range ids {
		if err = notify(ctx, tx, "r:"+requestID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) detach(id socketIdentity) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	detached, err := tx.Exec(ctx, `UPDATE nori_home_ai_devices SET connection_id=NULL,ollama_available=false WHERE id=$1 AND generation=$2 AND connection_id=$3`, id.deviceID, id.generation, id.connectionID)
	if err != nil {
		return
	}
	rows, err := tx.Query(ctx, `UPDATE nori_home_ai_requests SET status='failed',error_code='interrupted',payload=decode('','hex'),updated_at=clock_timestamp() WHERE device_id=$1 AND generation=$2 AND connection_id=$3 AND status IN ('dispatched','running') RETURNING id`, id.deviceID, id.generation, id.connectionID)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var requestID string
		if rows.Scan(&requestID) != nil {
			rows.Close()
			return
		}
		ids = append(ids, requestID)
	}
	if rows.Err() != nil {
		rows.Close()
		return
	}
	rows.Close()
	if detached.RowsAffected() == 1 {
		rows, err = tx.Query(ctx, `UPDATE nori_home_ai_requests SET status='failed',error_code='offline',payload=decode('','hex'),updated_at=clock_timestamp() WHERE device_id=$1 AND generation=$2 AND status='pending' RETURNING id`, id.deviceID, id.generation)
		if err != nil {
			return
		}
		for rows.Next() {
			var requestID string
			if rows.Scan(&requestID) != nil {
				rows.Close()
				return
			}
			ids = append(ids, requestID)
		}
		if rows.Err() != nil {
			rows.Close()
			return
		}
		rows.Close()
	}
	for _, requestID := range ids {
		if notify(ctx, tx, "r:"+requestID) != nil {
			return
		}
	}
	_ = tx.Commit(ctx)
}

func (s *Service) touch(ctx context.Context, id socketIdentity) bool {
	r, err := s.pool.Exec(ctx, `UPDATE nori_home_ai_devices d SET last_seen=clock_timestamp() WHERE d.id=$1 AND d.user_id=$2 AND d.generation=$3 AND d.connection_id=$4 AND d.revoked_at IS NULL AND d.access_expires_at>clock_timestamp() AND EXISTS(SELECT 1 FROM nori_home_ai_accounts WHERE id=d.user_id)`, id.deviceID, id.userID, id.generation, id.connectionID)
	return err == nil && r.RowsAffected() == 1
}

func validModels(models []homeaiwire.Model) bool {
	if len(models) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, m := range models {
		if len(m.Name) == 0 || len(m.Name) > 200 || len(m.Digest) == 0 || len(m.Digest) > 128 || !validName(m.Name) || !validName(m.Digest) || len(m.Capabilities) > 20 || seen[m.Name] {
			return false
		}
		seen[m.Name] = true
		for _, c := range m.Capabilities {
			if len(c) == 0 || len(c) > 64 || !validName(c) {
				return false
			}
		}
	}
	return true
}

func (s *Service) acceptMessage(ctx context.Context, id socketIdentity, msg homeaiwire.Message) error {
	if msg.Type == "hello" || msg.Type == "health" {
		if !validModels(msg.Models) || len(msg.Version) > 80 || strings.IndexFunc(msg.Version, func(r rune) bool { return r < 32 }) >= 0 {
			return ErrInvalidRequest
		}
		models, err := json.Marshal(msg.Models)
		if err != nil {
			return err
		}
		if string(models) == "null" {
			models = []byte("[]")
		}
		r, err := s.pool.Exec(ctx, `UPDATE nori_home_ai_devices d SET last_seen=clock_timestamp(),ollama_available=$1,models=$2,version=$3 WHERE d.id=$4 AND d.user_id=$5 AND d.generation=$6 AND d.connection_id=$7 AND d.revoked_at IS NULL AND d.access_expires_at>clock_timestamp() AND EXISTS(SELECT 1 FROM nori_home_ai_accounts WHERE id=d.user_id)`, msg.OllamaAvailable, models, msg.Version, id.deviceID, id.userID, id.generation, id.connectionID)
		if err != nil {
			return err
		}
		if r.RowsAffected() != 1 {
			return ErrRevoked
		}
		return nil
	}
	if msg.RequestID == "" || msg.AttemptID == "" {
		return ErrInvalidRequest
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	var next, total int64
	var httpStatus *int
	err = tx.QueryRow(ctx, `SELECT r.status,r.next_sequence,r.total_bytes,r.http_status FROM nori_home_ai_requests r JOIN nori_home_ai_devices d ON d.id=r.device_id JOIN nori_home_ai_accounts a ON a.id=r.user_id WHERE r.id=$1 AND r.attempt_id=$2 AND r.user_id=$3 AND r.device_id=$4 AND r.generation=$5 AND r.connection_id=$6 AND d.generation=$5 AND d.connection_id=$6 AND d.revoked_at IS NULL AND d.access_expires_at>clock_timestamp() AND r.deadline>clock_timestamp() FOR UPDATE OF r`, msg.RequestID, msg.AttemptID, id.userID, id.deviceID, id.generation, id.connectionID).Scan(&status, &next, &total, &httpStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "cancelled" || status == "failed" || status == "complete" {
		return nil
	}
	switch msg.Type {
	case "accepted":
		if status != "dispatched" {
			return ErrInvalidRequest
		}
		_, err = tx.Exec(ctx, `UPDATE nori_home_ai_requests SET status='running',payload=decode('','hex'),updated_at=clock_timestamp() WHERE id=$1`, msg.RequestID)
	case "headers":
		if status != "running" || httpStatus != nil || msg.Status < 100 || msg.Status > 599 || len(msg.ContentType) > 100 || strings.ContainsAny(msg.ContentType, "\r\n") {
			return ErrInvalidRequest
		}
		_, err = tx.Exec(ctx, `UPDATE nori_home_ai_requests SET http_status=$2,content_type=$3,updated_at=clock_timestamp() WHERE id=$1`, msg.RequestID, msg.Status, msg.ContentType)
	case "chunk":
		if status != "running" || httpStatus == nil || len(msg.Data) == 0 || len(msg.Data) > maxChunkBytes {
			return ErrInvalidRequest
		}
		if msg.Sequence < next {
			return nil
		}
		if msg.Sequence != next {
			return ErrInvalidRequest
		}
		var buffered int
		if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(octet_length(payload)),0) FROM nori_home_ai_chunks WHERE request_id=$1`, msg.RequestID).Scan(&buffered); err != nil {
			return err
		}
		if total+int64(len(msg.Data)) > maxResultBytes || buffered+len(msg.Data) > 1<<20 {
			_, err = tx.Exec(ctx, `UPDATE nori_home_ai_requests SET status='failed',error_code='interrupted',updated_at=clock_timestamp() WHERE id=$1`, msg.RequestID)
			if err != nil {
				return err
			}
			if err = notify(ctx, tx, "d:"+id.deviceID); err != nil {
				return err
			}
			break
		}
		sealed, sealErr := s.seal(fmt.Sprintf("%s:%d", msg.RequestID, msg.Sequence), msg.Data)
		if sealErr != nil {
			return sealErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO nori_home_ai_chunks(request_id,attempt_id,sequence,payload) VALUES($1,$2,$3,$4)`, msg.RequestID, msg.AttemptID, msg.Sequence, sealed); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE nori_home_ai_requests SET next_sequence=next_sequence+1,total_bytes=total_bytes+$2,updated_at=clock_timestamp() WHERE id=$1`, msg.RequestID, len(msg.Data))
	case "complete":
		if status != "running" || httpStatus == nil {
			return ErrInvalidRequest
		}
		_, err = tx.Exec(ctx, `UPDATE nori_home_ai_requests SET status='complete',updated_at=clock_timestamp() WHERE id=$1`, msg.RequestID)
	case "failed":
		if status != "running" && status != "dispatched" {
			return ErrInvalidRequest
		}
		code := "connector_failed"
		if httpStatus == nil {
			switch msg.Error {
			case "busy", "ollama_unavailable", "model_unavailable", "model_changed":
				code = "connector_" + msg.Error
			}
		}
		_, err = tx.Exec(ctx, `UPDATE nori_home_ai_requests SET status='failed',error_code=$2,payload=decode('','hex'),updated_at=clock_timestamp() WHERE id=$1`, msg.RequestID, code)
	default:
		return ErrInvalidRequest
	}
	if err != nil {
		return err
	}
	if err = notify(ctx, tx, "r:"+msg.RequestID); err != nil {
		return err
	}
	if msg.Type == "complete" || msg.Type == "failed" {
		if err = notify(ctx, tx, "d:"+id.deviceID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) nextMessage(ctx context.Context, id socketIdentity, activeRequest, activeAttempt string, cancelSent bool) (nextAction, error) {
	var connected bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nori_home_ai_devices d JOIN nori_home_ai_accounts a ON a.id=d.user_id WHERE d.id=$1 AND d.user_id=$2 AND d.generation=$3 AND d.connection_id=$4 AND d.revoked_at IS NULL AND d.access_expires_at>clock_timestamp())`, id.deviceID, id.userID, id.generation, id.connectionID).Scan(&connected)
	if err != nil {
		return nextAction{}, err
	}
	if !connected {
		return nextAction{}, ErrRevoked
	}
	if activeRequest != "" {
		var status, errorCode string
		err = s.pool.QueryRow(ctx, `SELECT status,COALESCE(error_code,'') FROM nori_home_ai_requests WHERE id=$1 AND attempt_id=$2 AND device_id=$3 AND connection_id=$4`, activeRequest, activeAttempt, id.deviceID, id.connectionID).Scan(&status, &errorCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return nextAction{waiting: true}, nil
		}
		if err != nil {
			return nextAction{}, err
		}
		if !cancelSent && (status == "cancelled" || status == "failed" && !strings.HasPrefix(errorCode, "connector_")) {
			return nextAction{msg: &homeaiwire.Message{Type: "cancel", RequestID: activeRequest, AttemptID: activeAttempt}}, nil
		}
		if status == "complete" || status == "failed" || status == "cancelled" {
			return nextAction{waiting: true}, nil
		}
		return nextAction{}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nextAction{}, err
	}
	defer tx.Rollback(ctx)
	var draining bool
	var expiry time.Time
	err = tx.QueryRow(ctx, `SELECT draining,access_expires_at FROM nori_home_ai_devices WHERE id=$1 AND user_id=$2 AND generation=$3 AND connection_id=$4 AND revoked_at IS NULL FOR UPDATE`, id.deviceID, id.userID, id.generation, id.connectionID).Scan(&draining, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return nextAction{}, ErrRevoked
	}
	if err != nil {
		return nextAction{}, err
	}
	if draining || time.Until(expiry) <= homeaiwire.RefreshLead {
		if err = failPending(ctx, tx, id); err != nil {
			return nextAction{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nextAction{}, err
		}
		return nextAction{}, nil
	}
	var msg homeaiwire.Message
	var sealed []byte
	err = tx.QueryRow(ctx, `UPDATE nori_home_ai_requests SET status='dispatched',connection_id=$1,updated_at=clock_timestamp() WHERE id=(SELECT r.id FROM nori_home_ai_requests r JOIN nori_home_ai_devices d ON d.id=r.device_id JOIN nori_home_ai_accounts a ON a.id=r.user_id WHERE r.device_id=$2 AND r.user_id=$3 AND r.generation=$4 AND r.status='pending' AND r.deadline>clock_timestamp() AND d.connection_id=$1 AND d.revoked_at IS NULL AND d.generation=$4 FOR UPDATE OF r SKIP LOCKED LIMIT 1) RETURNING id,attempt_id,operation,model,digest,deadline,payload`, id.connectionID, id.deviceID, id.userID, id.generation).Scan(&msg.RequestID, &msg.AttemptID, &msg.Operation, &msg.Model, &msg.Digest, &msg.Deadline, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nextAction{}, nil
	}
	if err != nil {
		return nextAction{}, err
	}
	payload, err := s.unseal(msg.RequestID, sealed)
	if err != nil {
		return nextAction{}, err
	}
	msg.Type = "infer"
	msg.Payload = payload
	if err = tx.Commit(ctx); err != nil {
		return nextAction{}, err
	}
	return nextAction{msg: &msg}, nil
}

func failPending(ctx context.Context, tx pgx.Tx, id socketIdentity) error {
	rows, err := tx.Query(ctx, `UPDATE nori_home_ai_requests SET status='failed',error_code='offline',payload=decode('','hex'),updated_at=clock_timestamp() WHERE device_id=$1 AND generation=$2 AND status='pending' RETURNING id`, id.deviceID, id.generation)
	if err != nil {
		return err
	}
	var requests []string
	for rows.Next() {
		var request string
		if err = rows.Scan(&request); err != nil {
			rows.Close()
			return err
		}
		requests = append(requests, request)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, request := range requests {
		if err = notify(ctx, tx, "r:"+request); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) beginDrain(ctx context.Context, id socketIdentity) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE nori_home_ai_devices d SET draining=true WHERE d.id=$1 AND d.user_id=$2 AND d.generation=$3 AND d.connection_id=$4 AND d.revoked_at IS NULL AND EXISTS(SELECT 1 FROM nori_home_ai_accounts WHERE id=d.user_id)`, id.deviceID, id.userID, id.generation, id.connectionID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrRevoked
	}
	if err = failPending(ctx, tx, id); err != nil {
		return err
	}
	if err = notify(ctx, tx, "d:"+id.deviceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
