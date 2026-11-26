package rowstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// GCPKMSKeyIDPrefix prefixes Cloud KMS key resource names in stored KeyID values.
	GCPKMSKeyIDPrefix = "gcpkms:"

	defaultGCPKMSEndpoint = "https://cloudkms.googleapis.com/v1"
	defaultMetadataURL    = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	gcpAccessTokenEnv     = "NORI_GCP_ACCESS_TOKEN"
	maxKMSResponseBytes   = 1 << 20
	metadataRefreshSkew   = 60 * time.Second
	defaultKMSHTTPTimeout = 10 * time.Second
)

// GCPKMSConfig configures a GCPKMSKey wrapper.
type GCPKMSConfig struct {
	// KeyName is the full Cloud KMS CryptoKey resource name, e.g.
	// "projects/my-proj/locations/us-central1/keyRings/nori/cryptoKeys/master".
	KeyName string
	// Endpoint overrides the Cloud KMS REST API base URL (defaults to https://cloudkms.googleapis.com/v1).
	Endpoint string
	// HTTPClient is used for Cloud KMS API requests and default metadata token fetches.
	HTTPClient *http.Client
	// TokenSource returns a Bearer OAuth2 access token. If nil, defaults to checking
	// NORI_GCP_ACCESS_TOKEN and falling back to the GCE/Cloud Run metadata server.
	TokenSource func(ctx context.Context) (string, error)
}

// GCPKMSKey implements KeyWrapper against the GCP Cloud KMS REST API.
type GCPKMSKey struct {
	keyName     string
	keyID       string
	endpoint    string
	client      *http.Client
	tokenSource func(ctx context.Context) (string, error)
}

var (
	_ KeyWrapper    = (*GCPKMSKey)(nil)
	_ AADKeyWrapper = (*GCPKMSKey)(nil)
)

// NewGCPKMSKey creates a KeyWrapper backed by a GCP Cloud KMS CryptoKey.
func NewGCPKMSKey(cfg GCPKMSConfig) (*GCPKMSKey, error) {
	keyName := strings.TrimSpace(cfg.KeyName)
	keyName = strings.TrimPrefix(keyName, "/")
	if keyName == "" {
		return nil, errors.New("gcp kms key name must not be empty")
	}
	if strings.ContainsAny(keyName, " \t\r\n?#") {
		return nil, errors.New("gcp kms key name contains invalid characters")
	}

	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		endpoint = defaultGCPKMSEndpoint
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultKMSHTTPTimeout}
	}

	tokenSource := cfg.TokenSource
	if tokenSource == nil {
		tokenSource = newDefaultGCPTokenSource(client, defaultMetadataURL, time.Now).Token
	}

	return &GCPKMSKey{
		keyName:     keyName,
		keyID:       GCPKMSKeyIDPrefix + keyName,
		endpoint:    endpoint,
		client:      client,
		tokenSource: tokenSource,
	}, nil
}

// KeyID returns the canonical key identifier stored alongside wrapped data keys.
func (k *GCPKMSKey) KeyID() string {
	return k.keyID
}

type kmsEncryptRequest struct {
	Plaintext                   string `json:"plaintext"`
	AdditionalAuthenticatedData string `json:"additionalAuthenticatedData"`
}

type kmsEncryptResponse struct {
	Ciphertext string `json:"ciphertext"`
	Name       string `json:"name"`
}

type kmsDecryptRequest struct {
	Ciphertext                  string `json:"ciphertext"`
	AdditionalAuthenticatedData string `json:"additionalAuthenticatedData"`
}

type kmsDecryptResponse struct {
	Plaintext string `json:"plaintext"`
}

// Wrap encrypts dataKey with Cloud KMS, binding it to dataKeyAAD.
func (k *GCPKMSKey) Wrap(ctx context.Context, dataKey []byte) ([]byte, string, error) {
	return k.WrapWithAAD(ctx, dataKey, nil)
}

