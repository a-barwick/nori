package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"nori/internal/connections"
	"nori/internal/connectors/google"
	"nori/internal/connectors/mcp"
	"nori/internal/connectors/nori"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/modelkeys"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/secret"
	"nori/internal/secret/rowstore"
	"nori/internal/servicekeys"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/users"
)

type AssistantServices struct {
	Connections *connections.Service
	Permissions *permissions.Service
	Adapters    *invoke.Registry
	Scheduler   *scheduler.Service
	MCP         *mcp.Service
	ModelKeys   *modelkeys.Service
	ServiceKeys *servicekeys.Service
	Credentials secret.CredentialStore
	Tools       extensions.Source
	Close       func()
}

func newCredentialKeys(s settings.Settings) (rowstore.KeyWrapper, error) {
	c := s.Connections
	var staticWrapper rowstore.KeyWrapper
	if c.CredentialKey != "" || c.CredentialKeySecret != "" {
		raw, err := secret.Resolve(context.Background(), Secrets(s), c.CredentialKey, c.CredentialKeySecret, "NORI_CREDENTIAL_KEY_SECRET")
		if err != nil {
			return nil, err
		}
		ring, err := rowstore.ParseKeyRing(raw)
		if err != nil {
			return nil, err
		}
		staticWrapper = ring
	}
	switch {
	case c.CredentialKMSKey != "":
		kmsKey, err := rowstore.NewGCPKMSKey(rowstore.GCPKMSConfig{KeyName: c.CredentialKMSKey})
		if err != nil {
			return nil, err
		}
		if staticWrapper != nil {
			return rowstore.NewKeyRing(kmsKey, staticWrapper)
		}
		return kmsKey, nil
	case staticWrapper != nil:
		return staticWrapper, nil
	case c.CredentialKeyFile != "":
		if s.Cloud != "local" {
			return nil, fmt.Errorf("NORI_CREDENTIAL_KEY_FILE is for local development only and requires NORI_CLOUD=local")
		}
		return rowstore.OpenLocalKey(c.CredentialKeyFile)
	default:
		return nil, fmt.Errorf("NORI_CREDENTIAL_STORE=database needs NORI_CREDENTIAL_KMS_KEY, NORI_CREDENTIAL_KEY, NORI_CREDENTIAL_KEY_SECRET (or NORI_CREDENTIAL_KEY_FILE on the local stack)")
	}
}

func (p *Storage) credentialKeys(s settings.Settings) (rowstore.KeyWrapper, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keyWrapper != nil {
		return p.keyWrapper, nil
	}
	w, err := newCredentialKeys(s)
	if err != nil {
		return nil, err
	}
	p.keyWrapper = w
	return w, nil
}

func Credentials(s settings.Settings, user users.User, store state.ResourceStore) (secret.CredentialStore, error) {
	return openCredentials(s, user, store, newCredentialKeys)
}

func (p *Storage) Credentials(s settings.Settings, user users.User, store state.ResourceStore) (secret.CredentialStore, error) {
	return openCredentials(s, user, store, p.credentialKeys)
}

func openCredentials(s settings.Settings, user users.User, store state.ResourceStore, keysFn func(settings.Settings) (rowstore.KeyWrapper, error)) (secret.CredentialStore, error) {
	if s.Connections.CredentialStore == "" {
		return secret.Unavailable{}, nil
	}
	if s.Connections.CredentialStore != "database" {
		return nil, fmt.Errorf("NORI_CREDENTIAL_STORE must be database or empty")
	}
	keys, err := keysFn(s)
	if err != nil {
		return nil, err
	}
	return &rowstore.Store{Resources: store, Keys: keys, Owner: user.ID}, nil
}

type AssistantStore interface {
	state.ResourceStore
	state.RunStore
}

func Assistant(s settings.Settings, user users.User, store AssistantStore) (*AssistantServices, error) {
	return openAssistant(s, user, store, Credentials)
}

