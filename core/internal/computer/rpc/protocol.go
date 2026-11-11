package rpc

const (
	HealthPath       = "/healthz"
	CancelPath       = "/cancel"
	CloseBrowserPath = "/browser/close"
	ExecPath         = "/exec"
	NavigatePath     = "/navigate"
	ScreenshotPath   = "/screenshot"
	StartJobPath     = "/job/start"
	JobStatusPath    = "/job/status"
	StopJobPath      = "/job/stop"
	StopTaskJobsPath = "/job/stop-task"
	PutFilePath      = "/file/put"
	GetFilePath      = "/file/get"
	OperationPath    = "/operation"
	CancelReadPath   = "/operation/cancel"
	SandboxRunPath   = "/sandbox/run"
	// SandboxSessionPath upgrades to a raw stream carrying a sandboxed program's stdin and stdout.
	SandboxSessionPath = "/sandbox/session"
	SandboxUpgrade     = "nori-stdio"
	ReopenRunPath      = "/run/reopen"
	OperationIDHeader  = "Nori-Operation-ID"
	DrainingHeader     = "Nori-Computer-Draining"
	BrowserRestarted   = "browser_restarted"
	MaxScreenshot      = 2 << 20
)

type Health struct {
	Ready    bool `json:"ready"`
	Receipts bool `json:"operation_receipts_v1"`
	TaskTabs bool `json:"browser_task_tabs_v1"`
	Jobs     bool `json:"shell_jobs_v1"`
	Files    bool `json:"files_v1"`
	Sandbox  bool `json:"sandbox_v1"`
	// Egress says the guest can run sandboxed programs with a declared-host egress allowlist.
	Egress bool `json:"egress_allowlist_v1"`
	// Sessions says the guest can run a sandboxed program as a streamed stdio session.
	Sessions bool `json:"sandbox_sessions_v1"`
	Reopen   bool `json:"run_reopen_v1"`
}

// SandboxRequest runs one packaged program with enforced limits and no network.
type SandboxRequest struct {
	RunID    string            `json:"run_id"`
	TaskID   string            `json:"task_id"`
	Argv     []string          `json:"argv"`
	Stdin    string            `json:"stdin"`
	Files    map[string]string `json:"files"`
	MemoryMB int               `json:"memory_mb"`
	Pids     int               `json:"pids"`
	CPUPct   int               `json:"cpu_percent"`
	WallMS   int               `json:"wall_ms"`
	// Network is "none" (no network) or "allowlist" (only AllowHosts, through the guest's egress proxy).
	Network    string   `json:"network"`
	AllowHosts []string `json:"allow_hosts,omitempty"`
}

type SandboxResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	TimedOut  bool   `json:"timed_out"`
	OOMKilled bool   `json:"oom_killed"`
	WallMS    int64  `json:"wall_ms"`
	// Denied lists hosts the program tried to reach outside its allowlist.
	Denied []string `json:"denied,omitempty"`
}
