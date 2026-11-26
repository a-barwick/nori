package connections

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"nori/internal/extensions"
	"nori/internal/secret"
	"nori/internal/state"
)

var (
	ErrUnknownIntegration = errors.New("this integration is not in the catalog")
	ErrNotConfigured      = errors.New("this integration is not configured in this deployment")
	ErrNotConnected       = errors.New("no connected account can do this")
	ErrUnknownAccount     = errors.New("the requested account is not connected")
	ErrAttemptInvalid     = errors.New("this sign-in link is not valid for this user")
	ErrWrongAccount       = errors.New("the provider signed in a different account than this connection")
	ErrInvalidAction      = errors.New("this action does not apply to the connection's state")
)

type ChooseAccount struct {
	Purpose  string
	Eligible []Record
}

func (e *ChooseAccount) Error() string {
	return fmt.Sprintf("%d connected accounts can do %s; choose one", len(e.Eligible), e.Purpose)
}

type Service struct {
	Store        state.ResourceStore
	Credentials  secret.CredentialStore
	Integrations map[string]Integration
	order        []string
	RedirectURL  string
	UserTimezone string
	DataResetAt  string
	Now          func() time.Time
	Transport    http.RoundTripper
	Cleanup      []func(ctx context.Context, connectionID string, purgeCache, deleteImported bool) error
	OnConnected  func(ctx context.Context, connectionID string)
}

func New(store state.ResourceStore, credentials secret.CredentialStore, redirectURL string, integrations ...Integration) *Service {
	s := &Service{Store: store, Credentials: credentials, RedirectURL: redirectURL, Integrations: map[string]Integration{}, Now: time.Now}
	for _, integration := range integrations {
		id := integration.Info().ID
		s.Integrations[id] = integration
		s.order = append(s.order, id)
	}
	return s
}

const attemptLifetime = 15 * time.Minute

func (s *Service) providerContext(ctx context.Context) context.Context {
	if s.Transport == nil {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: s.Transport, Timeout: 30 * time.Second})
}

func (s *Service) now() time.Time { return s.Now().UTC() }

func (s *Service) baseClient() *http.Client {
	if s.Transport != nil {
		return &http.Client{Transport: s.Transport, Timeout: 30 * time.Second}
	}
	return &http.Client{Timeout: 30 * time.Second}
}

type passwordCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Service) connectPassword(ctx context.Context, id, requestID string, expected int64, action Action, integration PasswordIntegration) (state.MutationReceipt, error) {
	username, password := strings.TrimSpace(action.Username), strings.TrimSpace(action.Password)
	if username == "" || password == "" {
		return state.MutationReceipt{}, fmt.Errorf("%w: this app connects with a username and an app-specific password", state.ErrInvalidResource)
	}
	account, err := integration.VerifyPassword(ctx, s.baseClient(), username, password)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	data, _ := json.Marshal(passwordCredential{Username: username, Password: password})
	stored, err := s.Credentials.Get(ctx, id)
	version := int64(0)
	if err == nil {
		version = stored.Version
	} else if !errors.Is(err, secret.ErrCredentialNotFound) {
		return state.MutationReceipt{}, err
	}
	if _, err := s.Credentials.Replace(ctx, id, version, data); err != nil {
		return state.MutationReceipt{}, err
	}
	info := integration.Info()
	capabilities := []string{}
	for _, c := range info.Capabilities {
		if c.Supported {
			capabilities = append(capabilities, c.ID)
		}
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	action.Password = ""
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/connections/" + id + "/actions", ExpectedRevision: expected, Payload: action,
		Collection: CollectionConnections, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := current.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnecting {
				return state.MutationResult{}, ErrInvalidAction
			}
			if record.Account.ProviderID != "" && record.Account.ProviderID != account.ProviderID {
				return state.MutationResult{}, fmt.Errorf("%w: these credentials belong to a different account than this connection", state.ErrInvalidResource)
			}
			now := s.now()
			record.Account, record.Status, record.Capabilities, record.GrantedScopes = account, StatusConnected, capabilities, []string{}
			record.AuthorizationEpoch++
			record.Health = Health{CheckedAt: &now, Code: HealthReady, Recovery: "none", LastSuccessAt: &now}
			return s.write(record, current.Revision+1, "applied", prefs)
		},
	})
	if err == nil && s.OnConnected != nil {
		s.OnConnected(context.WithoutCancel(ctx), id)
	}
	return receipt, err
}

func (s *Service) Records(ctx context.Context) ([]Record, []state.Resource, error) {
	var records []Record
	var resources []state.Resource
	after := ""
	for {
		page, next, err := s.Store.Resources(ctx, CollectionConnections, nil, after, 200)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range page {
			var record Record
			if err := r.Decode(&record); err != nil {
				return nil, nil, err
			}
			records, resources = append(records, s.lapsed(record, r)), append(resources, r)
		}
		if next == "" {
			return records, resources, nil
		}
		after = next
	}
}

func (s *Service) record(ctx context.Context, id string) (Record, state.Resource, error) {
	r, err := s.Store.Resource(ctx, CollectionConnections, id)
	if err != nil {
		return Record{}, r, err
	}
	var record Record
	err = r.Decode(&record)
	return s.lapsed(record, r), r, err
}

func (s *Service) lapsed(record Record, r state.Resource) Record {
	if record.Status != StatusConnecting || r.UpdatedAt.IsZero() || s.now().Sub(r.UpdatedAt) <= attemptLifetime {
		return record
	}
	record.Status = StatusNotConnected
	if record.Account.ProviderID != "" {
		record.Status = StatusAttention
	}
	record.Health = Health{Code: HealthNeedsSignIn, Recovery: "connect", Message: "The last sign-in was not finished."}
	return record
}

func (s *Service) ConnectTarget(ctx context.Context, integrationID, requestID string) (Record, error) {
	integration := s.Integrations[integrationID]
	if integration == nil || integration.Info().Auth != "oauth" || integration.OAuth(s.RedirectURL) == nil {
		return Record{}, ErrNotConfigured
	}
	records, _, err := s.Records(ctx)
	if err != nil {
		return Record{}, err
	}
	for _, record := range records {
		if record.IntegrationID == integrationID && record.Account.ProviderID == "" &&
			(record.Status == StatusNotConnected || record.Status == StatusConnecting) {
			return record, nil
		}
	}
	receipt, err := s.Create(ctx, requestID, Editable{IntegrationID: integrationID})
	if err != nil {
		return Record{}, err
	}
	var projection struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(receipt.Resource, &projection); err != nil || projection.ID == "" {
		return Record{}, errors.New("the connection could not be created")
	}
	record, _, err := s.record(ctx, projection.ID)
	return record, err
}

