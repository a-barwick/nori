package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"nori/internal/httpjson"
	"nori/internal/observability"
	"nori/internal/serviceauth"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

const defaultReadyCheckTimeout = 2 * time.Second

type UserRoutes struct {
	Handler http.Handler
	Resume  func(context.Context) error
}

type UserHandlerOptions struct {
	ReadyCheck       func(context.Context) error
	ReadyTimeout     time.Duration
	Metrics          http.Handler
	UserActivity     func(context.Context, users.User) (context.Context, users.User, func(), error)
	ResetData        func(context.Context, users.User, bool) error
	RateLimiter      *RateLimiter
	DisableRateLimit bool
	ProvisioningMode settings.UserProvisioningMode
}

const (
	dataResetPendingCode    = "data_reset_pending"
	dataResetNotPendingCode = "data_reset_not_pending"
	inviteRequiredCode      = "invite_required"
)

type userDataResetPendingKey struct{}

func userDataResetPending(ctx context.Context) bool {
	pending, _ := ctx.Value(userDataResetPendingKey{}).(bool)
	return pending
}

func UserHandler(directory users.Directory, open func(context.Context, users.User) (UserRoutes, func(), error), local bool, model string) http.Handler {
	return UserHandlerWithOptions(directory, open, local, model, UserHandlerOptions{DisableRateLimit: true})
}

func UserHandlerWithOptions(directory users.Directory, open func(context.Context, users.User) (UserRoutes, func(), error), local bool, model string, opts UserHandlerOptions) http.Handler {
	limiter := opts.RateLimiter
	if limiter == nil && !opts.DisableRateLimit {
		limiter = NewRateLimiterFromEnv()
	}
	provisioningMode := opts.ProvisioningMode
	if provisioningMode == "" {
		provisioningMode = settings.UserProvisioningModeFromEnv()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local && (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "") {
			writeJSON(w, 403, map[string]string{"error": "Use the web server to access the runtime."})
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok", "model": model})
			return
		}
		if r.URL.Path == "/readyz" || r.URL.Path == "/api/readyz" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
				return
			}
			serveReadyz(w, r, opts.ReadyCheck, opts.ReadyTimeout)
			return
		}
		verifiedEmail, isUserToken := serviceauth.VerifiedEmail(r.Context())
		if r.URL.Path == "/metrics" {
			if isUserToken {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden."})
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
				return
			}
			if opts.Metrics == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
				return
			}
			opts.Metrics.ServeHTTP(w, r)
			return
		}
		if r.Method == "POST" && (r.URL.Path == "/api/users/resolve" || r.URL.Path == "/api/users/redeem") {
			if limiter != nil {
				if ok, retryAfter := limiter.Allow(RateLimitResolve, "ip:"+clientIP(r)); !ok {
					writeRateLimited(w, r, RateLimitResolve, retryAfter)
					return
				}
			}
			var body struct {
				Email      string `json:"email"`
				Name       string `json:"name"`
				InviteCode string `json:"invite_code"`
				Code       string `json:"code"`
			}
			if err := httpjson.Decode(w, r, &body, 64<<10); err != nil {
				if errors.Is(err, httpjson.ErrTooLarge) {
					writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Request body is too large."})
					return
				}
				message := "Send a valid JSON object."
				if errors.Is(err, httpjson.ErrMultiple) {
					message = "Send one JSON object."
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": message})
				return
			}
			lookupEmail := body.Email
			if isUserToken {
				if trimmed := strings.ToLower(strings.TrimSpace(body.Email)); trimmed != "" && trimmed != verifiedEmail {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "Sign-in email mismatch."})
					return
				}
				lookupEmail = verifiedEmail
			}
			if limiter != nil {
				if normalizedEmail := strings.ToLower(strings.TrimSpace(lookupEmail)); normalizedEmail != "" {
					if ok, retryAfter := limiter.Allow(RateLimitResolve, "email:"+hashRateLimitKey(normalizedEmail)); !ok {
						writeRateLimited(w, r, RateLimitResolve, retryAfter)
						return
					}
				}
			}
			inviteCode := strings.TrimSpace(body.InviteCode)
			if inviteCode == "" {
				inviteCode = strings.TrimSpace(body.Code)
			}
			if r.URL.Path == "/api/users/redeem" {
				serveUserRedeem(w, r, directory, provisioningMode, lookupEmail, body.Name, inviteCode)
				return
			}
			serveUserResolve(w, r, directory, provisioningMode, lookupEmail, body.Name, inviteCode)
			return
		}
		id := ""
		if strings.HasPrefix(r.URL.Path, "/users/") {
			var path string
			id, path, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/users/"), "/")
			if id == "" {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
				return
			}
			copyURL := *r.URL
			copyURL.Path = "/" + path
			copyURL.RawPath = ""
			r = r.Clone(r.Context())
			r.URL = &copyURL
		}
		if id == "" && r.Method == "POST" && r.URL.Path == "/api/resume" {
			if isUserToken {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden."})
				return
			}
			rows, err := directory.List(r.Context())
			if err != nil {
				writeError(w, err)
				return
			}
			failed := false
			for _, row := range rows {
				ctx, row, done, err := startUserActivity(r.Context(), row, opts)
				if err != nil {
					failed = true
					continue
				}
				if row.ResetPending {
					done()
					continue
				}
				routes, release, err := open(ctx, row)
				if err != nil {
					done()
					failed = true
					continue
				}
				err = routes.Resume(ctx)
				release()
				done()
				if err != nil {
					failed = true
				}
			}
			if failed {
				writeJSON(w, 503, map[string]string{"error": "Some users need a retry."})
				return
			}
			writeJSON(w, 200, map[string]string{"status": "ok"})
			return
		}
		if id == "" {
			writeJSON(w, 403, map[string]string{"error": "A known user is required."})
			return
		}
		row, err := directory.Get(r.Context(), id)
		if err != nil {
			writeUserLookupError(w, r, err, "A known user is required.")
			return
		}
		if isUserToken && !strings.EqualFold(row.Email, verifiedEmail) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Access denied."})
			return
		}
		ctx := observability.BindHTTPUser(r.Context(), row.ID)
		if limiter != nil {
			cat, isSSE := classifyUserRequest(r.Method, r.URL.Path)
			if ok, retryAfter := limiter.Allow(cat, row.ID); !ok {
				writeRateLimited(w, r.WithContext(ctx), cat, retryAfter)
				return
			}
			if isSSE {
				releaseSSE, ok := limiter.AcquireSSE(row.ID)
				if !ok {
					writeSSELimitExceeded(w, r.WithContext(ctx))
					return
				}
				defer releaseSSE()
			}
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/settings/reset" && opts.ResetData != nil {
			jsonBody(func(w http.ResponseWriter, r *http.Request) {
				resetUserData(w, r, row, opts.ResetData)
			})(w, r.WithContext(ctx))
			return
		}
		requestCtx := ctx
		ctx, row, done, err := startUserActivity(ctx, row, opts)
		if err != nil {
			writeError(w, err)
			return
		}
		defer func() { done() }()
		if row.ResetPending {
			path := r.URL.Path
			if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
				path == "/v1/connections/oauth/callback" ||
				(strings.HasPrefix(path, "/v1/connections/") && strings.HasSuffix(path, "/resources")) {
				writeError(w, docstore.ErrUserDataResetPending)
				return
			}
			ctx = context.WithValue(ctx, userDataResetPendingKey{}, true)
		}
		routes, release, err := open(ctx, row)
		if err != nil {
			writeError(w, err)
			return
		}
		defer release()
		if r.URL.Path == "/v1/events" || r.URL.Path == "/v1/conversations/assistant/events" {
			done()
			done = func() {}
			ctx = requestCtx
		}
		routes.Handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

