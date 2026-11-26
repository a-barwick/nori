package homeai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type deviceAuthorization struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type oauthError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, oauthError{Error: "invalid_request"})
		return false
	}
	if r.PostForm.Get("client_id") != "nori-connector" {
		writeJSON(w, http.StatusUnauthorized, oauthError{Error: "invalid_client"})
		return false
	}
	return true
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/device_authorization", s.startAuthorization)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /oauth/revoke", s.revokeToken)
	mux.HandleFunc("GET /v1/connector/socket", s.socket)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if s.Ready(ctx) != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func (s *Service) startAuthorization(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("device_name"))
	if name == "" {
		name = "Home computer"
	}
	if !validName(name) {
		writeJSON(w, http.StatusBadRequest, oauthError{Error: "invalid_request"})
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := tokenHash(host)
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, oauthError{Error: "temporarily_unavailable"})
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "nori-home-ai-peer:"+host); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, oauthError{Error: "temporarily_unavailable"})
		return
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM nori_home_ai_grants WHERE peer_hash=$1 AND created_at>clock_timestamp()-interval '10 minutes'`, peer).Scan(&count); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, oauthError{Error: "temporarily_unavailable"})
		return
	}
	if count >= 60 {
		writeJSON(w, http.StatusTooManyRequests, oauthError{Error: "slow_down"})
		return
	}
	deviceCode, err := randomToken(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, oauthError{Error: "server_error"})
		return
	}
	userCode, err := randomCode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, oauthError{Error: "server_error"})
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO nori_home_ai_grants(id,device_code_hash,user_code_hash,user_code,device_name,peer_hash,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,'pending',clock_timestamp()+interval '10 minutes')`, uuid.NewString(), tokenHash(deviceCode), tokenHash(normalizedCode(userCode)), userCode, name, peer)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, oauthError{Error: "temporarily_unavailable"})
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, oauthError{Error: "temporarily_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, deviceAuthorization{DeviceCode: deviceCode, UserCode: userCode, VerificationURI: s.verificationURI, ExpiresIn: 600, Interval: 5})
}

func (s *Service) token(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	var tok tokenResponse
	var code string
	switch r.PostForm.Get("grant_type") {
	case "urn:ietf:params:oauth:grant-type:device_code":
		tok, code = s.redeemGrant(r.Context(), r.PostForm.Get("device_code"))
	case "refresh_token":
		tok, code = s.refresh(r.Context(), r.PostForm.Get("refresh_token"))
	default:
		code = "unsupported_grant_type"
	}
	if code != "" {
		writeJSON(w, http.StatusBadRequest, oauthError{Error: code})
		return
	}
	writeJSON(w, http.StatusOK, tok)
}

func newTokenResponse() (tokenResponse, error) {
	access, err := randomToken(32)
	if err != nil {
		return tokenResponse{}, err
	}
	refresh, err := randomToken(32)
	if err != nil {
		return tokenResponse{}, err
	}
	return tokenResponse{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: int(accessLifetime.Seconds())}, nil
}