func (s *Service) Project(record Record, r state.Resource, prefs Preferences) Projection {
	p := Projection{
		ID: record.ID, Revision: r.Revision, IntegrationID: record.IntegrationID, Title: record.Title,
		Status: record.Status, GrantedScopes: nonNil(record.GrantedScopes), SelectedResourceIDs: nonNil(record.SelectedResourceIDs),
		Health: record.Health, Defaults: []string{}, Capabilities: record.Capabilities,
	}
	if record.Account.ProviderID != "" {
		account := record.Account
		p.Account = &account
	}
	if !r.CreatedAt.IsZero() {
		created, updated := r.CreatedAt, r.UpdatedAt
		p.CreatedAt, p.UpdatedAt = &created, &updated
	}
	for _, scope := range record.DesiredScopes {
		if !slices.Contains(record.GrantedScopes, scope) {
			p.MissingScopes = append(p.MissingScopes, scope)
		}
	}
	for purpose, id := range prefs.AccountDefaults {
		if id == record.ID {
			p.Defaults = append(p.Defaults, purpose)
		}
	}
	sort.Strings(p.Defaults)
	return p
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *Service) Get(ctx context.Context, id string) (Projection, error) {
	record, r, err := s.record(ctx, id)
	if err != nil {
		return Projection{}, err
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return Projection{}, err
	}
	return s.Project(record, r, prefs), nil
}

func (s *Service) List(ctx context.Context) ([]Projection, error) {
	records, resources, err := s.Records(ctx)
	if err != nil {
		return nil, err
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Projection, 0, len(records))
	for i := range records {
		out = append(out, s.Project(records[i], resources[i], prefs))
	}
	return out, nil
}

func (s *Service) Catalog(ctx context.Context, query, category string) ([]IntegrationEntry, error) {
	records, _, err := s.Records(ctx)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	out := []IntegrationEntry{}
	for _, id := range s.order {
		integration := s.Integrations[id]
		info := s.info(integration)
		if category != "" && !slices.Contains(info.Categories, category) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(info.Title+" "+info.Description+" "+strings.Join(info.Categories, " ")), query) {
			continue
		}
		entry := IntegrationEntry{Info: info, ConnectionIDs: []string{}}
		for _, record := range records {
			if record.IntegrationID == id && record.Status != StatusDisconnected {
				entry.ConnectionIDs = append(entry.ConnectionIDs, record.ID)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *Service) Integration(ctx context.Context, id string) (IntegrationEntry, error) {
	entries, err := s.Catalog(ctx, "", "")
	if err != nil {
		return IntegrationEntry{}, err
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return IntegrationEntry{}, state.ErrNotFound
}

func (s *Service) info(integration Integration) Info {
	info := integration.Info()
	if info.Availability == "" {
		info.Availability = "available"
	}
	if info.Auth == "oauth" && integration.OAuth(s.RedirectURL) == nil {
		info.Availability, info.AvailabilityReason = "unavailable", "The provider's app credentials are not configured in this deployment."
	} else if _, err := s.Credentials.Get(context.Background(), "availability-probe"); errors.Is(err, secret.ErrCredentialUnavailable) && info.Auth != "none" {
		info.Availability, info.AvailabilityReason = "blocked", "Connected-account credential storage is not configured in this deployment."
	}
	return info
}

func (s *Service) receipt(record Record, revision int64, status string, prefs Preferences) (state.MutationReceipt, error) {
	projection := s.Project(record, state.Resource{Revision: revision}, prefs)
	data, err := json.Marshal(projection)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	code := 200
	if status == "queued" {
		code = 202
	}
	return state.MutationReceipt{Status: status, Resource: data, HTTPStatus: code}, nil
}

func (s *Service) validEditable(value Editable) (Integration, error) {
	integration, ok := s.Integrations[value.IntegrationID]
	if !ok {
		return nil, ErrUnknownIntegration
	}
	if len(value.Title) > 200 || len(value.SelectedResourceIDs) > 256 {
		return nil, state.ErrInvalidResource
	}
	return integration, nil
}

func (s *Service) Create(ctx context.Context, requestID string, value Editable) (state.MutationReceipt, error) {
	integration, err := s.validEditable(value)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	id := "conn_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("connection:"+requestID)).String(), "-", "")[:20]
	title := value.Title
	if title == "" {
		title = integration.Info().Title
	}
	record := Record{
		ID: id, IntegrationID: value.IntegrationID, Title: title, Status: StatusNotConnected,
		GrantedScopes: []string{}, SelectedResourceIDs: nonNil(value.SelectedResourceIDs), Configuration: value.Configuration,
		Health: Health{Code: HealthNeedsSignIn, Recovery: "connect"},
	}
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/connections", Payload: value, Collection: CollectionConnections, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			r, err := state.NewResource(CollectionConnections, id, 1, record, recordIndex(record), s.now())
			if err != nil {
				return state.MutationResult{}, err
			}
			rc, err := s.receipt(record, 1, "applied", prefs)
			rc.HTTPStatus = 201
			return state.MutationResult{Writes: []state.Resource{r}, Receipt: rc}, err
		},
	})
	return receipt, err
}

func recordIndex(record Record) map[string]string {
	return map[string]string{"integration_id": record.IntegrationID, "status": record.Status}
}

func (s *Service) Update(ctx context.Context, id, requestID string, expected int64, value Editable) (state.MutationReceipt, error) {
	if _, err := s.validEditable(value); err != nil {
		return state.MutationReceipt{}, err
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "PUT", Target: "/v1/connections/" + id, ExpectedRevision: expected, Payload: value,
		Collection: CollectionConnections, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := current.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if value.IntegrationID != record.IntegrationID {
				return state.MutationResult{}, state.ErrInvalidResource
			}
			if !slices.Equal(record.SelectedResourceIDs, value.SelectedResourceIDs) {
				record.AuthorizationEpoch++
			}
			record.Title, record.SelectedResourceIDs, record.Configuration = value.Title, nonNil(value.SelectedResourceIDs), value.Configuration
			return s.write(record, current.Revision+1, "applied", prefs)
		},
	})
}

