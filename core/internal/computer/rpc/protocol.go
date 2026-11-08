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
}
