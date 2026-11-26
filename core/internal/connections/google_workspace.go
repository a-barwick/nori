package connections

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"nori/internal/secret"
	"nori/internal/state"
	"nori/internal/users"
)

const (
	GoogleIntegrationID = "google"
	googleConnectionID  = "conn_google_workspace"
	googleStatePrefix   = "gw1"
	driveFileScope      = "https://www.googleapis.com/auth/drive.file"
)

type GoogleWorkspace struct {
	UserID, RedirectURL, StateKey string
}

type googleDriveIntegration interface {
	DriveAccount(context.Context, *http.Client) (string, error)
	DriveResources(context.Context, *http.Client) ([]Resource, error)
}

type credentialReplacement interface {
	PrepareReplacement(context.Context, string, int64, []byte) (state.Resource, error)
}

func googleGrantedScopes(integration Integration, scopes []string) ([]string, bool) {
	info := integration.Info()
	allowed := append([]string{}, info.BaseScopes...)
	for _, capability := range info.Capabilities {
		allowed = append(allowed, capability.Scopes...)
	}
	granted := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		switch scope {
		case "https://www.googleapis.com/auth/userinfo.email":
			scope = "email"
		case "https://www.googleapis.com/auth/userinfo.profile":
			scope = "profile"
		}
		if !slices.Contains(allowed, scope) {
			return nil, false
		}
		if !slices.Contains(granted, scope) {
			granted = append(granted, scope)
		}
	}
	return granted, true
}

func (s *Service) prepareGoogleCredential(ctx context.Context, record Record, token *oauth2.Token) (state.Resource, error) {
	writer, ok := s.Credentials.(credentialReplacement)
	if !ok {
		return state.Resource{}, secret.ErrCredentialUnavailable
	}
	existing, err := s.Credentials.Get(ctx, record.ID)
	if err != nil && !errors.Is(err, secret.ErrCredentialNotFound) {
		return state.Resource{}, err
	}
	if token.RefreshToken == "" && err == nil && record.Account.ProviderID != "" {
		var previous oauth2.Token
		if err := json.Unmarshal(existing.Data, &previous); err != nil {
			return state.Resource{}, err
		}
		token.RefreshToken = previous.RefreshToken
	}
	if token.RefreshToken == "" {
		return state.Resource{}, errors.New("Google did not provide offline access")
	}
	data, err := json.Marshal(token)
	if err != nil {
		return state.Resource{}, err
	}
	return writer.PrepareReplacement(ctx, record.ID, existing.Version, data)
}

func (g GoogleWorkspace) signState(nonce string, expiry time.Time) string {
	state := strings.Join([]string{googleStatePrefix, g.UserID, strconv.FormatInt(expiry.Unix(), 10), nonce}, ".")
	return state + "." + googleStateMAC(g.StateKey, state)
}

