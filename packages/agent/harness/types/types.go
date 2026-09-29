// Package harnesstypes holds the shared harness data types and interfaces from
// packages/agent/src/harness/types.ts, packages/agent/src/harness/session/types.ts
// and packages/agent/src/harness/runtime/types.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package is a leaf: it depends on the shared AI/agent DTOs and the
// harness leaf helpers, never on a concrete storage, runtime or tool
// implementation. Filesystem and shell operations are encoded as Result values
// so backend failures never panic across the capability boundary.
package harnesstypes

import (
	"encoding/json"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnessresult "github.com/minifish-org/pith/packages/agent/harness/result"
	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// Context is the invocation-scoped harness context.
type Context = harnesscontext.Context

// Result is the shared fallible-operation outcome.
type Result[TValue any, TError any] = harnessresult.Result[TValue, TError]

// Ok creates a successful Result.
func Ok[TValue any, TError any](value TValue) Result[TValue, TError] {
	return harnessresult.Ok[TValue, TError](value)
}

// Err creates a failed Result.
func Err[TValue any, TError any](err TError) Result[TValue, TError] {
	return harnessresult.Err[TValue, TError](err)
}

// GetOrThrow returns the success value or panics with the failure error.
// Intended for tests and explicit adapter boundaries.
func GetOrThrow[TValue any, TError any](result Result[TValue, TError]) TValue {
	if !result.OK {
		panic(result.Error)
	}
	return result.Value
}

// GetOrUndefined returns the success value and true, or the zero value and
// false.
func GetOrUndefined[TValue any, TError any](result Result[TValue, TError]) (TValue, bool) {
	if result.OK {
		return result.Value, true
	}
	var zero TValue
	return zero, false
}

// ToError normalizes an unknown thrown value into an error.
func ToError(value any) error {
	switch typed := value.(type) {
	case nil:
		return nil
	case error:
		return typed
	case string:
		return &plainError{message: typed}
	default:
		if encoded, err := json.Marshal(value); err == nil {
			return &plainError{message: string(encoded)}
		}
		return &plainError{message: "unknown error"}
	}
}

type plainError struct{ message string }

func (e *plainError) Error() string { return e.message }

// Skill is a skill loaded from a SKILL.md file or provided by an application.
type Skill struct {
	Name                   string `json:"name"`
	Description            string `json:"description"`
	Content                string `json:"content"`
	FilePath               string `json:"filePath"`
	DisableModelInvocation *bool  `json:"disableModelInvocation,omitempty"`
}

// PromptTemplate is a model-visible prompt template.
type PromptTemplate struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Content     string  `json:"content"`
}

// AgentHarnessResources are the resources available to explicit invocation.
type AgentHarnessResources[TSkill any, TPromptTemplate any] struct {
	PromptTemplates []TPromptTemplate `json:"promptTemplates,omitempty"`
	Skills          []TSkill          `json:"skills,omitempty"`
}

// Resources is the concrete harness resource bundle.
type Resources = AgentHarnessResources[Skill, PromptTemplate]

// AgentHarnessToolUpdateOptions are the options for one progress update.
type AgentHarnessToolUpdateOptions struct {
	Checkpoint *bool `json:"checkpoint,omitempty"`
}

// AgentHarnessToolUpdateCallback is the synchronous progress callback.
type AgentHarnessToolUpdateCallback[TDetails any] func(partial agenttypes.AgentToolResult[TDetails], options *AgentHarnessToolUpdateOptions)

// AgentHarnessToolInvocation is the durable identity of one logical tool call.
type AgentHarnessToolInvocation interface {
	InvocationID() string
	OperationID() string
	TurnID() string
	GetMemo(name string) (JsonValue, bool, error)
	SetMemo(name string, value JsonValue) error
}

// AgentHarnessTool is a tool definition executed by an AgentHarness.
type AgentHarnessTool[TContext any, TParameters any, TDetails any] struct {
	Tool             aitypes.Tool
	Label            string
	PrepareArguments func(args any) (TParameters, error)
	Replay           string
	ExecutionMode    agenttypes.ToolExecutionMode
	Execute          func(
		toolCallId string,
		params TParameters,
		onUpdate AgentHarnessToolUpdateCallback[TDetails],
		toolContext TContext,
		invocation AgentHarnessToolInvocation,
		ctx Context,
	) (agenttypes.AgentToolResult[TDetails], error)
}

