package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"nori/internal/push"
)

func TestPushDeviceRegisterUnregisterAndReassign(t *testing.T) {
	ctx := context.Background()
	store := push.NewMemoryStore()
	fixedNow := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return fixedNow }

	muxA := http.NewServeMux()
	(&PushDevicesAPI{Store: store, UserID: "user-alice"}).Register(muxA)

	muxB := http.NewServeMux()
	(&PushDevicesAPI{Store: store, UserID: "user-bob"}).Register(muxB)

	// 1. Alice registers a sandbox device token.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/devices/push", strings.NewReader(`{
		"token": "apns-token-shared-device",
		"device_id": "iphone-16-pro",
		"environment": "sandbox"
	}`))
	req.Header.Set("Content-Type", "application/json")
	muxA.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("alice register status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var aliceDev pushDeviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &aliceDev); err != nil {
		t.Fatalf("unmarshal alice device: %v", err)
	}
	if aliceDev.ID == "" || aliceDev.Environment != push.EnvSandbox || aliceDev.DeviceID != "iphone-16-pro" {
		t.Fatalf("unexpected alice device: %+v", aliceDev)
	}
	if strings.Contains(rec.Body.String(), "apns-token-shared-device") {
		t.Fatalf("response must not echo raw APNs token: %s", rec.Body.String())
	}

	// 2. Bob signs in on the same physical device (same APNs token) in production.
	fixedNow = fixedNow.Add(5 * time.Minute)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/devices/push", strings.NewReader(`{
		"token": "apns-token-shared-device",
		"device_id": "iphone-16-pro",
		"environment": "production"
	}`))
	req.Header.Set("Content-Type", "application/json")
	muxB.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob register status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var bobDev pushDeviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &bobDev); err != nil {
		t.Fatalf("unmarshal bob device: %v", err)
	}
	if bobDev.ID == aliceDev.ID {
		t.Fatalf("expected reassigned token to receive a fresh device ID, got same ID %q", bobDev.ID)
	}
	if bobDev.Environment != push.EnvProduction {
		t.Fatalf("bob environment = %q, want production", bobDev.Environment)
	}

	// Alice must no longer own any devices; Bob owns the reassigned token.
	aliceList, err := store.ListDevices(ctx, "user-alice")
	if err != nil || len(aliceList) != 0 {
		t.Fatalf("alice devices = %+v, err=%v; want 0 after reassignment", aliceList, err)
	}
	bobList, err := store.ListDevices(ctx, "user-bob")
	if err != nil || len(bobList) != 1 || bobList[0].ID != bobDev.ID {
		t.Fatalf("bob devices = %+v, err=%v; want 1", bobList, err)
	}

	// 3. Alice cannot delete either her old ID or Bob's new ID.
	for _, targetID := range []string{aliceDev.ID, bobDev.ID} {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodDelete, "/v1/devices/push/"+targetID, nil)
		muxA.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("alice delete %q status = %d, want 404", targetID, rec.Code)
		}
	}

	// 4. Bob signs out and deletes his device token.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/v1/devices/push/"+bobDev.ID, nil)
	muxB.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bob delete status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
	bobList, err = store.ListDevices(ctx, "user-bob")
	if err != nil || len(bobList) != 0 {
		t.Fatalf("bob devices after delete = %+v, err=%v; want 0", bobList, err)
	}

	// 5. Deleting again returns 404.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/v1/devices/push/"+bobDev.ID, nil)
	muxB.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob second delete status = %d, want 404", rec.Code)
	}
}

func TestPushDeviceValidationErrors(t *testing.T) {
	store := push.NewMemoryStore()
	mux := http.NewServeMux()
	(&PushDevicesAPI{Store: store, UserID: "user-1"}).Register(mux)

	cases := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
	}{
		{
			name:        "missing content-type",
			contentType: "",
			body:        `{"token":"abc","environment":"sandbox"}`,
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "empty token",
			contentType: "application/json",
			body:        `{"token":"   ","environment":"sandbox"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "invalid environment",
			contentType: "application/json",
			body:        `{"token":"abc123","environment":"staging"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "missing environment",
			contentType: "application/json",
			body:        `{"token":"abc123"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "malformed json",
			contentType: "application/json",
			body:        `{"token":`,
			wantStatus:  http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/devices/push", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestPushDeviceOpenAPIContract(t *testing.T) {
	ctx := context.Background()
	doc := loadOpenAPIDoc(t)
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}

	store := push.NewMemoryStore()
	mux := http.NewServeMux()
	(&PushDevicesAPI{Store: store, UserID: "openapi-push-user"}).Register(mux)

	check := func(method, target string, body any, wantStatus int, validateReq bool) []byte {
		t.Helper()
		var reqBody []byte
		if body != nil {
			reqBody, err = json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal %s %s: %v", method, target, err)
			}
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(reqBody))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		route, pathParams, err := router.FindRoute(req)
		if err != nil {
			t.Fatalf("find OpenAPI route %s %s: %v", method, target, err)
		}
		reqVal := &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
			Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		}
		if validateReq {
			if err := openapi3filter.ValidateRequest(ctx, reqVal); err != nil {
				t.Fatalf("OpenAPI request validation failed %s %s: %v", method, target, err)
			}
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, target, rec.Code, wantStatus, rec.Body.String())
		}
		respVal := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: reqVal,
			Status:                 rec.Code,
			Header:                 rec.Header(),
			Body:                   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
			Options:                &openapi3filter.Options{IncludeResponseStatus: true},
		}
		if err := openapi3filter.ValidateResponse(ctx, respVal); err != nil {
			t.Fatalf("OpenAPI response validation failed %s %s (%d): %v\nbody=%s", method, target, rec.Code, err, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	// 200 register
	raw := check(http.MethodPost, "/v1/devices/push", map[string]any{
		"token":       "hex-apns-token-001",
		"device_id":   "ios-device-1",
		"environment": "sandbox",
	}, http.StatusOK, true)
	var dev pushDeviceResponse
	if err := json.Unmarshal(raw, &dev); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// 400 validation error matches ErrorResponse schema
	check(http.MethodPost, "/v1/devices/push", map[string]any{
		"token":       "",
		"environment": "invalid",
	}, http.StatusBadRequest, false)

	// 204 unregister
	check(http.MethodDelete, "/v1/devices/push/"+dev.ID, nil, http.StatusNoContent, true)

	// 404 unregister missing ID matches ErrorResponse schema
	check(http.MethodDelete, "/v1/devices/push/"+dev.ID, nil, http.StatusNotFound, true)
}
