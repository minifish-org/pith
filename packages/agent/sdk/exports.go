// Agent SDK facade and native CLI driver.
//
// This is a Go port of packages/agent/src/index.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// index.ts is the assembled agent entry point: it re-exports the telemetry
// vocabulary, the compaction helpers, the harness session/runtime types, the
// concrete tool set and the streaming helpers. Go has no barrel re-export, so
// this file exposes typed aliases that resolve to the real declarations and
// adds the native runner used by cmd/pith. The runner drives the completed AI
// layer and the built-in tools; it never reaches into a provider-specific
// implementation.
package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/minifish-org/pith/packages/agent"
	harnessagent "github.com/minifish-org/pith/packages/agent/harness"
	"github.com/minifish-org/pith/packages/agent/harness/compaction"
	harnessskills "github.com/minifish-org/pith/packages/agent/harness/resources"
	harnessresult "github.com/minifish-org/pith/packages/agent/harness/result"
	harnessruntime "github.com/minifish-org/pith/packages/agent/harness/runtime"
	harnessmemory "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesshtelemetry "github.com/minifish-org/pith/packages/agent/harness/telemetry"
	"github.com/minifish-org/pith/packages/agent/harness/tools"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	harnessoutputcapture "github.com/minifish-org/pith/packages/agent/harness/utils/output_capture"
	harnesssearch "github.com/minifish-org/pith/packages/agent/search"
	"github.com/minifish-org/pith/packages/ai/api"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
	"github.com/minifish-org/pith/packages/telemetry"
)

// ---------------------------------------------------------------------------
// Re-exported telemetry vocabulary (packages/agent/src/index.ts)
// ---------------------------------------------------------------------------

// AttributeValue is a telemetry attribute value.
type AttributeValue = telemetry.AttributeValue

// ExactTelemetryAttributes is a telemetry attribute map.
type ExactTelemetryAttributes = telemetry.ExactTelemetryAttributes

// InferEventAttributes infers the event attribute type of a schema.
type InferEventAttributes = telemetry.InferEventAttributes

// InferOptionalAttributes infers optional attributes.
type InferOptionalAttributes = telemetry.InferOptionalAttributes

// InferRequiredAndOptionalAttributes infers required and optional attributes.
type InferRequiredAndOptionalAttributes = telemetry.InferRequiredAndOptionalAttributes

// InferStartAttributes infers span-start attributes.
type InferStartAttributes = telemetry.InferStartAttributes

// RecordedTelemetryEvent is a recorded telemetry event.
type RecordedTelemetryEvent = telemetry.RecordedTelemetryEvent

// RecordedTelemetrySpan is a recorded telemetry span.
type RecordedTelemetrySpan = telemetry.RecordedTelemetrySpan

// SchemaTelemetrySpan is a schema-derived telemetry span.
type SchemaTelemetrySpan = telemetry.SchemaTelemetrySpan

// SpanAttributes is a span attribute map.
type SpanAttributes = telemetry.SpanAttributes

// TelemetrySpanAttributes is the upstream alias of SpanAttributes.
type TelemetrySpanAttributes = telemetry.SpanAttributes

// SpanOptions are span-start options.
type SpanOptions = telemetry.SpanOptions

// SpanStatus is a span terminal status.
type SpanStatus = telemetry.SpanStatus

// TelemetryAttributeDefinition describes one telemetry attribute.
type TelemetryAttributeDefinition = telemetry.TelemetryAttributeDefinition

// TelemetryAttributeMetadata is attribute metadata.
type TelemetryAttributeMetadata = telemetry.TelemetryAttributeMetadata

// TelemetryAttributeType is the attribute value type discriminator.
type TelemetryAttributeType = harnesshtelemetry.TelemetryAttributeType

// TelemetryContext is the telemetry context interface.
type TelemetryContext = telemetry.TelemetryContext

// TelemetryEventAttributeDefinition describes an event attribute.
type TelemetryEventAttributeDefinition = telemetry.TelemetryEventAttributeDefinition

// TelemetryEventDefinition describes a telemetry event.
type TelemetryEventDefinition = telemetry.TelemetryEventDefinition