// WrapWithAAD encrypts dataKey with Cloud KMS, binding it to aad (or dataKeyAAD if empty).
func (k *GCPKMSKey) WrapWithAAD(ctx context.Context, dataKey, aad []byte) ([]byte, string, error) {
	if len(dataKey) == 0 {
		return nil, "", errors.New("data key must not be empty")
	}
	reqBody := kmsEncryptRequest{
		Plaintext:                   base64.StdEncoding.EncodeToString(dataKey),
		AdditionalAuthenticatedData: base64.StdEncoding.EncodeToString(effectiveDataKeyAAD(aad)),
	}
	var respBody kmsEncryptResponse
	url := k.endpoint + "/" + k.keyName + ":encrypt"
	if err := k.doJSON(ctx, url, reqBody, &respBody); err != nil {
		return nil, "", fmt.Errorf("gcp kms encrypt: %w", err)
	}
	if respBody.Ciphertext == "" {
		return nil, "", errors.New("gcp kms encrypt: empty ciphertext in response")
	}
	wrapped, err := base64.StdEncoding.DecodeString(respBody.Ciphertext)
	if err != nil {
		return nil, "", fmt.Errorf("gcp kms encrypt: invalid ciphertext encoding: %w", err)
	}
	return wrapped, k.keyID, nil
}

// Unwrap decrypts wrapped with Cloud KMS using dataKeyAAD.
func (k *GCPKMSKey) Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	return k.UnwrapWithAAD(ctx, wrapped, keyID, nil)
}

// UnwrapWithAAD decrypts wrapped with Cloud KMS using aad (or dataKeyAAD if empty).
func (k *GCPKMSKey) UnwrapWithAAD(ctx context.Context, wrapped []byte, keyID string, aad []byte) ([]byte, error) {
	if keyID != k.keyID {
		return nil, fmt.Errorf("the row was wrapped by key %s, not this gcp kms key %s", keyID, k.keyID)
	}
	if len(wrapped) == 0 {
		return nil, errors.New("wrapped key must not be empty")
	}
	reqBody := kmsDecryptRequest{
		Ciphertext:                  base64.StdEncoding.EncodeToString(wrapped),
		AdditionalAuthenticatedData: base64.StdEncoding.EncodeToString(effectiveDataKeyAAD(aad)),
	}
	var respBody kmsDecryptResponse
	url := k.endpoint + "/" + k.keyName + ":decrypt"
	if err := k.doJSON(ctx, url, reqBody, &respBody); err != nil {
		return nil, fmt.Errorf("gcp kms decrypt: %w", err)
	}
	if respBody.Plaintext == "" {
		return nil, errors.New("gcp kms decrypt: empty plaintext in response")
	}
	dataKey, err := base64.StdEncoding.DecodeString(respBody.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("gcp kms decrypt: invalid plaintext encoding: %w", err)
	}
	return dataKey, nil
}

func (k *GCPKMSKey) doJSON(ctx context.Context, url string, payload any, out any) error {
	token, err := k.tokenSource(ctx)
	if err != nil {
		return fmt.Errorf("fetch access token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("access token is empty")
	}

	rawReq, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawReq))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, maxKMSResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(rawResp))
		if len(msg) > 256 {
			msg = msg[:256]
		}
		return fmt.Errorf("http %d: %s", resp.StatusCode, msg)
	}
	if err := json.Unmarshal(rawResp, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

type metadataTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type defaultGCPTokenSource struct {
	client      *http.Client
	metadataURL string
	now         func() time.Time

	mu        sync.Mutex
	cached    string
	expiresAt time.Time
}

func newDefaultGCPTokenSource(client *http.Client, metadataURL string, now func() time.Time) *defaultGCPTokenSource {
	if now == nil {
		now = time.Now
	}
	return &defaultGCPTokenSource{
		client:      client,
		metadataURL: metadataURL,
		now:         now,
	}
}

func (s *defaultGCPTokenSource) Token(ctx context.Context) (string, error) {
	if envTok := strings.TrimSpace(os.Getenv(gcpAccessTokenEnv)); envTok != "" {
		return envTok, nil
	}

	s.mu.Lock()
	if s.cached != "" && s.now().Before(s.expiresAt) {
		tok := s.cached
		s.mu.Unlock()
		return tok, nil
	}
	s.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.metadataURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKMSResponseBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("metadata server returned http %d", resp.StatusCode)
	}

	var parsed metadataTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode metadata token: %w", err)
	}
	tok := strings.TrimSpace(parsed.AccessToken)
	if tok == "" {
		return "", errors.New("metadata server returned empty access token")
	}

	ttl := time.Duration(parsed.ExpiresIn) * time.Second
	if ttl > metadataRefreshSkew {
		ttl -= metadataRefreshSkew
	} else {
		ttl = 0
	}

	s.mu.Lock()
	s.cached = tok
	if ttl > 0 {
		s.expiresAt = s.now().Add(ttl)
	} else {
		s.expiresAt = time.Time{}
	}
	s.mu.Unlock()

	return tok, nil
}
