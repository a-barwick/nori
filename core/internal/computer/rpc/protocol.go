package rpc

const (
	HealthPath        = "/healthz"
	CancelPath        = "/cancel"
	CloseBrowserPath  = "/browser/close"
	ExecPath          = "/exec"
	NavigatePath      = "/navigate"
	ScreenshotPath    = "/screenshot"
	StartJobPath      = "/job/start"
	JobStatusPath     = "/job/status"
	StopJobPath       = "/job/stop"
	StopTaskJobsPath  = "/job/stop-task"
	PutFilePath       = "/file/put"
	GetFilePath       = "/file/get"
	OperationPath     = "/operation"
	CancelReadPath    = "/operation/cancel"
	SandboxRunPath    = "/sandbox/run"
	OperationIDHeader = "Nori-Operation-ID"
	DrainingHeader    = "Nori-Computer-Draining"
	BrowserRestarted  = "browser_restarted"
	MaxScreenshot     = 2 << 20
)

type Health struct {
	Ready    bool `json:"ready"`
	Receipts bool `json:"operation_receipts_v1"`
	TaskTabs bool `json:"browser_task_tabs_v1"`
	Jobs     bool `json:"shell_jobs_v1"`
	Files    bool `json:"files_v1"`
	Sandbox  bool `json:"sandbox_v1"`
}

// SandboxRequest runs one packaged program with enforced limits and no network.
type SandboxRequest struct {
	TaskID   string            `json:"task_id"`
	Argv     []string          `json:"argv"`
	Stdin    string            `json:"stdin"`
	Files    map[string]string `json:"files"`
	MemoryMB int               `json:"memory_mb"`
	Pids     int               `json:"pids"`
	CPUPct   int               `json:"cpu_percent"`
	WallMS   int               `json:"wall_ms"`
	Network  string            `json:"network"`
}

type SandboxResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	TimedOut  bool   `json:"timed_out"`
	OOMKilled bool   `json:"oom_killed"`
	WallMS    int64  `json:"wall_ms"`
}
