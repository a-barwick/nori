package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
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
	GoogleOAuthUser  func(string) string
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

func localOriginGuard(local bool, next http.Handler) http.Handler {
	if !local {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Use the web server to access the runtime."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type userRouter struct {
	directory        users.Directory
	open             func(context.Context, users.User) (UserRoutes, func(), error)
	model            string
	opts             UserHandlerOptions
	limiter          *RateLimiter
	provisioningMode settings.UserProvisioningMode
}

func (u *userRouter) registerOpsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", u.handleHealthz)
	mux.HandleFunc("/api/healthz", u.handleHealthz)
	mux.HandleFunc("/readyz", u.handleReadyz)
	mux.HandleFunc("/api/readyz", u.handleReadyz)
	mux.HandleFunc("/metrics", u.handleMetrics)
}

func (u *userRouter) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "model": u.model})
}

func (u *userRouter) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
		return
	}
	serveReadyz(w, r, u.opts.ReadyCheck, u.opts.ReadyTimeout)
}

func (u *userRouter) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if _, isUserToken := serviceauth.VerifiedEmail(r.Context()); isUserToken {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden."})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
		return
	}
	if u.opts.Metrics == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
		return
	}
	u.opts.Metrics.ServeHTTP(w, r)
}

func (u *userRouter) handleForbiddenUser(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "A known user is required."})
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
	router := &userRouter{
		directory:        directory,
		open:             open,
		model:            model,
		opts:             opts,
		limiter:          limiter,
		provisioningMode: provisioningMode,
	}
	mux := http.NewServeMux()
	router.registerOpsRoutes(mux)
	mux.HandleFunc("/api/users/resolve", router.handleUserResolveOrRedeem)
	mux.HandleFunc("/api/users/redeem", router.handleUserResolveOrRedeem)
	mux.HandleFunc("/api/oauth/google-workspace/callback", router.handleGoogleOAuthCallback)
	mux.HandleFunc("/api/resume", router.handleResume)
	mux.HandleFunc("/users/", router.handlePrefixedUser)
	mux.HandleFunc("/", router.handleForbiddenUser)
	return localOriginGuard(local, mux)
}

func (u *userRouter) handleUserResolveOrRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		u.handleForbiddenUser(w, r)
		return
	}
	verifiedEmail, isUserToken := serviceauth.VerifiedEmail(r.Context())
	verifiedIdentity, hasVerifiedIdentity := serviceauth.VerifiedIdentity(r.Context())
	isService := serviceauth.IsService(r.Context())

	var body struct {
		Email      string `json:"email"`
		Name       string `json:"name"`
		InviteCode string `json:"invite_code"`
		Code       string `json:"code"`
	}
	if err := httpjson.Decode(w, r, &body, 64<<10); err != nil {
		if u.limiter != nil {
			if ok, retryAfter := u.limiter.Allow(RateLimitResolve, "ip:"+clientIP(r)); !ok {
				writeRateLimited(w, r, RateLimitResolve, retryAfter)
				return
			}
		}
		status, code, message := httpjson.ErrorStatus(err)
		writeJSON(w, status, map[string]string{"error": message, "code": code})
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
	if u.limiter != nil {
		if normalizedEmail := strings.ToLower(strings.TrimSpace(lookupEmail)); normalizedEmail != "" {
			if ok, retryAfter := u.limiter.Allow(RateLimitResolve, "email:"+hashRateLimitKey(normalizedEmail)); !ok {
				writeRateLimited(w, r, RateLimitResolve, retryAfter)
				return
			}
		} else {
			if ok, retryAfter := u.limiter.Allow(RateLimitResolve, "ip:"+clientIP(r)); !ok {
				writeRateLimited(w, r, RateLimitResolve, retryAfter)
				return
			}
		}
	}
	inviteCode := strings.TrimSpace(body.InviteCode)
	if inviteCode == "" {
		inviteCode = strings.TrimSpace(body.Code)
	}
	if hasVerifiedIdentity || isService {
		identity := verifiedIdentity
		if isService {
			identity = users.Identity{
				Issuer: r.Header.Get("X-Nori-Identity-Issuer"), Subject: r.Header.Get("X-Nori-Identity-Subject"),
				EmailAuthoritative: r.Header.Get("X-Nori-Identity-Email-Authoritative") == "true" &&
					r.Header.Get("X-Nori-Identity-Issuer") == "https://accounts.google.com",
			}
		}
		serveIdentityResolve(w, r, u.directory, u.provisioningMode, identity, lookupEmail, body.Name, inviteCode)
		return
	}
	if r.URL.Path == "/api/users/redeem" {
		serveUserRedeem(w, r, u.directory, u.provisioningMode, lookupEmail, body.Name, inviteCode)
		return
	}
	serveUserResolve(w, r, u.directory, u.provisioningMode, lookupEmail, body.Name, inviteCode)
}

func (u *userRouter) handleGoogleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		u.handleForbiddenUser(w, r)
		return
	}
	_, isUserToken := serviceauth.VerifiedEmail(r.Context())
	id := ""
	if !isUserToken && u.opts.GoogleOAuthUser != nil {
		id = u.opts.GoogleOAuthUser(r.URL.Query().Get("state"))
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "This Google sign-in link is invalid or expired."})
		return
	}
	copyURL := *r.URL
	copyURL.Path, copyURL.RawPath = "/v1/connections/oauth/callback", ""
	cloned := r.Clone(r.Context())
	cloned.URL = &copyURL
	u.dispatchUser(w, cloned, id)
}

