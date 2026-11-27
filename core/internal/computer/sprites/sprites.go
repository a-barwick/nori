package sprites

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/superfly/sprites-go"
	"nori/internal/computer"
	"nori/internal/computer/vmscripts"
)

var checkpointIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const (
	// Conservative per-second compute rate for active 2 vCPU / 4 GB Sprite execution
	// (~$0.054/active-hour = $0.000015/sec). Reported into Nori's task budget.
	defaultComputeRatePerSecUSD = 0.000015
	defaultCPUs                 = 2
	defaultRamMB                = 4096
	bootstrapTimeout            = 6 * time.Minute
)

// Config configures the Fly.io Sprites provider adapter.
type Config struct {
	Token         string
	Org           string
	BaseURL       string
	Region        string
	CPUs          int
	RamMB         int
	StorageGB     int
	HTTPClient    *http.Client
	SkipBootstrap bool // optional, useful for tests
}

// Provider implements computer.Provider backed by Fly.io Sprites Firecracker microVMs.
type Provider struct {
	client *sdk.Client
	cfg    Config
	org    *sdk.OrganizationInfo
}

// New creates a new Sprites provider adapter.
func New(cfg Config) (*Provider, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, errors.New("NORI_SPRITES_API_TOKEN is required for sprites provider")
	}
	if cfg.CPUs <= 0 {
		cfg.CPUs = defaultCPUs
	}
	if cfg.RamMB <= 0 {
		cfg.RamMB = defaultRamMB
	}
	if cfg.StorageGB <= 0 {
		cfg.StorageGB = computer.DefaultMaxDiskGB
	}
	// The SDK eagerly opens a control WebSocket for each new Sprite handle.
	// Direct requests avoid leaving those sockets open after guest activity ends.
	opts := []sdk.Option{sdk.WithDisableControl()}
	if cfg.BaseURL != "" {
		opts = append(opts, sdk.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, sdk.WithHTTPClient(cfg.HTTPClient))
	}
	var org *sdk.OrganizationInfo
	if strings.TrimSpace(cfg.Org) != "" {
		org = &sdk.OrganizationInfo{Name: strings.TrimSpace(cfg.Org)}
	}
	return &Provider{
		client: sdk.New(token, opts...),
		cfg:    cfg,
		org:    org,
	}, nil
}

func (p *Provider) Name() string { return "sprites" }

func (p *Provider) Close() error {
	if p == nil || p.client == nil {
		return nil
	}
	return p.client.Close()
}

func spriteName(computerID string) string {
	clean := strings.ToLower(strings.TrimSpace(computerID))
	clean = strings.ReplaceAll(clean, "_", "-")
	if clean == "" {
		clean = "nori-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	}
	if !strings.HasPrefix(clean, "nori-") {
		clean = "nori-" + clean
	}
	return clean
}

func (p *Provider) spriteHandle(providerID string) *sdk.Sprite {
	if p.org != nil {
		return p.client.SpriteWithOrg(providerID, p.org)
	}
	return p.client.Sprite(providerID)
}

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if apiErr := sdk.IsAPIError(err); apiErr != nil && apiErr.StatusCode == http.StatusNotFound {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "404")
}

