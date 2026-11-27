package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func generateTestP8Key(t *testing.T) (string, *ecdsa.PublicKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
	return string(pemBytes), &priv.PublicKey
}

func TestAPNSClientJWTSigningCachingAndRefresh(t *testing.T) {
	keyPEM, pubKey := generateTestP8Key(t)

	var mu sync.Mutex
	var authHeaders []string
	var lastHeaders http.Header
	var lastPath string
	var lastPayload map[string]any

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authHeaders = append(authHeaders, r.Header.Get("authorization"))
		lastHeaders = r.Header.Clone()
		lastPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&lastPayload)
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	client, err := NewAPNSClient(APNSConfig{
		KeyID:         "KEY1234567",
		TeamID:        "TEAM987654",
		BundleID:      "com.nori.ios",
		PrivateKeyPEM: keyPEM,
		SandboxURL:    srv.URL,
		ProductionURL: srv.URL,
		HTTPClient:    srv.Client(),
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewAPNSClient: %v", err)
	}

	dev := Device{ID: "d1", UserID: "u1", Token: "tok-sandbox-1", Environment: EnvSandbox}
	notif := Notification{
		DeliveryID:    "deliv-1",
		InteractionID: "int-42",
		Title:         "Sensitive Subject Line From CEO",
		Body:          "Secret bank transfer details: $50,000",
	}

	// First send generates JWT.
	if err := client.Send(context.Background(), dev, notif); err != nil {
		t.Fatalf("first Send: %v", err)
	}

	// Second send 30 minutes later reuses the cached JWT (< 45 min).
	now = now.Add(30 * time.Minute)
	if err := client.Send(context.Background(), dev, notif); err != nil {
		t.Fatalf("second Send: %v", err)
	}

	// Third send 46 minutes after issue refreshes the JWT (< 50 min ceiling).
	now = now.Add(16 * time.Minute)
	if err := client.Send(context.Background(), dev, notif); err != nil {
		t.Fatalf("third Send: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(authHeaders) != 3 {
		t.Fatalf("got %d requests, want 3", len(authHeaders))
	}
	if authHeaders[0] != authHeaders[1] {
		t.Fatalf("expected JWT to be cached within 30m window")
	}
	if authHeaders[1] == authHeaders[2] {
		t.Fatalf("expected JWT to refresh after 46m")
	}

	// Verify ES256 signature, kid, iss, and iat on the refreshed JWT.
	rawJWT := strings.TrimPrefix(authHeaders[2], "bearer ")
	parsed, err := jwt.Parse(rawJWT, func(tok *jwt.Token) (any, error) {
		if tok.Method.Alg() != jwt.SigningMethodES256.Alg() {
			t.Fatalf("alg = %q, want ES256", tok.Method.Alg())
		}
		if tok.Header["kid"] != "KEY1234567" {
			t.Fatalf("kid = %v, want KEY1234567", tok.Header["kid"])
		}
		return pubKey, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("verify ES256 JWT failed: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["iss"] != "TEAM987654" {
		t.Fatalf("iss = %v, want TEAM987654", claims["iss"])
	}

	// Verify APNs headers and path.
	if lastPath != "/3/device/tok-sandbox-1" {
		t.Fatalf("path = %q, want /3/device/tok-sandbox-1", lastPath)
	}
	if got := lastHeaders.Get("apns-topic"); got != "com.nori.ios" {
		t.Fatalf("apns-topic = %q, want com.nori.ios", got)
	}
	if got := lastHeaders.Get("apns-collapse-id"); got != "deliv-1" {
		t.Fatalf("apns-collapse-id = %q, want deliv-1", got)
	}
	if got := lastHeaders.Get("apns-push-type"); got != "alert" {
		t.Fatalf("apns-push-type = %q, want alert", got)
	}

	// Verify lock-screen privacy: sensitive title/body sanitized, interaction_id preserved, no category/action.
	aps := lastPayload["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != DefaultTitle || alert["body"] != DefaultBody {
		t.Fatalf("alert not sanitized: %+v", alert)
	}
	if _, hasCategory := aps["category"]; hasCategory {
		t.Fatalf("lock-screen payload must not include category/Approve action: %+v", aps)
	}
	if lastPayload["interaction_id"] != "int-42" {
		t.Fatalf("interaction_id = %v, want int-42", lastPayload["interaction_id"])
	}
}

func TestAPNSClientEnvironmentRoutingAndInvalidTokenDeletion(t *testing.T) {
	keyPEM, _ := generateTestP8Key(t)

	var sandboxHits, prodHits int
	sandboxSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxHits++
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"reason":"Unregistered","timestamp":1700000000}`))
	}))
	defer sandboxSrv.Close()

	prodSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prodHits++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"reason":"BadDeviceToken"}`))
	}))
	defer prodSrv.Close()

	// Route both TLS test servers through a custom transport.
	tr := sandboxSrv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.InsecureSkipVerify = true
	httpClient := &http.Client{Transport: tr}

	store := NewMemoryStore()
	ctx := context.Background()
	devSandbox, err := store.RegisterDevice(ctx, "user-1", "tok-unregistered", "dev-1", EnvSandbox)
	if err != nil {
		t.Fatalf("register sandbox: %v", err)
	}
	devProd, err := store.RegisterDevice(ctx, "user-1", "tok-bad", "dev-2", EnvProduction)
	if err != nil {
		t.Fatalf("register prod: %v", err)
	}

	client, err := NewAPNSClient(APNSConfig{
		KeyID:         "KEY1",
		TeamID:        "TEAM1",
		BundleID:      "com.nori.ios",
		PrivateKeyPEM: keyPEM,
		SandboxURL:    sandboxSrv.URL,
		ProductionURL: prodSrv.URL,
		HTTPClient:    httpClient,
		Devices:       store,
	})
	if err != nil {
		t.Fatalf("NewAPNSClient: %v", err)
	}

	// 410 Unregistered -> sandbox hit & automatic deletion.
	err = client.Send(ctx, devSandbox, Notification{DeliveryID: "d-410"})
	var apnsErr *APNSError
	if !errors.As(err, &apnsErr) || apnsErr.StatusCode != http.StatusGone || !IsInvalidTokenError(err) {
		t.Fatalf("expected 410 invalid token error, got %v", err)
	}

	// 400 BadDeviceToken -> production hit & automatic deletion.
	err = client.Send(ctx, devProd, Notification{DeliveryID: "d-400"})
	if !errors.As(err, &apnsErr) || apnsErr.StatusCode != http.StatusBadRequest || !IsInvalidTokenError(err) {
		t.Fatalf("expected 400 BadDeviceToken error, got %v", err)
	}

	if sandboxHits != 1 || prodHits != 1 {
		t.Fatalf("hits sandbox=%d prod=%d, want 1 and 1", sandboxHits, prodHits)
	}

	remaining, err := store.ListDevices(ctx, "user-1")
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining devices = %+v, err=%v; want 0 after 410 & 400 auto-delete", remaining, err)
	}
}
