package serviceauth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const defaultGoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

type verifiedEmailKey struct{}

func WithVerifiedEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, verifiedEmailKey{}, strings.ToLower(strings.TrimSpace(email)))
}

func VerifiedEmail(ctx context.Context) (string, bool) {
	email, ok := ctx.Value(verifiedEmailKey{}).(string)
	return email, ok && email != ""
}

type googleIDClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	jwt.RegisteredClaims
}

type jwksCache struct {
	mu          sync.RWMutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time
	lastRefresh time.Time
	url         string
	client      *http.Client
}

func newJWKSCache(jwksURL string) *jwksCache {
	if strings.TrimSpace(jwksURL) == "" {
		jwksURL = defaultGoogleJWKSURL
	}
	return &jwksCache{
		url:    jwksURL,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *jwksCache) getKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" {
		return nil, errors.New("missing kid header")
	}
	c.mu.RLock()
	if time.Since(c.fetchedAt) < 15*time.Minute {
		if k, ok := c.keys[kid]; ok {
			c.mu.RUnlock()
			return k, nil
		}
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetchedAt) < 15*time.Minute {
		if k, ok := c.keys[kid]; ok {
			return k, nil
		}
		if time.Since(c.lastRefresh) < 30*time.Second {
			return nil, errors.New("unknown signing key")
		}
	}
	keys, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	c.keys = keys
	now := time.Now()
	c.fetchedAt = now
	c.lastRefresh = now
	k, ok := c.keys[kid]
	if !ok {
		return nil, errors.New("unknown signing key")
	}
	return k, nil
}

func (c *jwksCache) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch returned %d", resp.StatusCode)
	}
	var payload struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make(map[string]*rsa.PublicKey, len(payload.Keys))
	for _, k := range payload.Keys {
		if k.Kty != "RSA" || k.Kid == "" || k.N == "" || k.E == "" {
			continue
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		eInt := 0
		for _, b := range eb {
			eInt = eInt<<8 | int(b)
		}
		if eInt < 3 {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: eInt}
	}
	if len(out) == 0 {
		return nil, errors.New("no RSA keys in JWKS")
	}
	return out, nil
}

func verifyGoogleIDToken(ctx context.Context, cache *jwksCache, raw string, clientIDs, allowedEmails []string) (string, bool) {
	if len(clientIDs) == 0 || len(raw) == 0 || len(raw) > 8192 {
		return "", false
	}
	claims := &googleIDClaims{}
	token, err := jwt.ParseWithClaims(
		raw,
		claims,
		func(t *jwt.Token) (any, error) {
			kid, _ := t.Header["kid"].(string)
			return cache.getKey(ctx, kid)
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil || !token.Valid {
		return "", false
	}
	if claims.Issuer != "https://accounts.google.com" && claims.Issuer != "accounts.google.com" {
		return "", false
	}
	audOK := false
	for _, aud := range claims.Audience {
		if slices.Contains(clientIDs, aud) {
			audOK = true
			break
		}
	}
	if !audOK || !claims.EmailVerified {
		return "", false
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		return "", false
	}
	if len(allowedEmails) > 0 && !slices.Contains(allowedEmails, email) {
		return "", false
	}
	return email, true
}
