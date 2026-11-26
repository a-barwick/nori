package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nori/internal/api"
	"nori/internal/state/docstore"
)

func TestAssistantAccountDeleteEndpoint(t *testing.T) {
	t.Run("success returns 204", func(t *testing.T) {
		var gotConfirm string
		var gotResume bool
		handler := api.Handler(api.Deps{
			Assistant: &api.Assistant{
				DeleteAccount: func(_ context.Context, confirmation string, resumePending bool) error {
					gotConfirm = confirmation
					gotResume = resumePending
					return nil
				},
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/settings/account/delete", strings.NewReader(`{"confirmation":"alice@example.com","resume_pending":true}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 (%s)", rec.Code, rec.Body.String())
		}
		if gotConfirm != "alice@example.com" || !gotResume {
			t.Fatalf("got (%q, %v), want (alice@example.com, true)", gotConfirm, gotResume)
		}
	})

	cases := []struct {
		name       string
		body       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "empty confirmation returns 400",
			body:       `{"confirmation":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "validation_failed",
		},
		{
			name:       "email mismatch returns 400",
			body:       `{"confirmation":"wrong@example.com"}`,
			err:        api.ErrAccountDeleteEmailMismatch,
			wantStatus: http.StatusBadRequest,
			wantCode:   "validation_failed",
		},
		{
			name:       "legacy owner forbidden returns 403",
			body:       `{"confirmation":"owner@example.com"}`,
			err:        api.ErrAccountDeleteOwnerForbidden,
			wantStatus: http.StatusForbidden,
			wantCode:   "account_delete_forbidden",
		},
		{
			name:       "active worker lease returns 409 busy",
			body:       `{"confirmation":"alice@example.com"}`,
			err:        docstore.ErrUserDataBusy,
			wantStatus: http.StatusConflict,
			wantCode:   "account_delete_busy",
		},
		{
			name:       "resume when not pending returns 409",
			body:       `{"confirmation":"alice@example.com","resume_pending":true}`,
			err:        api.ErrAccountDeleteNotPending,
			wantStatus: http.StatusConflict,
			wantCode:   "account_delete_not_pending",
		},
		{
			name:       "partial failure returns 502 account_delete_pending",
			body:       `{"confirmation":"alice@example.com"}`,
			err:        api.ErrAccountDeletePending,
			wantStatus: http.StatusBadGateway,
			wantCode:   "account_delete_pending",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := api.Handler(api.Deps{
				Assistant: &api.Assistant{
					DeleteAccount: func(context.Context, string, bool) error { return tc.err },
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/settings/account/delete", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var resp struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", resp.Code, tc.wantCode)
			}
		})
	}
}
