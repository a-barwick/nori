package homeai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"nori/internal/homeaiwire"
)

type requestState struct {
	status      string
	httpStatus  *int
	contentType *string
	errorCode   *string
}

func (s *Service) Infer(ctx context.Context, userID, deviceID, model, digest, operation string, body []byte) (*http.Response, error) {
	if operation != "chat" && operation != "responses" {
		return nil, ErrInvalidRequest
	}
	if len(body) == 0 || len(body) > maxRequestBytes || !json.Valid(body) || len(model) == 0 || len(model) > 200 || len(digest) == 0 || len(digest) > 128 {
		return nil, ErrInvalidRequest
	}
	keepContext := false
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline || deadline.After(time.Now().Add(5*time.Minute)) {
		deadline = time.Now().Add(5 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer func() {
		if !keepContext {
			cancel()
		}
	}()
	id := uuid.NewString()
	attempt := uuid.NewString()
	sealed, err := s.seal(id, body)
	if err != nil {
		return nil, err
	}
	wake, unsubscribe := s.subscribe("r:" + id)
	keepSubscription := false
	defer func() {
		if !keepSubscription {
			unsubscribe()
		}
	}()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var revoked *time.Time
	var seen *time.Time
	var connection *string
	var available, draining bool
	var accessExpiry time.Time
	var generation int64
	var rawModels []byte
	err = tx.QueryRow(ctx, `SELECT revoked_at,last_seen,connection_id,ollama_available,generation,models,draining,access_expires_at FROM nori_home_ai_devices WHERE id=$1 AND user_id=$2 FOR UPDATE`, deviceID, userID).Scan(&revoked, &seen, &connection, &available, &generation, &rawModels, &draining, &accessExpiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}
	if revoked != nil {
		return nil, ErrRevoked
	}
	active, err := userActive(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, ErrRevoked
	}
	if seen == nil || connection == nil || time.Since(*seen) > staleAfter || draining || time.Until(accessExpiry) <= homeaiwire.RefreshLead {
		return nil, ErrOffline
	}
	if !available {
		return nil, ErrOllamaUnavailable
	}
	var models []homeaiwire.Model
	if err = json.Unmarshal(rawModels, &models); err != nil {
		return nil, err
	}
	found := false
	for _, m := range models {
		if m.Name == model {
			found = true
			if m.Digest != digest {
				return nil, ErrModelChanged
			}
			break
		}
	}
	if !found {
		return nil, ErrModelUnavailable
	}
	if err = expireRequests(ctx, tx, deviceID); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO nori_home_ai_requests(id,attempt_id,user_id,device_id,generation,operation,model,digest,payload,status,deadline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10)`, id, attempt, userID, deviceID, generation, operation, model, digest, sealed, deadline)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	if err = notify(ctx, tx, "d:"+deviceID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state, err := s.requestState(ctx, id, attempt, userID, deviceID)
		if err != nil {
			s.cancel(id, attempt, deviceID)
			return nil, err
		}
		if state.httpStatus != nil {
			reader, writer := io.Pipe()
			responseBody := &responseStream{PipeReader: reader, cancel: cancel}
			keepSubscription = true
			keepContext = true
			go s.stream(ctx, cancel, writer, id, attempt, userID, deviceID, wake, unsubscribe)
			header := make(http.Header)
			if state.contentType != nil {
				header.Set("Content-Type", *state.contentType)
			}
			return &http.Response{StatusCode: *state.httpStatus, Status: strconv.Itoa(*state.httpStatus) + " " + http.StatusText(*state.httpStatus), Header: header, Body: responseBody, ContentLength: -1}, nil
		}
		switch state.status {
		case "failed":
			return nil, requestError(state.errorCode)
		case "cancelled":
			return nil, ErrInterrupted
		case "complete":
			return nil, ErrInterrupted
		}
		select {
		case <-ctx.Done():
			s.cancel(id, attempt, deviceID)
			return nil, ctx.Err()
		case <-wake:
		case <-ticker.C:
		}
	}
}

func requestError(code *string) error {
	if code == nil {
		return ErrInterrupted
	}
	switch *code {
	case "revoked":
		return ErrRevoked
	case "offline":
		return ErrOffline
	case "connector_busy":
		return ErrBusy
	case "connector_ollama_unavailable":
		return ErrOllamaUnavailable
	case "connector_model_unavailable":
		return ErrModelUnavailable
	case "connector_model_changed":
		return ErrModelChanged
	default:
		return ErrInterrupted
	}
}

func (s *Service) requestState(ctx context.Context, id, attempt, userID, deviceID string) (requestState, error) {
	var state requestState
	err := s.pool.QueryRow(ctx, `SELECT r.status,r.http_status,r.content_type,r.error_code FROM nori_home_ai_requests r JOIN nori_home_ai_devices d ON d.id=r.device_id JOIN nori_home_ai_accounts a ON a.id=r.user_id WHERE r.id=$1 AND r.attempt_id=$2 AND r.user_id=$3 AND r.device_id=$4 AND d.generation=r.generation AND d.revoked_at IS NULL`, id, attempt, userID, deviceID).Scan(&state.status, &state.httpStatus, &state.contentType, &state.errorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, ErrRevoked
	}
	return state, err
}

type responseStream struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *responseStream) Close() error { r.cancel(); return r.PipeReader.Close() }

func (s *Service) cancel(id, attempt, deviceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE nori_home_ai_requests SET status='cancelled',payload=decode('','hex'),updated_at=clock_timestamp() WHERE id=$1 AND attempt_id=$2 AND status IN ('pending','dispatched','running')`, id, attempt); err != nil {
		return
	}
	if notify(ctx, tx, "d:"+deviceID) != nil {
		return
	}
	_ = tx.Commit(ctx)
}

func (s *Service) stream(ctx context.Context, cancel context.CancelFunc, w *io.PipeWriter, id, attempt, userID, deviceID string, wake <-chan struct{}, unsubscribe func()) {
	defer cancel()
	defer unsubscribe()
	finished := false
	defer func() {
		if !finished {
			s.cancel(id, attempt, deviceID)
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var next int64 = 1
	for {
		state, err := s.requestState(ctx, id, attempt, userID, deviceID)
		if err != nil {
			_ = w.CloseWithError(err)
			return
		}
		rows, err := s.pool.Query(ctx, `SELECT sequence,payload FROM nori_home_ai_chunks WHERE request_id=$1 AND attempt_id=$2 AND sequence >= $3 ORDER BY sequence LIMIT 16`, id, attempt, next)
		if err != nil {
			_ = w.CloseWithError(err)
			return
		}
		type chunk struct {
			sequence int64
			payload  []byte
		}
		var chunks []chunk
		for rows.Next() {
			var c chunk
			if err = rows.Scan(&c.sequence, &c.payload); err != nil {
				break
			}
			chunks = append(chunks, c)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			_ = w.CloseWithError(err)
			return
		}
		for _, c := range chunks {
			if c.sequence != next {
				_ = w.CloseWithError(ErrInterrupted)
				return
			}
			plain, err := s.unseal(fmt.Sprintf("%s:%d", id, c.sequence), c.payload)
			if err != nil {
				_ = w.CloseWithError(err)
				return
			}
			if _, err = w.Write(plain); err != nil {
				return
			}
			next++
		}
		if len(chunks) > 0 {
			if _, err = s.pool.Exec(ctx, `DELETE FROM nori_home_ai_chunks WHERE request_id=$1 AND attempt_id=$2 AND sequence<$3`, id, attempt, next); err != nil {
				_ = w.CloseWithError(err)
				return
			}
			continue
		}
		switch state.status {
		case "failed":
			if state.errorCode != nil && *state.errorCode == "revoked" {
				_ = w.CloseWithError(ErrRevoked)
			} else {
				_ = w.CloseWithError(ErrInterrupted)
			}
			return
		case "cancelled":
			_ = w.CloseWithError(ErrInterrupted)
			return
		case "complete":
			finished = true
			_, _ = s.pool.Exec(ctx, `DELETE FROM nori_home_ai_requests WHERE id=$1 AND attempt_id=$2`, id, attempt)
			_ = w.Close()
			return
		}
		select {
		case <-ctx.Done():
			_ = w.CloseWithError(ctx.Err())
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}
