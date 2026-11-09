// Package connections owns a user's connected accounts: catalog of integrations, one-time sign-in attempts, OAuth
// callbacks, multiple accounts per integration, selected resources, health, account choice for a call, the
// credential lifecycle (resolve, refresh, replace at an expected version, revoke) and disconnect.
//
// It never decides what a call may do; Permission checks do. It never stores credential material in records; the
// secret.CredentialStore does.
package connections

import (
	"context"
	"errors"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"nori/internal/extensions"
)

const (
	CollectionConnections = "connections"
	CollectionAttempts    = "auth_attempts"
	CollectionPreferences = "preferences"
	PreferencesID         = "preferences"
)

// Status values (contract ConnectionStatus).
const (
	StatusNotConnected  = "not_connected"
	StatusConnecting    = "connecting"
	StatusConnected     = "connected"
	StatusAttention     = "needs_attention"
	StatusDisabled      = "disabled"
	StatusDisconnecting = "disconnecting"
	StatusDisconnected  = "disconnected"
)

// Health codes (contract HealthCode).
const (
	HealthReady            = "ready"
	HealthNeedsSignIn      = "needs_sign_in"
	HealthNeedsMoreAccess  = "needs_more_access"
	HealthRevoked          = "revoked"
	HealthRateLimited      = "rate_limited"
	HealthTemporary        = "temporarily_failing"
	HealthProviderDown     = "provider_unavailable"
	HealthStoreUnavailable = "credential_store_unavailable"
)

// Account identifies the provider account behind a connection.
type Account struct {
	ProviderID string `json:"provider_id"`
	Label      string `json:"label"`
}

type Health struct {
	CheckedAt             *time.Time `json:"checked_at,omitempty"`
	Code                  string     `json:"code,omitempty"`
	Message               string     `json:"message,omitempty"`
	LastSuccessAt         *time.Time `json:"last_success_at,omitempty"`
	Recovery              string     `json:"recovery,omitempty"`
	AffectedCapabilityIDs []string   `json:"affected_capability_ids,omitempty"`
	RetryAfter            *time.Time `json:"retry_after,omitempty"`
}

// Record is the stored connection. Its JSON body is private; Projection is the public view.
type Record struct {
	ID                  string         `json:"id"`
	IntegrationID       string         `json:"integration_id"`
	Title               string         `json:"title"`
	Account             Account        `json:"account"`
	Status              string         `json:"status"`
	GrantedScopes       []string       `json:"granted_scopes"`
	DesiredScopes       []string       `json:"desired_scopes"`
	Capabilities        []string       `json:"capabilities"`
	SelectedResourceIDs []string       `json:"selected_resource_ids"`
	Health              Health         `json:"health"`
	Configuration       map[string]any `json:"configuration,omitempty"`
	// AuthorizationEpoch advances when admission must be re-evaluated: disconnect, a new grant, or a change of
	// selected resources. An approval bound to an older epoch no longer applies.
	AuthorizationEpoch int64 `json:"authorization_epoch"`
}

// Projection is the contract Connection (connections.schema.json#/$defs/Connection).
type Projection struct {
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	IntegrationID string `json:"integration_id"`
	Title         string `json:"title"`
	// Account is absent until a sign-in identifies the provider account.
	Account             *Account   `json:"account,omitempty"`
	Status              string     `json:"status"`
	GrantedScopes       []string   `json:"granted_scopes"`
	SelectedResourceIDs []string   `json:"selected_resource_ids"`
	Health              Health     `json:"health"`
	Defaults            []string   `json:"defaults"`
	CreatedAt           *time.Time `json:"created_at,omitempty"`
	UpdatedAt           *time.Time `json:"updated_at,omitempty"`
	MissingScopes       []string   `json:"missing_scopes,omitempty"`
	Capabilities        []string   `json:"capabilities,omitempty"`
}

// Editable is the contract ConnectionEditable.
type Editable struct {
	IntegrationID       string         `json:"integration_id"`
	Title               string         `json:"title"`
	SelectedResourceIDs []string       `json:"selected_resource_ids"`
	Configuration       map[string]any `json:"configuration,omitempty"`
}

