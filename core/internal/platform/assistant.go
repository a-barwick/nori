package platform

import (
	"fmt"
	"sync"

	"nori/internal/connections"
	"nori/internal/connectors/google"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/secret"
	"nori/internal/secret/localvault"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/users"
)

// AssistantServices are the user-scoped Connections, Permission checks and App connector adapters shared by the
// API and the task worker.
type AssistantServices struct {
	Connections *connections.Service
	Permissions *permissions.Service
	Adapters    *invoke.Registry
}

var (
	vaultMu sync.Mutex
	vaults  = map[string]*localvault.Vault{}
)

// Credentials opens the connected-account credential store for a user. Only the local development store exists
// until decision D1 picks a production backend; every other deployment gets secret.Unavailable.
func Credentials(s settings.Settings, user users.User) (secret.CredentialStore, error) {
	if s.Connections.CredentialStore == "" {
		return secret.Unavailable{}, nil
	}
	if s.Connections.CredentialStore != "local" || s.Cloud != "local" {
		return nil, fmt.Errorf("NORI_CREDENTIAL_STORE=local is the only store and requires NORI_CLOUD=local")
	}
	if s.Connections.CredentialDir == "" {
		return nil, fmt.Errorf("NORI_CREDENTIAL_STORE=local requires NORI_CREDENTIAL_DIR or NORI_LOCAL_ARTIFACT_DIR")
	}
	vaultMu.Lock()
	defer vaultMu.Unlock()
	vault := vaults[s.Connections.CredentialDir]
	if vault == nil {
		var err error
		if vault, err = localvault.Open(s.Connections.CredentialDir); err != nil {
			return nil, err
		}
		vaults[s.Connections.CredentialDir] = vault
	}
	return vault.ForUser(user.DataOwner())
}

// Assistant composes the user's assistant services over their scoped store.
func Assistant(s settings.Settings, user users.User, store state.ResourceStore) (*AssistantServices, error) {
	credentials, err := Credentials(s, user)
	if err != nil {
		return nil, err
	}
	adapters := invoke.NewRegistry()
	google.Register(adapters)
	integrations := []connections.Integration{google.Integration{ClientID: s.Connections.GoogleClientID, ClientSecret: s.Connections.GoogleClientSecret}}
	return &AssistantServices{
		Connections: connections.New(store, credentials, s.Connections.RedirectURL, integrations...),
		Permissions: &permissions.Service{Store: store, UnattendedWrites: s.Connections.UnattendedWrites, UserSends: s.Connections.UserSends},
		Adapters:    adapters,
	}, nil
}