func (s *Service) write(record Record, revision int64, status string, prefs Preferences, extra ...state.Resource) (state.MutationResult, error) {
	r, err := state.NewResource(CollectionConnections, record.ID, revision, record, recordIndex(record), s.now())
	if err != nil {
		return state.MutationResult{}, err
	}
	rc, err := s.receipt(record, revision, status, prefs)
	return state.MutationResult{Writes: append([]state.Resource{r}, extra...), Receipt: rc}, err
}

func (s *Service) Act(ctx context.Context, id, requestID string, expected int64, action Action) (state.MutationReceipt, error) {
	switch action.Action {
	case "connect", "repair":
		return s.connect(ctx, id, requestID, expected, action)
	case "disconnect":
		return s.disconnect(ctx, id, requestID, expected, action)
	case "enable", "disable":
		prefs, _, err := s.Preferences(ctx)
		if err != nil {
			return state.MutationReceipt{}, err
		}
		return s.Store.Mutate(ctx, state.Mutation{
			RequestID: requestID, Method: "POST", Target: "/v1/connections/" + id + "/actions", ExpectedRevision: expected, Payload: action,
			Collection: CollectionConnections, ID: id,
			Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
				if current == nil {
					return state.MutationResult{}, state.ErrNotFound
				}
				var record Record
				if err := current.Decode(&record); err != nil {
					return state.MutationResult{}, err
				}
				switch {
				case action.Action == "disable" && record.Status == StatusConnected:
					record.Status = StatusDisabled
				case action.Action == "enable" && record.Status == StatusDisabled:
					record.Status = StatusConnected
				default:
					return state.MutationResult{}, ErrInvalidAction
				}
				record.AuthorizationEpoch++
				return s.write(record, current.Revision+1, "applied", prefs)
			},
		})
	}
	return state.MutationReceipt{}, ErrInvalidAction
}

func randomToken() string {
	data := make([]byte, 32)
	_, _ = rand.Read(data)
	return base64.RawURLEncoding.EncodeToString(data)
}

func stateKey(value string) string {
	sum := sha256.Sum256([]byte("nori-oauth-state:" + value))
	return "att_" + hex.EncodeToString(sum[:20])
}

func (s *Service) scopesFor(integration Integration, capabilities []string, record Record) ([]string, []string, error) {
	info := integration.Info()
	if len(capabilities) == 0 {
		capabilities = record.Capabilities
	}
	if len(capabilities) == 0 {
		for _, capability := range info.Capabilities {
			if capability.Supported && capability.Effect == string(extensions.EffectRead) {
				capabilities = append(capabilities, capability.ID)
			}
		}
	}
	scopes := append([]string{}, info.BaseScopes...)
	for _, want := range capabilities {
		found := false
		for _, capability := range info.Capabilities {
			if capability.ID == want && capability.Supported {
				found = true
				for _, scope := range capability.Scopes {
					if !slices.Contains(scopes, scope) {
						scopes = append(scopes, scope)
					}
				}
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("%w: unknown capability %s", state.ErrInvalidResource, want)
		}
	}
	return scopes, capabilities, nil
}

func (s *Service) connect(ctx context.Context, id, requestID string, expected int64, action Action) (state.MutationReceipt, error) {
	if saved, err := s.Store.Receipt(ctx, requestID); err == nil {
		bound := action
		bound.Password = ""
		return s.replay(ctx, saved, state.Mutation{RequestID: requestID, Method: "POST", Target: "/v1/connections/" + id + "/actions", ExpectedRevision: expected, Payload: bound})
	}
	record, current, err := s.record(ctx, id)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	integration := s.Integrations[record.IntegrationID]
	if integration == nil {
		return state.MutationReceipt{}, ErrUnknownIntegration
	}
	if password, ok := integration.(PasswordIntegration); ok && integration.Info().Auth == "app_password" {
		return s.connectPassword(ctx, id, requestID, expected, action, password)
	}
	config := integration.OAuth(s.RedirectURL)
	if config == nil {
		return state.MutationReceipt{}, ErrNotConfigured
	}
	if _, err := s.Credentials.Get(ctx, id); errors.Is(err, secret.ErrCredentialUnavailable) {
		return state.MutationReceipt{}, err
	}
	scopes, capabilities, err := s.scopesFor(integration, action.Capabilities, record)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	returnTo := action.ReturnTo
	if returnTo == "" {
		returnTo = "/apps"
	} else if !ValidReturnTo(returnTo) {
		return state.MutationReceipt{}, state.ErrInvalidResource
	}
	returnTo = SafeReturnTo(returnTo)
	oauthState, verifier := randomToken(), oauth2.GenerateVerifier()
	attemptRecord := attempt{
		ID: stateKey(oauthState), ConnectionID: id, IntegrationID: record.IntegrationID, Scopes: scopes, Capabilities: capabilities,
		Verifier: verifier, ReturnTo: returnTo, ExpiresAt: s.now().Add(attemptLifetime), Status: "pending",
	}
	config.Scopes = scopes
	options := []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("include_granted_scopes", "true"), oauth2.SetAuthURLParam("prompt", "consent select_account")}
	if record.Account.Label != "" {
		options = append(options, oauth2.SetAuthURLParam("login_hint", record.Account.Label))
	}
	auth := AuthAttempt{ID: attemptRecord.ID, AuthorizeURL: config.AuthCodeURL(oauthState, options...), ExpiresAt: attemptRecord.ExpiresAt}
	authJSON, _ := json.Marshal(auth)
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	_ = current
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/connections/" + id + "/actions", ExpectedRevision: expected, Payload: action,
		Collection: CollectionConnections, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := current.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnecting {
				return state.MutationResult{}, ErrInvalidAction
			}
			if record.Status != StatusConnected && record.Account.ProviderID == "" {
				record.Status = StatusConnecting
			}
			record.DesiredScopes = scopes
			attemptResource, err := state.NewResource(CollectionAttempts, attemptRecord.ID, 1, attemptRecord, map[string]string{"connection_id": id}, s.now())
			if err != nil {
				return state.MutationResult{}, err
			}
			result, err := s.write(record, current.Revision+1, "queued", prefs, attemptResource)
			result.Receipt.Auth = authJSON
			return result, err
		},
	})
}

func (s *Service) replay(_ context.Context, saved state.MutationReceipt, m state.Mutation) (state.MutationReceipt, error) {
	binding, err := state.MutationBinding(m)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	if binding != saved.Binding {
		return state.MutationReceipt{}, state.ErrIdempotencyConflict
	}
	return saved, nil
}