// TelemetryParentDefinition describes a span parent.
type TelemetryParentDefinition = telemetry.TelemetryParentDefinition

// TelemetrySchemaDefinition is a telemetry schema.
type TelemetrySchemaDefinition = telemetry.TelemetrySchemaDefinition

// TelemetrySchemaSpanEndAttributes resolves span-end attributes from a schema.
var TelemetrySchemaSpanEndAttributes = telemetry.TelemetrySchemaSpanEndAttributes

// TelemetrySchemaSpanEventAttributes resolves span-event attributes from a schema.
var TelemetrySchemaSpanEventAttributes = telemetry.TelemetrySchemaSpanEventAttributes

// TelemetrySchemaSpanEventName is a span event name.
type TelemetrySchemaSpanEventName = telemetry.TelemetrySchemaSpanEventName

// TelemetrySchemaSpanName is a span name.
type TelemetrySchemaSpanName = telemetry.TelemetrySchemaSpanName

// TelemetrySchemaSpanStartAttributes resolves span-start attributes from a schema.
var TelemetrySchemaSpanStartAttributes = telemetry.TelemetrySchemaSpanStartAttributes

// TelemetrySchemaSpanUnion is the span union of a schema.
type TelemetrySchemaSpanUnion = telemetry.TelemetrySchemaSpanUnion

// TelemetrySpan is a live telemetry span.
type TelemetrySpan = telemetry.TelemetrySpan

// TelemetrySpanDefinition describes a span.
type TelemetrySpanDefinition = telemetry.TelemetrySpanDefinition

// TelemetryStartAttributeDefinition describes a span-start attribute.
type TelemetryStartAttributeDefinition = telemetry.TelemetryStartAttributeDefinition

// TypedSpanStarter starts typed spans for a schema.
type TypedSpanStarter = telemetry.TypedSpanStarter

// CreateTypedSpanStarter builds a typed span starter.
var CreateTypedSpanStarter = telemetry.CreateTypedSpanStarter

// DefineTelemetrySchema defines a telemetry schema.
var DefineTelemetrySchema = telemetry.DefineTelemetrySchema

// InMemoryTelemetryContext is the in-memory telemetry context.
type InMemoryTelemetryContext = telemetry.InMemoryTelemetryContext

// NOOP_TELEMETRY_CONTEXT is the shared no-op telemetry context.
var NOOP_TELEMETRY_CONTEXT = telemetry.NOOP_TELEMETRY_CONTEXT

// ---------------------------------------------------------------------------
// Re-exported tool declarations (packages/agent/src/harness/tools/index.ts)
// ---------------------------------------------------------------------------

// BashExecution is the mutable bash command plan.
type BashExecution = tools.BashExecution

// BashPrepare customizes a bash command before execution.
type BashPrepare = tools.BashPrepare

// BashToolDetails is the bash truncation metadata.
type BashToolDetails = tools.BashToolDetails

// BashToolInput is the parsed bash tool input.
type BashToolInput = tools.BashToolInput

// BashToolOptions configures the bash tool.
type BashToolOptions = tools.BashToolOptions

// EditToolDetails is the edit diff metadata.
type EditToolDetails = tools.EditToolDetails

// EditToolInput is the parsed edit tool input.
type EditToolInput = tools.EditToolInput

// ReadImageProcessor converts an image before attachment.
type ReadImageProcessor = tools.ReadImageProcessor

// ReadImageProcessorResult is the image processor outcome.
type ReadImageProcessorResult = tools.ReadImageProcessorResult

// ReadToolDetails is the read truncation metadata.
type ReadToolDetails = tools.ReadToolDetails

// ReadToolInput is the parsed read tool input.
type ReadToolInput = tools.ReadToolInput

// ReadToolOptions configures the read tool.
type ReadToolOptions = tools.ReadToolOptions

// ExecutionToolContext is the filesystem and shell tool context.
type ExecutionToolContext = tools.ExecutionToolContext

// WriteToolInput is the parsed write tool input.
type WriteToolInput = tools.WriteToolInput

// CreateBashTool builds the bash tool.
var CreateBashTool = tools.CreateBashTool

