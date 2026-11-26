package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/extensions"
	"nori/internal/secret"
	"nori/internal/state"
)

const (
	CollectionServers       = "mcp_servers"
	CollectionDefinitions   = "mcp_definitions"
	Version                 = "1"
	maxDiscoveredTools      = 200
	maxDiscoveredResources  = 200
	maxDiscoveredTemplates  = 100
	maxDiscoveredPrompts    = 200
	maxElicitations         = 16
	maxOtherContent         = 50
	maxStructuredContentLen = 32 << 10
	maxPromptMessages       = 50
	maxResourceContents     = 50
	maxPromptArguments      = 50
)

type Exposure struct {
	Default   string            `json:"default,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"`
}

type Editable struct {
	Name            string   `json:"name"`
	Transport       string   `json:"transport"`
	Endpoint        string   `json:"endpoint,omitempty"`
	CommandRef      string   `json:"command_ref,omitempty"`
	EnvironmentRefs []string `json:"environment_refs,omitempty"`
	Auth            string   `json:"auth"`
	Enabled         bool     `json:"enabled"`
	Exposure        Exposure `json:"exposure"`
}

type Action struct {
	Action   string `json:"action"`
	Tool     string `json:"tool,omitempty"`
	Exposure string `json:"exposure,omitempty"`
	ReturnTo string `json:"return_to,omitempty"`
}

func validExposure(v string) bool {
	switch v {
	case "direct", "discoverable", "programmatic", "disabled":
		return true
	}
	return false
}

func (a Action) Valid() bool {
	switch a.Action {
	case "connect", "reconnect", "sign_out", "start", "stop", "restart":
		return a.Tool == "" && a.Exposure == "" && a.ReturnTo == ""
	case "sign_in":
		return a.Tool == "" && a.Exposure == "" && (a.ReturnTo == "" || validReturnTo(a.ReturnTo))
	case "set_exposure":
		return validExposure(a.Exposure) && strings.TrimSpace(a.Tool) != "" && a.ReturnTo == ""
	}
	return false
}

type CredentialInput struct {
	RequestID string `json:"request_id"`
	Token     string `json:"token"`
}

type Diagnostic struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Observed struct {
	Status            string       `json:"status"`
	ProtocolVersion   string       `json:"protocol_version,omitempty"`
	Capabilities      []string     `json:"capabilities,omitempty"`
	DiscoveryRevision string       `json:"discovery_revision,omitempty"`
	ToolCount         int          `json:"tool_count"`
	ResourceCount     int          `json:"resource_count"`
	PromptCount       int          `json:"prompt_count"`
	LastSuccessAt     *time.Time   `json:"last_success_at,omitempty"`
	Error             *ErrorDetail `json:"error,omitempty"`
	Diagnostics       []Diagnostic `json:"diagnostics,omitempty"`
	Credential        string       `json:"credential,omitempty"`
}

type Source struct {
	Kind string `json:"kind"`
}

type Server struct {
	ID           string   `json:"id"`
	Revision     int64    `json:"revision"`
	Source       Source   `json:"source"`
	Name         string   `json:"name"`
	Transport    string   `json:"transport"`
	Endpoint     string   `json:"endpoint,omitempty"`
	ConnectionID string   `json:"connection_id,omitempty"`
	Enabled      bool     `json:"enabled"`
	Exposure     Exposure `json:"exposure"`
	Observed     Observed `json:"observed"`
	Auth         string   `json:"auth,omitempty"`
}

type stored struct {
	Server
	CommandRef      string            `json:"command_ref,omitempty"`
	CredentialEpoch string            `json:"credential_epoch,omitempty"`
	Tools           map[string]string `json:"tools,omitempty"`
}

type Tool struct {
	Name         string                   `json:"name"`
	Title        string                   `json:"title"`
	Exposure     string                   `json:"exposure"`
	Definition   extensions.DefinitionRef `json:"definition"`
	Availability string                   `json:"availability"`
	ReadOnlyHint bool                     `json:"read_only_hint,omitempty"`
}

