package platform

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"nori/internal/connections"
	"nori/internal/connectors/mcp"
	"nori/internal/connectors/nori"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/secret"
	"nori/internal/secret/rowstore"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/users"
)

// AssistantServices are the user-scoped Connections, Permission checks and connector adapters shared by the
// API and the task worker.
type AssistantServices struct {
	Connections *connections.Service
	Permissions *permissions.Service
	Adapters    *invoke.Registry
	Scheduler   *scheduler.Service
	MCP         *mcp.Service
	// Tools is the inventory the agent and the API offer: connected integrations, MCP servers and Nori's own tools.
	Tools extensions.Source
}

var (
	keyMu   sync.Mutex
	wrapper rowstore.KeyWrapper
)

// credentialKeys resolves the master key for the database credential store once per process.
func credentialKeys(s settings.Settings) (rowstore.KeyWrapper, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if wrapper != nil {
		return wrapper, nil
	}
	c := s.Connections
	switch {
	case c.CredentialKey != "" || c.CredentialKeySecret != "":
		raw, err := secret.Resolve(context.Background(), Secrets(s), c.CredentialKey, c.CredentialKeySecret, "NORI_CREDENTIAL_KEY_SECRET")
		if err != nil {
			return nil, err
		}
		master, err := rowstore.ParseMasterKey(raw)
		if err != nil {
			return nil, err
		}
		wrapper = master
	case c.CredentialKeyFile != "":
		if s.Cloud != "local" {
			return nil, fmt.Errorf("NORI_CREDENTIAL_KEY_FILE is for local development only and requires NORI_CLOUD=local")
		}
		local, err := rowstore.OpenLocalKey(c.CredentialKeyFile)
		if err != nil {
			return nil, err
		}
		wrapper = local
	default:
		return nil, fmt.Errorf("NORI_CREDENTIAL_STORE=database needs NORI_CREDENTIAL_KEY, NORI_CREDENTIAL_KEY_SECRET (or NORI_CREDENTIAL_KEY_FILE on the local stack)")
	}
	return wrapper, nil
}

// Credentials opens the connected-account credential store for a user: envelope-encrypted rows in the user's own
// store (D1), or secret.Unavailable when none is configured.
func Credentials(s settings.Settings, user users.User, store state.ResourceStore) (secret.CredentialStore, error) {
	if s.Connections.CredentialStore == "" {
		return secret.Unavailable{}, nil
	}
	if s.Connections.CredentialStore != "database" {
		return nil, fmt.Errorf("NORI_CREDENTIAL_STORE must be database or empty")
	}
	keys, err := credentialKeys(s)
	if err != nil {
		return nil, err
	}
	return &rowstore.Store{Resources: store, Keys: keys, Owner: user.ID}, nil
}

// AssistantStore is the user-scoped storage the assistant services need.
type AssistantStore interface {
	state.ResourceStore
	state.RunStore
}

// Assistant composes the user's assistant services over their scoped store.
func Assistant(s settings.Settings, user users.User, store AssistantStore) (*AssistantServices, error) {
	tasks := store
	credentials, err := Credentials(s, user, store)
	if err != nil {
		return nil, err
	}
	adapters := invoke.NewRegistry()
	conn := connections.New(store, credentials, s.Connections.RedirectURL)
	conn.UserTimezone = user.TimeZone
	perms := &permissions.Service{Store: store, UnattendedWrites: s.Connections.UnattendedWrites, UserSends: s.Connections.UserSends}
	sched := &scheduler.Service{Store: store, Tasks: tasks, Permissions: perms,
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
	servers := &mcp.Service{Store: store, Credentials: credentials, LocalProcesses: s.Cloud == "local", Commands: localCommands(s), RedirectURL: s.Connections.RedirectURL}
	// MCP targets are "<server>/<tool>", so the adapter serves every MCP prefix.
	adapters.Register(extensions.AdapterMCP, "", mcp.Adapter{Service: servers})
	return &AssistantServices{
		Connections: conn, Permissions: perms, Adapters: adapters, Scheduler: sched, MCP: servers,
		Tools: extensions.Sources{nori.Source{}, conn, servers},
	}, nil
}

// localCommands are the declared stdio MCP commands, honoured only on the local development stack.
func localCommands(s settings.Settings) map[string][]string {
	if s.Cloud != "local" {
		return map[string][]string{}
	}
	if len(s.Connections.MCPCommands) > 0 {
		slog.Info("local stdio MCP commands declared", "count", len(s.Connections.MCPCommands))
	}
	return s.Connections.MCPCommands
}
