package state

import (
	"context"
	"encoding/json"
	"time"

	"nori/internal/state/docstore"
)

type RunStore interface {
	Control(ctx context.Context) (Control, error)
	Session(ctx context.Context) (Session, error)
	GetRun(ctx context.Context, id string) (Run, error)
	Spend(ctx context.Context, update SpendUpdate) error
	Artifact(ctx context.Context, runID string, artifact Artifact) error
	GetUpload(ctx context.Context, id string) (Upload, error)
	SaveResult(ctx context.Context, result Result) (Result, error)
	GetResult(ctx context.Context, id string) (Result, error)
	RecentResults(ctx context.Context, limit int) ([]Result, error)
	RecentRuns(ctx context.Context, limit int) ([]Run, error)
	Memories(ctx context.Context) ([]Memory, error)
	MemoryLogs(ctx context.Context, afterDate string, limit int) ([]MemoryLog, error)
	MemoryLog(ctx context.Context, date string) (MemoryLog, error)
	SearchMemory(ctx context.Context, query docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error)
	ContextFrom(ctx context.Context) (time.Time, error)
	ConversationSummary(ctx context.Context) (ConversationSummary, error)
	ReadConversationMessage(ctx context.Context, id string) (Message, error)
	ConversationMessages(ctx context.Context, before string) (messages []Message, next string, err error)
}

type Artifacts interface {
	PutPNG(ctx context.Context, run, id string, data []byte) error
	GetPNG(ctx context.Context, run, id string) ([]byte, error)
	PutFile(ctx context.Context, key, mediaType string, data []byte) error
	GetFile(ctx context.Context, key string) ([]byte, error)
	DeleteFile(ctx context.Context, key string) error
}

type Store interface {
	RunStore
	Artifacts
}

type TaskEvidenceStore interface {
	AppendHistory(ctx context.Context, runID, parent string, data []byte) (ref string, err error)
	ReadHistory(ctx context.Context, runID, ref string) ([][]byte, error)
	StartTaskModelCheckpoint(ctx context.Context, runID string, checkpoint TaskModelCheckpoint) error
	SaveTaskModelCheckpoint(ctx context.Context, runID string, checkpoint TaskModelCheckpoint, messages []byte) (ref string, err error)
	TaskModelCheckpoint(ctx context.Context, runID, id string) (TaskModelCheckpoint, error)
	PutOperation(ctx context.Context, runID string, op OperationRecord) error
	SettleOperation(ctx context.Context, runID string, op OperationRecord) error
	Operation(ctx context.Context, runID, id string) (OperationRecord, error)
	Operations(ctx context.Context, runID string) ([]OperationRecord, error)
	PutCompletionCheck(ctx context.Context, runID string, check CompletionCheck) error
	ReadTaskSourceInputs(ctx context.Context, runID string) ([]ConversationInput, error)
	ReadTaskOutcome(ctx context.Context, runID string) (TaskOutcome, error)
}

type TaskDatabase interface {
	RecordUsage(ctx context.Context, usage Usage) error
	RunStore
	TaskEvidenceStore

	AcceptCommandBatch(ctx context.Context, commands []Command, fence *ConversationFence) ([]CommandReceipt, error)
	Pending(ctx context.Context, after string) (CommandPage, error)
	Delivered(ctx context.Context, commandID string) error
	Reconcile(ctx context.Context, runID string) (TaskSnapshot, error)

	Incorporate(ctx context.Context, req IncorporateRequest) error
	Guard(ctx context.Context, runID string, revision int64) (Run, error)
	Admit(ctx context.Context, runID string, revision int64, operation string, recoveryUntil time.Time) error
	Project(ctx context.Context, req ProjectRequest) error
	SetProgress(ctx context.Context, runID string, revision int64, text string) error
	ProjectQuestion(ctx context.Context, runID string, revision int64, question Question) error
	Publish(ctx context.Context, req PublishRequest) (Run, error)

	OpenTasks(ctx context.Context) ([]Run, error)
	PublicationTasks(ctx context.Context) ([]Run, error)
	PublicationWoken(ctx context.Context, runID string, generation int64) error
	CleanupTasks(ctx context.Context) ([]Run, error)
	FinishCleanup(ctx context.Context, runID, detail string) error

	RecordTaskStep(ctx context.Context, runID string, step TaskStep) error
	TaskSteps(ctx context.Context, runID string) ([]TaskStep, error)

	GetTaskControl(ctx context.Context, runID string) (TaskControl, error)
	AcquireTaskControl(ctx context.Context, runID, owner string) (TaskControl, TaskFence, error)
	RenewTaskControl(ctx context.Context, runID string, f TaskFence) error
	SaveTaskControl(ctx context.Context, f TaskFence, ctrl TaskControl) (TaskControl, error)
	AdvanceTaskStep(ctx context.Context, f TaskFence, ctrl TaskControl, spend *SpendUpdate) (TaskControl, Run, error)
	ReleaseTaskControl(ctx context.Context, runID string, f TaskFence) error
}