type definitionDoc struct {
	Definition extensions.Definition `json:"definition"`
	ServerID   string                `json:"server_id"`
	ToolName   string                `json:"tool_name"`
	ReadOnly   bool                  `json:"read_only_hint,omitempty"`
}

type Service struct {
	Store          state.ResourceStore
	Credentials    secret.CredentialStore
	LocalProcesses bool
	Commands       map[string][]string
	Now            func() time.Time
	Client         *http.Client
	RedirectURL    string
	OnSignedIn     func(ctx context.Context, serverID string)

	transportOnce sync.Once
	transport     http.RoundTripper
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

var (
	ErrUnsupported = errors.New("this MCP server setting is not supported in this deployment")
)

func decode(r state.Resource) (stored, error) {
	var server stored
	if err := r.Decode(&server); err != nil {
		return server, err
	}
	server.Revision = r.Revision
	return server, nil
}

func (s *Service) List(ctx context.Context) ([]Server, error) {
	page, _, err := s.Store.Resources(ctx, CollectionServers, nil, "", 100)
	if err != nil {
		return nil, err
	}
	out := []Server{}
	for _, r := range page {
		server, err := decode(r)
		if err != nil {
			return nil, err
		}
		out = append(out, s.project(ctx, server))
	}
	return out, nil
}

func (s *Service) server(ctx context.Context, id string) (stored, state.Resource, error) {
	r, err := s.Store.Resource(ctx, CollectionServers, id)
	if err != nil {
		return stored{}, r, err
	}
	server, err := decode(r)
	return server, r, err
}

func (s *Service) Get(ctx context.Context, id string) (Server, error) {
	server, _, err := s.server(ctx, id)
	if err != nil {
		return Server{}, err
	}
	return s.project(ctx, server), nil
}

func (s *Service) project(ctx context.Context, server stored) Server {
	p := server.Server
	if p.Auth == "configured" || p.Auth == "oauth" {
		p.Observed.Credential = "missing"
		if s.Credentials != nil {
			if credential, err := s.Credentials.Get(ctx, "mcp-"+server.ID); err == nil && credentialMatches(server, credential.Data) {
				p.Observed.Credential = "present"
			}
		}
	}
	return p
}

func (s *Service) validEndpoint(raw string) error {
	return validateRemoteURL(raw, s.LocalProcesses)
}

func (s *Service) validate(value Editable) error {
	if strings.TrimSpace(value.Name) == "" || len(value.Name) > 100 {
		return fmt.Errorf("%w: a server needs a name", state.ErrInvalidResource)
	}
	switch value.Transport {
	case "http":
		if err := s.validEndpoint(value.Endpoint); err != nil {
			return err
		}
	case "stdio":
		argv, ok := s.Commands[value.CommandRef]
		if !ok || len(argv) == 0 || strings.TrimSpace(argv[0]) == "" || !s.LocalProcesses {
			return fmt.Errorf("%w: managed stdio servers need a declared command and a managed process host", ErrUnsupported)
		}
	default:
		return state.ErrInvalidResource
	}
	switch value.Auth {
	case "none", "configured", "oauth":
	default:
		return state.ErrInvalidResource
	}
	if value.Exposure.Default != "" && !validExposure(value.Exposure.Default) {
		return fmt.Errorf("%w: invalid default exposure", state.ErrInvalidResource)
	}
	for tool, exp := range value.Exposure.Overrides {
		if strings.TrimSpace(tool) == "" || !validExposure(exp) {
			return fmt.Errorf("%w: invalid exposure override", state.ErrInvalidResource)
		}
	}
	return nil
}

func (s *Service) Save(ctx context.Context, id, requestID string, expected int64, value Editable) (state.MutationReceipt, error) {
	if err := s.validate(value); err != nil {
		return state.MutationReceipt{}, err
	}
	method, target, status := "PUT", "/v1/mcp/"+id, 200
	if id == "" {
		id = "mcp_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("mcp:"+requestID)).String(), "-", "")[:16]
		method, target, status = "POST", "/v1/mcp", 201
	}
	var previousCredential secret.Credential
	if id != "" && s.Credentials != nil {
		previousCredential, _ = s.Credentials.Get(ctx, "mcp-"+id)
	}
	var endpointChanged bool
	nextCredentialEpoch := uuid.NewString()
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: method, Target: target, ExpectedRevision: expected, Payload: value, Collection: CollectionServers, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			endpointChanged = false
			server := stored{Server: Server{ID: id, Source: Source{Kind: "user"}, Observed: Observed{Status: "disconnected"}}}
			revision := int64(1)
			if current != nil {
				var err error
				if server, err = decode(*current); err != nil {
					return state.MutationResult{}, err
				}
				revision = current.Revision + 1
				if server.Endpoint != value.Endpoint || server.Transport != value.Transport || server.Auth != value.Auth {
					endpointChanged = true
					server.CredentialEpoch = nextCredentialEpoch
					server.Tools, server.Observed = nil, Observed{Status: "disconnected"}
				}
			}
			server.Name, server.Transport, server.Endpoint, server.Enabled, server.Exposure = value.Name, value.Transport, value.Endpoint, value.Enabled, value.Exposure
			server.Auth, server.CommandRef = value.Auth, value.CommandRef
			if !value.Enabled {
				server.Observed.Status = "disabled"
			}
			return s.write(server, revision, status)
		},
	})
	if err != nil {
		return state.MutationReceipt{}, err
	}
	if endpointChanged && previousCredential.Version > 0 {
		if delErr := s.deleteCredentialVersion(ctx, id, previousCredential); delErr != nil {
			return state.MutationReceipt{}, delErr
		}
	}
	return receipt, nil
}