// CreateEditTool builds the edit tool.
var CreateEditTool = tools.CreateEditTool

// CreateReadTool builds the read tool.
var CreateReadTool = tools.CreateReadTool

// CreateWriteTool builds the write tool.
var CreateWriteTool = tools.CreateWriteTool

// BuiltinTool is the type-erased built-in tool registration entry.
type BuiltinTool = tools.BuiltinTool

// CreateBuiltinTools builds the four built-in execution tools.
var CreateBuiltinTools = tools.CreateBuiltinTools

// ---------------------------------------------------------------------------
// Re-exported compaction helpers
// ---------------------------------------------------------------------------

// BranchPreparation describes a prepared branch summary.
type BranchPreparation = compaction.BranchPreparation

// BranchSummaryDetails carries summary metadata.
type BranchSummaryDetails = compaction.BranchSummaryDetails

// BranchSummaryResult is a generated branch summary.
type BranchSummaryResult = compaction.BranchSummaryResult

// CollectEntriesResult is the branch-entry collection outcome.
type CollectEntriesResult = compaction.CollectEntriesResult

// FileOperations records file operations for a summary.
type FileOperations = compaction.FileOperations

// GenerateBranchSummaryOptions configures branch summarization.
type GenerateBranchSummaryOptions = compaction.GenerateBranchSummaryOptions

// CompactionPreparation describes a prepared compaction.
type CompactionPreparation = compaction.CompactionPreparation

// CompactionSettings configure compaction.
type CompactionSettings = compaction.CompactionSettings

// CompactResult is a generated compaction result.
type CompactResult = compaction.CompactResult

// DefaultCompactionSettings are the upstream default compaction settings.
var DefaultCompactionSettings = compaction.DefaultCompactionSettings

// CollectEntriesForBranchSummary collects entries for a branch summary.
var CollectEntriesForBranchSummary = compaction.CollectEntriesForBranchSummary

// GenerateBranchSummary generates a branch summary.
var GenerateBranchSummary = compaction.GenerateBranchSummary

// PrepareBranchEntries prepares branch entries.
var PrepareBranchEntries = compaction.PrepareBranchEntries

// CalculateContextTokens calculates the context token count.
var CalculateContextTokens = compaction.CalculateContextTokens

// Compact compacts a conversation.
var Compact = compaction.Compact

// EstimateContextTokens estimates the context token count.
var EstimateContextTokens = compaction.EstimateContextTokens

// EstimateTokens estimates tokens for a message.
var EstimateTokens = compaction.EstimateTokens

// FindCutPoint finds the compaction cut point.
var FindCutPoint = compaction.FindCutPoint

// FindTurnStartIndex finds the turn-start index.
var FindTurnStartIndex = compaction.FindTurnStartIndex

// GenerateSummary generates a compaction summary.
var GenerateSummary = compaction.GenerateSummary

// GenerateSummaryWithUsage generates a summary and reports usage.
var GenerateSummaryWithUsage = compaction.GenerateSummaryWithUsage

// GetLastAssistantUsage returns the last assistant usage.
var GetLastAssistantUsage = compaction.GetLastAssistantUsage

// PrepareCompaction prepares a compaction.
var PrepareCompaction = compaction.PrepareCompaction

// SerializeConversation serializes a conversation for summarization.
var SerializeConversation = compaction.SerializeConversation

// ShouldCompact reports whether compaction is required.
var ShouldCompact = compaction.ShouldCompact

// ---------------------------------------------------------------------------
// Re-exported harness runtime, session and resource helpers
// ---------------------------------------------------------------------------

// LaneSnapshotReduction is a reducer outcome.
type LaneSnapshotReduction = harnessruntime.LaneSnapshotReduction

// ReduceLaneSnapshot reduces a lane snapshot.
var ReduceLaneSnapshot = harnessruntime.ReduceLaneSnapshot

// AgentHarness is the assembled durable harness.
type AgentHarness = harnessagent.AgentHarness

// CreateAgentHarness builds the durable harness.
var CreateAgentHarness = harnessagent.CreateAgentHarness

// Session is a durable session.
type Session = harnesstypes.Session[harnesstypes.SessionMetadata]

