package sim_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"nori/internal/api"
	"nori/internal/serviceauth"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestIdentityBindingSecuritySimulation(t *testing.T) {
	ctx := context.Background()
	directory := records.NewUsers(memstore.New())
	t.Cleanup(func() { _ = directory.Close() })
	for _, row := range []users.User{
		{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@gmail.com", TimeZone: "UTC"},
		{ID: "22222222-2222-4222-8222-222222222222", Email: "bob@work.example", TimeZone: "UTC"},
	} {
		if err := directory.Create(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "test-key", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(jwks.Close)
	auth := serviceauth.Config{
		Key: "0123456789abcdef0123456789abcdef", Audience: "nori-sim",
		GoogleClientIDs: []string{"nori-client"}, GoogleJWKSURL: jwks.URL,
	}
	serviceToken, err := auth.Token()
	if err != nil {
		t.Fatal(err)
	}
	open := func(_ context.Context, row users.User) (api.UserRoutes, func(), error) {
		return api.UserRoutes{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, row.ID)
		})}, func() {}, nil
	}
	handler := auth.Protect(api.UserHandlerWithOptions(directory, open, false, "test", api.UserHandlerOptions{
		DisableRateLimit: true, ProvisioningMode: settings.UserProvisioningInvite,
	}))
	request := func(method, path, token, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	identityHeaders := func(subject string, authoritative bool) map[string]string {
		return map[string]string{
			"X-Nori-Identity-Issuer": "https://accounts.google.com", "X-Nori-Identity-Subject": subject,
			"X-Nori-Identity-Email-Authoritative": fmt.Sprint(authoritative),
		}
	}
	signGoogle := func(subject, email, hostedDomain string) string {
		t.Helper()
		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://accounts.google.com", "aud": "nori-client", "sub": subject,
			"email": email, "email_verified": true, "hd": hostedDomain,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		})
		token.Header["kid"] = "test-key"
		raw, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	assertStatus := func(res *httptest.ResponseRecorder, status int) {
		t.Helper()
		if res.Code != status {
			t.Fatalf("status %d, want %d: %s", res.Code, status, res.Body.String())
		}
	}

	t.Run("private tester signs in on iOS and web with one trial", func(t *testing.T) {
		roster := filepath.Join(t.TempDir(), "emails.json")
		if err := os.WriteFile(roster, []byte(`["tester@gmail.com","tester@workspace.example"]`), 0600); err != nil {
			t.Fatal(err)
		}
		betaAuth := auth
		betaAuth.PrivateBeta = serviceauth.NewPrivateBeta(roster)
		betaAuth.AllowedEmails = []string{"alice@gmail.com"}
		original := handler
		defer func() { handler = original }()
		handler = betaAuth.Protect(api.UserHandlerWithOptions(directory, open, false, "test", api.UserHandlerOptions{
			DisableRateLimit: true, ProvisioningMode: settings.UserProvisioningManual, PrivateBeta: betaAuth.PrivateBeta,
		}))
		ios := signGoogle("tester-id", "tester@gmail.com", "")
		res := request("POST", "/api/users/resolve", ios, "{}", nil)
		assertStatus(res, http.StatusOK)
		var row struct{ ID string }
		if err := json.Unmarshal(res.Body.Bytes(), &row); err != nil || !users.ValidID(row.ID) {
			t.Fatal("invalid account", err)
		}
		web := request("POST", "/api/users/resolve", serviceToken, `{"email":"tester@gmail.com"}`, identityHeaders("tester-id", true))
		assertStatus(web, http.StatusOK)
		if res.Body.String() != web.Body.String() {
			t.Fatal("web resolved a different user")
		}
		assertStatus(request("GET", "/users/"+row.ID+"/v1/test", ios, "", nil), http.StatusOK)
		assertStatus(request("POST", "/api/users/resolve", signGoogle("stranger", "stranger@gmail.com", ""), "{}", nil), http.StatusUnauthorized)
		assertStatus(request("POST", "/api/users/resolve", signGoogle("unverified", "tester@workspace.example", ""), "{}", nil), http.StatusUnauthorized)
		assertStatus(request("POST", "/api/users/resolve", serviceToken, `{"email":"tester@workspace.example"}`, identityHeaders("unverified", false)), http.StatusForbidden)
		assertStatus(request("POST", "/api/users/resolve", signGoogle("workspace", "tester@workspace.example", "workspace.example"), "{}", nil), http.StatusOK)
		assertStatus(request("POST", "/api/users/resolve", signGoogle("another-subject", "tester@gmail.com", ""), "{}", nil), http.StatusForbidden)
	})

	res := request("POST", "/api/users/resolve", serviceToken, `{"email":"alice@gmail.com"}`, nil)
	assertStatus(res, http.StatusForbidden)
	res = request("POST", "/api/users/resolve", serviceToken, `{"email":"alice@gmail.com"}`, identityHeaders("alice-subject", true))
	assertStatus(res, http.StatusOK)
	if res.Header().Get("X-Nori-Identity-Bound") != "1" || !strings.Contains(res.Body.String(), "11111111-1111-4111-8111-111111111111") {
		t.Fatalf("legacy Gmail account was not bound: %s", res.Body.String())
	}
	res = request("POST", "/api/users/resolve", serviceToken, `{"email":"alice@gmail.com"}`, identityHeaders("different-subject", true))
	assertStatus(res, http.StatusForbidden)
	res = request("POST", "/api/users/resolve", serviceToken, `{"email":"alice-new@example.com"}`, identityHeaders("alice-subject", false))
	assertStatus(res, http.StatusOK)
	if !strings.Contains(res.Body.String(), "11111111-1111-4111-8111-111111111111") {
		t.Fatalf("bound identity did not retain account after email change: %s", res.Body.String())
	}

	res = request("POST", "/api/users/resolve", serviceToken, `{"email":"bob@work.example"}`, identityHeaders("bob-subject", true))
	assertStatus(res, http.StatusForbidden)
	if _, err := directory.BindIdentity(ctx, "22222222-2222-4222-8222-222222222222", users.Identity{Issuer: "https://accounts.google.com", Subject: "bob-subject"}); err != nil {
		t.Fatal(err)
	}
	res = request("GET", "/users/22222222-2222-4222-8222-222222222222/v1/test", signGoogle("bob-subject", "bob-new@work.example", "work.example"), "", nil)
	assertStatus(res, http.StatusOK)
	res = request("GET", "/users/22222222-2222-4222-8222-222222222222/v1/test", signGoogle("other-subject", "bob@work.example", "work.example"), "", identityHeaders("bob-subject", true))
	assertStatus(res, http.StatusForbidden)

	invite, err := directory.CreateInvite(ctx, users.CreateInviteOptions{Domain: "work.example"})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"email":"new@work.example","invite_code":%q}`, invite.Code)
	res = request("POST", "/api/users/redeem", serviceToken, body, identityHeaders("new-subject", false))
	assertStatus(res, http.StatusForbidden)
	res = request("POST", "/api/users/redeem", serviceToken, body, identityHeaders("new-subject", true))
	assertStatus(res, http.StatusOK)
	res = request("POST", "/api/users/redeem", serviceToken, body, identityHeaders("other-subject", true))
	assertStatus(res, http.StatusForbidden)
}