func googleStateMAC(key, value string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("nori-google-workspace-oauth\x00" + value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// GoogleWorkspaceCallbackUser routes a provider redirect without a browser session. The
// receiving user store still validates and consumes the associated PKCE attempt.
func GoogleWorkspaceCallbackUser(raw, key string, now time.Time) string {
	if key == "" || len(raw) > 256 {
		return ""
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 5 || parts[0] != googleStatePrefix || !users.ValidID(parts[1]) || len(parts[3]) != 43 {
		return ""
	}
	stamp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !time.Unix(stamp, 0).After(now) || time.Unix(stamp, 0).After(now.Add(attemptLifetime)) {
		return ""
	}
	expected := googleStateMAC(key, strings.Join(parts[:4], "."))
	if !hmac.Equal([]byte(parts[4]), []byte(expected)) {
		return ""
	}
	return parts[1]
}

func (s *Service) currentConnection(record Record) bool {
	return s.GoogleWorkspace == nil || record.IntegrationID != GoogleIntegrationID || record.ID == googleConnectionID
}

func (s *Service) oauthConfig(integration Integration) *oauth2.Config {
	if integration == nil {
		return nil
	}
	if s.GoogleWorkspace != nil && integration.Info().ID == GoogleIntegrationID {
		return integration.OAuth(s.GoogleWorkspace.RedirectURL)
	}
	return integration.OAuth(s.RedirectURL)
}

func (s *Service) StartGoogleDrivePicker(ctx context.Context, id, returnTo string) (AuthAttempt, error) {
	if s.GoogleWorkspace == nil || id != googleConnectionID {
		return AuthAttempt{}, state.ErrNotFound
	}
	if returnTo != "/apps" && !IsIOSOAuthCallback(returnTo) {
		return AuthAttempt{}, state.ErrInvalidResource
	}
	record, _, err := s.record(ctx, id)
	if err != nil {
		return AuthAttempt{}, err
	}
	if !googlePickerReady(record) {
		return AuthAttempt{}, fmt.Errorf("%w: grant Google Drive access before choosing files", ErrNotConnected)
	}
	config := s.oauthConfig(s.Integrations[GoogleIntegrationID])
	if config == nil {
		return AuthAttempt{}, ErrNotConfigured
	}
	expiry := s.now().Add(attemptLifetime)
	oauthState := s.GoogleWorkspace.signState(randomToken(), expiry)
	a := attempt{ID: stateKey(oauthState), ConnectionID: id, IntegrationID: GoogleIntegrationID, DrivePicker: true,
		Verifier: oauth2.GenerateVerifier(), ReturnTo: returnTo, ExpiresAt: expiry, Status: "pending", AccountEpoch: record.AuthorizationEpoch}
	r, err := state.NewResource(CollectionAttempts, a.ID, 0, a, map[string]string{"connection_id": id}, s.now())
	if err != nil {
		return AuthAttempt{}, err
	}
	if _, err := s.Store.PutResource(ctx, r, 0); err != nil {
		return AuthAttempt{}, err
	}
	config.Scopes = []string{driveFileScope}
	authorize := config.AuthCodeURL(oauthState, oauth2.S256ChallengeOption(a.Verifier),
		oauth2.SetAuthURLParam("prompt", "consent"), oauth2.SetAuthURLParam("include_granted_scopes", "false"),
		oauth2.SetAuthURLParam("login_hint", record.Account.Label), oauth2.SetAuthURLParam("trigger_onepick", "true"),
		oauth2.SetAuthURLParam("allow_multiple", "true"), oauth2.SetAuthURLParam("mimetypes", "application/vnd.google-apps.document,application/vnd.google-apps.spreadsheet,application/pdf"))
	return AuthAttempt{ID: a.ID, AuthorizeURL: authorize, ExpiresAt: expiry}, nil
}

func googlePickerReady(record Record) bool {
	usable, _ := usableRecord(record)
	return usable && record.IntegrationID == GoogleIntegrationID && record.Account.Label != "" && slices.Contains(record.GrantedScopes, driveFileScope)
}

func (s *Service) claimGoogleAttempt(ctx context.Context, a attempt, r state.Resource) (state.Resource, bool, error) {
	a.Status = "processing"
	next, err := state.NewResource(CollectionAttempts, a.ID, 0, a, r.Index, s.now())
	if err != nil {
		return state.Resource{}, false, err
	}
	saved, err := s.Store.PutResource(ctx, next, r.Revision)
	if errors.Is(err, state.ErrRevisionConflict) {
		return state.Resource{}, false, nil
	}
	return saved, err == nil, err
}

func (s *Service) finishDrivePicker(ctx context.Context, a attempt, r state.Resource, code, providerError, pickedFiles string) (string, error) {
	redirect := func(result string) string {
		return BuildReturnRedirect(a.ReturnTo, url.Values{"connection": {a.ConnectionID}, "result": {result}})
	}
	if a.Status != "pending" {
		return redirect(a.Status), nil
	}
	finish := func(status string) (string, error) {
		a.Status = status
		next, err := state.NewResource(CollectionAttempts, a.ID, 0, a, r.Index, s.now())
		if err != nil {
			return "", err
		}
		if _, err := s.Store.PutResource(ctx, next, r.Revision); err != nil {
			if !errors.Is(err, state.ErrRevisionConflict) {
				return "", err
			}
			saved, err := s.Store.Resource(ctx, CollectionAttempts, a.ID)
			if err != nil {
				return "", err
			}
			if err := saved.Decode(&a); err != nil {
				return "", err
			}
		}
		return redirect(a.Status), nil
	}
	if providerError == "access_denied" || (providerError == "" && pickedFiles == "") {
		return finish("cancelled")
	}
	if providerError != "" || s.GoogleWorkspace == nil || code == "" || s.now().After(a.ExpiresAt) || len(pickedFiles) > 8192 {
		return finish("failed")
	}
	record, _, err := s.record(ctx, a.ConnectionID)
	if err != nil || record.AuthorizationEpoch != a.AccountEpoch || !googlePickerReady(record) {
		return finish("failed")
	}
	integration, ok := s.Integrations[GoogleIntegrationID].(googleDriveIntegration)
	config := s.oauthConfig(s.Integrations[GoogleIntegrationID])
	if !ok || config == nil {
		return finish("failed")
	}
	claimed, acquired, err := s.claimGoogleAttempt(ctx, a, r)
	if err != nil {
		return "", err
	}
	if !acquired {
		return redirect("processing"), nil
	}
	r = claimed
	config.Scopes = []string{driveFileScope}
	exchange, cancel := context.WithTimeout(s.providerContext(ctx), 20*time.Second)
	defer cancel()
	token, err := config.Exchange(exchange, code, oauth2.VerifierOption(a.Verifier))
	if err != nil {
		return finish("failed")
	}
	if !slices.Equal(grantedScopes(token, config.Scopes), config.Scopes) {
		return finish("failed")
	}
	account, err := integration.DriveAccount(exchange, config.Client(exchange, token))
	if err != nil || !strings.EqualFold(record.Account.Label, account) {
		return finish("failed")
	}
	return finish("drive_files_selected")
}

func (s *Service) googleResources(ctx context.Context, grant Grant, integration Integration) ([]Resource, error) {
	resources := []Resource{}
	if slices.Contains(grant.Connection.Capabilities, "calendar.read") {
		calendars, err := integration.Resources(ctx, grant.Client)
		if err != nil {
			return nil, err
		}
		resources = append(resources, calendars...)
	}
	if drive, ok := integration.(googleDriveIntegration); ok && slices.Contains(grant.Connection.GrantedScopes, driveFileScope) {
		files, err := drive.DriveResources(ctx, grant.Client)
		if err != nil {
			return nil, err
		}
		for i := range files {
			files[i].Selected = true
		}
		resources = append(resources, files...)
	}
	return resources, nil
}