// AgentHarnessToolContextSource is a static or provider-resolved tool context.
type AgentHarnessToolContextSource[TContext any] struct {
	Static  *TContext
	Resolve func(ctx Context) (TContext, error)
}

// AgentHarnessStreamOptions are the curated provider request options.
type AgentHarnessStreamOptions struct {
	Transport       *aitypes.Transport       `json:"transport,omitempty"`
	TimeoutMs       *int                     `json:"timeoutMs,omitempty"`
	MaxRetries      *int                     `json:"maxRetries,omitempty"`
	MaxRetryDelayMs *int                     `json:"maxRetryDelayMs,omitempty"`
	Headers         map[string]string        `json:"headers,omitempty"`
	Metadata        map[string]any           `json:"metadata,omitempty"`
	CacheRetention  *aitypes.CacheRetention  `json:"cacheRetention,omitempty"`
	Deferred        *aitypes.DeferredRequest `json:"deferred,omitempty"`
}

// AgentHarnessStreamOptionsPatch is a per-request stream option patch. A nil
// value deletes the key; a nil map clears all keys.
type AgentHarnessStreamOptionsPatch struct {
	Transport       *aitypes.Transport       `json:"transport,omitempty"`
	TimeoutMs       *int                     `json:"timeoutMs,omitempty"`
	MaxRetries      *int                     `json:"maxRetries,omitempty"`
	MaxRetryDelayMs *int                     `json:"maxRetryDelayMs,omitempty"`
	Headers         map[string]*string       `json:"headers,omitempty"`
	Metadata        map[string]any           `json:"metadata,omitempty"`
	CacheRetention  *aitypes.CacheRetention  `json:"cacheRetention,omitempty"`
	Deferred        *aitypes.DeferredRequest `json:"deferred,omitempty"`
}

// FileKind is the kind of filesystem object.
type FileKind string

// File object kinds.
const (
	FileKindFile      FileKind = "file"
	FileKindDirectory FileKind = "directory"
	FileKindSymlink   FileKind = "symlink"
)

// FileErrorCode is a backend-independent file error code.
type FileErrorCode string

// File error codes.
const (
	FileErrorAborted          FileErrorCode = "aborted"
	FileErrorNotFound         FileErrorCode = "not_found"
	FileErrorPermissionDenied FileErrorCode = "permission_denied"
	FileErrorNotDirectory     FileErrorCode = "not_directory"
	FileErrorIsDirectory      FileErrorCode = "is_directory"
	FileErrorInvalid          FileErrorCode = "invalid"
	FileErrorNotSupported     FileErrorCode = "not_supported"
	FileErrorUnknown          FileErrorCode = "unknown"
)

// FileError is returned by FileSystem operations.
type FileError struct {
	Code    FileErrorCode `json:"code"`
	Message string        `json:"message"`
	Path    *string       `json:"path,omitempty"`
	Cause   error         `json:"-"`
}

func (e *FileError) Error() string { return e.Message }
func (e *FileError) Unwrap() error { return e.Cause }

// NewFileError builds a file error.
func NewFileError(code FileErrorCode, message string, path *string, cause error) *FileError {
	return &FileError{Code: code, Message: message, Path: path, Cause: cause}
}

// ExecutionErrorCode is a backend-independent execution error code.
type ExecutionErrorCode string

// Execution error codes.
const (
	ExecutionErrorAborted          ExecutionErrorCode = "aborted"
	ExecutionErrorTimeout          ExecutionErrorCode = "timeout"
	ExecutionErrorShellUnavailable ExecutionErrorCode = "shell_unavailable"
	ExecutionErrorSpawn            ExecutionErrorCode = "spawn_error"
	ExecutionErrorCallback         ExecutionErrorCode = "callback_error"
	ExecutionErrorUnknown          ExecutionErrorCode = "unknown"
)

// ExecutionError is returned by ExecutionEnv exec.
type ExecutionError struct {
	Code    ExecutionErrorCode `json:"code"`
	Message string             `json:"message"`
	Cause   error              `json:"-"`
}

func (e *ExecutionError) Error() string { return e.Message }
func (e *ExecutionError) Unwrap() error { return e.Cause }

