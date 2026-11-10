package platform

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"nori/internal/computer"
	"nori/internal/computer/rpc"
	"nori/internal/connectors/replay"
	"os"
	"sync"

	"nori/internal/computersession"
	"nori/internal/connections"
	"nori/internal/connectors/google"
	"nori/internal/connectors/icloud"
	"nori/internal/connectors/mcp"
	"nori/internal/connectors/microsoft"
	"nori/internal/connectors/nori"
	"nori/internal/connectors/plugin"
	"nori/internal/delivery"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/plugins"
	"nori/internal/scheduler"
	"nori/internal/secret"
	"nori/internal/secret/localvault"
	"nori/internal/secret/rowstore"
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
	Sessions    *computersession.Service
	// Tools is the inventory the agent and the API offer: connected App connectors and Nori's own tools.
	Tools extensions.Source
}

var (
	vaultMu sync.Mutex
	vaults  = map[string]*localvault.Vault{}
)

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
	case c.CredentialKMSKey != "":
		kms, err := rowstore.OpenCloudKMS(context.Background(), c.CredentialKMSKey)
		if err != nil {
			return nil, err
		}
		wrapper = kms
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
		return nil, fmt.Errorf("NORI_CREDENTIAL_STORE=database needs NORI_CREDENTIAL_KMS_KEY (or NORI_CREDENTIAL_KEY_FILE on the local stack)")
	}
	return wrapper, nil
}

// Credentials opens the connected-account credential store for a user: envelope-encrypted rows in the user's own
// store (D1), the file vault on the local stack, or secret.Unavailable when none is configured.
func Credentials(s settings.Settings, user users.User, store state.ResourceStore) (secret.CredentialStore, error) {
	if s.Connections.CredentialStore == "" {
		return secret.Unavailable{}, nil
	}
	if s.Connections.CredentialStore == "database" {
		keys, err := credentialKeys(s)
		if err != nil {
			return nil, err
		}
		return &rowstore.Store{Resources: store, Keys: keys}, nil
	}
	if s.Connections.CredentialStore != "local" || s.Cloud != "local" {
		return nil, fmt.Errorf("NORI_CREDENTIAL_STORE is database, or local (file vault) with NORI_CLOUD=local")
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
	credentials, err := Credentials(s, user, store)
	if err != nil {
		return nil, err
	}
	adapters := invoke.NewRegistry()
	google.Register(adapters)
	microsoft.Register(adapters)
	icloud.Register(adapters)
	integrations := []connections.Integration{google.Integration{ClientID: s.Connections.GoogleClientID, ClientSecret: s.Connections.GoogleClientSecret},
		microsoft.Integration{ClientID: s.Connections.MicrosoftClientID, ClientSecret: s.Connections.MicrosoftClientSecret, Tenant: s.Connections.MicrosoftTenant}, icloud.Integration{}}
	conn := connections.New(store, credentials, s.Connections.RedirectURL, integrations...)
	conn.UserTimezone = user.TimeZone
	if s.Connections.ProviderReplay != "" {
		if s.Cloud != "local" {
			return nil, fmt.Errorf("NORI_PROVIDER_REPLAY is for local development only and requires NORI_CLOUD=local")
		}
		transport, err := replay.Shared(s.Connections.ProviderReplay, user.ID)
		if err != nil {
			return nil, err
		}
		conn.Transport = transport
	}
	perms := &permissions.Service{Store: store, UnattendedWrites: s.Connections.UnattendedWrites, UserSends: s.Connections.UserSends}
	channels := map[string]scheduler.Channel{}
	c := s.Connections
	var testRoots *x509.CertPool
	if c.DeliveryTestCAFile != "" {
		if s.Cloud != "local" {
			return nil, fmt.Errorf("NORI_DELIVERY_TEST_CA_FILE is for local development only and requires NORI_CLOUD=local")
		}
		pem, err := os.ReadFile(c.DeliveryTestCAFile)
		if err != nil {
			return nil, fmt.Errorf("NORI_DELIVERY_TEST_CA_FILE: %w", err)
		}
		if testRoots, err = x509.SystemCertPool(); err != nil || testRoots == nil {
			testRoots = x509.NewCertPool()
		}
		if !testRoots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("NORI_DELIVERY_TEST_CA_FILE holds no PEM certificate")
		}
	}
	if push := delivery.NewWebPush(c.VAPIDPublicKey, c.VAPIDPrivateKey, c.VAPIDSubject); push != nil {
		push.TrustRoots(testRoots)
		channels["push"] = push
	}
	if email := delivery.NewSMTP(c.SMTPHost, c.SMTPPort, c.SMTPUsername, c.SMTPPassword, c.EmailFrom); email != nil {
		email.Roots = testRoots
		channels["email"] = email
	}
	sched := &scheduler.Service{Store: store, Tasks: tasks, Permissions: perms, Events: google.EventSource{Connections: conn},
		Channels: channels, Targets: credentials,
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
	pluginService := &plugins.Service{Store: store, MCP: servers, Builtin: plugins.Builtin}
	adapters.Register(extensions.AdapterMCP, "", mcp.Adapter{Service: servers})
	if provider := Computer(s); provider != nil {
		servers.Sandboxed = func(ctx context.Context, installationID string) (io.ReadWriteCloser, error) {
			return sandboxedServer(ctx, provider, pluginService, installationID)
		}
	}
	plugin.Register(adapters, plugin.Adapter{Plugins: pluginService, Computer: Computer(s)})
	return &AssistantServices{
		Connections: conn, Permissions: perms, Adapters: adapters, Scheduler: sched, MCP: servers,
		Plugins:  pluginService,
		Sessions: &computersession.Service{Store: store, Runs: tasks, Provider: Computer(s), Permissions: perms},
		Tools:    extensions.Sources{nori.Source{}, conn, servers, pluginService},
	}, nil
}

// localCommands are the declared stdio MCP commands, honoured only on the local development stack.
func localCommands(s settings.Settings) map[string][]string {
	if s.Cloud != "local" {
		return map[string][]string{}
	}
	if len(s.Connections.MCPCommands) > 0 {
		log.Printf("local stdio MCP commands declared: %d", len(s.Connections.MCPCommands))
	}
	return s.Connections.MCPCommands
}

// sandboxedServer starts an installed package's program as a stdio MCP server in a sandboxed session on the computer,
// with the package's approved hosts as its egress allowlist (no network when it declares none).
func sandboxedServer(ctx context.Context, provider computer.Provider, packages *plugins.Service, installationID string) (io.ReadWriteCloser, error) {
	program, err := packages.Program(ctx, installationID)
	if err != nil {
		return nil, err
	}
	run := "mcp-" + installationID
	client, err := computer.Open(ctx, provider, run)
	if err != nil {
		return nil, err
	}
	client.SetTaskID(run)
	req := rpc.SandboxRequest{Argv: program.Argv, Files: program.Files, MemoryMB: program.MemoryMB, Pids: 64, CPUPct: 50, WallMS: 600_000, Network: "none"}
	if len(program.AllowHosts) > 0 {
		req.Network, req.AllowHosts = "allowlist", program.AllowHosts
	}
	stream, err := client.SandboxSession(ctx, req)
	if err != nil {
		client.Close()
		return nil, err
	}
	return sessionStream{ReadWriteCloser: stream, client: client}, nil
}

// sessionStream closes the computer client with the session.
type sessionStream struct {
	io.ReadWriteCloser
	client *computer.Client
}

func (s sessionStream) Close() error {
	err := s.ReadWriteCloser.Close()
	s.client.Close()
	return err
}
