package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"nori/internal/extensions"
	"nori/internal/secret"
	"nori/internal/state"
)

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(clone)
}

func (s *Service) connect(ctx context.Context, server stored) (*sdk.ClientSession, error) {
	return s.connectWith(ctx, server, nil)
}

func (s *Service) connectWith(ctx context.Context, server stored, elicit func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error)) (*sdk.ClientSession, error) {
	var options *sdk.ClientOptions
	if elicit != nil {
		options = &sdk.ClientOptions{ElicitationHandler: elicit}
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "nori", Version: Version}, options)
	switch server.Transport {
	case "http":
		base := s.outboundTransport()
		httpClient := s.newHTTPClient(60 * time.Second)
		if server.Auth == "configured" {
			if s.Credentials == nil {
				return nil, secret.ErrCredentialUnavailable
			}
			credential, err := s.Credentials.Get(ctx, "mcp-"+server.ID)
			if err != nil {
				return nil, fmt.Errorf("the server's credential is not available: %w", err)
			}
			token, err := configuredToken(server, credential.Data)
			if err != nil {
				return nil, err
			}
			httpClient.Transport = bearer{token: token, base: base}
		}
		if server.Auth == "oauth" {
			transport, err := s.oauthTransport(ctx, server, base)
			if err != nil {
				return nil, err
			}
			httpClient.Transport = transport
		}
		return client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.Endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: 1}, nil)
	case "stdio":
		if !s.LocalProcesses {
			return nil, ErrUnsupported
		}
		argv := s.Commands[server.CommandRef]
		if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
			return nil, fmt.Errorf("%w: stdio command %q is not configured", ErrUnsupported, server.CommandRef)
		}
		return client.Connect(ctx, &sdk.CommandTransport{Command: localCommand(ctx, argv)}, nil)
	}
	return nil, ErrUnsupported
}

var slug = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func modelName(server, tool string) string {
	name := "mcp_" + strings.ToLower(slug.ReplaceAllString(server, "_")) + "__" + slug.ReplaceAllString(tool, "_")
	if len(name) <= 64 {
		return name
	}
	sum := sha256.Sum256([]byte(server + "/" + tool))
	return name[:57] + "_" + hex.EncodeToString(sum[:3])
}

func toolDefinition(server stored, tool *sdk.Tool) (extensions.Definition, bool, error) {
	schema, err := json.Marshal(tool.InputSchema)
	if err != nil || string(schema) == "null" {
		schema = []byte(`{"type":"object"}`)
	}
	var output json.RawMessage
	if tool.OutputSchema != nil {
		output, _ = json.Marshal(tool.OutputSchema)
	}
	readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
	title := tool.Title
	if title == "" {
		title = tool.Name
	}
	description := clipRaw(tool.Description, 4000)
	exposure := server.Exposure.Default
	if override, ok := server.Exposure.Overrides[tool.Name]; ok {
		exposure = override
	}
	if exposure == "" {
		exposure = string(extensions.ExposureDiscoverable)
	}
	toolID := strings.ToLower(slug.ReplaceAllString(tool.Name, "-"))
	if len(toolID) > 60 {
		toolID = toolID[:60]
	}
	cleanToolID := strings.Trim(toolID, "-._")
	refID := "mcp/" + strings.ToLower(server.ID) + "/" + cleanToolID
	if strings.HasPrefix(tool.Name, "@") {
		refID = "mcp/" + strings.ToLower(server.ID) + "/protocol/" + cleanToolID
	}
	content, _ := json.Marshal([]any{title, description, schema, output, exposure, tool.Name})
	sum := sha256.Sum256(content)
	revision := Version + "-" + hex.EncodeToString(sum[:6])
	d, err := extensions.Seal(extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: refID, Version: revision},
		Extension:       extensions.DefinitionRef{ID: "mcp/" + strings.ToLower(server.ID), Version: Version},
		ModelName:       modelName(server.Name, tool.Name),
		Title:           title,
		Description:     description,
		Capability:      server.ID,
		CapabilityTitle: server.Name + " (MCP)",
		InputSchema:     schema,
		OutputSchema:    output,
		Binding:         extensions.Binding{Adapter: extensions.AdapterMCP, Target: server.ID + "/" + tool.Name, Version: Version},
		Policy:          extensions.Policy{Effect: extensions.EffectUnknown, Retry: "reconcile_only", TimeoutMS: 60_000, Location: extensions.LocationRuntime},
		Exposure:        extensions.Exposure(exposure),
	})
	return d, readOnly, err
}

func definitionKey(d extensions.Definition) string {
	return "def_" + strings.TrimPrefix(d.Ref.Digest, "sha256:")[:40]
}