// NewExecutionError builds an execution error.
func NewExecutionError(code ExecutionErrorCode, message string, cause error) *ExecutionError {
	return &ExecutionError{Code: code, Message: message, Cause: cause}
}

// CompactionErrorCode is a stable compaction error code.
type CompactionErrorCode string

// Compaction error codes.
const (
	CompactionErrorAborted             CompactionErrorCode = "aborted"
	CompactionErrorSummarizationFailed CompactionErrorCode = "summarization_failed"
)

// CompactionError is returned by compaction helpers.
type CompactionError struct {
	Code    CompactionErrorCode `json:"code"`
	Message string              `json:"message"`
	Cause   error               `json:"-"`
}

func (e *CompactionError) Error() string { return e.Message }
func (e *CompactionError) Unwrap() error { return e.Cause }

// NewCompactionError builds a compaction error.
func NewCompactionError(code CompactionErrorCode, message string, cause error) *CompactionError {
	return &CompactionError{Code: code, Message: message, Cause: cause}
}

// BranchSummaryErrorCode is a stable branch-summary error code.
type BranchSummaryErrorCode string

// Branch summary error codes.
const (
	BranchSummaryErrorAborted             BranchSummaryErrorCode = "aborted"
	BranchSummaryErrorSummarizationFailed BranchSummaryErrorCode = "summarization_failed"
)

// BranchSummaryError is returned by branch summarization helpers.
type BranchSummaryError struct {
	Code    BranchSummaryErrorCode `json:"code"`
	Message string                 `json:"message"`
	Cause   error                  `json:"-"`
}

func (e *BranchSummaryError) Error() string { return e.Message }
func (e *BranchSummaryError) Unwrap() error { return e.Cause }

// NewBranchSummaryError builds a branch summary error.
func NewBranchSummaryError(code BranchSummaryErrorCode, message string, cause error) *BranchSummaryError {
	return &BranchSummaryError{Code: code, Message: message, Cause: cause}
}

// FileInfo is the metadata of one filesystem object.
type FileInfo struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Kind    FileKind `json:"kind"`
	Size    int      `json:"size"`
	MtimeMs float64  `json:"mtimeMs"`
}

// TextLine is one UTF-8 line read from a text file.
type TextLine struct {
	Text       string `json:"text"`
	Terminated bool   `json:"terminated"`
}

// TextLineReader is a pull-based UTF-8 line reader.
type TextLineReader interface {
	ReadLine(ctx Context) Result[*TextLine, FileError]
	Close(ctx Context)
}

// FileSystem is the filesystem capability used by the harness. Operation
// methods never throw: failures are encoded in the returned Result.
type FileSystem interface {
	Cwd() string
	AbsolutePath(path string, ctx Context) Result[string, FileError]
	JoinPath(parts []string, ctx Context) Result[string, FileError]
	ReadTextFile(path string, ctx Context) Result[string, FileError]
	OpenTextLineReader(path string, ctx Context) Result[TextLineReader, FileError]
	ReadTextLines(path string, options *ReadTextLinesOptions, ctx Context) Result[[]string, FileError]
	ReadBinaryFile(path string, ctx Context) Result[[]byte, FileError]
	WriteFile(path string, content []byte, ctx Context) Result[struct{}, FileError]
	AppendFile(path string, content []byte, ctx Context) Result[struct{}, FileError]
	RenameFile(sourcePath string, destinationPath string, ctx Context) Result[struct{}, FileError]
	FileInfo(path string, ctx Context) Result[FileInfo, FileError]
	ListDir(path string, ctx Context) Result[[]FileInfo, FileError]
	CanonicalPath(path string, ctx Context) Result[string, FileError]
	Exists(path string, ctx Context) Result[bool, FileError]
	CreateDir(path string, options *CreateDirOptions, ctx Context) Result[struct{}, FileError]
	Remove(path string, options *RemoveOptions, ctx Context) Result[struct{}, FileError]
	CreateTempDir(prefix *string, ctx Context) Result[string, FileError]
	CreateTempFile(options *CreateTempFileOptions, ctx Context) Result[string, FileError]
	Cleanup(ctx Context)
}

// ReadTextLinesOptions bounds a text line read.
type ReadTextLinesOptions struct {
	MaxLines *int `json:"maxLines,omitempty"`
}

// CreateDirOptions configures directory creation.
type CreateDirOptions struct {
	Recursive *bool `json:"recursive,omitempty"`
}

