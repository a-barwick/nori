package homeai

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/homeaiwire"
	"nori/internal/users"
)

var (
	ErrOffline            = errors.New("home connector is offline")
	ErrBusy               = errors.New("home connector is busy")
	ErrRevoked            = errors.New("home connector was disconnected")
	ErrDeviceNotFound     = errors.New("home connector not found")
	ErrOllamaUnavailable  = errors.New("Ollama is unavailable")
	ErrModelUnavailable   = errors.New("home model is unavailable")
	ErrModelChanged       = errors.New("home model has changed")
	ErrInterrupted        = errors.New("home model request was interrupted")
	ErrPairingNotFound    = errors.New("pairing code not found")
	ErrPairingExpired     = errors.New("pairing code expired")
	ErrPairingUsed        = errors.New("pairing code was already used")
	ErrPairingRateLimited = errors.New("too many pairing attempts")
	ErrDeviceLimit        = errors.New("home connector limit reached")
	ErrInvalidRequest     = errors.New("invalid home connector request")
)

const (
	maxRequestBytes = 2 << 20
	maxChunkBytes   = 32 << 10
	maxResultBytes  = 8 << 20
	accessLifetime  = 50 * time.Minute
	refreshLifetime = 90 * 24 * time.Hour
	staleAfter      = 90 * time.Second
)

type Options struct {
	PostgresURL       string
	Key               []byte
	VerificationURI   string
	MaxConns          int32
	PostgresListenURL string
}

type Service struct {
	pool            *pgxpool.Pool
	aead            cipher.AEAD
	verificationURI string
	listenURL       string
	stop            context.CancelFunc
	done            chan struct{}
	cleanupDone     chan struct{}
	mu              sync.Mutex
	waiters         map[string]map[chan struct{}]struct{}
}

type Pairing struct {
	UserCode   string    `json:"user_code"`
	DeviceName string    `json:"device_name"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Device struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Status   string             `json:"status"`
	Models   []homeaiwire.Model `json:"models"`
	LastSeen *time.Time         `json:"last_seen_at,omitempty"`
	Version  string             `json:"version,omitempty"`
}

func Open(ctx context.Context, opts Options) (*Service, error) {
	if len(opts.Key) != 32 {
		return nil, errors.New("home AI encryption key must contain 32 bytes")
	}
	u, err := url.Parse(opts.VerificationURI)
	if err != nil || u == nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid home AI verification URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, errors.New("home AI verification URL must use HTTPS")
	}
	block, err := aes.NewCipher(opts.Key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	config, err := pgxpool.ParseConfig(opts.PostgresURL)
	if err != nil {
		return nil, err
	}
	if opts.MaxConns > 0 {
		config.MaxConns = opts.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	listenURL := opts.PostgresListenURL
	if listenURL == "" {
		listenURL = opts.PostgresURL
	}
	s := &Service{pool: pool, aead: aead, verificationURI: u.String(), listenURL: listenURL, done: make(chan struct{}), cleanupDone: make(chan struct{}), waiters: map[string]map[chan struct{}]struct{}{}}
	if err := s.Ready(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	watchCtx, stop := context.WithCancel(context.Background())
	s.stop = stop
	go s.watch(watchCtx)
	go func() { defer close(s.cleanupDone); s.cleanupLoop(watchCtx, time.Minute) }()
	return s, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Service) Close() {
	if s != nil && s.pool != nil {
		s.stop()
		<-s.done
		<-s.cleanupDone
		s.pool.Close()
	}
}

func (s *Service) Ready(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("home AI service is unavailable")
	}
	var ok bool
	if err := s.pool.QueryRow(ctx, "SELECT to_regclass('nori_home_ai_devices') IS NOT NULL AND to_regclass('nori_home_ai_requests') IS NOT NULL").Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("home AI database migration is not applied")
	}
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT draining, pairing_hash FROM nori_home_ai_devices WHERE false)`).Scan(&ok); err != nil {
		return err
	}
	return nil
}

func (s *Service) seal(id string, value []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, value, []byte(id)), nil
}

func (s *Service) unseal(id string, value []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(value) < n {
		return nil, errors.New("invalid encrypted home AI data")
	}
	return s.aead.Open(nil, value[:n], value[n:], []byte(id))
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(v string) []byte { h := sha256.Sum256([]byte(v)); return h[:] }

func normalizedCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func randomCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:]), nil
}

func validName(name string) bool {
	if len(name) < 1 || len(name) > 100 {
		return false
	}
	return strings.IndexFunc(name, unicode.IsControl) < 0
}