func (s *Service) redeemGrant(ctx context.Context, code string) (tokenResponse, string) {
	var empty tokenResponse
	if len(code) < 32 || len(code) > 200 {
		return empty, "invalid_grant"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return empty, "server_error"
	}
	defer tx.Rollback(ctx)
	var id, name, status string
	var userID *string
	var expiry time.Time
	var lastPoll *time.Time
	var interval int
	pairingHash := tokenHash(code)
	err = tx.QueryRow(ctx, `SELECT id,device_name,status,user_id,expires_at,last_polled,poll_interval FROM nori_home_ai_grants WHERE device_code_hash=$1 FOR UPDATE`, pairingHash).Scan(&id, &name, &status, &userID, &expiry, &lastPoll, &interval)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, "invalid_grant"
	}
	if err != nil {
		return empty, "server_error"
	}
	if time.Now().After(expiry) {
		return empty, "expired_token"
	}
	if status == "redeemed" {
		return empty, "invalid_grant"
	}
	if status == "denied" {
		return empty, "access_denied"
	}
	if lastPoll != nil && time.Since(*lastPoll) < time.Duration(interval)*time.Second {
		if _, err = tx.Exec(ctx, `UPDATE nori_home_ai_grants SET last_polled=clock_timestamp(),poll_interval=LEAST(poll_interval+5,60) WHERE id=$1`, id); err != nil {
			return empty, "server_error"
		}
		if tx.Commit(ctx) != nil {
			return empty, "server_error"
		}
		return empty, "slow_down"
	}
	if _, err = tx.Exec(ctx, `UPDATE nori_home_ai_grants SET last_polled=clock_timestamp() WHERE id=$1`, id); err != nil {
		return empty, "server_error"
	}
	if status == "pending" {
		if tx.Commit(ctx) != nil {
			return empty, "server_error"
		}
		return empty, "authorization_pending"
	}
	if userID == nil {
		return empty, "invalid_grant"
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "nori-home-ai:"+*userID); err != nil {
		return empty, "server_error"
	}
	active, err := userActive(ctx, tx, *userID)
	if err != nil {
		return empty, "server_error"
	}
	if !active {
		return empty, "access_denied"
	}
	var existing int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM nori_home_ai_devices WHERE user_id=$1 AND revoked_at IS NULL`, *userID).Scan(&existing); err != nil {
		return empty, "server_error"
	}
	if existing > 0 {
		return empty, "access_denied"
	}
	tok, err := newTokenResponse()
	if err != nil {
		return empty, "server_error"
	}
	_, err = tx.Exec(ctx, `INSERT INTO nori_home_ai_devices(id,user_id,name,access_hash,refresh_hash,access_expires_at,refresh_expires_at,pairing_hash) VALUES($1,$2,$3,$4,$5,clock_timestamp()+$6::interval,clock_timestamp()+$7::interval,$8)`, uuid.NewString(), *userID, name, tokenHash(tok.AccessToken), tokenHash(tok.RefreshToken), fmt.Sprintf("%d seconds", int(accessLifetime.Seconds())), fmt.Sprintf("%d seconds", int(refreshLifetime.Seconds())), pairingHash)
	if err != nil {
		return empty, "server_error"
	}
	if _, err = tx.Exec(ctx, `UPDATE nori_home_ai_grants SET status='redeemed' WHERE id=$1`, id); err != nil {
		return empty, "server_error"
	}
	if tx.Commit(ctx) != nil {
		return empty, "server_error"
	}
	return tok, ""
}

func (s *Service) refresh(ctx context.Context, token string) (tokenResponse, string) {
	var empty tokenResponse
	if len(token) < 32 || len(token) > 200 {
		return empty, "invalid_grant"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return empty, "server_error"
	}
	defer tx.Rollback(ctx)
	var deviceID, userID string
	var expiry time.Time
	var revoked *time.Time
	hash := tokenHash(token)
	err = tx.QueryRow(ctx, `SELECT id,user_id,refresh_expires_at,revoked_at FROM nori_home_ai_devices WHERE refresh_hash=$1 FOR UPDATE`, hash).Scan(&deviceID, &userID, &expiry, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT d.id,d.user_id FROM nori_home_ai_devices d JOIN nori_home_ai_refresh_history h ON h.device_id=d.id WHERE h.token_hash=$1 FOR UPDATE OF d`, hash).Scan(&deviceID, &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return empty, "invalid_grant"
		}
		if err != nil {
			return empty, "server_error"
		}
		if _, err = tx.Exec(ctx, `UPDATE nori_home_ai_devices SET revoked_at=COALESCE(revoked_at,clock_timestamp()),generation=generation+1,connection_id=NULL WHERE id=$1`, deviceID); err != nil {
			return empty, "server_error"
		}
		if _, err = tx.Exec(ctx, `DELETE FROM nori_home_ai_requests WHERE device_id=$1`, deviceID); err != nil {
			return empty, "server_error"
		}
		if err = notify(ctx, tx, "d:"+deviceID); err != nil {
			return empty, "server_error"
		}
		if tx.Commit(ctx) != nil {
			return empty, "server_error"
		}
		return empty, "invalid_grant"
	}
	if err != nil {
		return empty, "server_error"
	}
	if revoked != nil || time.Now().After(expiry) {
		return empty, "invalid_grant"
	}
	active, err := userActive(ctx, tx, userID)
	if err != nil {
		return empty, "server_error"
	}
	if !active {
		return empty, "invalid_grant"
	}
	tok, err := newTokenResponse()
	if err != nil {
		return empty, "server_error"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO nori_home_ai_refresh_history(token_hash,device_id,expires_at) VALUES($1,$2,$3)`, hash, deviceID, expiry); err != nil {
		return empty, "server_error"
	}
	if _, err = tx.Exec(ctx, `UPDATE nori_home_ai_devices SET refresh_hash=$2,access_hash=$3,refresh_expires_at=clock_timestamp()+$4::interval,access_expires_at=clock_timestamp()+$5::interval,generation=generation+1,connection_id=NULL WHERE id=$1`, deviceID, tokenHash(tok.RefreshToken), tokenHash(tok.AccessToken), fmt.Sprintf("%d seconds", int(refreshLifetime.Seconds())), fmt.Sprintf("%d seconds", int(accessLifetime.Seconds()))); err != nil {
		return empty, "server_error"
	}
	if _, err = tx.Exec(ctx, `UPDATE nori_home_ai_requests SET status='failed',error_code='interrupted',updated_at=clock_timestamp() WHERE device_id=$1 AND status IN ('dispatched','running')`, deviceID); err != nil {
		return empty, "server_error"
	}
	if tx.Commit(ctx) != nil {
		return empty, "server_error"
	}
	return tok, ""
}

func (s *Service) revokeToken(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	token := r.PostForm.Get("token")
	if len(token) >= 32 && len(token) <= 200 {
		ctx := r.Context()
		hash := tokenHash(token)
		var err error
		if r.PostForm.Get("token_type_hint") == "device_code" {
			err = s.revokePairing(ctx, hash)
		} else {
			var deviceID, userID string
			err = s.pool.QueryRow(ctx, `SELECT id,user_id FROM nori_home_ai_devices WHERE refresh_hash=$1 OR access_hash=$1 UNION SELECT d.id,d.user_id FROM nori_home_ai_devices d JOIN nori_home_ai_refresh_history h ON h.device_id=d.id WHERE h.token_hash=$1 LIMIT 1`, hash).Scan(&deviceID, &userID)
			if err == nil {
				err = s.Revoke(ctx, userID, deviceID)
			}
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, ErrDeviceNotFound) {
			writeJSON(w, http.StatusServiceUnavailable, oauthError{Error: "temporarily_unavailable"})
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// Revocation accepts the original secret device code so a connector can undo a
// redemption whose token response never reached it. Updating the grant
// serializes cancellation with approval and redemption.
func (s *Service) revokePairing(ctx context.Context, hash []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE nori_home_ai_grants SET status='denied' WHERE device_code_hash=$1 AND status<>'denied'`, hash); err != nil {
		return err
	}
	var deviceID, userID string
	err = tx.QueryRow(ctx, `SELECT id,user_id FROM nori_home_ai_devices WHERE pairing_hash=$1 AND revoked_at IS NULL FOR UPDATE`, hash).Scan(&deviceID, &userID)
	if err == nil {
		if err := revokeDevice(ctx, tx, userID, deviceID); err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return tx.Commit(ctx)
}