// RemoveOptions configures removal.
type RemoveOptions struct {
	Recursive *bool `json:"recursive,omitempty"`
	Force     *bool `json:"force,omitempty"`
}

// CreateTempFileOptions configures temporary file creation.
type CreateTempFileOptions struct {
	Prefix *string `json:"prefix,omitempty"`
	Suffix *string `json:"suffix,omitempty"`
}

// ShellOutputRetention selects which portion of bounded output survives.
type ShellOutputRetention string

// Shell output retention.
const (
	ShellOutputHead ShellOutputRetention = "head"
	ShellOutputTail ShellOutputRetention = "tail"
)

// ShellOutputLimits are the source-side limits for one combined output view.
type ShellOutputLimits struct {
	MaxBytes int                   `json:"maxBytes"`
	MaxLines int                   `json:"maxLines"`
	Retain   *ShellOutputRetention `json:"retain,omitempty"`
}

// ShellOutputCaptureOptions is the bounded shell capture request.
type ShellOutputCaptureOptions struct {
	Limits ShellOutputLimits `json:"limits"`
	Spill  *bool             `json:"spill,omitempty"`
}

// ShellOutputTruncation is truncation metadata without the retained text.
type ShellOutputTruncation struct {
	Truncated             bool    `json:"truncated"`
	TruncatedBy           *string `json:"truncatedBy"`
	TotalLines            int     `json:"totalLines"`
	TotalBytes            int     `json:"totalBytes"`
	OutputLines           int     `json:"outputLines"`
	OutputBytes           int     `json:"outputBytes"`
	LastLinePartial       bool    `json:"lastLinePartial"`
	FirstLineExceedsLimit bool    `json:"firstLineExceedsLimit"`
	MaxLines              int     `json:"maxLines"`
	MaxBytes              int     `json:"maxBytes"`
}

// ShellOutputTruncationFrom projects a truncation result onto its metadata.
func ShellOutputTruncationFrom(result truncate.TruncationResult) ShellOutputTruncation {
	return ShellOutputTruncation{
		Truncated:             result.Truncated,
		TruncatedBy:           result.TruncatedBy,
		TotalLines:            result.TotalLines,
		TotalBytes:            result.TotalBytes,
		OutputLines:           result.OutputLines,
		OutputBytes:           result.OutputBytes,
		LastLinePartial:       result.LastLinePartial,
		FirstLineExceedsLimit: result.FirstLineExceedsLimit,
		MaxLines:              result.MaxLines,
		MaxBytes:              result.MaxBytes,
	}
}

// ShellOutputMetadata accompanies a bounded output view.
type ShellOutputMetadata struct {
	Truncation    ShellOutputTruncation `json:"truncation"`
	SpillPath     *string               `json:"spillPath,omitempty"`
	LastLineBytes *int                  `json:"lastLineBytes,omitempty"`
}

// ShellOutputView is a complete bounded output view.
type ShellOutputView struct {
	ShellOutputMetadata
	Text string `json:"text"`
}

// ShellOutputUpdate is an incremental change to a bounded output view.
type ShellOutputUpdate struct {
	Kind     string               `json:"kind"`
	Output   *ShellOutputView     `json:"output,omitempty"`
	Text     *string              `json:"text,omitempty"`
	Metadata *ShellOutputMetadata `json:"metadata,omitempty"`
	Drop     *int                 `json:"drop,omitempty"`
}

// Shell output update kinds.
const (
	ShellOutputUpdateReplace  = "replace"
	ShellOutputUpdateAppend   = "append"
	ShellOutputUpdateSlide    = "slide"
	ShellOutputUpdateMetadata = "metadata"
)

// ShellExecResult is a bounded shell completion.
type ShellExecResult struct {
	ShellOutputMetadata
	ExitCode int `json:"exitCode"`
}

// ShellExecOptions are the options for Shell.Exec.
type ShellExecOptions struct {
	Cwd        *string                                     `json:"cwd,omitempty"`
	Env        map[string]string                           `json:"env,omitempty"`
	InheritEnv *bool                                       `json:"inheritEnv,omitempty"`
	Timeout    *float64                                    `json:"timeout,omitempty"`
	Capture    *ShellOutputCaptureOptions                  `json:"capture,omitempty"`
	OnUpdate   func(update ShellOutputUpdate, ctx Context) `json:"-"`
}

