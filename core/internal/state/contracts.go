package state

import (
	"context"
	"time"
)

// The contracts are layered by what a caller needs. Agent steps use Store, task activities use TaskStore, and
// conversation processing and the HTTP API use ConversationStore. A backend implements Database for documents and
// ArtifactStore for artifact bytes; Compose joins them.

// RunStore reads and updates task records.
type RunStore interface {
	Control(ctx context.Context) (Control, error)
	Session(ctx context.Context) (Session, error)
	GetRun(ctx context.Context, id string) (Run, error)
	// Spend records cumulative model cost on a run. It is unguarded: cost already incurred is recorded even if a
	// correction superseded the revision that spent it.
	Spend(ctx context.Context, update SpendUpdate) error
	// Artifact records that a run owns an artifact. Its bytes are written with PutPNG.
	Artifact(ctx context.Context, runID string, artifact Artifact) error
	GetUpload(ctx context.Context, id string) (Upload, error)
	SaveResult(ctx context.Context, result Result) (Result, error)
	GetResult(ctx context.Context, id string) (Result, error)
	RecentResults(ctx context.Context, limit int) ([]Result, error)
	RecentRuns(ctx context.Context, limit int) ([]Run, error)
}

// Artifacts writes and reads private PNG artifacts by run and artifact ID.
type Artifacts interface {
	PutPNG(ctx context.Context, run, id string, data []byte) error
	GetPNG(ctx context.Context, run, id string) ([]byte, error)
	// PutFile saves a file once under a key from ValidFileKey, and returns ErrFileExists when the key is taken.
	PutFile(ctx context.Context, key, mediaType string, data []byte) error
	GetFile(ctx context.Context, key string) ([]byte, error)
}

// Store is the run-state surface the agent steps use.
type Store interface {
	RunStore
	Artifacts
}

// TaskEvidenceStore is separate from display tracing: successful writes here
// are required before an operation or completion can be treated as recorded.
type TaskEvidenceStore interface {
	AppendHistory(ctx context.Context, runID, parent string, data []byte) (ref string, err error)
	ReadHistory(ctx context.Context, runID, ref string) ([][]byte, error)
	PutOperation(ctx context.Context, runID string, op OperationRecord) error
	Operations(ctx context.Context, runID string) ([]OperationRecord, error)
	// WriteOperations lists only the writes that needed approval, without reading the task's other operations.
	WriteOperations(ctx context.Context, runID string) ([]OperationRecord, error)
	PutCompletionCheck(ctx context.Context, runID string, check CompletionCheck) error
	// ReadTaskSourceInputs resolves the exact retained user inputs a task's commands cite, instead of relying on the
	// bounded recent-conversation view.
	ReadTaskSourceInputs(ctx context.Context, runID string) ([]ConversationInput, error)
	// ReadTaskOutcome reads what a finished task's last completion proposal left unmet and the saved results it
	// cites, so the outer loop can compare the answer with them.
	ReadTaskOutcome(ctx context.Context, runID string) (TaskOutcome, error)
}

