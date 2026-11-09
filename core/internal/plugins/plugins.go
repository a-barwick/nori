// Package plugins installs versioned packages that contribute skills, prompts and MCP server configurations, and
// manages user skills. Installing grants no action authority: tools a package brings still go through Permission
// checks, and access a package requests must be approved explicitly at install and again when an update asks for
// more.
//
// Executable packages (tool contributions with their own code, executor kind "isolated") need the Computer
// runtime's isolated execution, which does not exist yet; such packages are rejected with a visible reason rather
// than run without isolation.
package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/connectors/mcp"
	"nori/internal/extensions"
	"nori/internal/state"
)

const (
	CollectionPackages      = "plugin_packages"
	CollectionInstallations = "plugin_installations"
	CollectionSkills        = "skills"
)

var ErrUnsupported = errors.New("this package needs isolated execution, which is not available yet")

type Ref struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type Access struct {
	ConnectionTypes []string `json:"connection_types"`
	ActionClasses   []string `json:"action_classes"`
	Network         []string `json:"network,omitempty"`
	Filesystem      []string `json:"filesystem,omitempty"`
}

type Skill struct {
	Ref             Ref            `json:"ref"`
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	Instructions    string         `json:"instructions"`
	Inputs          map[string]any `json:"inputs"`
	RequiredToolIDs []string       `json:"required_tool_ids"`
	Examples        []string       `json:"examples,omitempty"`
	Scope           string         `json:"scope,omitempty"`
}

type Contribution struct {
	Kind   string             `json:"kind"`
	Tool   json.RawMessage    `json:"tool,omitempty"`
	Skill  *Skill             `json:"skill,omitempty"`
	Name   string             `json:"name,omitempty"`
	Text   string             `json:"text,omitempty"`
	Server *mcpServerTemplate `json:"server,omitempty"`
	// Hook, Handler and DeadlineMS describe a hook contribution: the program receives {"hook", "handler", ...}.
	Hook       string         `json:"hook,omitempty"`
	Handler    string         `json:"handler,omitempty"`
	DeadlineMS int            `json:"deadline_ms,omitempty"`
	Extra      map[string]any `json:"-"`
}

type mcpServerTemplate struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Endpoint  string `json:"endpoint,omitempty"`
	Auth      string `json:"auth,omitempty"`
}

// Manifest is the contract PackageManifest.
type Manifest struct {
	SchemaVersion   int            `json:"schema_version"`
	Package         Ref            `json:"package"`
	Publisher       map[string]any `json:"publisher"`
	Title           string         `json:"title,omitempty"`
	Description     string         `json:"description,omitempty"`
	Categories      []string       `json:"categories,omitempty"`
	ContractVersion string         `json:"contract_version"`
	Dependencies    []struct {
		ID           string `json:"id"`
		VersionRange string `json:"version_range"`
	} `json:"dependencies,omitempty"`
	Contributions   []Contribution `json:"contributions"`
	RequestedAccess Access         `json:"requested_access"`
	Executor        *Executor      `json:"executor,omitempty"`
	// Files holds an executable package's program text (isolated executor only), bounded in size.
	Files     map[string]string `json:"files,omitempty"`
	Uninstall string            `json:"uninstall,omitempty"`
}

// Executor declares how a package's tools run. Only "isolated" runs code, in the Computer runtime's sandbox.
type Executor struct {
	Kind    string `json:"kind"`
	Runtime string `json:"runtime,omitempty"`
	Entry   string `json:"entry,omitempty"`
	Limits  *struct {
		MemoryMB int `json:"memory_mb,omitempty"`
		CPUMS    int `json:"cpu_ms,omitempty"`
		WallMS   int `json:"wall_ms,omitempty"`
	} `json:"limits,omitempty"`
}

var runtimes = map[string][]string{"python3": {"python3"}, "node": {"node"}, "sh": {"/bin/sh"}}

type packageDoc struct {
	Manifest Manifest `json:"manifest"`
	Source   string   `json:"source"`
	Verified bool     `json:"verified"`
}