func (s *Service) Callback(ctx context.Context, oauthState, code, providerError string) (string, error) {
	key := stateKey(oauthState)
	r, err := s.Store.Resource(ctx, CollectionAttempts, key)
	if errors.Is(err, state.ErrNotFound) {
		return "", ErrAttemptInvalid
	}
	if err != nil {
		return "", err
	}
	var a attempt
	if err = r.Decode(&a); err != nil {
		return "", err
	}
	redirect := func(result string) string {
		return BuildReturnRedirect(a.ReturnTo, url.Values{"connection": {a.ConnectionID}, "result": {result}})
	}
	if a.Status != "pending" {
		return redirect(a.Status), nil
	}
	finish := func(status, reason string) (string, error) {
		a.Status, a.Reason = status, reason
		next, err := state.NewResource(CollectionAttempts, a.ID, r.Revision+1, a, r.Index, s.now())
		if err != nil {
			if IsIOSOAuthCallback(a.ReturnTo) {
				return redirect(status), nil
			}
			return "", err
		}
		if _, err = s.Store.PutResource(ctx, next, r.Revision); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
			if IsIOSOAuthCallback(a.ReturnTo) {
				return redirect(status), nil
			}
			return "", err
		}
		if status != "connected" {
			s.settleFailedAttempt(ctx, a.ConnectionID, reason)
		}
		return redirect(status), nil
	}
	if providerError != "" {
		if providerError == "access_denied" {
			return finish("cancelled", "The sign-in was cancelled.")
		}
		return finish("failed", "The provider did not grant access.")
	}
	if s.now().After(a.ExpiresAt) {
		return finish("failed", "The sign-in link expired. Start again from Apps.")
	}
	integration := s.Integrations[a.IntegrationID]
	config := integration.OAuth(s.RedirectURL)
	if config == nil {
		return finish("failed", ErrNotConfigured.Error())
	}
	config.Scopes = a.Scopes
	exchange, cancel := context.WithTimeout(s.providerContext(ctx), 20*time.Second)
	defer cancel()
	token, err := config.Exchange(exchange, code, oauth2.VerifierOption(a.Verifier))
	if err != nil {
		slog.WarnContext(ctx, "connection OAuth code exchange failed", "connection_id", a.ConnectionID, "reason", redactOAuth(err))
		return finish("failed", "The provider refused the sign-in code.")
	}
	account, err := integration.Identify(exchange, config.Client(exchange, token))
	if err != nil {
		return finish("failed", "The account could not be identified.")
	}
	granted := grantedScopes(token, a.Scopes)
	record, current, err := s.record(ctx, a.ConnectionID)
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "The connection could not be loaded.")
		}
		return "", err
	}
	if record.Account.ProviderID != "" && record.Account.ProviderID != account.ProviderID {
		return finish("failed", ErrWrongAccount.Error())
	}
	if record.Account.ProviderID == "" {
		if existing, ok := s.sameAccount(ctx, a.IntegrationID, account.ProviderID, a.ConnectionID); ok {
			return s.mergeSignIn(ctx, a, r, existing, account, integration, token, granted, finish)
		}
	}
	if err = s.storeToken(ctx, a.ConnectionID, token); err != nil {
		reason := "The account's credential could not be saved."
		if errors.Is(err, secret.ErrCredentialUnavailable) {
			reason = secret.ErrCredentialUnavailable.Error()
		}
		return finish("failed", reason)
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "Account preferences could not be loaded.")
		}
		return "", err
	}
	_, err = s.Store.Mutate(ctx, state.Mutation{
		RequestID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("callback:"+a.ID)).String(), Method: "CALLBACK", Target: a.ID,
		Payload: a.ID, Collection: CollectionConnections, ID: a.ConnectionID, SkipRevisionCheck: true,
		Apply: func(cur *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if cur == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := cur.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnecting || record.Status == StatusDisconnected && cur.Revision != current.Revision {
				return state.MutationResult{}, ErrInvalidAction
			}
			now := s.now()
			record.Account, record.Status, record.GrantedScopes = account, StatusConnected, granted
			record.Capabilities = s.grantedCapabilities(integration, granted)
			record.AuthorizationEpoch++
			record.Health = Health{CheckedAt: &now, Code: HealthReady, Recovery: "none", LastSuccessAt: &now}
			if missing := missingScopes(record.DesiredScopes, granted); len(missing) > 0 {
				record.Status = StatusAttention
				record.Health.Code, record.Health.Recovery = HealthNeedsMoreAccess, "request_access"
				record.Health.Message = "Some requested access was not granted."
			}
			done := a
			done.Status = "connected"
			attemptResource, err := state.NewResource(CollectionAttempts, a.ID, r.Revision+1, done, r.Index, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			return s.write(record, cur.Revision+1, "applied", prefs, attemptResource)
		},
	})
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "The connection could not be saved.")
		}
		return "", err
	}
	if s.OnConnected != nil {
		s.OnConnected(context.WithoutCancel(ctx), a.ConnectionID)
	}
	return redirect("connected"), nil
}

func (s *Service) sameAccount(ctx context.Context, integrationID, providerID, except string) (Record, bool) {
	if providerID == "" {
		return Record{}, false
	}
	records, _, err := s.Records(ctx)
	if err != nil {
		return Record{}, false
	}
	for _, record := range records {
		if record.ID != except && record.IntegrationID == integrationID && record.Account.ProviderID == providerID &&
			record.Status != StatusDisconnected && record.Status != StatusDisconnecting {
			return record, true
		}
	}
	return Record{}, false
}

