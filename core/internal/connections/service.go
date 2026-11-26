package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
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
	Store           state.ResourceStore
	Credentials     secret.CredentialStore
	Integrations    map[string]Integration
	order           []string
	RedirectURL     string
	UserTimezone    string
	DataResetAt     string
	Now             func() time.Time
	Transport       http.RoundTripper
	Cleanup         []func(ctx context.Context, connectionID string, purgeCache, deleteImported bool) error
	OnConnected     func(ctx context.Context, connectionID string)
	GoogleWorkspace *GoogleWorkspace
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
			if !s.currentConnection(record) {
				continue
			}
			records, resources = append(records, s.lapsed(record, r)), append(resources, r)
		}
		if next == "" {
			return records, resources, nil
		}
		after = next
	}
}

func (s *Service) rawRecord(ctx context.Context, id string) (Record, state.Resource, error) {
	r, err := s.Store.Resource(ctx, CollectionConnections, id)
	if err != nil {
		return Record{}, r, err
	}
	var record Record
	err = r.Decode(&record)
	if err == nil && !s.currentConnection(record) {
		return Record{}, r, state.ErrNotFound
	}
	return record, r, err
}

func (s *Service) record(ctx context.Context, id string) (Record, state.Resource, error) {
	record, r, err := s.rawRecord(ctx, id)
	if err != nil {
		return Record{}, r, err
	}
	return s.lapsed(record, r), r, nil
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
	if integration == nil || integration.Info().Auth != "oauth" || s.oauthConfig(integration) == nil {
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
	if info.Auth == "oauth" && s.oauthConfig(integration) == nil {
		info.Availability, info.AvailabilityReason = "unavailable", "The provider's app credentials are not configured in this deployment."
	} else if info.Auth != "none" && secret.IsUnavailable(s.Credentials) {
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
	singleGoogleAccount := s.GoogleWorkspace != nil && value.IntegrationID == GoogleIntegrationID
	if singleGoogleAccount {
		id = googleConnectionID
	}
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
		RequestID: requestID, Method: "POST", Target: "/v1/connections", Payload: value, Collection: CollectionConnections, ID: id, SkipRevisionCheck: singleGoogleAccount,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if singleGoogleAccount && current != nil {
				var existing Record
				if err := current.Decode(&existing); err != nil {
					return state.MutationResult{}, err
				}
				rc, err := s.receipt(s.lapsed(existing, *current), current.Revision, "applied", prefs)
				return state.MutationResult{Receipt: rc}, err
			}
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