func (u *userRouter) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		u.handleForbiddenUser(w, r)
		return
	}
	if _, isUserToken := serviceauth.VerifiedEmail(r.Context()); isUserToken {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden."})
		return
	}
	rows, err := u.directory.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	failed := false
	for _, row := range rows {
		ctx, row, done, err := startUserActivity(r.Context(), row, u.opts)
		if err != nil {
			failed = true
			continue
		}
		if row.ResetPending {
			done()
			continue
		}
		routes, release, err := u.open(ctx, row)
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Some users need a retry."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (u *userRouter) handlePrefixedUser(w http.ResponseWriter, r *http.Request) {
	id, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/users/"), "/")
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
		return
	}
	copyURL := *r.URL
	copyURL.Path = "/" + path
	copyURL.RawPath = ""
	cloned := r.Clone(r.Context())
	cloned.URL = &copyURL
	u.dispatchUser(w, cloned, id)
}

func (u *userRouter) dispatchUser(w http.ResponseWriter, r *http.Request, id string) {
	verifiedEmail, isUserToken := serviceauth.VerifiedEmail(r.Context())
	verifiedIdentity, hasVerifiedIdentity := serviceauth.VerifiedIdentity(r.Context())
	isService := serviceauth.IsService(r.Context())

	row, err := u.directory.Get(r.Context(), id)
	if err != nil {
		writeUserLookupError(w, r, err, "A known user is required.")
		return
	}
	if hasVerifiedIdentity && !row.HasIdentity(verifiedIdentity) {
		if row.IdentityIssuer != "" || !strings.EqualFold(row.Email, verifiedEmail) || !users.MayBindLegacyAccount(verifiedIdentity, row.Email) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Access denied."})
			return
		}
		identities, ok := u.directory.(users.IdentityDirectory)
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Nori could not check your account. Try again shortly."})
			return
		}
		resolved, err := identities.ResolveIdentity(r.Context(), verifiedIdentity, verifiedEmail, "", "manual", "")
		if err != nil || resolved.ID != row.ID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Access denied."})
			return
		}
		row = resolved
	} else if isUserToken && !hasVerifiedIdentity && !strings.EqualFold(row.Email, verifiedEmail) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Access denied."})
		return
	}
	ctx := observability.BindHTTPUser(r.Context(), row.ID)
	if isService && r.Method == http.MethodPost && r.URL.Path == "/v1/settings/account/delete" {
		identity := users.Identity{Issuer: r.Header.Get("X-Nori-Identity-Issuer"), Subject: r.Header.Get("X-Nori-Identity-Subject")}
		email, decodeErr := url.PathUnescape(r.Header.Get("X-Nori-Identity-Email"))
		email = users.Email(email)
		address, emailErr := mail.ParseAddress(email)
		if identity.Validate() == nil && row.HasIdentity(identity) && decodeErr == nil && emailErr == nil && address.Address == email {
			ctx = serviceauth.WithVerifiedIdentity(ctx, email, identity)
		}
	}
	if u.limiter != nil {
		cat, isSSE := classifyUserRequest(r.Method, r.URL.Path)
		if ok, retryAfter := u.limiter.Allow(cat, row.ID); !ok {
			writeRateLimited(w, r.WithContext(ctx), cat, retryAfter)
			return
		}
		if isSSE {
			releaseSSE, ok := u.limiter.AcquireSSE(row.ID)
			if !ok {
				writeSSELimitExceeded(w, r.WithContext(ctx))
				return
			}
			defer releaseSSE()
		}
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/settings/reset" && u.opts.ResetData != nil {
		jsonBody(func(w http.ResponseWriter, r *http.Request) {
			resetUserData(w, r, row, u.opts.ResetData)
		})(w, r.WithContext(ctx))
		return
	}
	requestCtx := ctx
	ctx, row, done, err := startUserActivity(ctx, row, u.opts)
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
	routes, release, err := u.open(ctx, row)
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
}

func serveIdentityResolve(w http.ResponseWriter, r *http.Request, directory users.Directory, mode settings.UserProvisioningMode, identity users.Identity, email, name, inviteCode string) {
	if identity.Validate() != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "A verified sign-in is required."})
		return
	}
	identities, ok := directory.(users.IdentityDirectory)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Nori could not check your account. Try again shortly."})
		return
	}
	row, err := identities.ResolveIdentity(r.Context(), identity, email, name, string(mode), inviteCode)
	if err != nil {
		switch {
		case errors.Is(err, users.ErrInviteRequired):
			if r.URL.Path == "/api/users/redeem" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invite code is required.", "code": "invite_code_required"})
			} else {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "An invite code is required to access Nori.", "code": inviteRequiredCode})
			}
		case errors.Is(err, users.ErrIdentityUnbound):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Your account requires identity verification.", "code": "identity_unbound"})
		case errors.Is(err, users.ErrIdentityConflict), errors.Is(err, users.ErrIdentityNotFound), errors.Is(err, users.ErrIdentityInvalid):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "No user for this sign-in."})
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
			slog.WarnContext(r.Context(), "identity resolution failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Nori could not check your account. Try again shortly."})
		}
		return
	}
	observability.BindHTTPUser(r.Context(), row.ID)
	w.Header().Set("X-Nori-Identity-Bound", "1")
	writeJSON(w, http.StatusOK, map[string]string{"id": row.ID})
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
	router := &userRouter{model: model, opts: opts}
	mux := http.NewServeMux()
	router.registerOpsRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
	})
	return localOriginGuard(local, mux)
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
