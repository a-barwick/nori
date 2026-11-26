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

const (
	StatusNotConnected  = "not_connected"
	StatusConnecting    = "connecting"
	StatusConnected     = "connected"
	StatusAttention     = "needs_attention"
	StatusDisabled      = "disabled"
	StatusDisconnecting = "disconnecting"
	StatusDisconnected  = "disconnected"
)

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
	AuthorizationEpoch  int64          `json:"authorization_epoch"`
}

type Projection struct {
	ID                  string     `json:"id"`
	Revision            int64      `json:"revision"`
	IntegrationID       string     `json:"integration_id"`
	Title               string     `json:"title"`
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

type Editable struct {
	IntegrationID       string         `json:"integration_id"`
	Title               string         `json:"title"`
	SelectedResourceIDs []string       `json:"selected_resource_ids"`
	Configuration       map[string]any `json:"configuration,omitempty"`
}

type Action struct {
	Action                string   `json:"action"`
	Capabilities          []string `json:"capabilities,omitempty"`
	ReturnTo              string   `json:"return_to,omitempty"`
	PurgeCache            bool     `json:"purge_cache,omitempty"`
	Username              string   `json:"username,omitempty"`
	Password              string   `json:"password,omitempty"`
	DeleteImportedContent bool     `json:"delete_imported_content,omitempty"`
}

type AuthAttempt struct {
	ID           string    `json:"id"`
	AuthorizeURL string    `json:"authorize_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type attempt struct {
	ID            string    `json:"id"`
	ConnectionID  string    `json:"connection_id"`
	IntegrationID string    `json:"integration_id"`
	Scopes        []string  `json:"scopes"`
	Capabilities  []string  `json:"capabilities"`
	Verifier      string    `json:"verifier"`
	ReturnTo      string    `json:"return_to"`
	ExpiresAt     time.Time `json:"expires_at"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason,omitempty"`
	DrivePicker   bool      `json:"drive_picker,omitempty"`
	AccountEpoch  int64     `json:"account_epoch,omitempty"`
}

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
	BaseScopes         []string     `json:"-"`
}

type IntegrationEntry struct {
	Info
	InstallationID string   `json:"installation_id,omitempty"`
	ConnectionIDs  []string `json:"connection_ids"`
}

type PasswordIntegration interface {
	Integration
	VerifyPassword(ctx context.Context, client *http.Client, username, password string) (Account, error)
	Client(base *http.Client, username, password string) *http.Client
}

var ErrBadPassword = errors.New("the provider rejected the username or app-specific password")

type Integration interface {
	Info() Info
	OAuth(redirectURL string) *oauth2.Config
	Identify(ctx context.Context, client *http.Client) (Account, error)
	Revoke(ctx context.Context, token *oauth2.Token) error
	Resources(ctx context.Context, client *http.Client) ([]Resource, error)
	Definitions() []extensions.Definition
}