// SessionMetadata is session metadata.
type SessionMetadata = harnesstypes.SessionMetadata

// MemorySessionRepo is the in-memory session repository.
type MemorySessionRepo = harnessmemory.MemorySessionRepo

// MemorySessionRepoOptions configure the in-memory session repo.
type MemorySessionRepoOptions = harnessmemory.MemorySessionRepoOptions

// NewMemorySessionRepo builds an in-memory session repo.
var NewMemorySessionRepo = harnessmemory.NewMemorySessionRepo

// HarnessEvent is a harness lifecycle event.
type HarnessEvent = harnesstypes.HarnessEvent

// SearchQuery is a session search query.
type SearchQuery = harnesssearch.SearchQuery

// SessionSearchHit is one session search result.
type SessionSearchHit = harnesssearch.SessionSearchHit

// Skill is a loaded skill.
type Skill = harnesstypes.Skill

// PromptTemplate is a model-visible prompt template.
type PromptTemplate = harnesstypes.PromptTemplate

// LoadSkills loads skills from disk.
var LoadSkills = harnessskills.LoadSkills

// LoadPromptTemplates loads prompt templates from disk.
var LoadPromptTemplates = harnessskills.LoadPromptTemplates

// ---------------------------------------------------------------------------
// Re-exported execution environment contracts
// ---------------------------------------------------------------------------

// ExecutionEnv is the injected execution environment.
type ExecutionEnv = harnesstypes.ExecutionEnv

// ExecutionError is a tagged execution failure.
type ExecutionError = harnesstypes.ExecutionError

// ExecutionErrorCode is an execution error code.
type ExecutionErrorCode = harnesstypes.ExecutionErrorCode

// FileError is a tagged file failure.
type FileError = harnesstypes.FileError

// FileErrorCode is a file error code.
type FileErrorCode = harnesstypes.FileErrorCode

// FileInfo describes a filesystem object.
type FileInfo = harnesstypes.FileInfo

// FileKind is a filesystem object kind.
type FileKind = harnesstypes.FileKind

// FileSystem is the filesystem protocol.
type FileSystem = harnesstypes.FileSystem

// Shell is the shell protocol.
type Shell = harnesstypes.Shell

// ShellExecOptions configure a shell execution.
type ShellExecOptions = harnesstypes.ShellExecOptions

// ShellExecResult is a shell execution result.
type ShellExecResult = harnesstypes.ShellExecResult

// ShellOutputCaptureOptions configure output capture.
type ShellOutputCaptureOptions = harnesstypes.ShellOutputCaptureOptions

// ShellOutputLimits bound retained shell output.
type ShellOutputLimits = harnesstypes.ShellOutputLimits

// ShellOutputMetadata is shell output metadata.
type ShellOutputMetadata = harnesstypes.ShellOutputMetadata

// ShellOutputRetention selects the retained region.
type ShellOutputRetention = harnesstypes.ShellOutputRetention

// ShellOutputTruncation is shell output truncation metadata.
type ShellOutputTruncation = harnesstypes.ShellOutputTruncation

// ShellOutputUpdate is an incremental shell output update.
type ShellOutputUpdate = harnesstypes.ShellOutputUpdate

// ShellOutputView is a bounded shell output view.
type ShellOutputView = harnesstypes.ShellOutputView

// Result is the shared fallible-operation carrier.
type Result[TValue any, TError any] = harnessresult.Result[TValue, TError]

// Ok builds a successful Result.
var Ok = harnesstypes.Ok[any, any]

// Err builds a failed Result.
var Err = harnesstypes.Err[any, any]

// GetOrThrow unwraps a Result or panics.
var GetOrThrow = harnesstypes.GetOrThrow[any, any]

// GetOrUndefined unwraps a Result or reports absence.
var GetOrUndefined = harnesstypes.GetOrUndefined[any, any]

// ToError normalizes an unknown thrown value.
var ToError = harnesstypes.ToError

// ApplyShellOutputUpdate applies one shell output update.
var ApplyShellOutputUpdate = harnessoutputcapture.ApplyShellOutputUpdate

// SetDefaultStreamFn installs the process-wide default stream function.
var SetDefaultStreamFn = agent.SetDefaultStreamFn

