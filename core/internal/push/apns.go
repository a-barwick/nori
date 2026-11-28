package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	DefaultSandboxURL    = "https://api.sandbox.push.apple.com"
	DefaultProductionURL = "https://api.push.apple.com"

	// DefaultExpirationTTL bounds APNs delivery retries for approvals, morning briefs, and updates.
	DefaultExpirationTTL = 24 * time.Hour
	// UrgentExpirationTTL bounds APNs delivery retries for time-sensitive urgent alerts.
	UrgentExpirationTTL = 1 * time.Hour

	// Apple rejects provider JWTs older than 60 minutes; refresh at 45 minutes
	// to stay well under the 50-minute ceiling with clock-skew margin.
	defaultTokenRefreshInterval = 45 * time.Minute
)

// Notification is the lock-screen-safe APNs payload passed to a Sender.
type Notification struct {
	DeliveryID    string
	CollapseID    string
	InteractionID string
	Title         string
	Body          string
	Sound         string
	Badge         *int
	TTL           time.Duration
}

// Sender delivers a push notification to a single registered APNs device.
type Sender interface {
	Send(ctx context.Context, device Device, n Notification) error
}

// APNSError represents a non-200 response from Apple Push Notification service.
type APNSError struct {
	StatusCode int
	Reason     string
	Timestamp  int64
}

func (e *APNSError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("apns: status %d (%s)", e.StatusCode, e.Reason)
	}
	return fmt.Sprintf("apns: status %d", e.StatusCode)
}

// IsInvalidTokenError reports whether APNs rejected the device token as permanently invalid
// (410 Unregistered or 400 BadDeviceToken / DeviceTokenNotForTopic).
func IsInvalidTokenError(err error) bool {
	var apnsErr *APNSError
	if !errors.As(err, &apnsErr) {
		return false
	}
	if apnsErr.StatusCode == http.StatusGone {
		return true
	}
	if apnsErr.StatusCode == http.StatusBadRequest {
		return apnsErr.Reason == "BadDeviceToken" || apnsErr.Reason == "DeviceTokenNotForTopic"
	}
	return false
}

// IsPermanentError reports whether retrying the same APNs request is futile.
func IsPermanentError(err error) bool {
	var apnsErr *APNSError
	if !errors.As(err, &apnsErr) {
		return false
	}
	// 429 TooManyRequests and 5xx are transient; other 4xx errors are permanent.
	return apnsErr.StatusCode >= 400 && apnsErr.StatusCode < 500 && apnsErr.StatusCode != http.StatusTooManyRequests
}

// APNSConfig configures the direct HTTP/2 ES256 APNs client.
type APNSConfig struct {
	KeyID         string
	TeamID        string
	BundleID      string
	PrivateKeyPEM string
	SandboxURL    string
	ProductionURL string
	HTTPClient    *http.Client
	Devices       DeviceDeleter
	Now           func() time.Time
}

// ConfigFromEnv loads APNSConfig from standard NORI_APNS_* environment variables.
// Returns (cfg, false) when APNs is not configured.
func ConfigFromEnv() (APNSConfig, bool) {
	cfg := APNSConfig{
		KeyID:         strings.TrimSpace(os.Getenv("NORI_APNS_KEY_ID")),
		TeamID:        strings.TrimSpace(os.Getenv("NORI_APNS_TEAM_ID")),
		BundleID:      strings.TrimSpace(os.Getenv("NORI_APNS_BUNDLE_ID")),
		PrivateKeyPEM: strings.TrimSpace(os.Getenv("NORI_APNS_PRIVATE_KEY")),
	}
	if cfg.KeyID == "" || cfg.TeamID == "" || cfg.BundleID == "" || cfg.PrivateKeyPEM == "" {
		return cfg, false
	}
	return cfg, true
}

// APNSClient sends notifications directly to Apple's HTTP/2 APNs API using
// cached ES256 JWT provider tokens signed by the configured .p8 key.
type APNSClient struct {
	keyID         string
	teamID        string
	bundleID      string
	privateKey    *ecdsa.PrivateKey
	sandboxURL    string
	productionURL string
	httpClient    *http.Client
	devices       DeviceDeleter
	now           func() time.Time

	tokenMu     sync.Mutex
	cachedToken string
	tokenIssued time.Time
}