// Action is the contract ConnectionAction value.
type Action struct {
	Action       string   `json:"action"`
	Capabilities []string `json:"capabilities,omitempty"`
	ReturnTo     string   `json:"return_to,omitempty"`
	// PurgeCache defaults to true when absent (D4): disconnecting drops Nori's cached copy of the account's data.
	PurgeCache *bool `json:"purge_cache,omitempty"`
	// Username and Password connect an app-password integration (connect or repair). Write-only: stored as the
	// credential and never returned.
	Username              string `json:"username,omitempty"`
	Password              string `json:"password,omitempty"`
	DeleteImportedContent bool   `json:"delete_imported_content,omitempty"`
}

// AuthAttempt is the contract AuthAttempt.
type AuthAttempt struct {
	ID           string    `json:"id"`
	AuthorizeURL string    `json:"authorize_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// attempt is the stored, private sign-in attempt. It is keyed by a hash of the OAuth state, so the database never
// holds a usable state value.
type attempt struct {
	ID            string    `json:"id"`
	ConnectionID  string    `json:"connection_id"`
	IntegrationID string    `json:"integration_id"`
	Scopes        []string  `json:"scopes"`
	Capabilities  []string  `json:"capabilities"`
	Verifier      string    `json:"verifier"`
	ReturnTo      string    `json:"return_to"`
	ExpiresAt     time.Time `json:"expires_at"`
	Status        string    `json:"status"` // pending, connected, cancelled, failed
	Reason        string    `json:"reason,omitempty"`
}

// Resource is the contract Resource.
type Resource struct {
	Ref      ResourceRef `json:"ref"`
	Title    string      `json:"title"`
	Kind     string      `json:"kind"`
	Selected bool        `json:"selected"`
	Writable bool        `json:"writable"`
}

type ResourceRef struct {
	ConnectionID     string `json:"connection_id"`
	Kind             string `json:"kind"`
	ProviderID       string `json:"provider_id"`
	ProviderRevision string `json:"provider_revision,omitempty"`
}

// Capability is one purpose an integration supports and the scopes it needs.
type Capability struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Effect    string   `json:"effect"`
	Supported bool     `json:"supported"`
	Reason    string   `json:"reason,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
}

type Purpose struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Info is the integration's catalog entry, without the user's connection IDs.
type Info struct {
	ID                 string       `json:"id"`
	Title              string       `json:"title"`
	Description        string       `json:"description"`
	Categories         []string     `json:"categories"`
	Icon               string       `json:"icon,omitempty"`
	Source             string       `json:"source"`
	Auth               string       `json:"auth"`
	MultipleAccounts   bool         `json:"multiple_accounts"`
	Purposes           []Purpose    `json:"purposes"`
	Capabilities       []Capability `json:"capabilities"`
	Availability       string       `json:"availability"`
	AvailabilityReason string       `json:"availability_reason,omitempty"`
	// BaseScopes are always requested, for example to identify the account.
	BaseScopes []string `json:"-"`
}

// IntegrationEntry is the contract Integration.
type IntegrationEntry struct {
	Info
	InstallationID string   `json:"installation_id,omitempty"`
	ConnectionIDs  []string `json:"connection_ids"`
}

// PasswordIntegration is an integration that signs in with a username and an app-specific password instead of
// OAuth (Info.Auth "app_password"). VerifyPassword checks the pair with the provider and identifies the account
// without reading account content; Client returns an HTTP client that authenticates with it.
type PasswordIntegration interface {
	Integration
	VerifyPassword(ctx context.Context, client *http.Client, username, password string) (Account, error)
	Client(base *http.Client, username, password string) *http.Client
}

// ErrBadPassword is a username or app-specific password the provider rejected.
var ErrBadPassword = errors.New("the provider rejected the username or app-specific password")

// Integration is the auth and catalog half of an App connector. The execution half is an invoke.Adapter.
type Integration interface {
	Info() Info
	// OAuth returns the client configuration, or nil when the integration is not configured in this deployment.
	OAuth(redirectURL string) *oauth2.Config
	// Identify reads the account identity with a fresh grant. It must not read account content.
	Identify(ctx context.Context, client *http.Client) (Account, error)
	// Revoke asks the provider to revoke the grant. Unsupported revocation returns nil.
	Revoke(ctx context.Context, token *oauth2.Token) error
	// Resources lists selectable resources such as calendars.
	Resources(ctx context.Context, client *http.Client) ([]Resource, error)
	// Definitions are the tools the integration contributes. Account choice happens per call.
	Definitions() []extensions.Definition
}