func (s *Service) CollapseDuplicates(ctx context.Context) (map[string][]string, error) {
	if len(s.Integrations) == 0 {
		return nil, nil
	}
	records, resources, err := s.Records(ctx)
	if err != nil {
		return nil, err
	}
	groups := map[string][]int{}
	var order []string
	for i, record := range records {
		if record.Account.ProviderID == "" || record.Status == StatusDisconnected || record.Status == StatusDisconnecting {
			continue
		}
		key := record.IntegrationID + "\x00" + record.Account.ProviderID
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}
	removed := map[string][]string{}
	for _, key := range order {
		members := groups[key]
		if len(members) < 2 {
			continue
		}
		keep := members[0]
		for _, i := range members[1:] {
			a, b := records[i], records[keep]
			if len(a.Capabilities) > len(b.Capabilities) || len(a.Capabilities) == len(b.Capabilities) && resources[i].UpdatedAt.After(resources[keep].UpdatedAt) {
				keep = i
			}
		}
		kept := records[keep]
		integration := s.Integrations[kept.IntegrationID]
		if integration == nil {
			continue
		}
		var drop []string
		var deletes []state.ResourceKey
		for _, i := range members {
			if i != keep {
				drop = append(drop, records[i].ID)
				deletes = append(deletes, state.ResourceKey{Collection: CollectionConnections, ID: records[i].ID})
			}
		}
		prefs, prefsResource, err := s.Preferences(ctx)
		if err != nil {
			return removed, err
		}
		_, err = s.Store.Mutate(ctx, state.Mutation{
			RequestID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("collapse:"+kept.ID+":"+strings.Join(drop, ","))).String(),
			Method:    "REPAIR", Target: kept.ID, Payload: drop, Collection: CollectionConnections, ID: kept.ID, SkipRevisionCheck: true,
			Apply: func(cur *state.Resource, read func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
				if cur == nil {
					return state.MutationResult{}, state.ErrNotFound
				}
				var record Record
				if err := cur.Decode(&record); err != nil {
					return state.MutationResult{}, err
				}
				for _, i := range members {
					for _, scope := range records[i].GrantedScopes {
						if !slices.Contains(record.GrantedScopes, scope) {
							record.GrantedScopes = append(record.GrantedScopes, scope)
						}
					}
					for _, scope := range records[i].DesiredScopes {
						if !slices.Contains(record.DesiredScopes, scope) {
							record.DesiredScopes = append(record.DesiredScopes, scope)
						}
					}
				}
				record.Capabilities = s.grantedCapabilities(integration, record.GrantedScopes)
				record.AuthorizationEpoch++
				var extra []state.Resource
				changed := false
				for purpose, id := range prefs.AccountDefaults {
					if slices.Contains(drop, id) {
						prefs.AccountDefaults[purpose], changed = record.ID, true
					}
				}
				if changed && prefsResource.Revision > 0 {
					next, err := state.NewResource(CollectionPreferences, PreferencesID, prefsResource.Revision+1, prefs, nil, s.now())
					if err != nil {
						return state.MutationResult{}, err
					}
					extra = append(extra, next)
				}
				result, err := s.write(record, cur.Revision+1, "applied", prefs, extra...)
				result.Deletes = append(result.Deletes, deletes...)
				return result, err
			},
		})
		if err != nil {
			return removed, err
		}
		for _, id := range drop {
			if err := s.Credentials.Delete(ctx, id); err != nil && !errors.Is(err, secret.ErrCredentialUnavailable) {
				slog.WarnContext(ctx, "duplicate connection credential delete failed", "connection_id", id, "err", err)
			}
			for _, cleanup := range s.Cleanup {
				_ = cleanup(ctx, id, true, false)
			}
		}
		removed[kept.ID] = drop
	}
	return removed, nil
}

func (s *Service) mergeSignIn(ctx context.Context, a attempt, attemptResource state.Resource, existing Record, account Account,
	integration Integration, token *oauth2.Token, granted []string, finish func(status, reason string) (string, error)) (string, error) {
	if token.RefreshToken == "" {
		if credential, err := s.Credentials.Get(ctx, existing.ID); err == nil {
			var old oauth2.Token
			if json.Unmarshal(credential.Data, &old) == nil {
				token.RefreshToken = old.RefreshToken
			}
		}
	}
	if err := s.storeToken(ctx, existing.ID, token); err != nil {
		reason := "The account's credential could not be saved."
		if errors.Is(err, secret.ErrCredentialUnavailable) {
			reason = secret.ErrCredentialUnavailable.Error()
		}
		return finish("failed", reason)
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "Account preferences could not be loaded.")
		}
		return "", err
	}
	var desired []string
	if added, _, err := s.record(ctx, a.ConnectionID); err == nil {
		desired = added.DesiredScopes
	}
	_, err = s.Store.Mutate(ctx, state.Mutation{
		RequestID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("callback:"+a.ID)).String(), Method: "CALLBACK", Target: a.ID,
		Payload: a.ID, Collection: CollectionConnections, ID: existing.ID, SkipRevisionCheck: true,
		Apply: func(cur *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if cur == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := cur.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnecting || record.Status == StatusDisconnected {
				return state.MutationResult{}, ErrInvalidAction
			}
			now := s.now()
			union := func(a, b []string) []string {
				out := append([]string{}, a...)
				for _, v := range b {
					if !slices.Contains(out, v) {
						out = append(out, v)
					}
				}
				return out
			}
			record.Account, record.Status = account, StatusConnected
			record.GrantedScopes = union(record.GrantedScopes, granted)
			record.DesiredScopes = union(record.DesiredScopes, desired)
			record.Capabilities = s.grantedCapabilities(integration, record.GrantedScopes)
			record.AuthorizationEpoch++
			record.Health = Health{CheckedAt: &now, Code: HealthReady, Recovery: "none", LastSuccessAt: &now,
				Message: "This account was already connected; its access was updated."}
			if missing := missingScopes(record.DesiredScopes, record.GrantedScopes); len(missing) > 0 {
				record.Status = StatusAttention
				record.Health.Code, record.Health.Recovery = HealthNeedsMoreAccess, "request_access"
				record.Health.Message = "This account was already connected; some requested access was not granted."
			}
			done := a
			done.Status = "connected"
			attemptNext, err := state.NewResource(CollectionAttempts, a.ID, attemptResource.Revision+1, done, attemptResource.Index, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			result, err := s.write(record, cur.Revision+1, "applied", prefs, attemptNext)
			result.Deletes = append(result.Deletes, state.ResourceKey{Collection: CollectionConnections, ID: a.ConnectionID})
			return result, err
		},
	})
	if err != nil {
		if IsIOSOAuthCallback(a.ReturnTo) {
			return finish("failed", "The connection could not be saved.")
		}
		return "", err
	}
	if s.OnConnected != nil {
		s.OnConnected(context.WithoutCancel(ctx), a.ConnectionID)
		s.OnConnected(context.WithoutCancel(ctx), existing.ID)
	}
	return BuildReturnRedirect(a.ReturnTo, url.Values{"connection": {existing.ID}, "result": {"connected"}}), nil
}