// NewAPNSClient validates the configuration and parses the .p8 ES256 private key.
func NewAPNSClient(cfg APNSConfig) (*APNSClient, error) {
	if strings.TrimSpace(cfg.KeyID) == "" {
		return nil, errors.New("apns: KeyID is required")
	}
	if strings.TrimSpace(cfg.TeamID) == "" {
		return nil, errors.New("apns: TeamID is required")
	}
	if strings.TrimSpace(cfg.BundleID) == "" {
		return nil, errors.New("apns: BundleID is required")
	}
	key, err := parseP8PrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}

	sandboxURL := strings.TrimRight(strings.TrimSpace(cfg.SandboxURL), "/")
	if sandboxURL == "" {
		sandboxURL = DefaultSandboxURL
	}
	productionURL := strings.TrimRight(strings.TrimSpace(cfg.ProductionURL), "/")
	if productionURL == "" {
		productionURL = DefaultProductionURL
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ForceAttemptHTTP2 = true
		transport.MaxIdleConns = 32
		transport.MaxIdleConnsPerHost = 16
		transport.IdleConnTimeout = 90 * time.Second
		httpClient = &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
		}
	}

	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}

	return &APNSClient{
		keyID:         strings.TrimSpace(cfg.KeyID),
		teamID:        strings.TrimSpace(cfg.TeamID),
		bundleID:      strings.TrimSpace(cfg.BundleID),
		privateKey:    key,
		sandboxURL:    sandboxURL,
		productionURL: productionURL,
		httpClient:    httpClient,
		devices:       cfg.Devices,
		now:           nowFn,
	}, nil
}

func parseP8PrivateKey(pemData string) (*ecdsa.PrivateKey, error) {
	if strings.TrimSpace(pemData) == "" {
		return nil, errors.New("apns: private key PEM is required")
	}
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("apns: failed to decode .p8 PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Fall back to SEC1 EC private key if supplied in that format.
		ecKey, ecErr := x509.ParseECPrivateKey(block.Bytes)
		if ecErr != nil {
			return nil, fmt.Errorf("apns: parse .p8 private key: %w", err)
		}
		return ecKey, nil
	}
	ecKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns: .p8 private key must be ECDSA")
	}
	return ecKey, nil
}

func (c *APNSClient) providerToken() (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	now := c.now().UTC()
	if c.cachedToken != "" && now.Sub(c.tokenIssued) < defaultTokenRefreshInterval {
		return c.cachedToken, nil
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": c.teamID,
		"iat": now.Unix(),
	})
	tok.Header["kid"] = c.keyID

	signed, err := tok.SignedString(c.privateKey)
	if err != nil {
		return "", fmt.Errorf("apns: sign provider JWT: %w", err)
	}
	c.cachedToken = signed
	c.tokenIssued = now
	return signed, nil
}

type apnsPayload struct {
	APS           apnsAPS `json:"aps"`
	InteractionID string  `json:"interaction_id,omitempty"`
}

type apnsAPS struct {
	Alert apnsAlert `json:"alert"`
	Sound string    `json:"sound,omitempty"`
	Badge *int      `json:"badge,omitempty"`
}

type apnsAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type apnsErrorBody struct {
	Reason    string `json:"reason"`
	Timestamp int64  `json:"timestamp"`
}

// Send delivers a single lock-screen-safe alert push to device over HTTP/2.
// On 410 Unregistered or 400 BadDeviceToken, it automatically deletes the token
// from the configured DeviceDeleter (if set) before returning the *APNSError.
func (c *APNSClient) Send(ctx context.Context, device Device, n Notification) error {
	if !ValidEnvironment(device.Environment) {
		return ErrInvalidEnvironment
	}
	if strings.TrimSpace(device.Token) == "" {
		return ErrInvalidToken
	}

	bearer, err := c.providerToken()
	if err != nil {
		return err
	}

	title, body := SanitizeLockScreenCopy(n.Title, n.Body)
	sound := strings.TrimSpace(n.Sound)
	if sound == "" {
		sound = "default"
	}

	payload := apnsPayload{
		APS: apnsAPS{
			Alert: apnsAlert{
				Title: title,
				Body:  body,
			},
			Sound: sound,
			Badge: n.Badge,
		},
		InteractionID: strings.TrimSpace(n.InteractionID),
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("apns: marshal payload: %w", err)
	}

	baseURL := c.productionURL
	if device.Environment == EnvSandbox {
		baseURL = c.sandboxURL
	}
	endpoint := baseURL + "/3/device/" + url.PathEscape(device.Token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return fmt.Errorf("apns: build request: %w", err)
	}
	req.Header.Set("authorization", "bearer "+bearer)
	req.Header.Set("apns-topic", c.bundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	ttl := n.TTL
	if ttl <= 0 {
		ttl = expirationTTLForBody(body)
	}
	req.Header.Set("apns-expiration", strconv.FormatInt(c.now().UTC().Add(ttl).Unix(), 10))
	req.Header.Set("content-type", "application/json")

	collapseID := strings.TrimSpace(n.CollapseID)
	if collapseID == "" {
		collapseID = strings.TrimSpace(n.DeliveryID)
	}
	if collapseID != "" {
		if len(collapseID) > 64 {
			collapseID = collapseID[:64]
		}
		req.Header.Set("apns-collapse-id", collapseID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("apns: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}

	var errBody apnsErrorBody
	rawErr, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	_ = json.Unmarshal(rawErr, &errBody)

	apnsErr := &APNSError{
		StatusCode: resp.StatusCode,
		Reason:     errBody.Reason,
		Timestamp:  errBody.Timestamp,
	}
	if c.devices != nil && IsInvalidTokenError(apnsErr) {
		_ = c.devices.DeleteDeviceByToken(ctx, device.UserID, device.Token)
	}
	return apnsErr
}
