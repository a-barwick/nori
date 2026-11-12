package serviceauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	CallerHeader = "X-Nori-Service-Assertion"
	QueueHeader  = "X-Nori-Queue-Assertion"
	Audience     = "nori-core"
)

type Callers struct {
	Mode                          string
	Web, Internal, Gateway, Queue string
}

func (c Callers) Validate() error {
	switch c.Mode {
	case "", "off", "observe", "enforce":
	default:
		return fmt.Errorf("NORI_ROUTE_AUTH_MODE must be off, observe or enforce")
	}
	seen := map[string]bool{}
	for _, key := range []string{c.Web, c.Internal, c.Gateway, c.Queue} {
		if key == "" {
			continue
		}
		if len(key) < 32 || seen[key] {
			return errors.New("caller keys must be distinct and at least 32 bytes")
		}
		seen[key] = true
	}
	return nil
}

func SignCaller(key, role, subject string) (string, error) {
	if len(key) < 32 {
		return "", errors.New("caller signing key is missing or too short")
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer: "nori-" + role, Subject: subject, Audience: jwt.ClaimStrings{Audience},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(key))
}

func (c Callers) Verify(raw string) (role, subject string) {
	if len(raw) == 0 || len(raw) > 4096 {
		return "", ""
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(_ *jwt.Token) (any, error) {
		var key string
		switch claims.Issuer {
		case "nori-browser":
			key = c.Web
		case "nori-internal":
			key = c.Internal
		case "nori-gateway":
			key = c.Gateway
		}
		if len(key) < 32 {
			return nil, errors.New("unknown service caller")
		}
		return []byte(key), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience(Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(5*time.Second))
	if err != nil || !token.Valid || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return "", ""
	}
	lifetime := claims.ExpiresAt.Sub(claims.IssuedAt.Time)
	if lifetime <= 0 || lifetime > time.Minute {
		return "", ""
	}
	return strings.TrimPrefix(claims.Issuer, "nori-"), claims.Subject
}

// QueueAssertion binds a wake to one process path. The queue can retry it after
// any delay. A wake is only a notification to drain already saved work.
func QueueAssertion(key, path string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("nori-queue-v1:" + path))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c Callers) QueueValid(path, assertion string) bool {
	return len(c.Queue) >= 32 && hmac.Equal([]byte(QueueAssertion(c.Queue, path)), []byte(assertion))
}
