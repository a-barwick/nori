package serviceauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"nori/internal/httpjson"
)

const issuer = "nori-service"

type serviceKey struct{}

func IsService(ctx context.Context) bool {
	verified, _ := ctx.Value(serviceKey{}).(bool)
	return verified
}

type Config struct {
	Key, Audience   string
	GoogleClientIDs []string
	AllowedEmails   []string
	GoogleJWKSURL   string
}

func (c Config) Validate() error {
	if len(c.Key) < 32 || strings.TrimSpace(c.Audience) == "" {
		return errors.New("service authentication needs NORI_SERVICE_TOKEN_KEY (at least 32 bytes) and NORI_SERVICE_AUDIENCE")
	}
	return nil
}
func (c Config) Token() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Issuer: issuer, Audience: jwt.ClaimStrings{c.Audience}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}).SignedString([]byte(c.Key))
}
func (c Config) Valid(raw string) bool {
	if c.Validate() != nil || len(raw) > 4096 {
		return false
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return []byte(c.Key), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(c.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(5*time.Second))
	return err == nil && token.Valid && claims.IssuedAt != nil && claims.ExpiresAt != nil && claims.ExpiresAt.Sub(claims.IssuedAt.Time) > 0 && claims.ExpiresAt.Sub(claims.IssuedAt.Time) <= time.Minute
}
func isHealthExemptPath(path string) bool {
	return path == "/healthz" || path == "/api/healthz" || path == "/readyz" || path == "/api/readyz"
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, http.StatusUnauthorized, map[string]string{
		"error": "Authentication required",
		"code":  "unauthorized",
	})
}

func (c Config) Protect(next http.Handler) http.Handler {
	var jwks *jwksCache
	if len(c.GoogleClientIDs) > 0 {
		jwks = newJWKSCache(c.GoogleJWKSURL)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthExemptPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || scheme != "Bearer" {
			writeUnauthorized(w)
			return
		}
		if c.Valid(token) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), serviceKey{}, true)))
			return
		}
		if jwks != nil {
			if email, identity, ok := verifyGoogleIDToken(r.Context(), jwks, token, c.GoogleClientIDs, c.AllowedEmails); ok {
				ctx := WithVerifiedIdentity(r.Context(), email, identity)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		writeUnauthorized(w)
	})
}
