package serviceauth

import (
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"strings"
	"time"
)

const issuer = "nori-service"

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
func (c Config) Protect(next http.Handler) http.Handler {
	var jwks *jwksCache
	if len(c.GoogleClientIDs) > 0 {
		jwks = newJWKSCache(c.GoogleJWKSURL)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || scheme != "Bearer" {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		if c.Valid(token) {
			next.ServeHTTP(w, r)
			return
		}
		if jwks != nil {
			if email, ok := verifyGoogleIDToken(r.Context(), jwks, token, c.GoogleClientIDs, c.AllowedEmails); ok {
				next.ServeHTTP(w, r.WithContext(WithVerifiedEmail(r.Context(), email)))
				return
			}
		}
		http.Error(w, "Authentication required", http.StatusUnauthorized)
	})
}