func serveUserResolve(w http.ResponseWriter, r *http.Request, directory users.Directory, mode settings.UserProvisioningMode, email, name, inviteCode string) {
	row, err := directory.FindEmail(r.Context(), email)
	if err == nil {
		observability.BindHTTPUser(r.Context(), row.ID)
		writeJSON(w, http.StatusOK, map[string]string{"id": row.ID})
		return
	}
	if !errors.Is(err, state.ErrNotFound) {
		writeUserLookupError(w, r, err, "No user for this sign-in.")
		return
	}
	switch mode {
	case settings.UserProvisioningOpen:
		provisionOpenUser(w, r, directory, email, name)
	case settings.UserProvisioningInvite:
		if inviteCode == "" {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "An invite code is required to access Nori.",
				"code":  inviteRequiredCode,
			})
			return
		}
		redeemInviteForUser(w, r, directory, inviteCode, email, name)
	default:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "No user for this sign-in."})
	}
}

func serveUserRedeem(w http.ResponseWriter, r *http.Request, directory users.Directory, mode settings.UserProvisioningMode, email, name, inviteCode string) {
	switch mode {
	case settings.UserProvisioningOpen:
		provisionOpenUser(w, r, directory, email, name)
	case settings.UserProvisioningInvite:
		if inviteCode == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Invite code is required.",
				"code":  "invite_code_required",
			})
			return
		}
		redeemInviteForUser(w, r, directory, inviteCode, email, name)
	default:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Invite sign-up is not enabled."})
	}
}