type TaskStore interface {
	TaskDatabase
	Artifacts
}

type Database interface {
	TaskDatabase
	ReadUsageCosts(context.Context, time.Time, time.Time, func(docstore.UsageCostRecord)) error
	ConversationDatabase
	ResourceStore
	EditMemory(ctx context.Context, id string, expectedUpdated int64, text string) (Memory, error)
	ForgetMemory(ctx context.Context, id string, expectedUpdated int64) (Memory, error)
	MutateMemory(ctx context.Context, req MemoryMutationRequest) (MutationReceipt, error)
	SaveMemoryActionReceipt(ctx context.Context, requestID, binding string, resource json.RawMessage) (MutationReceipt, error)
	WorkVersion(ctx context.Context, tasks bool) (string, error)
	Close() error
}

type ConversationDatabase interface {
	AcceptConversationInput(ctx context.Context, id, text string, attachments []string) error
	AcceptConversationInputWithModel(ctx context.Context, id, text string, attachments []string, sel *ModelSelection) error
	CreateUpload(ctx context.Context, upload Upload) (Upload, error)
	CompleteUpload(ctx context.Context, id string) (Upload, error)
	ClaimExpiredUploads(ctx context.Context, pendingBefore, readyBefore time.Time, limit int) ([]string, error)
	FinishExpiredUpload(ctx context.Context, id string) error
	MarkDelivered(ctx context.Context, ids []string) error
	AcquireConversation(ctx context.Context, owner, turnID string) (ConversationControl, *ConversationTurn, error)
	CheckConversation(ctx context.Context, f ConversationFence) (stale bool, err error)
	ConversationCommand(ctx context.Context, id string) (*CommandReceipt, error)
	ConversationTurnsAfter(ctx context.Context, after time.Time) ([]ConversationTurn, error)
	OldestConversationTurnsAfter(ctx context.Context, after time.Time) ([]ConversationTurn, error)
	ConversationSummary(ctx context.Context) (ConversationSummary, error)
	SaveConversationSummary(ctx context.Context, s, expected ConversationSummary) error
	CommitFlushAndCompact(ctx context.Context, req FlushAndCompactRequest) (ConversationSummary, error)
	CommitDreamConsolidate(ctx context.Context, req DreamConsolidateRequest) (ConversationSummary, error)
	Memories(ctx context.Context) ([]Memory, error)
	MemoryLogs(ctx context.Context, afterDate string, limit int) ([]MemoryLog, error)
	MemoryLog(ctx context.Context, date string) (MemoryLog, error)
	SearchMemory(ctx context.Context, query docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error)
	ConversationMessages(ctx context.Context, before string) (messages []Message, next string, err error)
	ConversationProjection(ctx context.Context) (Session, error)
	CancelConversation(ctx context.Context, turnID string) error
	RetryConversation(ctx context.Context, turnID, messageID string) error
	ConversationStatus(ctx context.Context) (ConversationControl, *ConversationTurn, error)
	ConversationTraces(ctx context.Context) ([]ConversationAttempt, error)
	EnsureConversationIntroduction(ctx context.Context, prompt string) error
	PublishConversation(ctx context.Context, f ConversationFence, t ConversationTurn, text string) error
	ReleaseConversation(ctx context.Context, f ConversationFence) error
	RenewConversation(ctx context.Context, f ConversationFence) (stale bool, err error)
	RestartConversation(ctx context.Context, f ConversationFence, t ConversationTurn) (ConversationTurn, time.Duration, error)
	SaveConversation(ctx context.Context, f ConversationFence, t ConversationTurn) error
	UserRoutingPending(ctx context.Context) (bool, error)
	MarkSeen(ctx context.Context, messageID string) error
	ReadPosition(ctx context.Context) (ReadMark, error)

	EventPosition(ctx context.Context) (int64, error)
	WatchEvents(ctx context.Context, after int64, deliver func([]LogEvent) error) error
	ConversationMessage(ctx context.Context, id string) (Message, error)
}

type ConversationStore interface {
	Database
	Artifacts
}