func (s *Service) Discover(ctx context.Context, id string) (Server, error) {
	server, r, err := s.server(ctx, id)
	if err != nil {
		return Server{}, err
	}
	discover, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	now := s.now()
	observed := Observed{Status: "connected", Diagnostics: server.Observed.Diagnostics}
	diagnose := func(level, message string) {
		observed.Diagnostics = append(observed.Diagnostics, Diagnostic{At: now, Level: level, Message: clip(message, 1000)})
		if len(observed.Diagnostics) > 50 {
			observed.Diagnostics = observed.Diagnostics[len(observed.Diagnostics)-50:]
		}
	}
	session, err := s.connect(discover, server)
	tools := map[string]string{}
	var docs []definitionDoc
	if err != nil {
		observed.Status = "failed"
		if strings.Contains(err.Error(), "401") {
			observed.Status = "needs_sign_in"
		}
		observed.Error = &ErrorDetail{Code: "connect_failed", Message: clip(err.Error(), 500), Retryable: true}
		diagnose("error", "connect: "+err.Error())
	} else {
		defer session.Close()
		if init := session.InitializeResult(); init != nil {
			observed.ProtocolVersion = init.ProtocolVersion
			if init.Capabilities != nil {
				if init.Capabilities.Tools != nil {
					observed.Capabilities = append(observed.Capabilities, "tools")
				}
				if init.Capabilities.Resources != nil {
					observed.Capabilities = append(observed.Capabilities, "resources")
				}
				if init.Capabilities.Prompts != nil {
					observed.Capabilities = append(observed.Capabilities, "prompts")
				}
			}
		}
		for tool, err := range session.Tools(discover, nil) {
			if err != nil {
				diagnose("error", "tools/list: "+err.Error())
				observed.Status = "failed"
				break
			}
			if len(docs) >= maxDiscoveredTools {
				diagnose("warning", fmt.Sprintf("tools truncated at %d", maxDiscoveredTools))
				break
			}
			if strings.TrimSpace(tool.Name) == "" || strings.HasPrefix(tool.Name, "@") {
				diagnose("warning", fmt.Sprintf("tool %s not offered: reserved name", tool.Name))
				continue
			}
			d, readOnly, err := toolDefinition(server, tool)
			if err != nil {
				diagnose("warning", fmt.Sprintf("tool %s not offered: %v", tool.Name, err))
				continue
			}
			tools[tool.Name] = definitionKey(d)
			docs = append(docs, definitionDoc{Definition: d, ServerID: server.ID, ToolName: tool.Name, ReadOnly: readOnly})
		}
		if slices.Contains(observed.Capabilities, "resources") {
			for _, err := range session.Resources(discover, nil) {
				if err != nil {
					break
				}
				if observed.ResourceCount >= maxDiscoveredResources {
					diagnose("warning", fmt.Sprintf("resources truncated at %d", maxDiscoveredResources))
					break
				}
				observed.ResourceCount++
			}
		}
		if slices.Contains(observed.Capabilities, "prompts") {
			for _, err := range session.Prompts(discover, nil) {
				if err != nil {
					break
				}
				if observed.PromptCount >= maxDiscoveredPrompts {
					diagnose("warning", fmt.Sprintf("prompts truncated at %d", maxDiscoveredPrompts))
					break
				}
				observed.PromptCount++
			}
		}
		for _, tool := range protocolTools(observed.Capabilities) {
			d, _, err := toolDefinition(server, tool)
			if err == nil {
				d.Policy.Effect, d.Policy.Retry, d.Ref.Digest = extensions.EffectRead, "safe", ""
				d, err = extensions.Seal(d)
			}
			if err != nil {
				diagnose("warning", fmt.Sprintf("%s not offered: %v", tool.Name, err))
				continue
			}
			tools[tool.Name] = definitionKey(d)
			docs = append(docs, definitionDoc{Definition: d, ServerID: server.ID, ToolName: tool.Name, ReadOnly: true})
		}
		if init := session.InitializeResult(); init != nil && init.Capabilities != nil {
			if init.Capabilities.Tools != nil && init.Capabilities.Tools.ListChanged ||
				init.Capabilities.Resources != nil && (init.Capabilities.Resources.ListChanged || init.Capabilities.Resources.Subscribe) ||
				init.Capabilities.Prompts != nil && init.Capabilities.Prompts.ListChanged {
				diagnose("info", "The server sends list-change and subscription notifications; Nori opens a session per call, so it does not receive them. Reconnect re-discovers tools, resources and prompts.")
			}
		}
		diagnose("info", "Not offered to servers: sampling and roots. Elicitation during a call is answered cancel and shown to the task so it can ask the user.")
		if observed.Status == "connected" {
			observed.LastSuccessAt = &now
		}
		diagnose("info", fmt.Sprintf("discovered %d tools", len(tools)))
	}
	for _, doc := range docs {
		resource, err := state.NewResource(CollectionDefinitions, definitionKey(doc.Definition), 0, doc, map[string]string{"server_id": server.ID}, now)
		if err != nil {
			return Server{}, err
		}
		if _, err = s.Store.PutResource(ctx, resource, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
			return Server{}, err
		}
	}
	observed.ToolCount = len(tools)
	sum := sha256.Sum256(fmt.Append(nil, tools))
	observed.DiscoveryRevision = hex.EncodeToString(sum[:8])
	if observed.Status == "connected" {
		server.Tools = tools
	}
	server.Observed = observed
	next, err := state.NewResource(CollectionServers, id, 0, server, nil, now)
	if err != nil {
		return Server{}, err
	}
	saved, err := s.Store.PutResource(ctx, next, r.Revision)
	if err != nil {
		return Server{}, err
	}
	server.Revision = saved.Revision
	return server.Server, nil
}

func clipRaw(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := 0
	for i := range text {
		if i > limit {
			break
		}
		cut = i
	}
	return text[:cut]
}

func clip(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	return clipRaw(text, limit) + "…"
}