func (s *Service) write(server stored, revision int64, status int) (state.MutationResult, error) {
	server.Revision = revision
	resource, err := state.NewResource(CollectionServers, server.ID, revision, server, nil, s.now())
	if err != nil {
		return state.MutationResult{}, err
	}
	data, _ := json.Marshal(server.Server)
	return state.MutationResult{Writes: []state.Resource{resource}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: status}}, nil
}

func (s *Service) SetCredential(ctx context.Context, id, token string) error {
	server, _, err := s.server(ctx, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" || len(token) > 8192 {
		return state.ErrInvalidResource
	}
	key := "mcp-" + id
	if s.Credentials == nil {
		return secret.ErrCredentialUnavailable
	}
	server, err = s.advanceCredentialEpoch(ctx, server)
	if err != nil {
		return err
	}
	current, err := s.Credentials.Get(ctx, key)
	version := current.Version
	if errors.Is(err, secret.ErrCredentialNotFound) {
		version = 0
	} else if err != nil {
		return err
	}
	encoded, err := encodeConfiguredCredential(server.CredentialEpoch, token)
	if err != nil {
		return err
	}
	latest, _, err := s.server(ctx, id)
	if err != nil {
		return err
	}
	if latest.CredentialEpoch != server.CredentialEpoch {
		return state.ErrRevisionConflict
	}
	_, err = s.Credentials.Replace(ctx, key, version, encoded)
	return err
}