var packageID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$`)

// Validate checks a manifest before anything is activated and returns its content digest.
func Validate(m Manifest) (string, error) {
	if m.SchemaVersion < 1 || !packageID.MatchString(m.Package.ID) || m.Package.Version == "" || m.Publisher["id"] == nil || m.ContractVersion == "" {
		return "", fmt.Errorf("%w: the manifest needs a schema version, a publisher/package id, a version, a publisher and a contract version", state.ErrInvalidResource)
	}
	isolated := m.Executor != nil && m.Executor.Kind == "isolated"
	if isolated {
		size := 0
		for name, content := range m.Files {
			size += len(name) + len(content)
		}
		if _, ok := runtimes[m.Executor.Runtime]; !ok || m.Files[m.Executor.Entry] == "" || size > 256<<10 || m.Executor.Limits == nil || m.Executor.Limits.WallMS <= 0 {
			return "", fmt.Errorf("%w: an executable package needs a runtime (python3, node, sh), its entry file, limits and at most 256 KiB of files", state.ErrInvalidResource)
		}
		// The sandbox enforces no network; a package that asks for hosts cannot be honestly constrained yet.
		if len(m.RequestedAccess.Network) > 0 {
			return "", fmt.Errorf("%w: sandboxed packages run without network; host allowlists are not enforceable yet", ErrUnsupported)
		}
	}
	for _, c := range m.Contributions {
		switch c.Kind {
		case "skill":
			if c.Skill == nil || strings.TrimSpace(c.Skill.Instructions) == "" || c.Skill.Title == "" {
				return "", fmt.Errorf("%w: a skill needs a title and instructions", state.ErrInvalidResource)
			}
		case "prompt":
			if c.Name == "" || c.Text == "" {
				return "", fmt.Errorf("%w: a prompt needs a name and text", state.ErrInvalidResource)
			}
		case "mcp_server":
			if c.Server == nil || c.Server.Transport != "http" || c.Server.Endpoint == "" {
				return "", fmt.Errorf("%w: package MCP servers must be remote HTTP servers", ErrUnsupported)
			}
		case "tool":
			var d extensions.Definition
			if err := json.Unmarshal(c.Tool, &d); err != nil {
				return "", fmt.Errorf("%w: invalid tool contribution", state.ErrInvalidResource)
			}
			if extensions.Reserved[d.ModelName] || d.Policy.Effect == extensions.EffectControl {
				return "", fmt.Errorf("%w: a package cannot claim %s or control effects", state.ErrInvalidResource, d.ModelName)
			}
			if !isolated {
				return "", ErrUnsupported
			}
			if !extensions.ValidModelName(d.ModelName) || len(d.InputSchema) == 0 {
				return "", fmt.Errorf("%w: a package tool needs a valid model name and an input schema", state.ErrInvalidResource)
			}
		case "hook":
			if !isolated {
				return "", fmt.Errorf("%w: package hooks need isolated execution", ErrUnsupported)
			}
			// Context, progress and lifecycle hooks would start the sandbox for every task; not offered yet.
			if c.Hook != HookPreInvocation && c.Hook != HookResult {
				return "", fmt.Errorf("%w: only pre_invocation and result hooks run today", ErrUnsupported)
			}
			if c.Handler == "" || c.DeadlineMS < 0 {
				return "", fmt.Errorf("%w: a hook needs a handler name", state.ErrInvalidResource)
			}
		default:
			return "", fmt.Errorf("%w: unknown contribution %s", state.ErrInvalidResource, c.Kind)
		}
	}
	for _, class := range m.RequestedAccess.ActionClasses {
		if class == "control" {
			return "", fmt.Errorf("%w: a package cannot request control authority", state.ErrInvalidResource)
		}
	}
	data, _ := json.Marshal([]any{m.Contributions, m.Executor, m.Files})
	sum := sha256.Sum256(append(data, []byte(m.Package.ID+"@"+m.Package.Version)...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Service manages one user's packages, installations and skills.
type Service struct {
	Store   state.ResourceStore
	MCP     *mcp.Service
	Builtin []Manifest
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func packageKey(id, version string) string {
	return "pkg_" + strings.NewReplacer("/", "__", ".", "_").Replace(id) + "_" + strings.ReplaceAll(version, ".", "_")
}

// packages lists every known package version: built-ins (registry, verified) and uploads (unverified).
func (s *Service) packages(ctx context.Context) ([]packageDoc, error) {
	out := []packageDoc{}
	for _, m := range s.Builtin {
		out = append(out, packageDoc{Manifest: m, Source: "registry", Verified: true})
	}
	page, _, err := s.Store.Resources(ctx, CollectionPackages, nil, "", 200)
	if err != nil {
		return nil, err
	}
	for _, r := range page {
		var doc packageDoc
		if r.Decode(&doc) == nil {
			out = append(out, doc)
		}
	}
	return out, nil
}

func (s *Service) find(ctx context.Context, id, version string) (packageDoc, error) {
	all, err := s.packages(ctx)
	if err != nil {
		return packageDoc{}, err
	}
	for _, doc := range all {
		if doc.Manifest.Package.ID == id && doc.Manifest.Package.Version == version {
			return doc, nil
		}
	}
	return packageDoc{}, state.ErrNotFound
}

// Upload records an uploaded manifest as an unverified package version. Versions are immutable.
func (s *Service) Upload(ctx context.Context, m Manifest) (Manifest, error) {
	digest, err := Validate(m)
	if err != nil {
		return m, err
	}
	m.Package.Digest = digest
	r, err := state.NewResource(CollectionPackages, packageKey(m.Package.ID, m.Package.Version), 0, packageDoc{Manifest: m, Source: "upload"}, nil, s.now())
	if err != nil {
		return m, err
	}
	if _, err = s.Store.PutResource(ctx, r, 0); errors.Is(err, state.ErrRevisionConflict) {
		existing, findErr := s.find(ctx, m.Package.ID, m.Package.Version)
		if findErr == nil && existing.Manifest.Package.Digest != digest {
			return m, fmt.Errorf("%w: this version was already uploaded with different contents", state.ErrInvalidResource)
		}
		return m, nil
	}
	return m, err
}

// CatalogEntry is the contract CatalogEntry.
type CatalogEntry struct {
	Package              Ref            `json:"package"`
	Title                string         `json:"title"`
	Description          string         `json:"description"`
	Publisher            map[string]any `json:"publisher"`
	Categories           []string       `json:"categories"`
	LatestVersion        string         `json:"latest_version"`
	Provenance           map[string]any `json:"provenance"`
	RequestedAccess      Access         `json:"requested_access"`
	ContributionsSummary map[string]int `json:"contributions_summary"`
	InstallationID       string         `json:"installation_id,omitempty"`
}

func summarize(m Manifest) map[string]int {
	summary := map[string]int{"tools": 0, "skills": 0, "prompts": 0, "mcp_servers": 0}
	for _, c := range m.Contributions {
		switch c.Kind {
		case "tool":
			summary["tools"]++
		case "skill":
			summary["skills"]++
		case "prompt":
			summary["prompts"]++
		case "mcp_server":
			summary["mcp_servers"]++
		}
	}
	return summary
}

func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			var x, y int
			fmt.Sscan(pa[i], &x)
			fmt.Sscan(pb[i], &y)
			return x > y
		}
	}
	return len(pa) > len(pb)
}

// Catalog lists the latest version of each package with whether this user installed it.
func (s *Service) Catalog(ctx context.Context, query string) ([]CatalogEntry, error) {
	all, err := s.packages(ctx)
	if err != nil {
		return nil, err
	}
	latest := map[string]packageDoc{}
	for _, doc := range all {
		id := doc.Manifest.Package.ID
		if cur, ok := latest[id]; !ok || newer(doc.Manifest.Package.Version, cur.Manifest.Package.Version) {
			latest[id] = doc
		}
	}
	installs, err := s.Installations(ctx)
	if err != nil {
		return nil, err
	}
	out := []CatalogEntry{}
	query = strings.ToLower(query)
	for id, doc := range latest {
		m := doc.Manifest
		if query != "" && !strings.Contains(strings.ToLower(m.Title+" "+m.Description+" "+strings.Join(m.Categories, " ")), query) {
			continue
		}
		digest, _ := Validate(m)
		entry := CatalogEntry{Package: Ref{ID: id, Version: m.Package.Version, Digest: digest}, Title: m.Title, Description: m.Description,
			Publisher: m.Publisher, Categories: nonNil(m.Categories), LatestVersion: m.Package.Version,
			Provenance: map[string]any{"source": doc.Source, "verified": doc.Verified}, RequestedAccess: m.RequestedAccess, ContributionsSummary: summarize(m)}
		for _, inst := range installs {
			if inst.Package.ID == id && inst.State != "removing" {
				entry.InstallationID = inst.ID
			}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

type HistoryEntry struct {
	At      time.Time `json:"at"`
	Event   string    `json:"event"`
	Version string    `json:"version,omitempty"`
}

// Installation is the contract Installation.
type Installation struct {
	ID                      string         `json:"id"`
	Revision                int64          `json:"revision"`
	Package                 Ref            `json:"package"`
	Pinned                  bool           `json:"pinned,omitempty"`
	AvailableVersion        string         `json:"available_version,omitempty"`
	Enabled                 bool           `json:"enabled"`
	State                   string         `json:"state"`
	ConfigurationRevision   int64          `json:"configuration_revision"`
	ApprovedAccess          Access         `json:"approved_access"`
	RequiredConnectionTypes []string       `json:"required_connection_types,omitempty"`
	Diagnostics             []string       `json:"diagnostics,omitempty"`
	History                 []HistoryEntry `json:"history,omitempty"`
	CreatedAt               *time.Time     `json:"created_at,omitempty"`
	UpdatedAt               *time.Time     `json:"updated_at,omitempty"`
}

type installationDoc struct {
	Installation
	Configuration map[string]any `json:"configuration,omitempty"`
	ServerIDs     []string       `json:"server_ids,omitempty"`
	Previous      string         `json:"previous_version,omitempty"`
}

func (s *Service) Installations(ctx context.Context) ([]Installation, error) {
	page, _, err := s.Store.Resources(ctx, CollectionInstallations, nil, "", 200)
	if err != nil {
		return nil, err
	}
	out := []Installation{}
	for _, r := range page {
		inst, err := s.decodeInstallation(ctx, r)
		if err == nil {
			out = append(out, inst.Installation)
		}
	}
	return out, nil
}

func (s *Service) decodeInstallation(ctx context.Context, r state.Resource) (installationDoc, error) {
	var doc installationDoc
	if err := r.Decode(&doc); err != nil {
		return doc, err
	}
	doc.Revision = r.Revision
	// Computed on every read: a value saved before an update is stale.
	doc.AvailableVersion = ""
	if all, err := s.packages(ctx); err == nil {
		for _, p := range all {
			if p.Manifest.Package.ID == doc.Package.ID && newer(p.Manifest.Package.Version, doc.Package.Version) &&
				(doc.AvailableVersion == "" || newer(p.Manifest.Package.Version, doc.AvailableVersion)) {
				doc.AvailableVersion = p.Manifest.Package.Version
			}
		}
	}
	return doc, nil
}

func (s *Service) Installation(ctx context.Context, id string) (Installation, error) {
	r, err := s.Store.Resource(ctx, CollectionInstallations, id)
	if err != nil {
		return Installation{}, err
	}
	doc, err := s.decodeInstallation(ctx, r)
	return doc.Installation, err
}

func covers(approved, requested Access) []string {
	var missing []string
	for _, t := range requested.ConnectionTypes {
		if !slices.Contains(approved.ConnectionTypes, t) {
			missing = append(missing, "connection:"+t)
		}
	}
	for _, c := range requested.ActionClasses {
		if !slices.Contains(approved.ActionClasses, c) {
			missing = append(missing, "action:"+c)
		}
	}
	for _, h := range requested.Network {
		if !slices.Contains(approved.Network, h) {
			missing = append(missing, "network:"+h)
		}
	}
	for _, f := range requested.Filesystem {
		if !slices.Contains(approved.Filesystem, f) {
			missing = append(missing, "filesystem:"+f)
		}
	}
	return missing
}

// Install activates a package version after validation; requested access must be approved in full.
func (s *Service) Install(ctx context.Context, requestID, id, version string, approved Access, configuration map[string]any) (state.MutationReceipt, error) {
	doc, err := s.find(ctx, id, version)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	digest, err := Validate(doc.Manifest)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	if missing := covers(approved, doc.Manifest.RequestedAccess); len(missing) > 0 {
		return state.MutationReceipt{}, fmt.Errorf("%w: approve the access the package requests: %s", state.ErrInvalidResource, strings.Join(missing, ", "))
	}
	for _, dep := range doc.Manifest.Dependencies {
		if !s.installed(ctx, dep.ID) {
			return state.MutationReceipt{}, fmt.Errorf("%w: install %s first", state.ErrInvalidResource, dep.ID)
		}
	}
	installID := "inst_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("install:"+requestID)).String(), "-", "")[:20]
	now := s.now()
	inst := installationDoc{Installation: Installation{ID: installID, Package: Ref{ID: id, Version: version, Digest: digest}, Enabled: true, State: "ready",
		ConfigurationRevision: 1, ApprovedAccess: approved, RequiredConnectionTypes: doc.Manifest.RequestedAccess.ConnectionTypes,
		History: []HistoryEntry{{At: now, Event: "installed", Version: version}}}, Configuration: configuration}
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/plugins/installations", Payload: []any{id, version, approved, configuration},
		Collection: CollectionInstallations, ID: installID,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			return s.write(inst, 1, 201)
		},
	})
	if err != nil {
		return receipt, err
	}
	s.activate(ctx, installID, doc.Manifest)
	return receipt, nil
}

func (s *Service) installed(ctx context.Context, packageID string) bool {
	all, err := s.Installations(ctx)
	if err != nil {
		return false
	}
	for _, inst := range all {
		if inst.Package.ID == packageID && inst.State == "ready" {
			return true
		}
	}
	return false
}

func (s *Service) write(inst installationDoc, revision int64, status int) (state.MutationResult, error) {
	inst.Revision = revision
	r, err := state.NewResource(CollectionInstallations, inst.ID, revision, inst, map[string]string{"package": inst.Package.ID}, s.now())
	if err != nil {
		return state.MutationResult{}, err
	}
	data, _ := json.Marshal(inst.Installation)
	return state.MutationResult{Writes: []state.Resource{r}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: status}}, nil
}

// activate exposes a version's contributions: skills become available and MCP servers are configured (disabled
// until the user connects them).
func (s *Service) activate(ctx context.Context, installID string, m Manifest) {
	r, err := s.Store.Resource(ctx, CollectionInstallations, installID)
	if err != nil {
		return
	}
	doc, err := s.decodeInstallation(ctx, r)
	if err != nil {
		return
	}
	var diagnostics []string
	for _, c := range m.Contributions {
		switch c.Kind {
		case "skill":
			skill := *c.Skill
			skill.Ref = Ref{ID: m.Package.ID + "/" + slugify(skill.Title), Version: m.Package.Version}
			if _, err := s.putSkill(ctx, skill, installID); err != nil {
				diagnostics = append(diagnostics, "skill "+skill.Title+": "+err.Error())
			}
		case "mcp_server":
			if s.MCP == nil {
				continue
			}
			auth := c.Server.Auth
			if auth == "" {
				auth = "none"
			}
			receipt, err := s.MCP.Save(ctx, "", uuid.NewSHA1(uuid.NameSpaceOID, []byte(installID+":"+c.Server.Name+":"+m.Package.Version)).String(), 0,
				mcp.Editable{Name: c.Server.Name, Transport: "http", Endpoint: c.Server.Endpoint, Auth: auth, Enabled: true, Exposure: mcp.Exposure{Default: "discoverable"}})
			if err != nil {
				diagnostics = append(diagnostics, "MCP server "+c.Server.Name+": "+err.Error())
				continue
			}
			var server mcp.Server
			if json.Unmarshal(receipt.Resource, &server) == nil && !slices.Contains(doc.ServerIDs, server.ID) {
				doc.ServerIDs = append(doc.ServerIDs, server.ID)
			}
		}
	}
	doc.Diagnostics = diagnostics
	if next, err := state.NewResource(CollectionInstallations, installID, 0, doc, r.Index, s.now()); err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(text string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(text), "-"), "-")
}

// Review compares the installed version with another (update) or describes removal (uninstall).
type Review struct {
	FromVersion    string              `json:"from_version"`
	ToVersion      string              `json:"to_version,omitempty"`
	ToolsAdded     []string            `json:"tools_added"`
	ToolsRemoved   []string            `json:"tools_removed"`
	SchemasChanged []string            `json:"schemas_changed"`
	AccessAdded    []string            `json:"access_added"`
	AccessRemoved  []string            `json:"access_removed"`
	Dependents     map[string][]string `json:"dependents"`
}

func contributionNames(m Manifest) map[string]string {
	out := map[string]string{}
	for _, c := range m.Contributions {
		data, _ := json.Marshal(c)
		name := c.Kind + ":" + c.Name
		if c.Skill != nil {
			name = "skill:" + c.Skill.Title
		}
		if c.Server != nil {
			name = "mcp_server:" + c.Server.Name
		}
		if c.Kind == "hook" {
			name = "hook:" + c.Hook + ":" + c.Handler
		}
		out[name] = string(data)
	}
	return out
}

func (s *Service) Review(ctx context.Context, id, version string) (Review, error) {
	inst, err := s.Installation(ctx, id)
	if err != nil {
		return Review{}, err
	}
	from, err := s.find(ctx, inst.Package.ID, inst.Package.Version)
	if err != nil {
		return Review{}, err
	}
	review := Review{FromVersion: inst.Package.Version, ToolsAdded: []string{}, ToolsRemoved: []string{}, SchemasChanged: []string{},
		AccessAdded: []string{}, AccessRemoved: []string{}, Dependents: map[string][]string{"automation_ids": {}, "goal_ids": {}}}
	before := contributionNames(from.Manifest)
	after := map[string]string{}
	if version != "" {
		to, err := s.find(ctx, inst.Package.ID, version)
		if err != nil {
			return Review{}, err
		}
		review.ToVersion = version
		after = contributionNames(to.Manifest)
		// Compared with what the user granted, so the review lists exactly what the update will ask to approve.
		review.AccessAdded = nonNil(covers(inst.ApprovedAccess, to.Manifest.RequestedAccess))
		review.AccessRemoved = nonNil(covers(to.Manifest.RequestedAccess, inst.ApprovedAccess))
	}
	for name, body := range after {
		if old, ok := before[name]; !ok {
			review.ToolsAdded = append(review.ToolsAdded, name)
		} else if old != body {
			review.SchemasChanged = append(review.SchemasChanged, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			review.ToolsRemoved = append(review.ToolsRemoved, name)
		}
	}
	return review, nil
}

// Act enables, disables, updates, rolls back, pins or uninstalls. An update needing new access fails until the
// new access is approved; a pinned installation is never updated automatically.
func (s *Service) Act(ctx context.Context, id, requestID string, expected int64, action, version string, approved *Access, removeState bool) (state.MutationReceipt, error) {
	var target *packageDoc
	if action == "update" || action == "rollback" {
		doc, err := s.find(ctx, s.packageOf(ctx, id), version)
		if err != nil {
			return state.MutationReceipt{}, err
		}
		if _, err := Validate(doc.Manifest); err != nil {
			return state.MutationReceipt{}, err
		}
		target = &doc
	}
	var serverIDs []string
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/plugins/installations/" + id + "/actions", ExpectedRevision: expected,
		Payload: []any{action, version, approved, removeState}, Collection: CollectionInstallations, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var doc installationDoc
			if err := current.Decode(&doc); err != nil {
				return state.MutationResult{}, err
			}
			now := s.now()
			serverIDs = doc.ServerIDs
			switch action {
			case "enable":
				doc.Enabled, doc.State = true, "ready"
			case "disable":
				doc.Enabled, doc.State = false, "disabled"
			case "pin":
				doc.Pinned = true
			case "update", "rollback":
				access := doc.ApprovedAccess
				if approved != nil {
					access = *approved
				}
				if missing := covers(access, target.Manifest.RequestedAccess); len(missing) > 0 {
					return state.MutationResult{}, fmt.Errorf("%w: this version requests more access; approve %s", state.ErrInvalidResource, strings.Join(missing, ", "))
				}
				digest, _ := Validate(target.Manifest)
				doc.Previous, doc.Package, doc.ApprovedAccess = doc.Package.Version, Ref{ID: doc.Package.ID, Version: version, Digest: digest}, access
				doc.RequiredConnectionTypes = target.Manifest.RequestedAccess.ConnectionTypes
			case "uninstall":
				doc.Enabled, doc.State = false, "removing"
			default:
				return state.MutationResult{}, state.ErrInvalidResource
			}
			doc.History = append(doc.History, HistoryEntry{At: now, Event: action, Version: doc.Package.Version})
			return s.write(doc, current.Revision+1, 200)
		},
	})
	if err != nil {
		return receipt, err
	}
	switch action {
	case "update", "rollback":
		s.activate(ctx, id, target.Manifest)
	case "disable", "uninstall":
		s.deactivate(ctx, id, serverIDs, action == "uninstall")
	case "enable":
		if doc, err := s.find(ctx, s.packageOf(ctx, id), s.versionOf(ctx, id)); err == nil {
			s.activate(ctx, id, doc.Manifest)
		}
	}
	return receipt, nil
}

func (s *Service) packageOf(ctx context.Context, id string) string {
	inst, _ := s.Installation(ctx, id)
	return inst.Package.ID
}

func (s *Service) versionOf(ctx context.Context, id string) string {
	inst, _ := s.Installation(ctx, id)
	return inst.Package.Version
}

// deactivate stops new use of a package's contributions. Historical operations and results stay readable.
func (s *Service) deactivate(ctx context.Context, installID string, serverIDs []string, remove bool) {
	page, _, _ := s.Store.Resources(ctx, CollectionSkills, map[string]string{"installation_id": installID}, "", 200)
	for _, r := range page {
		var doc skillDoc
		if r.Decode(&doc) != nil {
			continue
		}
		doc.Disabled = true
		if next, err := state.NewResource(CollectionSkills, r.ID, 0, doc, r.Index, s.now()); err == nil {
			_, _ = s.Store.PutResource(ctx, next, r.Revision)
		}
	}
	if s.MCP != nil {
		for _, id := range serverIDs {
			server, err := s.MCP.Get(ctx, id)
			if err != nil {
				continue
			}
			_, _ = s.MCP.Save(ctx, id, uuid.NewString(), server.Revision, mcp.Editable{Name: server.Name, Transport: server.Transport,
				Endpoint: server.Endpoint, Auth: fallback(server.Auth, "none"), Enabled: false, Exposure: server.Exposure})
		}
	}
	if remove {
		// Uninstalled packages leave the installation list; operations and results they produced stay readable.
		_, _ = s.Store.Mutate(ctx, state.Mutation{
			RequestID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("uninstall:"+installID)).String(), Method: "UNINSTALL", Target: installID,
			Payload: installID, Collection: CollectionInstallations, ID: installID, SkipRevisionCheck: true,
			Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
				if current == nil {
					return state.MutationResult{}, state.ErrNotFound
				}
				return state.MutationResult{Deletes: []state.ResourceKey{{Collection: CollectionInstallations, ID: installID}},
					Receipt: state.MutationReceipt{Status: "applied", Resource: current.Body}}, nil
			},
		})
	}
}

func fallback(value, other string) string {
	if value == "" {
		return other
	}
	return value
}

// Inventory implements extensions.Source: tools of ready, enabled installations whose package runs isolated. A
// package's own effect claim is not trusted: its tools are unknown-effect and need explicit authority.
func (s *Service) Inventory(ctx context.Context) (extensions.Inventory, error) {
	installs, err := s.Installations(ctx)
	if err != nil {
		return extensions.Inventory{}, err
	}
	var inventory extensions.Inventory
	for _, inst := range installs {
		if !inst.Enabled || inst.State != "ready" {
			continue
		}
		doc, err := s.find(ctx, inst.Package.ID, inst.Package.Version)
		if err != nil || doc.Manifest.Executor == nil || doc.Manifest.Executor.Kind != "isolated" {
			continue
		}
		for _, d := range s.ToolDefinitions(inst.ID, doc.Manifest) {
			inventory.Definitions = append(inventory.Definitions, d)
		}
		inventory.Capabilities = append(inventory.Capabilities, extensions.CapabilityState{Capability: "plugin_" + slugify(inst.Package.ID),
			Title: fallback(doc.Manifest.Title, inst.Package.ID) + " (plugin)", Account: "Runs in the computer's sandbox without network", Availability: "Installed", Ready: true})
	}
	return inventory, nil
}

// ToolDefinitions seals a package version's tool contributions as registry definitions bound to the installation.
func (s *Service) ToolDefinitions(installID string, m Manifest) []extensions.Definition {
	var out []extensions.Definition
	limits := m.Executor.Limits
	for _, c := range m.Contributions {
		if c.Kind != "tool" {
			continue
		}
		var declared extensions.Definition
		if json.Unmarshal(c.Tool, &declared) != nil {
			continue
		}
		timeout := max(min(limits.WallMS+5000, 300_000), 1000)
		d, err := extensions.Seal(extensions.Definition{
			Ref:       extensions.DefinitionRef{ID: "plugin/" + slugify(m.Package.ID) + "/" + slugify(declared.ModelName), Version: m.Package.Version},
			Extension: extensions.DefinitionRef{ID: "plugin/" + slugify(m.Package.ID), Version: m.Package.Version},
			ModelName: declared.ModelName, Title: fallback(declared.Title, declared.ModelName), Description: declared.Description,
			Capability: "plugin_" + slugify(m.Package.ID), CapabilityTitle: fallback(m.Title, m.Package.ID) + " (plugin)",
			InputSchema: declared.InputSchema, OutputSchema: declared.OutputSchema,
			Binding:  extensions.Binding{Adapter: extensions.AdapterPlugin, Target: installID + "/" + declared.ModelName, Version: m.Package.Version},
			Policy:   extensions.Policy{Effect: extensions.EffectUnknown, Retry: "reconcile_only", TimeoutMS: timeout, Location: extensions.LocationComputer},
			Exposure: extensions.ExposureDiscoverable,
		})
		if err == nil {
			out = append(out, d)
		}
	}
	return out
}

// Program returns what the sandbox runs for an installation's tool: the argument vector, files and limits.
const (
	HookPreInvocation = "pre_invocation"
	HookResult        = "result"
	// Hook deadlines are bounded so a slow hook cannot hold a call: 2 s by default, at most 10 s.
	defaultHookDeadlineMS = 2000
	maxHookDeadlineMS     = 10000
)

// Hook is one of an installed package's hooks. Hooks apply only to the package's own tools.
type Hook struct {
	Kind       string
	Handler    string
	DeadlineMS int
}

// Program is how an installed package's code runs in the sandbox.
type Program struct {
	Argv     []string
	Files    map[string]string
	MemoryMB int
	WallMS   int
	Hooks    []Hook
}

func (s *Service) Program(ctx context.Context, installID string) (Program, error) {
	inst, err := s.Installation(ctx, installID)
	if err != nil {
		return Program{}, err
	}
	if !inst.Enabled || inst.State != "ready" {
		return Program{}, fmt.Errorf("the plugin is %s", inst.State)
	}
	doc, err := s.find(ctx, inst.Package.ID, inst.Package.Version)
	if err != nil {
		return Program{}, err
	}
	e := doc.Manifest.Executor
	if e == nil || e.Kind != "isolated" {
		return Program{}, ErrUnsupported
	}
	p := Program{Argv: append(append([]string{}, runtimes[e.Runtime]...), e.Entry), Files: doc.Manifest.Files, MemoryMB: 256, WallMS: e.Limits.WallMS}
	if e.Limits.MemoryMB > 0 {
		p.MemoryMB = e.Limits.MemoryMB
	}
	for _, c := range doc.Manifest.Contributions {
		if c.Kind != "hook" {
			continue
		}
		deadline := c.DeadlineMS
		if deadline <= 0 {
			deadline = defaultHookDeadlineMS
		}
		p.Hooks = append(p.Hooks, Hook{Kind: c.Hook, Handler: c.Handler, DeadlineMS: min(deadline, maxHookDeadlineMS)})
	}
	return p, nil
}