func (s *Service) settleFailedAttempt(ctx context.Context, connectionID, reason string) {
	record, r, err := s.record(ctx, connectionID)
	if err != nil || record.Status != StatusConnecting {
		return
	}
	now := s.now()
	record.Status = StatusNotConnected
	if record.Account.ProviderID != "" {
		record.Status = StatusAttention
	}
	record.Health = Health{CheckedAt: &now, Code: HealthNeedsSignIn, Recovery: "connect", Message: reason}
	next, err := state.NewResource(CollectionConnections, record.ID, 0, record, recordIndex(record), now)
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

func missingScopes(desired, granted []string) []string {
	var missing []string
	for _, scope := range desired {
		if !slices.Contains(granted, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}

func (s *Service) grantedCapabilities(integration Integration, granted []string) []string {
	capabilities := []string{}
	for _, capability := range integration.Info().Capabilities {
		if !capability.Supported {
			continue
		}
		ok := true
		for _, scope := range capability.Scopes {
			ok = ok && slices.Contains(granted, scope)
		}
		if ok {
			capabilities = append(capabilities, capability.ID)
		}
	}
	return capabilities
}

func grantedScopes(token *oauth2.Token, requested []string) []string {
	raw, ok := token.Extra("scope").(string)
	if !ok || raw == "" {
		return requested
	}
	out := []string{}
	for _, scope := range strings.Fields(raw) {
		if !slices.Contains(out, scope) {
			out = append(out, scope)
		}
	}
	return out
}

func redactOAuth(err error) string {
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) {
		return "provider error " + retrieve.ErrorCode
	}
	return "transport error"
}

func (s *Service) storeToken(ctx context.Context, connectionID string, token *oauth2.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	current, err := s.Credentials.Get(ctx, connectionID)
	version := current.Version
	if errors.Is(err, secret.ErrCredentialNotFound) {
		version = 0
	} else if err != nil {
		return err
	}
	_, err = s.Credentials.Replace(ctx, connectionID, version, data)
	return err
}

func (s *Service) disconnect(ctx context.Context, id, requestID string, expected int64, action Action) (state.MutationReceipt, error) {
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/connections/" + id + "/actions", ExpectedRevision: expected, Payload: action,
		Collection: CollectionConnections, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var record Record
			if err := current.Decode(&record); err != nil {
				return state.MutationResult{}, err
			}
			if record.Status == StatusDisconnected {
				return state.MutationResult{}, ErrInvalidAction
			}
			record.Status = StatusDisconnecting
			record.AuthorizationEpoch++
			return s.write(record, current.Revision+1, "queued", prefs)
		},
	})
	if err != nil {
		return receipt, err
	}
	s.finishDisconnect(context.WithoutCancel(ctx), id, action)
	return receipt, nil
}