func (s *Service) Act(ctx context.Context, id, requestID string, expected int64, action, tool, exposure string, returnTo ...string) (state.MutationReceipt, error) {
	switch action {
	case "connect", "reconnect":
		if expected > 0 {
			current, _, err := s.server(ctx, id)
			if err != nil {
				return state.MutationReceipt{}, err
			}
			if current.Revision != expected {
				return state.MutationReceipt{}, state.ErrRevisionConflict
			}
		}
		if _, err := s.Discover(ctx, id); err != nil {
			return state.MutationReceipt{}, err
		}
		server, _, err := s.server(ctx, id)
		if err != nil {
			return state.MutationReceipt{}, err
		}
		data, _ := json.Marshal(s.project(ctx, server))
		return state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "applied", Resource: data, HTTPStatus: 200}, nil
	case "set_exposure":
		if !validExposure(exposure) || strings.TrimSpace(tool) == "" {
			return state.MutationReceipt{}, state.ErrInvalidResource
		}
		return s.Store.Mutate(ctx, state.Mutation{
			RequestID: requestID, Method: "POST", Target: "/v1/mcp/" + id + "/actions", ExpectedRevision: expected,
			Payload: []string{action, tool, exposure}, Collection: CollectionServers, ID: id,
			Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
				if current == nil {
					return state.MutationResult{}, state.ErrNotFound
				}
				server, err := decode(*current)
				if err != nil {
					return state.MutationResult{}, err
				}
				if server.Exposure.Overrides == nil {
					server.Exposure.Overrides = map[string]string{}
				}
				server.Exposure.Overrides[tool] = exposure
				return s.write(server, current.Revision+1, 200)
			},
		})
	case "sign_in":
		if expected > 0 {
			current, _, err := s.server(ctx, id)
			if err != nil {
				return state.MutationReceipt{}, err
			}
			if current.Revision != expected {
				return state.MutationReceipt{}, state.ErrRevisionConflict
			}
		}
		target := ""
		if len(returnTo) > 0 {
			target = returnTo[0]
		}
		attempt, err := s.SignIn(ctx, id, target)
		if err != nil {
			return state.MutationReceipt{}, err
		}
		server, _, err := s.server(ctx, id)
		if err != nil {
			return state.MutationReceipt{}, err
		}
		data, _ := json.Marshal(s.project(ctx, server))
		auth, _ := json.Marshal(attempt)
		return state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "queued", Resource: data, Auth: auth, HTTPStatus: 200}, nil
	case "sign_out":
		if expected > 0 {
			current, _, err := s.server(ctx, id)
			if err != nil {
				return state.MutationReceipt{}, err
			}
			if current.Revision != expected {
				return state.MutationReceipt{}, state.ErrRevisionConflict
			}
		}
		if err := s.SignOut(ctx, id); err != nil {
			return state.MutationReceipt{}, err
		}
		server, _, err := s.server(ctx, id)
		if err != nil {
			return state.MutationReceipt{}, err
		}
		data, _ := json.Marshal(s.project(ctx, server))
		return state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "applied", Resource: data, HTTPStatus: 200}, nil
	case "start", "stop", "restart":
		return state.MutationReceipt{}, fmt.Errorf("%w: managed processes are started per call on the local stack", ErrUnsupported)
	}
	return state.MutationReceipt{}, state.ErrInvalidResource
}

func (s *Service) Tools(ctx context.Context, id string) ([]Tool, error) {
	server, _, err := s.server(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []Tool{}
	for name, key := range server.Tools {
		r, err := s.Store.Resource(ctx, CollectionDefinitions, key)
		if err != nil {
			continue
		}
		var doc definitionDoc
		if r.Decode(&doc) != nil {
			continue
		}
		exposure := string(doc.Definition.Exposure)
		if override, ok := server.Exposure.Overrides[name]; ok {
			exposure = override
		}
		availability := "ready"
		if !server.Enabled || exposure == "disabled" {
			availability = "disabled"
		}
		out = append(out, Tool{Name: name, Title: doc.Definition.Title, Exposure: exposure, Definition: doc.Definition.Ref, Availability: availability, ReadOnlyHint: doc.ReadOnly})
	}
	return out, nil
}

func (s *Service) Inventory(ctx context.Context) (extensions.Inventory, error) {
	servers, err := s.List(ctx)
	if err != nil {
		return extensions.Inventory{}, err
	}
	var inventory extensions.Inventory
	for _, summary := range servers {
		server, _, err := s.server(ctx, summary.ID)
		if err != nil {
			continue
		}
		state := extensions.CapabilityState{Capability: server.ID, Title: server.Name + " (MCP)", Account: server.Endpoint, Availability: server.Observed.Status}
		if server.Enabled && server.Observed.Status == "connected" {
			state.Ready = true
			for name, key := range server.Tools {
				r, err := s.Store.Resource(ctx, CollectionDefinitions, key)
				if err != nil {
					continue
				}
				var doc definitionDoc
				if r.Decode(&doc) != nil {
					continue
				}
				if override, ok := server.Exposure.Overrides[name]; ok {
					doc.Definition.Exposure = extensions.Exposure(override)
				}
				inventory.Definitions = append(inventory.Definitions, doc.Definition)
			}
		}
		inventory.Capabilities = append(inventory.Capabilities, state)
	}
	return inventory, nil
}