func (p *Provider) Ensure(ctx context.Context, spec computer.Spec) (computer.Instance, error) {
	name := spriteName(spec.ComputerID)
	created := false

	var sp *sdk.Sprite
	var err error
	if p.org != nil {
		sp, err = p.client.GetSpriteWithOrg(ctx, name, p.org)
	} else {
		sp, err = p.client.GetSprite(ctx, name)
	}
	if err != nil {
		if !isNotFoundErr(err) {
			return computer.Instance{}, fmt.Errorf("get sprite %s: %w", name, err)
		}
		storageGB := spec.MaxDiskGB
		if storageGB <= 0 {
			storageGB = p.cfg.StorageGB
		}
		sc := &sdk.SpriteConfig{
			CPUs:      p.cfg.CPUs,
			RamMB:     p.cfg.RamMB,
			Region:    p.cfg.Region,
			StorageGB: storageGB,
		}
		if p.org != nil {
			sp, err = p.client.CreateSpriteWithOrg(ctx, name, sc, p.org, []string{"nori", "computer:" + spec.ComputerID})
		} else {
			sp, err = p.client.CreateSprite(ctx, name, sc)
		}
		if err != nil {
			return computer.Instance{}, fmt.Errorf("create sprite %s: %w", name, err)
		}
		created = true
	}
	if sp == nil {
		sp = p.spriteHandle(name)
	}

	// Enforce private URL auth BEFORE running bootstrap.
	if created || sp.URLSettings == nil || !strings.EqualFold(strings.TrimSpace(sp.URLSettings.Auth), "sprite") {
		if err := p.client.UpdateURLSettings(ctx, name, &sdk.URLSettings{Auth: "sprite"}); err != nil {
			return computer.Instance{}, fmt.Errorf("configure private URL auth on sprite %s: %w", name, err)
		}
	}

	needsBootstrap := !p.cfg.SkipBootstrap && created
	if !p.cfg.SkipBootstrap && !created {
		if spec.CurrentBootstrapVersion != vmscripts.Version {
			needsBootstrap = true
		} else {
			checkCmd := fmt.Sprintf(`[ -f /home/nori/.nori/bootstrap-%s.done ] && [ -x /home/nori/.nori/bin/nori-desktop-start ] && [ -x /home/nori/.nori/bin/nori-browser ] && [ -x /home/nori/.nori/bin/nori-watchdog ]`, vmscripts.Version)
			res, checkErr := p.exec(ctx, name, computer.ExecRequest{
				Command: checkCmd,
				WorkDir: computer.HomeDir,
				Timeout: 15 * time.Second,
			})
			if checkErr != nil || res.ExitCode != 0 {
				needsBootstrap = true
			}
		}
	}
	if created || needsBootstrap {
		if err := p.SetNetworkPolicy(ctx, name, computer.DefaultNetworkPolicy()); err != nil {
			return computer.Instance{}, fmt.Errorf("apply baseline network policy on sprite %s: %w", name, err)
		}
	}
	if needsBootstrap {
		if err := p.installScriptsAndBootstrap(ctx, name, spec); err != nil {
			return computer.Instance{}, err
		}
	}

	baselineID := ""
	if created {
		if ckpt, cErr := p.Checkpoint(ctx, name, "baseline"); cErr == nil {
			baselineID = ckpt
		}
	}

	return computer.Instance{
		ProviderID:           name,
		Status:               computer.StatusReady,
		BaselineCheckpointID: baselineID,
		BootstrapVersion:     vmscripts.Version,
	}, nil
}

