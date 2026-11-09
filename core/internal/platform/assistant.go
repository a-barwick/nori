package platform

import (
	"context"
	"fmt"
	"sync"

	"nori/internal/connections"
	"nori/internal/connectors/google"
	"nori/internal/connectors/mcp"
	"nori/internal/connectors/nori"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/plugins"
	"nori/internal/scheduler"
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
	Scheduler   *scheduler.Service
	MCP         *mcp.Service
	Plugins     *plugins.Service
	// Tools is the inventory the agent and the API offer: connected App connectors and Nori's own tools.
	Tools extensions.Source
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

// AssistantStore is the user-scoped storage the assistant services need.
type AssistantStore interface {
	state.ResourceStore
	state.RunStore
}

// Assistant composes the user's assistant services over their scoped store.
func Assistant(s settings.Settings, user users.User, store AssistantStore) (*AssistantServices, error) {
	tasks := store
	credentials, err := Credentials(s, user)
	if err != nil {
		return nil, err
	}
	adapters := invoke.NewRegistry()
	google.Register(adapters)
	integrations := []connections.Integration{google.Integration{ClientID: s.Connections.GoogleClientID, ClientSecret: s.Connections.GoogleClientSecret}}
	conn := connections.New(store, credentials, s.Connections.RedirectURL, integrations...)
	perms := &permissions.Service{Store: store, UnattendedWrites: s.Connections.UnattendedWrites, UserSends: s.Connections.UserSends}
	sched := &scheduler.Service{Store: store, Tasks: tasks, Permissions: perms, Events: google.EventSource{Connections: conn},
		Results: func(ctx context.Context, taskID string) []string {
			results, err := tasks.RecentResults(ctx, 200)
			if err != nil {
				return nil
			}
			ids := []string{}
			for _, result := range results {
				if result.RunID == taskID {
					ids = append(ids, result.ID)
				}
			}
			return ids
		},
		Timezone: func(ctx context.Context) string {
			prefs, _, err := conn.Preferences(ctx)
			if err != nil {
				return ""
			}
			return prefs.Timezone
		},
	}
	nori.Register(adapters, nori.Adapter{Scheduler: sched, Unattended: s.Connections.UnattendedWrites})
	servers := &mcp.Service{Store: store, Credentials: credentials, LocalProcesses: s.Cloud == "local", Commands: map[string][]string{}}
	// MCP targets are "<server>/<tool>", so the adapter serves every MCP prefix.
	adapters.Register(extensions.AdapterMCP, "", mcp.Adapter{Service: servers})
	return &AssistantServices{
		Connections: conn, Permissions: perms, Adapters: adapters, Scheduler: sched, MCP: servers,
		Plugins: &plugins.Service{Store: store, MCP: servers, Builtin: plugins.Builtin},
		Tools:   extensions.Sources{nori.Source{}, conn, servers},
	}, nil
}