// UUIDv7 generates a time-ordered UUIDv7 string.
var UUIDv7 = aiutils.UUIDv7

// ---------------------------------------------------------------------------
// Native runner
// ---------------------------------------------------------------------------

// DefaultSystemPrompt is the instruction prepended to every fresh transcript.
const DefaultSystemPrompt = "You are a coding agent. Use the provided tools to complete the user's task."

// DefaultMaxTokens bounds a single model response when the caller gives no
// explicit limit.
const DefaultMaxTokens = 4096

// RunOptions configure one native agent run.
type RunOptions struct {
	// Context cancels the run. Nil uses context.Background.
	Context context.Context
	// BaseURL is the OpenAI-compatible API root, e.g. https://host/v1.
	BaseURL string
	// Model is the model identifier sent on the wire.
	Model string
	// APIKey authenticates the request.
	APIKey string
	// Cwd is the working directory of the injected execution environment.
	Cwd string
	// SessionPath, when set, is a JSON-lines conversation file that is loaded
	// before the run and rewritten after every turn.
	SessionPath string
	// Prompt is the user message appended for this run.
	Prompt string
	// SystemPrompt overrides DefaultSystemPrompt.
	SystemPrompt string
	// MaxTokens overrides DefaultMaxTokens.
	MaxTokens int
	// Stdout receives the assistant text. Nil discards it.
	Stdout io.Writer
	// Stderr receives progress diagnostics. Nil discards them.
	Stderr io.Writer
}

// Run drives one assistant turn loop against an OpenAI-compatible Chat
// Completions endpoint and the built-in read/write/edit/bash tools.
//
// The loop appends the user prompt to the loaded history, streams an assistant
// message, executes every requested tool, appends the ordered tool results and
// repeats until the assistant returns no tool calls. Assistant text is written
// to Stdout as it is produced; credentials never reach the output.
func Run(options RunOptions) error {
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	stdout := options.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	if strings.TrimSpace(options.BaseURL) == "" {
		return fmt.Errorf("missing base URL")
	}
	if strings.TrimSpace(options.Model) == "" {
		return fmt.Errorf("missing model")
	}
	if options.APIKey == "" {
		return fmt.Errorf("missing API key")
	}

	history, err := LoadSession(options.SessionPath)
	if err != nil {
		return err
	}

	if options.Cwd != "" {
		if info, statErr := os.Stat(options.Cwd); statErr != nil || !info.IsDir() {
			return fmt.Errorf("invalid cwd %q", options.Cwd)
		}
	}
	env := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: options.Cwd})

	builtins := tools.CreateBuiltinTools()
	declarations := make([]aitypes.Tool, 0, len(builtins))
	byName := make(map[string]tools.BuiltinTool, len(builtins))
	for _, builtin := range builtins {
		declarations = append(declarations, builtin.Tool)
		byName[builtin.Tool.Name] = builtin
	}

	systemPrompt := options.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = DefaultSystemPrompt
	}
	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	now := float64(time.Now().UnixMilli())
	system := aitypes.NewSystemMessage(systemPrompt, now)
	system.ToolsAdded = declarations
	user := aitypes.NewUserMessage(options.Prompt, now)

	transcript := make([]aitypes.Message, 0, len(history)+2)
	transcript = append(transcript, aitypes.NewSystemMessageVariant(system))
	transcript = append(transcript, history...)
	transcript = append(transcript, aitypes.NewUserMessageVariant(user))

	model := &aitypes.Model{
		Id:            options.Model,
		Name:          options.Model,
		Api:           aitypes.ApiOpenAICompletions,
		Provider:      aitypes.ProviderOpenAI,
		BaseUrl:       options.BaseURL,
		Input:         []aitypes.ModelInputModality{aitypes.ModelInputText, aitypes.ModelInputImage},
		ContextWindow: 128000,
		MaxTokens:     float64(maxTokens),
	}

	apiKey := options.APIKey
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		streamOptions := &aitypes.SimpleStreamOptions{
			StreamOptions: aitypes.StreamOptions{
				ProviderRequestOptions: aitypes.ProviderRequestOptions{APIKey: &apiKey},
			},
		}
		stream := api.OpenAICompletionsStreamSimple(model, aitypes.NewTranscriptContext(transcript), streamOptions)
		assistant, streamErr := stream.Result(ctx)
		if streamErr != nil {
			return streamErr
		}
		if assistant.StopReason == aitypes.StopReasonError || assistant.StopReason == aitypes.StopReasonAborted {
			if assistant.ErrorMessage != nil {
				return fmt.Errorf("%s", *assistant.ErrorMessage)
			}
			return fmt.Errorf("assistant stream stopped: %s", assistant.StopReason)
		}

		writeAssistantText(stdout, assistant)

		assistantForHistory := assistant
		assistantForHistory.Role = aitypes.AssistantMessageRole
		transcript = append(transcript, aitypes.NewAssistantMessageVariant(assistantForHistory))

		calls := collectToolCalls(assistant)
		if len(calls) == 0 {
			break
		}
		for _, call := range calls {
			resultMessage := executeToolCall(ctx, byName, env, call)
			transcript = append(transcript, aitypes.NewToolResultMessageVariant(resultMessage))
		}

		if err := SaveSession(options.SessionPath, transcript[1:]); err != nil {
			return err
		}
	}

	return SaveSession(options.SessionPath, transcript[1:])
}