func (s *Service) finishDisconnect(ctx context.Context, id string, action Action) {
	record, r, err := s.record(ctx, id)
	if err != nil || record.Status != StatusDisconnecting {
		return
	}
	var problems []string
	_, shared := s.sameAccount(ctx, record.IntegrationID, record.Account.ProviderID, id)
	if integration := s.Integrations[record.IntegrationID]; integration != nil && !shared {
		if credential, err := s.Credentials.Get(ctx, id); err == nil {
			var token oauth2.Token
			if json.Unmarshal(credential.Data, &token) == nil {
				revoke, cancel := context.WithTimeout(s.providerContext(ctx), 15*time.Second)
				if err := integration.Revoke(revoke, &token); err != nil {
					problems = append(problems, "The provider did not confirm revocation; remove Nori's access in the account's security settings.")
				}
				cancel()
			}
		}
	}
	if err := s.Credentials.Delete(ctx, id); err != nil && !errors.Is(err, secret.ErrCredentialUnavailable) {
		problems = append(problems, "The stored credential could not be deleted.")
	}
	for _, cleanup := range s.Cleanup {
		if err := cleanup(ctx, id, action.PurgeCache, action.DeleteImportedContent); err != nil {
			problems = append(problems, "Some cached connector data could not be removed.")
		}
	}
	now := s.now()
	record.Status, record.GrantedScopes, record.Capabilities = StatusDisconnected, []string{}, []string{}
	record.Health = Health{CheckedAt: &now, Code: HealthNeedsSignIn, Recovery: "connect", Message: strings.Join(problems, " ")}
	next, err := state.NewResource(CollectionConnections, id, 0, record, recordIndex(record), now)
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

func (s *Service) Resources(ctx context.Context, id string) ([]Resource, error) {
	grant, err := s.Grant(ctx, id)
	if err != nil {
		return nil, err
	}
	integration := s.Integrations[grant.Connection.IntegrationID]
	list, err := integration.Resources(ctx, grant.Client)
	if err != nil {
		s.ReportFailure(ctx, id, err)
		return nil, err
	}
	for i := range list {
		list[i].Ref.ConnectionID = id
		list[i].Selected = slices.Contains(grant.Connection.SelectedResourceIDs, list[i].Ref.ProviderID)
	}
	return list, nil
}

type Grant struct {
	Connection Record
	Revision   int64
	Client     *http.Client
	Token      *oauth2.Token
}

var refreshLocks sync.Map

func (s *Service) Grant(ctx context.Context, id string) (Grant, error) {
	record, r, err := s.record(ctx, id)
	if err != nil {
		return Grant{}, err
	}
	if record.Status != StatusConnected && record.Status != StatusAttention {
		return Grant{}, fmt.Errorf("%w: the account is %s", ErrNotConnected, record.Status)
	}
	integration := s.Integrations[record.IntegrationID]
	if integration == nil {
		return Grant{}, ErrUnknownIntegration
	}
	if password, ok := integration.(PasswordIntegration); ok && integration.Info().Auth == "app_password" {
		credential, err := s.Credentials.Get(ctx, id)
		if err != nil {
			return Grant{}, err
		}
		var pc passwordCredential
		if err := json.Unmarshal(credential.Data, &pc); err != nil {
			return Grant{}, errors.New("stored credential is malformed")
		}
		return Grant{Connection: record, Revision: r.Revision, Client: password.Client(s.baseClient(), pc.Username, pc.Password)}, nil
	}
	config := integration.OAuth(s.RedirectURL)
	if config == nil {
		return Grant{}, ErrNotConfigured
	}
	credential, err := s.Credentials.Get(ctx, id)
	if err != nil {
		return Grant{}, err
	}
	var token oauth2.Token
	if err = json.Unmarshal(credential.Data, &token); err != nil {
		return Grant{}, errors.New("stored credential is malformed")
	}
	if !token.Valid() {
		lock, _ := refreshLocks.LoadOrStore(id, &sync.Mutex{})
		mu := lock.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		if latest, err := s.Credentials.Get(ctx, id); err == nil && latest.Version != credential.Version {
			credential = latest
			_ = json.Unmarshal(credential.Data, &token)
		}
		if !token.Valid() {
			refresh, cancel := context.WithTimeout(s.providerContext(ctx), 20*time.Second)
			fresh, err := config.TokenSource(refresh, &token).Token()
			cancel()
			if err != nil {
				s.ReportFailure(ctx, id, err)
				return Grant{}, fmt.Errorf("%w: %s", ErrNotConnected, redactOAuth(err))
			}
			if fresh.RefreshToken == "" {
				fresh.RefreshToken = token.RefreshToken
			}
			data, _ := json.Marshal(fresh)
			if _, err = s.Credentials.Replace(ctx, id, credential.Version, data); errors.Is(err, secret.ErrCredentialConflict) {
				latest, readErr := s.Credentials.Get(ctx, id)
				if readErr != nil {
					return Grant{}, readErr
				}
				_ = json.Unmarshal(latest.Data, fresh)
			} else if err != nil {
				return Grant{}, err
			}
			token = *fresh
		}
	}
	client := config.Client(s.providerContext(context.WithoutCancel(ctx)), &token)
	client.Timeout = 30 * time.Second
	return Grant{Connection: record, Revision: r.Revision, Client: client, Token: &token}, nil
}

func (s *Service) ReportFailure(ctx context.Context, id string, failure error) {
	code, recovery, message := HealthTemporary, "wait", "The provider is temporarily failing."
	var retrieve *oauth2.RetrieveError
	switch {
	case errors.As(failure, &retrieve) && (retrieve.ErrorCode == "invalid_grant" || retrieve.ErrorCode == "unauthorized_client"):
		code, recovery, message = HealthRevoked, "connect", "Access was revoked or expired. Sign in again."
	case errors.Is(failure, ErrInsufficientScope):
		code, recovery, message = HealthNeedsMoreAccess, "request_access", "This needs more access than the account granted."
	case errors.Is(failure, ErrRateLimited):
		code, recovery, message = HealthRateLimited, "wait", "The provider is limiting requests."
	case errors.Is(failure, ErrUnauthorized):
		code, recovery, message = HealthNeedsSignIn, "connect", "The provider rejected the credential. Sign in again."
	case errors.Is(failure, secret.ErrCredentialUnavailable):
		code, recovery, message = HealthStoreUnavailable, "none", secret.ErrCredentialUnavailable.Error()
	}
	record, r, err := s.record(ctx, id)
	if err != nil {
		return
	}
	now := s.now()
	record.Health.CheckedAt, record.Health.Code, record.Health.Recovery, record.Health.Message = &now, code, recovery, message
	if code == HealthRevoked || code == HealthNeedsSignIn {
		record.Status = StatusAttention
	}
	next, err := state.NewResource(CollectionConnections, id, 0, record, recordIndex(record), now)
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

func (s *Service) ReportSuccess(ctx context.Context, id string) {
	record, r, err := s.record(ctx, id)
	if err != nil {
		return
	}
	if record.Health.Code == HealthReady && record.Status == StatusConnected && record.Health.LastSuccessAt != nil && s.now().Sub(*record.Health.LastSuccessAt) < 10*time.Minute {
		return
	}
	now := s.now()
	if missing := missingScopes(record.DesiredScopes, record.GrantedScopes); record.Status == StatusAttention && len(missing) > 0 {
		if record.Health.Code == HealthNeedsMoreAccess && record.Health.LastSuccessAt != nil && now.Sub(*record.Health.LastSuccessAt) < 10*time.Minute {
			return
		}
		record.Health = Health{CheckedAt: &now, Code: HealthNeedsMoreAccess, Recovery: "request_access", Message: "Some requested access was not granted.", LastSuccessAt: &now}
	} else {
		record.Health = Health{CheckedAt: &now, Code: HealthReady, Recovery: "none", LastSuccessAt: &now}
		if record.Status == StatusAttention {
			record.Status = StatusConnected
		}
	}
	next, err := state.NewResource(CollectionConnections, id, 0, record, recordIndex(record), now)
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

var (
	ErrInsufficientScope = errors.New("insufficient scope")
	ErrRateLimited       = errors.New("rate limited")
	ErrUnauthorized      = errors.New("unauthorized")
)

func (s *Service) Select(ctx context.Context, integrationID, purpose, requested string) (Record, int64, error) {
	records, resources, err := s.Records(ctx)
	if err != nil {
		return Record{}, 0, err
	}
	var eligible []int
	connected := false
	for i, record := range records {
		usable, _ := usableRecord(record)
		if record.IntegrationID != integrationID || !usable {
			continue
		}
		connected = true
		if purpose == "" || slices.Contains(record.Capabilities, purpose) {
			eligible = append(eligible, i)
		}
	}
	if requested != "" {
		for _, i := range eligible {
			if records[i].ID == requested || strings.EqualFold(records[i].Account.Label, requested) || strings.EqualFold(records[i].Title, requested) {
				return records[i], resources[i].Revision, nil
			}
		}
		return Record{}, 0, ErrUnknownAccount
	}
	if len(eligible) == 0 {
		if connected {
			return Record{}, 0, fmt.Errorf("%w: %w", ErrNotConnected, ErrInsufficientScope)
		}
		return Record{}, 0, ErrNotConnected
	}
	prefs, _, err := s.Preferences(ctx)
	if err != nil {
		return Record{}, 0, err
	}
	if id := prefs.AccountDefaults[purpose]; id != "" {
		for _, i := range eligible {
			if records[i].ID == id {
				return records[i], resources[i].Revision, nil
			}
		}
	}
	purposes := make([]string, 0, len(prefs.AccountDefaults))
	for p := range prefs.AccountDefaults {
		purposes = append(purposes, p)
	}
	sort.Strings(purposes)
	for _, p := range purposes {
		id := prefs.AccountDefaults[p]
		for _, i := range eligible {
			if records[i].ID == id {
				return records[i], resources[i].Revision, nil
			}
		}
	}
	if len(eligible) == 1 {
		return records[eligible[0]], resources[eligible[0]].Revision, nil
	}
	same := records[eligible[0]].Account.ProviderID != ""
	for _, i := range eligible[1:] {
		same = same && records[i].Account.ProviderID == records[eligible[0]].Account.ProviderID
	}
	if same {
		best := eligible[0]
		for _, i := range eligible[1:] {
			if len(records[i].Capabilities) > len(records[best].Capabilities) ||
				len(records[i].Capabilities) == len(records[best].Capabilities) && resources[i].UpdatedAt.After(resources[best].UpdatedAt) {
				best = i
			}
		}
		return records[best], resources[best].Revision, nil
	}
	choice := &ChooseAccount{Purpose: purpose}
	for _, i := range eligible {
		choice.Eligible = append(choice.Eligible, records[i])
	}
	return Record{}, 0, choice
}

func usableRecord(record Record) (usable, partial bool) {
	if record.Status == StatusConnected {
		return true, false
	}
	if record.Status == StatusAttention && (record.Health.Code == HealthNeedsMoreAccess || record.Health.Code == HealthReady) {
		return true, record.Health.Code == HealthNeedsMoreAccess || len(missingScopes(record.DesiredScopes, record.GrantedScopes)) > 0
	}
	return false, false
}

func (s *Service) Inventory(ctx context.Context) (extensions.Inventory, error) {
	records, _, err := s.Records(ctx)
	if err != nil {
		return extensions.Inventory{}, err
	}
	var inventory extensions.Inventory
	for _, id := range s.order {
		integration := s.Integrations[id]
		granted := map[string]bool{}
		var labels, attention []string
		for _, record := range records {
			if record.IntegrationID != id {
				continue
			}
			usable, partial := usableRecord(record)
			switch {
			case usable:
				labels = append(labels, record.Account.Label)
				if partial {
					attention = append(attention, record.Account.Label+" (some access not granted)")
				}
				for _, capability := range record.Capabilities {
					granted[capability] = true
				}
			case record.Status == StatusAttention || record.Status == StatusConnecting || record.Status == StatusDisabled:
				attention = append(attention, fmt.Sprintf("%s (%s)", record.Account.Label, record.Status))
			}
		}
		groups := map[string]string{}
		usable := map[string]bool{}
		askable := integration.Info().Auth == "oauth" && integration.OAuth(s.RedirectURL) != nil
		var groupOrder []string
		for _, d := range integration.Definitions() {
			if _, seen := groups[d.Capability]; !seen {
				groups[d.Capability] = d.CapabilityTitle
				groupOrder = append(groupOrder, d.Capability)
			}
			if d.RequiredCapability == "" || granted[d.RequiredCapability] {
				inventory.Definitions = append(inventory.Definitions, d)
				usable[d.Capability] = true
			} else if askable {
				if len(labels) == 0 {
					d.Exposure = extensions.ExposureDiscoverable
				}
				inventory.Definitions = append(inventory.Definitions, d)
			}
		}
		for _, capability := range groupOrder {
			stateText := "Not connected. The user can connect it in Apps."
			if askable {
				stateText = "Not connected. Load it and call the tool the request needs: that asks the user to connect the account from this task, and the task resumes once they have"
			}
			ready := len(labels) > 0 && usable[capability]
			if ready {
				stateText = "Connected"
			} else if len(labels) > 0 {
				stateText = "Connected, but this access was not granted. The user can grant it in Apps."
				if askable {
					stateText = "Connected, but this access was not granted. Load it and call the tool the request needs: that asks the user for the access from this task"
				}
			} else if len(attention) > 0 {
				stateText = "Needs attention in Apps: " + strings.Join(attention, ", ")
				if askable {
					stateText += ". Load it and call the tool the request needs: that asks the user to sign in from this task"
				}
			}
			inventory.Capabilities = append(inventory.Capabilities, extensions.CapabilityState{
				Capability: capability, Title: groups[capability], Account: strings.Join(labels, ", "), Availability: stateText, Ready: ready,
			})
		}
	}
	return inventory, nil
}

func (s *Service) NeedsSignIn(ctx context.Context, integrationID, requested string) (Record, bool) {
	records, _, err := s.Records(ctx)
	if err != nil {
		return Record{}, false
	}
	var found *Record
	for i, r := range records {
		if r.IntegrationID != integrationID || r.Status != StatusAttention || (r.Health.Code != HealthNeedsSignIn && r.Health.Code != HealthRevoked) {
			continue
		}
		if requested != "" && (r.ID == requested || strings.EqualFold(r.Account.Label, requested)) {
			return records[i], true
		}
		if found == nil {
			found = &records[i]
		}
	}
	if found == nil || requested != "" {
		return Record{}, false
	}
	return *found, true
}

func (s *Service) StartRepair(ctx context.Context, id, requestID, returnTo string, capabilities []string) (AuthAttempt, error) {
	record, r, err := s.record(ctx, id)
	if err != nil {
		return AuthAttempt{}, err
	}
	integration := s.Integrations[record.IntegrationID]
	if integration == nil || integration.Info().Auth != "oauth" {
		return AuthAttempt{}, fmt.Errorf("%w: this account signs in with an app password in Apps", ErrInvalidAction)
	}
	action := Action{Action: "repair", ReturnTo: returnTo}
	if len(capabilities) > 0 {
		action.Capabilities = append(append([]string{}, record.Capabilities...), capabilities...)
	}
	receipt, err := s.connect(ctx, id, requestID, r.Revision, action)
	if err != nil {
		return AuthAttempt{}, err
	}
	var attempt AuthAttempt
	if err := json.Unmarshal(receipt.Auth, &attempt); err != nil || attempt.AuthorizeURL == "" {
		return AuthAttempt{}, errors.New("the sign-in attempt could not be started")
	}
	return attempt, nil
}

func (s *Service) LackingCapability(ctx context.Context, integrationID, capability, requested string) (Record, bool) {
	records, _, err := s.Records(ctx)
	if err != nil {
		return Record{}, false
	}
	for _, r := range records {
		usable, _ := usableRecord(r)
		if r.IntegrationID != integrationID || !usable || slices.Contains(r.Capabilities, capability) {
			continue
		}
		if requested == "" || r.ID == requested || strings.EqualFold(r.Account.Label, requested) {
			return r, true
		}
	}
	return Record{}, false
}

func (s *Service) ScopesFor(integrationID, capability string) []string {
	integration := s.Integrations[integrationID]
	if integration == nil {
		return nil
	}
	for _, c := range integration.Info().Capabilities {
		if c.ID == capability {
			return c.Scopes
		}
	}
	return nil
}