func (p *Provider) ensureCanonicalHome(ctx context.Context, providerID string) error {
	// Fly Sprites boot as OS user "sprite" with /home/sprite. Symlink /home/nori -> /home/sprite
	// so HOME, /etc/passwd, computer_files, and interactive desktop terminals share one directory.
	// If an older bootstrap created a real /home/nori directory, merge its contents into /home/sprite first.
	const cmd = `set -euo pipefail
if [ "$(id -un)" = "sprite" ] && [ -d /home/sprite ] && [ ! -L /home/nori ]; then
  SUDO=""
  if [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1; then SUDO="sudo"; fi
  if [ -d /home/nori ]; then
    cp -an /home/nori/. /home/sprite/
    bak="/home/.nori-migrate-$$"
    ${SUDO} mv /home/nori "${bak}"
    ${SUDO} ln -s /home/sprite /home/nori
    ${SUDO} rm -rf "${bak}"
  else
    ${SUDO} ln -s /home/sprite /home/nori
  fi
fi`
	res, err := p.exec(ctx, providerID, computer.ExecRequest{
		Command: cmd,
		WorkDir: "/",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("link /home/nori on sprite %s: %w", providerID, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("link /home/nori on sprite %s failed (exit %d): %s", providerID, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (p *Provider) installScriptsAndBootstrap(ctx context.Context, providerID string, spec computer.Spec) error {
	if err := p.ensureCanonicalHome(ctx, providerID); err != nil {
		return err
	}
	scripts := []struct {
		path string
		body string
		mode uint32
	}{
		{"/home/nori/.nori/bootstrap.sh", vmscripts.BootstrapSH, 0o755},
		{"/home/nori/.nori/bin/nori-desktop-start", vmscripts.DesktopStartSH, 0o755},
		{"/home/nori/.nori/bin/nori-browser", vmscripts.BrowserJS, 0o755},
		{"/home/nori/.nori/bin/nori-watchdog", vmscripts.WatchdogSH, 0o755},
	}
	for _, s := range scripts {
		if err := p.WriteFile(ctx, providerID, s.path, []byte(s.body), s.mode); err != nil {
			return fmt.Errorf("install VM script %s: %w", s.path, err)
		}
	}
	memLimit := spec.MemoryLimitMB
	if memLimit <= 0 {
		memLimit = computer.DefaultMemoryLimitMB
	}
	res, err := p.exec(ctx, providerID, computer.ExecRequest{
		Command: fmt.Sprintf("NORI_BOOTSTRAP_VERSION=%s NORI_COMPUTER_MEMORY_WATCHDOG_MB=%d flock /home/nori/.nori/bootstrap.lock /home/nori/.nori/bootstrap.sh", vmscripts.Version, memLimit),
		WorkDir: computer.HomeDir,
		Timeout: bootstrapTimeout,
	})
	if err != nil {
		return fmt.Errorf("bootstrap sprite %s: %w", providerID, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("bootstrap sprite %s failed (exit %d): %s", providerID, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (p *Provider) Status(ctx context.Context, providerID string) (computer.InstanceStatus, error) {
	var sp *sdk.Sprite
	var err error
	if p.org != nil {
		sp, err = p.client.GetSpriteWithOrg(ctx, providerID, p.org)
	} else {
		sp, err = p.client.GetSprite(ctx, providerID)
	}
	if err != nil {
		if isNotFoundErr(err) {
			return computer.InstanceStatus{}, computer.ErrNotFound
		}
		return computer.InstanceStatus{}, err
	}
	// Status is polled by the clients. Keep it on the management API so
	// polling does not extend the Sprite's active lifetime.
	status, standby := mapSpriteStatus(sp.Status)
	out := computer.InstanceStatus{
		Status:               status,
		Standby:              standby,
		EventuallyConsistent: true,
		DiskUsedBytes:        -1, // Fly metadata omits used storage.
		DiskMaxBytes:         int64(p.cfg.StorageGB) << 30,
		LastActiveAt:         sp.LastRunningAt,
	}
	if sp.Config != nil && sp.Config.StorageGB > 0 {
		out.DiskMaxBytes = int64(sp.Config.StorageGB) << 30
	}
	return out, nil
}

// ListCheckpoints may wake a Sprite, so only call it for an explicit checkpoint action.
func (p *Provider) ListCheckpoints(ctx context.Context, providerID string) ([]computer.CheckpointInfo, error) {
	ckpts, err := p.client.ListCheckpoints(ctx, providerID, "")
	if err != nil {
		return nil, err
	}
	out := make([]computer.CheckpointInfo, 0, len(ckpts))
	for _, c := range ckpts {
		if !restorableCheckpoint(c.ID) {
			continue
		}
		out = append(out, computer.CheckpointInfo{
			ID:        c.ID,
			Label:     c.Comment,
			CreatedAt: c.CreateTime,
		})
	}
	return out, nil
}

func mapSpriteStatus(raw string) (computer.Status, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "running", "active", "ready":
		return computer.StatusReady, false
	case "warm":
		// Fly Sprites transitions to "warm" milliseconds after an exec ends while
		// remaining immediately ready. Mark Standby so an explicit Sleep stays asleep.
		return computer.StatusReady, true
	case "cold", "sleeping", "stopped", "suspended":
		return computer.StatusSleeping, false
	case "creating", "starting", "provisioning":
		return computer.StatusProvisioning, false
	case "destroying", "deleting":
		return computer.StatusDeleting, false
	case "destroyed", "deleted":
		return computer.StatusDeleted, false
	default:
		if raw == "" {
			return computer.StatusSleeping, false
		}
		return computer.StatusReady, false
	}
}

func (p *Provider) Exec(ctx context.Context, providerID string, req computer.ExecRequest) (computer.ExecResult, error) {
	if req.Timeout <= 0 || req.Timeout > computer.MaxExecTimeout {
		req.Timeout = computer.DefaultExecTimeout
	}
	return p.exec(ctx, providerID, req)
}

func (p *Provider) exec(ctx context.Context, providerID string, req computer.ExecRequest) (computer.ExecResult, error) {
	execCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	const noriEnvPrefix = `export HOME="/home/nori" NPM_CONFIG_PREFIX="/home/nori/.local/npm" PATH="/home/nori/.nori/bin:/home/nori/.local/npm/bin:/home/nori/.local/bin:/home/nori/.local/venv/bin:${PATH:-/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin}"; `
	sp := p.spriteHandle(providerID)
	command := noriEnvPrefix + req.Command
	jobID := ""
	if req.Background {
		jobID = "job_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		logPath := fmt.Sprintf("/home/nori/.nori/logs/%s.log", jobID)
		pidPath := fmt.Sprintf("/home/nori/.nori/logs/%s.pid", jobID)
		exitPath := fmt.Sprintf("/home/nori/.nori/logs/%s.exit", jobID)
		wrapped := fmt.Sprintf(`(%s); echo $? > %s`, noriEnvPrefix+req.Command, shellQuote(exitPath))
		command = fmt.Sprintf(`%smkdir -p /home/nori/.nori/logs && nohup bash -lc %s >%s 2>&1 </dev/null & pid=$!; echo "$pid" > %s; echo "$pid"`,
			noriEnvPrefix, shellQuote(wrapped), shellQuote(logPath), shellQuote(pidPath))
	}

	cmd := sp.CommandContext(execCtx, "bash", "-lc", command)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if len(req.Env) > 0 {
		env := make([]string, 0, len(req.Env))
		for k, v := range req.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	stdout := computer.NewLimitedBuffer(computer.MaxExecOutputBytes + 4096)
	stderr := computer.NewLimitedBuffer(computer.MaxExecOutputBytes + 4096)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	started := time.Now()
	err := cmd.Run()
	elapsed := time.Since(started)
	costUSD := elapsed.Seconds() * defaultComputeRatePerSecUSD

	if err != nil {
		var exitErr *sdk.ExitError
		if errors.As(err, &exitErr) {
			return computer.ExecResult{
				ExitCode:   exitErr.ExitCode(),
				Stdout:     stdout.String(),
				Stderr:     stderr.String(),
				DurationMS: elapsed.Milliseconds(),
				JobID:      jobID,
				CostUSD:    costUSD,
			}, nil
		}
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return computer.ExecResult{
				ExitCode:   124,
				Stdout:     stdout.String(),
				Stderr:     stderr.String(),
				DurationMS: elapsed.Milliseconds(),
				JobID:      jobID,
				CostUSD:    costUSD,
			}, computer.ErrTimeout
		}
		return computer.ExecResult{
			ExitCode:   1,
			Stdout:     stdout.String(),
			Stderr:     stderr.String(),
			DurationMS: elapsed.Milliseconds(),
			JobID:      jobID,
			CostUSD:    costUSD,
		}, err
	}

	return computer.ExecResult{
		ExitCode:   cmd.ExitCode(),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: elapsed.Milliseconds(),
		JobID:      jobID,
		CostUSD:    costUSD,
	}, nil
}

func (p *Provider) ReadFile(ctx context.Context, providerID, filePath string, maxBytes int64) ([]byte, error) {
	sp := p.spriteHandle(providerID)
	sfs := sp.Filesystem()
	if maxBytes > 0 {
		if info, err := sfs.Stat(filePath); err == nil && info.Size() > maxBytes {
			return nil, computer.ErrFileTooLarge
		}
	}
	data, err := sfs.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || isNotFoundErr(err) {
			return nil, computer.ErrNotFound
		}
		return nil, err
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, computer.ErrFileTooLarge
	}
	return data, nil
}

func (p *Provider) WriteFile(ctx context.Context, providerID, filePath string, data []byte, mode uint32) error {
	sp := p.spriteHandle(providerID)
	sfs := sp.Filesystem()
	if dir := path.Dir(filePath); dir != "" && dir != "/" {
		if err := sfs.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if mode == 0 {
		mode = 0o644
	}
	return sfs.WriteFileContext(ctx, filePath, data, fs.FileMode(mode))
}

func (p *Provider) ListFiles(_ context.Context, providerID, dir string) ([]computer.FileEntry, error) {
	sp := p.spriteHandle(providerID)
	entries, err := sp.Filesystem().ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || isNotFoundErr(err) {
			return nil, computer.ErrNotFound
		}
		return nil, err
	}
	out := make([]computer.FileEntry, 0, len(entries))
	for _, e := range entries {
		fe := computer.FileEntry{
			Name:  e.Name(),
			Path:  path.Join(dir, e.Name()),
			IsDir: e.IsDir(),
		}
		if info, err := e.Info(); err == nil {
			fe.Size = info.Size()
			fe.Mode = info.Mode().String()
			fe.ModTime = info.ModTime().UTC()
		}
		out = append(out, fe)
	}
	return out, nil
}

func (p *Provider) DeletePath(ctx context.Context, providerID, targetPath string) error {
	sp := p.spriteHandle(providerID)
	err := sp.Filesystem().RemoveAllContext(ctx, targetPath)
	if err != nil && (errors.Is(err, fs.ErrNotExist) || isNotFoundErr(err)) {
		return computer.ErrNotFound
	}
	return err
}

func (p *Provider) DesktopEndpoint(ctx context.Context, providerID string) (computer.DesktopTarget, error) {
	var sp *sdk.Sprite
	var err error
	if p.org != nil {
		sp, err = p.client.GetSpriteWithOrg(ctx, providerID, p.org)
	} else {
		sp, err = p.client.GetSprite(ctx, providerID)
	}
	if err != nil {
		if isNotFoundErr(err) {
			return computer.DesktopTarget{}, computer.ErrNotFound
		}
		return computer.DesktopTarget{}, err
	}
	rawURL := strings.TrimSpace(sp.URL)
	if rawURL == "" {
		return computer.DesktopTarget{}, fmt.Errorf("sprite %s has no private URL", providerID)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return computer.DesktopTarget{}, fmt.Errorf("invalid sprite URL %q: %w", rawURL, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = "/websockify"
	u.RawQuery = ""
	u.Fragment = ""

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(p.cfg.Token))
	rawToken, err := p.ReadFile(ctx, providerID, "/home/nori/.nori/desktop.token", 256)
	if err != nil {
		return computer.DesktopTarget{}, fmt.Errorf("read desktop token for sprite %s: %w", providerID, err)
	}
	desktopToken := strings.TrimSpace(string(rawToken))
	if _, err := base64.RawURLEncoding.DecodeString(desktopToken); err != nil || len(desktopToken) != 43 {
		return computer.DesktopTarget{}, fmt.Errorf("invalid desktop token for sprite %s", providerID)
	}
	headers.Set("X-Nori-Desktop-Token", desktopToken)
	return computer.DesktopTarget{
		WSURL:   u.String(),
		Headers: headers,
	}, nil
}

func (p *Provider) SetNetworkPolicy(ctx context.Context, providerID string, policy computer.NetworkPolicy) error {
	rules := make([]sdk.NetworkPolicyRule, 0, len(policy.DenyDomains)+len(policy.AllowDomains)+1)
	seen := map[string]bool{}
	addDeny := func(domain string) {
		d := strings.TrimSpace(domain)
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		rules = append(rules, sdk.NetworkPolicyRule{Domain: d, Action: "deny"})
	}
	if policy.BlockPrivate {
		for _, d := range computer.DefaultNetworkPolicy().DenyDomains {
			addDeny(d)
		}
		// Fly enforces these rules on egress from the VM. Guest-local
		// loopback remains available to Chromium and the desktop stack.
		addDeny("*.flycast")
	}
	for _, d := range policy.DenyDomains {
		addDeny(d)
	}
	if len(policy.AllowDomains) > 0 {
		for _, d := range policy.AllowDomains {
			trimmed := strings.TrimSpace(d)
			if trimmed != "" {
				rules = append(rules, sdk.NetworkPolicyRule{Domain: trimmed, Action: "allow"})
			}
		}
	} else {
		rules = append(rules, sdk.NetworkPolicyRule{Domain: "*", Action: "allow"})
	}
	return p.client.UpdateNetworkPolicy(ctx, providerID, &sdk.NetworkPolicy{Rules: rules})
}

func (p *Provider) Checkpoint(ctx context.Context, providerID, label string) (string, error) {
	previous, err := p.client.ListCheckpoints(ctx, providerID, "")
	if err != nil {
		return "", err
	}
	existing := make(map[string]bool, len(previous))
	for _, checkpoint := range previous {
		existing[checkpoint.ID] = true
	}
	stream, err := p.client.CreateCheckpointWithComment(ctx, providerID, label)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var streamErr error
	if err := stream.ProcessAll(func(msg *sdk.StreamMessage) error {
		if msg != nil && msg.Type == "error" && msg.Error != "" {
			streamErr = errors.New(msg.Error)
		}
		return nil
	}); err != nil {
		return "", err
	}
	if streamErr != nil {
		return "", streamErr
	}
	ckpts, err := p.client.ListCheckpoints(ctx, providerID, "")
	if err != nil {
		return "", err
	}
	var newest *sdk.Checkpoint
	for _, c := range ckpts {
		if !restorableCheckpoint(c.ID) || existing[c.ID] || (label != "" && c.Comment != label) {
			continue
		}
		if newest == nil || c.CreateTime.After(newest.CreateTime) {
			newest = c
		}
	}
	if newest == nil {
		return "", errors.New("checkpoint created but not listed")
	}
	return newest.ID, nil
}

func restorableCheckpoint(id string) bool {
	return !strings.EqualFold(id, "Current") && checkpointIDPattern.MatchString(id)
}

func (p *Provider) Restore(ctx context.Context, providerID, checkpointID string) error {
	if !restorableCheckpoint(checkpointID) {
		return computer.ErrNotFound
	}
	stream, err := p.client.RestoreCheckpoint(ctx, providerID, checkpointID)
	if err != nil {
		return err
	}
	defer stream.Close()
	var streamErr error
	if err := stream.ProcessAll(func(msg *sdk.StreamMessage) error {
		if msg != nil && msg.Type == "error" && msg.Error != "" {
			streamErr = errors.New(msg.Error)
		}
		return nil
	}); err != nil {
		return err
	}
	return streamErr
}

func (p *Provider) Sleep(ctx context.Context, providerID string) error {
	// Sprites auto-sleep when no active exec or HTTP/WS services hold the machine awake.
	// Closing Chromium via CDP commits session cookies cleanly before stopping desktop daemons.
	_, err := p.Exec(ctx, providerID, computer.ExecRequest{
		Command: `nori-browser close >/dev/null 2>&1 || true; sleep 1; pkill -f "[w]ebsockify" >/dev/null 2>&1 || true; pkill -x x11vnc >/dev/null 2>&1 || true; pkill -f "[d]esktop-gate" >/dev/null 2>&1 || true; pkill -x fluxbox >/dev/null 2>&1 || true; pkill -x Xvfb >/dev/null 2>&1 || true; pkill -f "[n]ori-watchdog" >/dev/null 2>&1 || true`,
		WorkDir: computer.HomeDir,
		Timeout: 15 * time.Second,
	})
	if err != nil && isNotFoundErr(err) {
		return computer.ErrNotFound
	}
	return err
}

func (p *Provider) Destroy(ctx context.Context, providerID string) error {
	err := p.client.DestroySprite(ctx, providerID)
	if err != nil && isNotFoundErr(err) {
		return computer.ErrNotFound
	}
	return err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