// Shell is the shell execution capability used by the harness.
type Shell interface {
	Exec(command string, options *ShellExecOptions, ctx Context) Result[ShellExecResult, ExecutionError]
	Cleanup(ctx Context)
}

// ExecutionEnv is the filesystem and process execution environment.
type ExecutionEnv interface {
	FileSystem
	Shell
}

// ModelIdentity names a provider/model pair.
type ModelIdentity struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// CompactionSettings are the compaction thresholds and retention settings.
type CompactionSettings struct {
	Enabled          bool `json:"enabled"`
	ReserveTokens    int  `json:"reserveTokens"`
	KeepRecentTokens int  `json:"keepRecentTokens"`
}

// DefaultCompactionSettings are the upstream defaults.
var DefaultCompactionSettings = CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000}

// FileOperations are the file operations extracted from summarized history.
type FileOperations struct {
	Read     []string `json:"read"`
	Written  []string `json:"written"`
	Edited   []string `json:"edited"`
	Modified []string `json:"modified"`
}

// CompactionPreparation are the prepared inputs for a compaction run.
type CompactionPreparation struct {
	MessagesToSummarize []agenttypes.AgentMessage `json:"messagesToSummarize"`
	TurnPrefixMessages  []agenttypes.AgentMessage `json:"turnPrefixMessages"`
	RetainedTail        []agenttypes.AgentMessage `json:"retainedTail"`
	IsSplitTurn         bool                      `json:"isSplitTurn"`
	TokensBefore        float64                   `json:"tokensBefore"`
	PreviousSummary     *string                   `json:"previousSummary,omitempty"`
	FileOps             FileOperations            `json:"fileOps"`
	Settings            CompactionSettings        `json:"settings"`
}

// BranchPreparation is the prepared branch content for summarization.
type BranchPreparation struct {
	Messages    []agenttypes.AgentMessage `json:"messages"`
	FileOps     FileOperations            `json:"fileOps"`
	TotalTokens float64                   `json:"totalTokens"`
}

// RetryPolicy is the bounded-attempt retry policy shared with the AI layer.
type RetryPolicy = aiutils.RetryPolicy

// DriveOptions are the request options for one installed drive pass.
type DriveOptions struct {
	OperationID  string `json:"operationId"`
	WaitForRetry *bool  `json:"waitForRetry,omitempty"`
	PollDeferred *bool  `json:"pollDeferred,omitempty"`
}

// DriveOutcome is the settled or waiting outcome of one drive pass.
type DriveOutcome struct {
	Kind        string                  `json:"kind"`
	Outcome     *OperationResultRecord  `json:"outcome,omitempty"`
	OperationID string                  `json:"operationId,omitempty"`
	Reason      string                  `json:"reason,omitempty"`
	NotBefore   *float64                `json:"notBefore,omitempty"`
	Deferred    *aitypes.DeferredHandle `json:"deferred,omitempty"`
}

// HarnessEvent is one observable harness lifecycle event.
type HarnessEvent struct {
	Type        string          `json:"type"`
	Lane        *string         `json:"lane,omitempty"`
	OperationID *string         `json:"operationId,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// AgentHarnessOptions are the process-local harness construction options.
type AgentHarnessOptions[TContext any] struct {
	Session            AnySession
	Models             any
	Model              *aitypes.Model
	ThinkingLevel      *agenttypes.ThinkingLevel
	ActiveToolNames    []string
	Tools              []AgentHarnessTool[TContext, any, any]
	ToolContext        *AgentHarnessToolContextSource[TContext]
	SystemPrompt       *string
	SystemPromptFn     func(toolContext TContext, ctx Context) (string, error)
	Resources          *Resources
	StreamOptions      *AgentHarnessStreamOptions
	Retry              *RetryPolicy
	Compaction         *CompactionSettings
	SteeringMode       *agenttypes.QueueMode
	FollowUpMode       *agenttypes.QueueMode
	ToolExecution      *agenttypes.ToolExecutionMode
	ToProviderMessages func(messages []agenttypes.AgentMessage, ctx Context) ([]aitypes.Message, error)
	EntryProjectors    map[string]EntryProjector
}

// AnySession is the type-erased session handle used by the options.
type AnySession = Session[SessionMetadata]