func provisionOpenUser(w http.ResponseWriter, r *http.Request, directory users.Directory, email, name string) {
	provisioner, ok := directory.(users.AutoProvisioner)
	if !ok {
		slog.WarnContext(r.Context(), "auto-provisioning unavailable on user directory")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Nori could not create your account. Try again shortly."})
		return
	}
	row, _, err := provisioner.FindOrCreateByEmail(r.Context(), email, name)
	if err != nil {
		if strings.Contains(err.Error(), "user email must be") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A valid sign-in email is required."})
			return
		}
		slog.WarnContext(r.Context(), "open user provisioning failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Nori could not create your account. Try again shortly."})
		return
	}
	observability.BindHTTPUser(r.Context(), row.ID)
	writeJSON(w, http.StatusOK, map[string]string{"id": row.ID})
}

func redeemInviteForUser(w http.ResponseWriter, r *http.Request, directory users.Directory, inviteCode, email, name string) {
	store, ok := directory.(users.InviteStore)
	if !ok {
		slog.WarnContext(r.Context(), "invite store unavailable on user directory")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Nori could not verify your invite code. Try again shortly."})
		return
	}
	row, _, err := store.RedeemInviteAndCreateUser(r.Context(), inviteCode, email, name)
	if err != nil {
		switch {
		case errors.Is(err, users.ErrInviteNotFound):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Invalid invite code.", "code": "invalid_invite"})
		case errors.Is(err, users.ErrInviteExpired):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "This invite code has expired.", "code": "invite_expired"})
		case errors.Is(err, users.ErrInviteExhausted):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "This invite code has already been used.", "code": "invite_exhausted"})
		case errors.Is(err, users.ErrInviteRevoked):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "This invite code has been revoked.", "code": "invite_revoked"})
		case errors.Is(err, users.ErrInviteEmailMismatch):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "This invite code cannot be used with this email address.", "code": "invite_email_mismatch"})
		case strings.Contains(err.Error(), "user email must be"):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A valid sign-in email is required."})
		default:
			slog.WarnContext(r.Context(), "invite redemption failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Nori could not verify your invite code. Try again shortly."})
		}
		return
	}
	observability.BindHTTPUser(r.Context(), row.ID)
	writeJSON(w, http.StatusOK, map[string]string{"id": row.ID})
}

func writeUserLookupError(w http.ResponseWriter, r *http.Request, err error, missing string) {
	if errors.Is(err, state.ErrNotFound) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": missing})
		return
	}
	slog.WarnContext(r.Context(), "account lookup unavailable", "err", err)
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Nori could not check your account. Try again shortly."})
}

func resetUserData(w http.ResponseWriter, r *http.Request, user users.User, reset func(context.Context, users.User, bool) error) {
	var body struct {
		Confirmation  string `json:"confirmation"`
		ResumePending bool   `json:"resume_pending,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Confirmation != "RESET" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Type RESET to confirm."})
		return
	}
	if err := reset(r.Context(), user, body.ResumePending); err != nil {
		if errors.Is(err, docstore.ErrUserDataBusy) || errors.Is(err, docstore.ErrUserDataResetNotPending) {
			writeError(w, err)
			return
		}
		slog.ErrorContext(r.Context(), "user data reset failed", "err", err)
		body := map[string]string{"error": "Nori could not finish your data reset. Try again."}
		if errors.Is(err, docstore.ErrUserDataResetPending) {
			body["code"] = dataResetPendingCode
			body["error"] = docstore.ErrUserDataResetPending.Error()
		}
		writeJSON(w, http.StatusBadGateway, body)
		return
	}
	slog.InfoContext(r.Context(), "user data reset completed")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func startUserActivity(ctx context.Context, row users.User, opts UserHandlerOptions) (context.Context, users.User, func(), error) {
	if opts.UserActivity == nil {
		return ctx, row, func() {}, nil
	}
	return opts.UserActivity(ctx, row)
}

func OpsHandler(local bool, model string, opts UserHandlerOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local && (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "") {
			writeJSON(w, 403, map[string]string{"error": "Use the web server to access the runtime."})
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok", "model": model})
			return
		}
		if r.URL.Path == "/readyz" || r.URL.Path == "/api/readyz" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
				return
			}
			serveReadyz(w, r, opts.ReadyCheck, opts.ReadyTimeout)
			return
		}
		if r.URL.Path == "/metrics" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
				return
			}
			if opts.Metrics == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
				return
			}
			opts.Metrics.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
	})
}

func serveReadyz(w http.ResponseWriter, r *http.Request, readyCheck func(context.Context) error, timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultReadyCheckTimeout
	}
	if readyCheck != nil {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := readyCheck(ctx); err != nil {
			if r.Method == http.MethodHead {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unavailable",
				"error":  "database unavailable",
			})
			return
		}
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
