package serviceauth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"nori/internal/serviceauth"
)

func TestGoogleOIDCProtect(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eBytes := big.NewInt(int64(priv.PublicKey.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": "k1",
				"n":   base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			}},
		})
	}))
	defer jwks.Close()

	cfg := serviceauth.Config{
		Key:             testSecret,
		Audience:        testAud,
		GoogleClientIDs: []string{"ios-client-id.apps.googleusercontent.com"},
		AllowedEmails:   []string{"austinbarwickdavis@gmail.com"},
		GoogleJWKSURL:   jwks.URL,
	}

	var gotEmail string
	var gotIsUser bool
	h := cfg.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEmail, gotIsUser = serviceauth.VerifiedEmail(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	sign := func(claims jwt.MapClaims, kid string) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = kid
		raw, err := tok.SignedString(priv)
		if err != nil {
			t.Fatalf("SignedString: %v", err)
		}
		return raw
	}

	now := time.Now()
	valid := sign(jwt.MapClaims{
		"iss":            "https://accounts.google.com",
		"aud":            "ios-client-id.apps.googleusercontent.com",
		"sub":            "12345",
		"email":          "AustinBarwickDavis@gmail.com",
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(5 * time.Minute).Unix(),
	}, "k1")

	req := httptest.NewRequest(http.MethodGet, "/users/u1/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+valid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !gotIsUser || gotEmail != "austinbarwickdavis@gmail.com" {
		t.Fatalf("valid Google ID token failed: code=%d isUser=%v email=%q", rec.Code, gotIsUser, gotEmail)
	}

	wrongEmail := sign(jwt.MapClaims{
		"iss":            "https://accounts.google.com",
		"aud":            "ios-client-id.apps.googleusercontent.com",
		"sub":            "99999",
		"email":          "intruder@gmail.com",
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(5 * time.Minute).Unix(),
	}, "k1")
	req2 := httptest.NewRequest(http.MethodGet, "/users/u1/v1/capabilities", nil)
	req2.Header.Set("Authorization", "Bearer "+wrongEmail)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("wrong email should be 401, got %d", rec2.Code)
	}

	wrongAud := sign(jwt.MapClaims{
		"iss":            "https://accounts.google.com",
		"aud":            "other-client.apps.googleusercontent.com",
		"sub":            "12345",
		"email":          "austinbarwickdavis@gmail.com",
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(5 * time.Minute).Unix(),
	}, "k1")
	req3 := httptest.NewRequest(http.MethodGet, "/users/u1/v1/capabilities", nil)
	req3.Header.Set("Authorization", "Bearer "+wrongAud)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("wrong audience should be 401, got %d", rec3.Code)
	}

	for _, test := range []struct {
		name   string
		change func(jwt.MapClaims)
	}{
		{"missing subject", func(c jwt.MapClaims) { delete(c, "sub") }},
		{"missing issued-at", func(c jwt.MapClaims) { delete(c, "iat") }},
		{"invalid lifetime", func(c jwt.MapClaims) { c["exp"] = c["iat"] }},
		{"multiple audiences without authorized party", func(c jwt.MapClaims) {
			c["aud"] = []string{"ios-client-id.apps.googleusercontent.com", "another-client"}
		}},
		{"multiple audiences with different authorized party", func(c jwt.MapClaims) {
			c["aud"] = []string{"ios-client-id.apps.googleusercontent.com", "another-client"}
			c["azp"] = "another-client"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := jwt.MapClaims{
				"iss": "https://accounts.google.com", "aud": "ios-client-id.apps.googleusercontent.com", "sub": "12345",
				"email": "austinbarwickdavis@gmail.com", "email_verified": true,
				"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
			}
			test.change(claims)
			req := httptest.NewRequest(http.MethodGet, "/users/u1/v1/capabilities", nil)
			req.Header.Set("Authorization", "Bearer "+sign(claims, "k1"))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("invalid claims received status %d", rec.Code)
			}
		})
	}
}
