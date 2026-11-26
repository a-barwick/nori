package serviceauth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"nori/internal/serviceauth"
)

const (
	testSecret = "0123456789abcdef0123456789abcdef"
	testAud    = "nori-core"
)

func signCustomToken(t *testing.T, method jwt.SigningMethod, key any, claims jwt.RegisteredClaims) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return raw
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name   string
		cfg    serviceauth.Config
		wantOK bool
	}{
		{"valid_32_byte_key", serviceauth.Config{Key: testSecret, Audience: testAud}, true},
		{"short_key_31_bytes", serviceauth.Config{Key: strings.Repeat("a", 31), Audience: testAud}, false},
		{"empty_key", serviceauth.Config{Key: "", Audience: testAud}, false},
		{"empty_audience", serviceauth.Config{Key: testSecret, Audience: ""}, false},
		{"whitespace_audience", serviceauth.Config{Key: testSecret, Audience: "   "}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantOK && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tc.wantOK {
				if _, err := tc.cfg.Token(); err == nil {
					t.Errorf("Token() = nil on invalid config, want error")
				}
			}
		})
	}
}

func TestTokenValidationMatrix(t *testing.T) {
	cfg := serviceauth.Config{Key: testSecret, Audience: testAud}
	validTok, err := cfg.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	now := time.Now()

	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{
			name:  "valid_hs256_token",
			token: validTok,
			want:  true,
		},
		{
			name: "expired_beyond_leeway",
			token: signCustomToken(t, jwt.SigningMethodHS256, []byte(testSecret), jwt.RegisteredClaims{
				Issuer:    "nori-service",
				Audience:  jwt.ClaimStrings{testAud},
				IssuedAt:  jwt.NewNumericDate(now.Add(-2 * time.Minute)),
				ExpiresAt: jwt.NewNumericDate(now.Add(-time.Minute)),
			}),
			want: false,
		},
		{
			name: "wrong_audience",
			token: signCustomToken(t, jwt.SigningMethodHS256, []byte(testSecret), jwt.RegisteredClaims{
				Issuer:    "nori-service",
				Audience:  jwt.ClaimStrings{"other-service"},
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			}),
			want: false,
		},
		{
			name: "wrong_issuer",
			token: signCustomToken(t, jwt.SigningMethodHS256, []byte(testSecret), jwt.RegisteredClaims{
				Issuer:    "rogue-issuer",
				Audience:  jwt.ClaimStrings{testAud},
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			}),
			want: false,
		},
		{
			name: "wrong_secret",
			token: signCustomToken(t, jwt.SigningMethodHS256, []byte("fedcba9876543210fedcba9876543210"), jwt.RegisteredClaims{
				Issuer:    "nori-service",
				Audience:  jwt.ClaimStrings{testAud},
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			}),
			want: false,
		},
		{
			name: "wrong_signing_algorithm_hs384",
			token: signCustomToken(t, jwt.SigningMethodHS384, []byte(testSecret), jwt.RegisteredClaims{
				Issuer:    "nori-service",
				Audience:  jwt.ClaimStrings{testAud},
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			}),
			want: false,
		},
		{
			name: "alg_none_rejected",
			token: signCustomToken(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, jwt.RegisteredClaims{
				Issuer:    "nori-service",
				Audience:  jwt.ClaimStrings{testAud},
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			}),
			want: false,
		},
		{
			name: "lifetime_exceeds_one_minute",
			token: signCustomToken(t, jwt.SigningMethodHS256, []byte(testSecret), jwt.RegisteredClaims{
				Issuer:    "nori-service",
				Audience:  jwt.ClaimStrings{testAud},
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(2 * time.Minute)),
			}),
			want: false,
		},
		{
			name: "missing_issued_at",
			token: signCustomToken(t, jwt.SigningMethodHS256, []byte(testSecret), jwt.RegisteredClaims{
				Issuer:    "nori-service",
				Audience:  jwt.ClaimStrings{testAud},
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			}),
			want: false,
		},
		{
			name: "missing_expires_at",
			token: signCustomToken(t, jwt.SigningMethodHS256, []byte(testSecret), jwt.RegisteredClaims{
				Issuer:   "nori-service",
				Audience: jwt.ClaimStrings{testAud},
				IssuedAt: jwt.NewNumericDate(now),
			}),
			want: false,
		},
		{
			name:  "oversized_token_rejected",
			token: validTok + "." + strings.Repeat("x", 4096),
			want:  false,
		},
		{
			name:  "empty_token",
			token: "",
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.Valid(tc.token); got != tc.want {
				t.Errorf("Valid(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestProtectMiddleware(t *testing.T) {
	cfg := serviceauth.Config{Key: testSecret, Audience: testAud}
	validTok, err := cfg.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	expiredTok := signCustomToken(t, jwt.SigningMethodHS256, []byte(testSecret), jwt.RegisteredClaims{
		Issuer:    "nori-service",
		Audience:  jwt.ClaimStrings{testAud},
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-3 * time.Minute)),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Minute)),
	})

	protected := cfg.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok:" + r.URL.Path))
	}))

	cases := []struct {
		name       string
		path       string
		authHeader string
		wantStatus int
	}{
		{name: "healthz_unauthenticated_exempt", path: "/healthz", authHeader: "", wantStatus: http.StatusOK},
		{name: "api_healthz_unauthenticated_exempt", path: "/api/healthz", authHeader: "", wantStatus: http.StatusOK},
		{name: "readyz_unauthenticated_exempt", path: "/readyz", authHeader: "", wantStatus: http.StatusOK},
		{name: "api_readyz_unauthenticated_exempt", path: "/api/readyz", authHeader: "", wantStatus: http.StatusOK},
		{name: "healthz_ignores_bad_auth_header", path: "/healthz", authHeader: "Bearer invalid", wantStatus: http.StatusOK},
		{name: "metrics_requires_auth_missing_header", path: "/metrics", authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "metrics_requires_auth_expired_token", path: "/metrics", authHeader: "Bearer " + expiredTok, wantStatus: http.StatusUnauthorized},
		{name: "metrics_allows_valid_bearer_token", path: "/metrics", authHeader: "Bearer " + validTok, wantStatus: http.StatusOK},
		{name: "api_missing_header", path: "/v1/tasks", authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "api_wrong_scheme_basic", path: "/v1/tasks", authHeader: "Basic " + validTok, wantStatus: http.StatusUnauthorized},
		{name: "api_lowercase_bearer_rejected", path: "/v1/tasks", authHeader: "bearer " + validTok, wantStatus: http.StatusUnauthorized},
		{name: "api_malformed_header_no_space", path: "/v1/tasks", authHeader: validTok, wantStatus: http.StatusUnauthorized},
		{name: "api_valid_bearer_token", path: "/v1/tasks", authHeader: "Bearer " + validTok, wantStatus: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			protected.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("GET %s (auth=%q) status = %d, want %d", tc.path, tc.authHeader, rec.Code, tc.wantStatus)
			}
		})
	}
}