// TaskDatabase is the document half of the task surface. Every operation that changes a task is one atomic
// transaction in the backend, and its decisions come from the rules in this package.
type TaskDatabase interface {
	RecordUsage(ctx context.Context, usage Usage) error
	RunStore
	TaskEvidenceStore

	// Accept admits one command; AcceptCommandBatch admits a whole batch atomically, optionally fenced by the
	// conversation turn that produced it.
	Accept(ctx context.Context, cmd Command) (Run, error)
	AcceptCommandBatch(ctx context.Context, commands []Command, fence *ConversationFence) ([]CommandReceipt, error)
	// Pending lists accepted commands whose delivery to the worker is not confirmed; Delivered confirms one.
	Pending(ctx context.Context) ([]Command, error)
	Delivered(ctx context.Context, commandID string) error
	// Reconcile returns a task with the commands it has accepted, in order.
	Reconcile(ctx context.Context, runID string) (TaskSnapshot, error)

	// Worker fences and projections. Each checks the task's revision.
	Incorporate(ctx context.Context, req IncorporateRequest) error
	Guard(ctx context.Context, runID string, revision int64) (Run, error)
	Admit(ctx context.Context, runID string, revision int64, operation string) error
	// UseApproval moves a prepared write to attempted, in one transaction that also checks the task's revision and
	// the saved approve command. ErrApprovalUsed means an earlier attempt already took it, so it must not send.
	UseApproval(ctx context.Context, runID string, revision int64, operation, commandID string) (OperationRecord, error)
	Project(ctx context.Context, req ProjectRequest) error
	// SetProgress replaces the task's latest progress line. Unlike Project it adds no event, so polling a long job
	// can't grow the task's saved record.
	SetProgress(ctx context.Context, runID string, revision int64, text string) error
	ProjectQuestion(ctx context.Context, runID string, revision int64, question Question) error
	Publish(ctx context.Context, req PublishRequest) (Run, error)

	// Recovery queries.
	OpenTasks(ctx context.Context) ([]Run, error)
	PublicationTasks(ctx context.Context) ([]Run, error)
	PublicationWoken(ctx context.Context, runID string, generation int64) error
	CleanupTasks(ctx context.Context) ([]Run, error)
	FinishCleanup(ctx context.Context, runID string) error
	OperationRevisions(ctx context.Context, runID string) ([]int64, error)

	// Detailed traces, fetched only when opened.
	RecordTaskStep(ctx context.Context, runID string, step TaskStep) error
	TaskSteps(ctx context.Context, runID string) ([]TaskStep, error)
}

// TaskStore is what task activities use: the task documents plus artifacts.
type TaskStore interface {
	TaskDatabase
	Artifacts
}

// Database is everything a document backend provides. A backend commits each operation atomically, applies the rules
// in this package inside its transactions, and keeps its saved document layout unchanged.
type Database interface {
	TaskDatabase
	ConversationDatabase
	Close() error
}

// ConversationDatabase is the conversation half of a document backend.
type ConversationDatabase interface {
	AcceptConversationInput(ctx context.Context, id, text string, attachments []string) error
	CreateUpload(ctx context.Context, upload Upload) (Upload, error)
	MarkDelivered(ctx context.Context, ids []string) error
	AcquireConversation(ctx context.Context, owner, turnID string) (ConversationControl, *ConversationTurn, error)
	CheckConversation(ctx context.Context, f ConversationFence) (stale bool, err error)
	ConversationCommand(ctx context.Context, id string) (*CommandReceipt, error)
	ConversationHistory(ctx context.Context) (turns []ConversationTurn, truncated bool, err error)
	ConversationMessages(ctx context.Context, before string) (messages []Message, next string, err error)
	ConversationProjection(ctx context.Context) (Session, error)
	ConversationStatus(ctx context.Context) (ConversationControl, *ConversationTurn, error)
	ConversationTraces(ctx context.Context) ([]ConversationAttempt, error)
	EnsureConversationIntroduction(ctx context.Context, prompt string) error
	PublishConversation(ctx context.Context, f ConversationFence, t ConversationTurn, text string) error
	ReleaseConversation(ctx context.Context, f ConversationFence) error
	RenewConversation(ctx context.Context, f ConversationFence) (stale bool, err error)
	RestartConversation(ctx context.Context, f ConversationFence, t ConversationTurn) (ConversationTurn, time.Duration, error)
	SaveConversation(ctx context.Context, f ConversationFence, t ConversationTurn) error
	UserRoutingPending(ctx context.Context) (bool, error)

	// The event log. Writes happen inside the transactions above.
	EventPosition(ctx context.Context) (int64, error)
	WatchEvents(ctx context.Context, after int64, deliver func([]LogEvent) error) error
	ConversationMessage(ctx context.Context, id string) (Message, error)
}

// ConversationStore is the storage used by conversation processing and its HTTP API. Task activities need only
// TaskStore. Open one with platform.OpenStore.
type ConversationStore interface {
	Database
	Artifacts
}