func userActive(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, userID string) (bool, error) {
	if !users.ValidID(userID) {
		return false, nil
	}
	var active bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nori_home_ai_accounts WHERE id=$1)`, userID).Scan(&active)
	return active, err
}

func (s *Service) LookupPairing(ctx context.Context, code string) (Pairing, error) {
	var p Pairing
	var status string
	err := s.pool.QueryRow(ctx, `SELECT user_code, device_name, expires_at, status FROM nori_home_ai_grants WHERE user_code_hash=$1`, tokenHash(normalizedCode(code))).Scan(&p.UserCode, &p.DeviceName, &p.ExpiresAt, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrPairingNotFound
	}
	if err != nil {
		return p, err
	}
	if time.Now().After(p.ExpiresAt) {
		return p, ErrPairingExpired
	}
	if status != "pending" {
		return p, ErrPairingUsed
	}
	return p, nil
}

func (s *Service) Approve(ctx context.Context, userID, code string) (Device, error) {
	var d Device
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return d, err
	}
	defer tx.Rollback(ctx)
	var status string
	var expiry time.Time
	err = tx.QueryRow(ctx, `SELECT device_name, status, expires_at FROM nori_home_ai_grants WHERE user_code_hash=$1 FOR UPDATE`, tokenHash(normalizedCode(code))).Scan(&d.Name, &status, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrPairingNotFound
	}
	if err != nil {
		return d, err
	}
	if time.Now().After(expiry) {
		return d, ErrPairingExpired
	}
	if status != "pending" {
		return d, ErrPairingUsed
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-home-ai:"+userID); err != nil {
		return d, err
	}
	active, err := userActive(ctx, tx, userID)
	if err != nil {
		return d, err
	}
	if !active {
		return d, ErrRevoked
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM nori_home_ai_devices WHERE user_id=$1 AND revoked_at IS NULL) + (SELECT count(*) FROM nori_home_ai_grants WHERE user_id=$1 AND status='approved' AND expires_at>clock_timestamp())`, userID).Scan(&count)
	if err != nil {
		return d, err
	}
	if count >= 1 {
		return d, ErrDeviceLimit
	}
	_, err = tx.Exec(ctx, `UPDATE nori_home_ai_grants SET status='approved', user_id=$1 WHERE user_code_hash=$2`, userID, tokenHash(normalizedCode(code)))
	if err != nil {
		return d, err
	}
	d.Status = "pairing"
	return d, tx.Commit(ctx)
}

func (s *Service) List(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,last_seen,connection_id IS NOT NULL AND NOT draining AND last_seen > clock_timestamp()-interval '90 seconds',ollama_available,models,version FROM nori_home_ai_devices WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		var online, available bool
		var models []byte
		if err := rows.Scan(&d.ID, &d.Name, &d.LastSeen, &online, &available, &models, &d.Version); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(models, &d.Models); err != nil {
			return nil, err
		}
		if d.Models == nil {
			d.Models = []homeaiwire.Model{}
		}
		d.Status = "offline"
		if online && !available {
			d.Status = "ollama_unavailable"
		}
		if online && available {
			d.Status = "online"
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) Revoke(ctx context.Context, userID, deviceID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := revokeDevice(ctx, tx, userID, deviceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func revokeDevice(ctx context.Context, tx pgx.Tx, userID, deviceID string) error {
	r, err := tx.Exec(ctx, `UPDATE nori_home_ai_devices SET revoked_at=COALESCE(revoked_at,clock_timestamp()),generation=generation+1,connection_id=NULL WHERE id=$1 AND user_id=$2`, deviceID, userID)
	if err != nil {
		return err
	}
	if r.RowsAffected() == 0 {
		return ErrDeviceNotFound
	}
	if _, err = tx.Exec(ctx, `DELETE FROM nori_home_ai_requests WHERE device_id=$1 AND user_id=$2`, deviceID, userID); err != nil {
		return err
	}
	return notify(ctx, tx, "d:"+deviceID)
}

func expireRequests(ctx context.Context, tx pgx.Tx, deviceID string) error {
	rows, err := tx.Query(ctx, `UPDATE nori_home_ai_requests SET status='failed',error_code='interrupted',payload=decode('','hex'),updated_at=clock_timestamp() WHERE ($1='' OR device_id=$1) AND deadline<=clock_timestamp() AND status IN ('pending','dispatched','running') RETURNING id,device_id`, deviceID)
	if err != nil {
		return err
	}
	type expired struct{ request, device string }
	var items []expired
	for rows.Next() {
		var item expired
		if err = rows.Scan(&item.request, &item.device); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if err = notify(ctx, tx, "d:"+item.device); err != nil {
			return err
		}
		if err = notify(ctx, tx, "r:"+item.request); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) cleanup(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = expireRequests(ctx, tx, ""); err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM nori_home_ai_grants WHERE expires_at < clock_timestamp()-interval '1 hour'`,
		`DELETE FROM nori_home_ai_refresh_history WHERE expires_at < clock_timestamp()`,
		`DELETE FROM nori_home_ai_requests WHERE deadline < clock_timestamp()-interval '15 minutes'`,
		`DELETE FROM nori_home_ai_pairing_attempts WHERE bucket < clock_timestamp()-interval '1 hour'`,
	} {
		if _, err = tx.Exec(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) CheckPairingAttempt(ctx context.Context, userID string) error {
	if !users.ValidID(userID) {
		return ErrInvalidRequest
	}
	var count int
	err := s.pool.QueryRow(ctx, `INSERT INTO nori_home_ai_pairing_attempts(user_id,bucket,attempts) VALUES($1,date_trunc('minute',clock_timestamp()),1) ON CONFLICT(user_id,bucket) DO UPDATE SET attempts=nori_home_ai_pairing_attempts.attempts+1 RETURNING attempts`, userID).Scan(&count)
	if err != nil {
		return err
	}
	if count > 12 {
		return ErrPairingRateLimited
	}
	return nil
}

func IsPreflightError(err error) bool {
	return errors.Is(err, ErrOffline) || errors.Is(err, ErrBusy) || errors.Is(err, ErrOllamaUnavailable) || errors.Is(err, ErrModelUnavailable) || errors.Is(err, ErrModelChanged) || errors.Is(err, ErrDeviceNotFound) || errors.Is(err, ErrInvalidRequest)
}