func writeAssistantText(writer io.Writer, assistant aitypes.AssistantMessage) {
	for _, block := range assistant.Content {
		if block.IsText() && block.Text != nil && block.Text.Text != "" {
			fmt.Fprintln(writer, block.Text.Text)
		}
	}
}

func collectToolCalls(assistant aitypes.AssistantMessage) []aitypes.ToolCall {
	calls := make([]aitypes.ToolCall, 0)
	for _, block := range assistant.Content {
		if block.IsToolCall() && block.ToolCall != nil {
			calls = append(calls, *block.ToolCall)
		}
	}
	return calls
}

func executeToolCall(
	ctx context.Context,
	byName map[string]tools.BuiltinTool,
	env harnesstypes.ExecutionEnv,
	call aitypes.ToolCall,
) aitypes.ToolResultMessage {
	timestamp := float64(time.Now().UnixMilli())
	builtin, ok := byName[call.Name]
	if !ok {
		return aitypes.NewToolResultMessage(
			call.Id,
			call.Name,
			[]aitypes.ContentBlock{aitypes.TextBlock(fmt.Sprintf("Unknown tool: %s", call.Name))},
			true,
			timestamp,
		)
	}
	params, err := builtin.PrepareArguments(json.RawMessage(call.Arguments))
	if err != nil {
		return aitypes.NewToolResultMessage(
			call.Id,
			call.Name,
			[]aitypes.ContentBlock{aitypes.TextBlock(err.Error())},
			true,
			timestamp,
		)
	}
	result, err := builtin.Execute(call.Id, params, env, ctx)
	if err != nil {
		return aitypes.NewToolResultMessage(
			call.Id,
			call.Name,
			[]aitypes.ContentBlock{aitypes.TextBlock(err.Error())},
			true,
			timestamp,
		)
	}
	content := result.Content
	if len(content) == 0 {
		content = []aitypes.ContentBlock{aitypes.TextBlock("(no tool output)")}
	}
	message := aitypes.NewToolResultMessage(call.Id, call.Name, content, false, timestamp)
	if result.Details != nil {
		if encoded, marshalErr := json.Marshal(result.Details); marshalErr == nil {
			message.Details = encoded
		}
	}
	return message
}

// LoadSession reads a JSON-lines conversation file. A missing or empty path
// yields an empty history. A malformed line is a hard error so a corrupt
// session is never silently truncated.
func LoadSession(path string) ([]aitypes.Message, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var messages []aitypes.Message
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message aitypes.Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			return nil, fmt.Errorf("session: invalid message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// SaveSession writes the conversation as JSON lines. A nil or empty path is a
// no-op. The write is atomic so an interrupted save cannot leave a truncated
// history behind.
func SaveSession(path string, messages []aitypes.Message) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	var builder strings.Builder
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return err
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, []byte(builder.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