func (p *Storage) Assistant(s settings.Settings, user users.User, store AssistantStore) (*AssistantServices, error) {
	var unregister func()
	services, err := openAssistant(s, user, store, func(s settings.Settings, user users.User, store state.ResourceStore) (secret.CredentialStore, error) {
		creds, err := p.Credentials(s, user, store)
		if err != nil {
			return nil, err
		}
		if rs, ok := creds.(*rowstore.Store); ok {
			unregister = p.registerRowStore(user.ID, rs)
		}
		return creds, nil
	})
	if err != nil {
		if unregister != nil {
			unregister()
		}
		return nil, err
	}
	if unregister != nil {
		prev := services.Close
		services.Close = func() {
			unregister()
			if prev != nil {
				prev()
			}
		}
	}
	return services, nil
}

func openAssistant(
	s settings.Settings,
	user users.User,
	store AssistantStore,
	credsFn func(settings.Settings, users.User, state.ResourceStore) (secret.CredentialStore, error),
) (*AssistantServices, error) {
	tasks := store
	credentials, err := credsFn(s, user, store)
	if err != nil {
		return nil, err
	}
	adapters := invoke.NewRegistry()
	var integrations []connections.Integration
	workspace := s.Connections.GoogleWorkspace
	if workspace.EnabledFor(user.Email) {
		integrations = append(integrations, google.Integration{ClientID: workspace.ClientID, ClientSecret: workspace.ClientSecret})
		google.Register(adapters)
	}
	conn := connections.New(store, credentials, s.Connections.RedirectURL, integrations...)
	if len(integrations) > 0 {
		conn.GoogleWorkspace = &connections.GoogleWorkspace{UserID: user.ID, RedirectURL: workspace.RedirectURL, StateKey: workspace.ClientSecret}
	}
	conn.UserTimezone = user.TimeZone
	conn.DataResetAt = user.DataResetAt
	perms := &permissions.Service{
		Store:            store,
		UnattendedWrites: s.Connections.UnattendedWrites,
		UserSends:        s.Connections.UserSends,
		ValidateEditedPayload: func(toolID string, edited map[string]string, originalPayload json.RawMessage) error {
			if len(edited) == 0 {
				return nil
			}
			var meta struct {
				Effect     string   `json:"effect"`
				Recipients []string `json:"recipients"`
			}
			_ = json.Unmarshal(originalPayload, &meta)
			lowTool := strings.ToLower(toolID)
			isMessageTool := len(meta.Recipients) > 0 ||
				meta.Effect == "sensitive_send" ||
				strings.Contains(lowTool, "mail") ||
				strings.Contains(lowTool, "email") ||
				strings.Contains(lowTool, "draft") ||
				strings.Contains(lowTool, "message") ||
				strings.Contains(lowTool, "send")
			if !isMessageTool {
				return fmt.Errorf("edited_fields are only permitted for message or draft approvals")
			}
			return nil
		},
	}
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
		Preferences: func(ctx context.Context) (connections.Preferences, error) {
			prefs, _, err := conn.Preferences(ctx)
			return prefs, err
		},
	}
	nori.Register(adapters, nori.Adapter{Scheduler: sched, Unattended: s.Connections.UnattendedWrites})
	servers := &mcp.Service{Store: store, Credentials: credentials, LocalProcesses: s.Cloud == "local", Commands: localCommands(s), RedirectURL: s.Connections.RedirectURL}
	adapters.Register(extensions.AdapterMCP, "", mcp.Adapter{Service: servers})
	var mk *modelkeys.Service
	if s.Models.BYOKRequired && s.Connections.CredentialStore == "database" {
		mk = &modelkeys.Service{Store: credentials}
	}
	var sk *servicekeys.Service
	if s.Connections.CredentialStore == "database" {
		sk = &servicekeys.Service{Store: credentials}
	}
	closeFn := func() {
		if rs, ok := credentials.(*rowstore.Store); ok {
			rs.EvictAll()
		}
	}
	return &AssistantServices{
		Connections: conn, Permissions: perms, Adapters: adapters, Scheduler: sched, MCP: servers,
		ModelKeys: mk, ServiceKeys: sk, Credentials: credentials,
		Tools: extensions.Sources{nori.Source{}, conn, servers},
		Close: closeFn,
	}, nil
}

func localCommands(s settings.Settings) map[string][]string {
	if s.Cloud != "local" {
		return map[string][]string{}
	}
	if len(s.Connections.MCPCommands) > 0 {
		slog.Info("local stdio MCP commands declared", "count", len(s.Connections.MCPCommands))
	}
	return s.Connections.MCPCommands
}
