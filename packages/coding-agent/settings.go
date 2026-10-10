// Package codingagent is the embedded, headless SDK surface for the Pith
// coding agent.
//
// This file ports the configuration half of packages/coding-agent/src/core from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. It is a deliberate,
// documented Go adaptation: the SDK exposes one raw JSON Settings document
// (map[string]json.RawMessage) so every unknown key survives a load/save round
// trip instead of being dropped by a typed schema. Typed section structs are
// still provided for callers that want them.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/minifish-org/pith/packages/ai/auth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// File modes used when the SDK creates settings/auth/config files. They are
// applied only on creation so administrator-managed modes and ACLs survive.
const (
	SettingsDirMode   os.FileMode = 0o700
	SettingsFileMode  os.FileMode = 0o600
	AuthFileWriteMode os.FileMode = 0o600
)

// ConfigDirName is the project-local configuration directory name.
const ConfigDirName = ".pi"

// AgentDirEnvVar overrides the default agent directory.
const AgentDirEnvVar = "PI_CODING_AGENT_DIR"

// DefaultHTTPIdleTimeoutMs is the suggested HTTP header/body idle-timeout setting.
// It does not configure net/http automatically. The host must apply idle
// monitoring to its transport; http.Client.Timeout is a whole-request deadline.
const DefaultHTTPIdleTimeoutMs = 300_000

// Compaction/retry defaults are surfaced so callers do not have to rediscover
// them, but they are never used to cap caller-provided values.
const (
	DefaultCompactionReserveTokens    = 16384
	DefaultCompactionKeepRecentTokens = 20000
	DefaultMaxAgentRetryDelayMs       = 60000
)

// GetAgentDir returns the default agent configuration directory. The SDK never
// reads it implicitly: callers pass explicit paths, and this helper is only for
// callers that want upstream-compatible defaults.
func GetAgentDir() string {
	if env := os.Getenv(AgentDirEnvVar); env != "" {
		return expandTilde(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(ConfigDirName, "agent")
	}
	return filepath.Join(home, ConfigDirName, "agent")
}

func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func stripBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}

// Settings is a raw JSON settings document. Unknown keys, false, numeric zero
// and null are all preserved because every value is kept as its original JSON.
type Settings map[string]json.RawMessage

// CloneSettings returns a deep copy of a settings document.
func CloneSettings(settings Settings) Settings {
	if settings == nil {
		return nil
	}
	out := make(Settings, len(settings))
	for key, value := range settings {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func decodeJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

// mergeJSONValue recursively merges two JSON objects. Anything that is not an
// object on both sides is replaced by the override (including arrays and null).
func mergeJSONValue(base, override json.RawMessage) (json.RawMessage, bool) {
	baseObject, ok := decodeJSONObject(base)
	if !ok {
		return nil, false
	}
	overrideObject, ok := decodeJSONObject(override)
	if !ok {
		return nil, false
	}
	merged := make(map[string]json.RawMessage, len(baseObject)+len(overrideObject))
	for key, value := range baseObject {
		merged[key] = value
	}
	for key, overrideValue := range overrideObject {
		if baseValue, exists := merged[key]; exists {
			if nested, ok := mergeJSONValue(baseValue, overrideValue); ok {
				merged[key] = nested
				continue
			}
		}
		merged[key] = overrideValue
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// MergeSettings deep-merges overrides on top of base. Nested JSON objects merge
// recursively; arrays, scalars and null replace. base is not mutated.
func MergeSettings(base, overrides Settings) Settings {
	if base == nil && overrides == nil {
		return Settings{}
	}
	if overrides == nil {
		return CloneSettings(base)
	}
	out := make(Settings, len(base)+len(overrides))
	for key, value := range base {
		out[key] = append(json.RawMessage(nil), value...)
	}
	for key, overrideValue := range overrides {
		overrideCopy := append(json.RawMessage(nil), overrideValue...)
		if baseValue, exists := out[key]; exists {
			if merged, ok := mergeJSONValue(baseValue, overrideCopy); ok {
				out[key] = merged
				continue
			}
		}
		out[key] = overrideCopy
	}
	return out
}

func loadSettingsFile(path string) (Settings, error) {
	if path == "" {
		return Settings{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Settings{}, nil
		}
		return nil, fmt.Errorf("read settings %s: %w", path, err)
	}
	data = stripBOM(data)
	if len(bytes.TrimSpace(data)) == 0 {
		return Settings{}, nil
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse settings %s: %w", path, err)
	}
	if settings == nil {
		settings = Settings{}
	}
	return settings, nil
}

// LoadSettings loads global and project settings and applies caller overrides.
// Precedence is overrides > project > global. A missing file is empty settings;
// an existing malformed file is an error and is never silently ignored.
func LoadSettings(globalFile, projectFile string, overrides Settings) (Settings, error) {
	global, err := loadSettingsFile(globalFile)
	if err != nil {
		return nil, err
	}
	project, err := loadSettingsFile(projectFile)
	if err != nil {
		return nil, err
	}
	return MergeSettings(MergeSettings(global, project), overrides), nil
}

// SaveSettings writes settings atomically with private permissions. The file is
// fully encoded before the destination is touched, so an interrupted write
// leaves the previous file intact.
func SaveSettings(file string, settings Settings) error {
	if file == "" {
		return errors.New("settings path is empty")
	}
	if settings == nil {
		settings = Settings{}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(file, data, SettingsFileMode)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, SettingsDirMode); err != nil {
		return fmt.Errorf("create settings directory %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".pith-settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings file: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set settings permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	tempName = ""
	return nil
}

// ---------------------------------------------------------------------------
// Typed settings sections
// ---------------------------------------------------------------------------

// CompactionModelOverride overrides compaction token budgets for one model.
type CompactionModelOverride struct {
	ReserveTokens    *float64 `json:"reserveTokens,omitempty"`
	KeepRecentTokens *float64 `json:"keepRecentTokens,omitempty"`
}

// CompactionSettings configures automatic context compaction.
type CompactionSettings struct {
	Enabled          *bool                              `json:"enabled,omitempty"`
	ReserveTokens    *float64                           `json:"reserveTokens,omitempty"`
	KeepRecentTokens *float64                           `json:"keepRecentTokens,omitempty"`
	ModelOverrides   map[string]CompactionModelOverride `json:"modelOverrides,omitempty"`
}

// BranchSummarySettings configures branch summarization.
type BranchSummarySettings struct {
	ReserveTokens *float64 `json:"reserveTokens,omitempty"`
	SkipPrompt    *bool    `json:"skipPrompt,omitempty"`
}

// ProviderRetrySettings configures provider-level retry behavior.
type ProviderRetrySettings struct {
	TimeoutMs       *float64 `json:"timeoutMs,omitempty"`
	MaxRetries      *float64 `json:"maxRetries,omitempty"`
	MaxRetryDelayMs *float64 `json:"maxRetryDelayMs,omitempty"`
}

// RetrySettings configures agent-level retry behavior.
type RetrySettings struct {
	Enabled         *bool                  `json:"enabled,omitempty"`
	MaxRetries      *float64               `json:"maxRetries,omitempty"`
	BaseDelayMs     *float64               `json:"baseDelayMs,omitempty"`
	MaxAgentDelayMs *float64               `json:"maxAgentDelayMs,omitempty"`
	Provider        *ProviderRetrySettings `json:"provider,omitempty"`
}

// TuiMode selects the terminal renderer mode. It is data only; the SDK does not
// ship TUI widgets.
type TuiMode string

// TUI modes.
const (
	TuiModeRegular    TuiMode = "regular"
	TuiModeFullscreen TuiMode = "fullscreen"
)

// FullscreenExitOutput selects what a fullscreen exit prints.
type FullscreenExitOutput string

// Fullscreen exit outputs.
const (
	FullscreenExitTranscript FullscreenExitOutput = "transcript"
	FullscreenExitResumeHint FullscreenExitOutput = "resume-hint"
)

// TerminalSettings holds terminal rendering preferences (data only).
type TerminalSettings struct {
	ShowImages           *bool `json:"showImages,omitempty"`
	ImageWidthCells      *int  `json:"imageWidthCells,omitempty"`
	ClearOnShrink        *bool `json:"clearOnShrink,omitempty"`
	ShowTerminalProgress *bool `json:"showTerminalProgress,omitempty"`
	Hyperlinks           any   `json:"hyperlinks,omitempty"`
	Images               any   `json:"images,omitempty"`
	TrueColor            any   `json:"trueColor,omitempty"`
}

// ImageSettings configures image handling.
type ImageSettings struct {
	AutoResize  *bool `json:"autoResize,omitempty"`
	BlockImages *bool `json:"blockImages,omitempty"`
}

// ThinkingBudgetsSettings overrides token budgets per thinking level.
type ThinkingBudgetsSettings struct {
	Minimal *float64 `json:"minimal,omitempty"`
	Low     *float64 `json:"low,omitempty"`
	Medium  *float64 `json:"medium,omitempty"`
	High    *float64 `json:"high,omitempty"`
}

// MermaidRenderingMode selects Mermaid diagram rendering.
type MermaidRenderingMode string

// Mermaid rendering modes.
const (
	MermaidRenderingOff       MermaidRenderingMode = "off"
	MermaidRenderingFinal     MermaidRenderingMode = "final"
	MermaidRenderingStreaming MermaidRenderingMode = "streaming"
)

// CacheWarmingMode selects when prompt caches are warmed.
type CacheWarmingMode string

// Cache warming modes. CacheWarmingModes is the declaration order.
const (
	CacheWarmingOff       CacheWarmingMode = "off"
	CacheWarmingStreaming CacheWarmingMode = "streaming"
	CacheWarmingIdle      CacheWarmingMode = "idle"
)

// CacheWarmingModes lists the supported cache-warming modes.
var CacheWarmingModes = []CacheWarmingMode{CacheWarmingOff, CacheWarmingStreaming, CacheWarmingIdle}

// MarkdownSettings configures Markdown rendering (data only).
type MarkdownSettings struct {
	CodeBlockIndent *string              `json:"codeBlockIndent,omitempty"`
	Mermaid         MermaidRenderingMode `json:"mermaid,omitempty"`
}

// WarningSettings configures non-fatal warnings.
type WarningSettings struct {
	AnthropicExtraUsage *bool `json:"anthropicExtraUsage,omitempty"`
}

// DefaultProjectTrust is the global project-trust policy.
type DefaultProjectTrust string

// Project trust policies.
const (
	ProjectTrustAsk    DefaultProjectTrust = "ask"
	ProjectTrustAlways DefaultProjectTrust = "always"
	ProjectTrustNever  DefaultProjectTrust = "never"
)

// TransportSetting is the preferred provider transport.
type TransportSetting = aitypes.Transport

// PackageSource is an npm/git package source. The string form loads all
// resources; the object form filters resources and may disable autoload.
type PackageSource struct {
	Source     string   `json:"source"`
	Autoload   *bool    `json:"autoload,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Skills     []string `json:"skills,omitempty"`
	Prompts    []string `json:"prompts,omitempty"`
	Themes     []string `json:"themes,omitempty"`
}

// UnmarshalJSON accepts either the string shorthand or the object form.
func (p *PackageSource) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var source string
		if err := json.Unmarshal(trimmed, &source); err != nil {
			return err
		}
		*p = PackageSource{Source: source}
		return nil
	}
	type alias PackageSource
	var value alias
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return err
	}
	*p = PackageSource(value)
	return nil
}

// MarshalJSON writes the string shorthand when only the source is set.
func (p PackageSource) MarshalJSON() ([]byte, error) {
	if p.Autoload == nil && p.Extensions == nil && p.Skills == nil && p.Prompts == nil && p.Themes == nil {
		return json.Marshal(p.Source)
	}
	type alias PackageSource
	return json.Marshal(alias(p))
}

// ---------------------------------------------------------------------------
// Settings storage and manager
// ---------------------------------------------------------------------------

// SettingsScope identifies one settings layer.
type SettingsScope string

// Settings scopes.
const (
	SettingsScopeGlobal  SettingsScope = "global"
	SettingsScopeProject SettingsScope = "project"
)

// SettingsManagerCreateOptions configures manager construction.
type SettingsManagerCreateOptions struct {
	ProjectTrusted *bool
}

// SettingsStorage is synchronous, lock-scoped settings storage. fn receives the
// current file contents (nil when absent) and returns the next contents, or nil
// to leave the file unchanged.
type SettingsStorage interface {
	WithLock(scope SettingsScope, fn func(current []byte) (next []byte, err error)) error
}

// SettingsError is a non-fatal load or persistence failure for one scope.
type SettingsError struct {
	Scope SettingsScope
	Path  string
	Err   error
}

// Error implements error.
func (e SettingsError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("invalid %s settings file %s: %v", e.Scope, e.Path, e.Err)
	}
	return fmt.Sprintf("invalid %s settings: %v", e.Scope, e.Err)
}

// Unwrap exposes the underlying error.
func (e SettingsError) Unwrap() error { return e.Err }

// FileSettingsStorage stores global and project settings in files.
type FileSettingsStorage struct {
	GlobalSettingsPath  string
	ProjectSettingsPath string
}

// NewFileSettingsStorage builds file storage for a working directory and agent
// directory.
func NewFileSettingsStorage(cwd, agentDir string) *FileSettingsStorage {
	return &FileSettingsStorage{
		GlobalSettingsPath:  filepath.Join(agentDir, "settings.json"),
		ProjectSettingsPath: filepath.Join(cwd, ConfigDirName, "settings.json"),
	}
}

func (s *FileSettingsStorage) pathFor(scope SettingsScope) string {
	if scope == SettingsScopeGlobal {
		return s.GlobalSettingsPath
	}
	return s.ProjectSettingsPath
}

// WithLock reads, transforms and atomically writes one settings file.
func (s *FileSettingsStorage) WithLock(scope SettingsScope, fn func(current []byte) (next []byte, err error)) error {
	path := s.pathFor(scope)
	var current []byte
	if data, err := os.ReadFile(path); err == nil {
		current = stripBOM(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next, err := fn(current)
	if err != nil {
		return err
	}
	if next != nil {
		return writeFileAtomic(path, next, SettingsFileMode)
	}
	return nil
}

// InMemorySettingsStorage keeps settings in memory (no file I/O).
type InMemorySettingsStorage struct {
	mu      sync.Mutex
	global  []byte
	project []byte
}

// NewInMemorySettingsStorage builds an empty in-memory storage.
func NewInMemorySettingsStorage() *InMemorySettingsStorage {
	return &InMemorySettingsStorage{}
}

// WithLock reads, transforms and stores one scope.
func (s *InMemorySettingsStorage) WithLock(scope SettingsScope, fn func(current []byte) (next []byte, err error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.global
	if scope == SettingsScopeProject {
		current = s.project
	}
	next, err := fn(append([]byte(nil), current...))
	if err != nil {
		return err
	}
	if next != nil {
		stored := append([]byte(nil), next...)
		if scope == SettingsScopeGlobal {
			s.global = stored
		} else {
			s.project = stored
		}
	}
	return nil
}

func migrateSettings(settings Settings) Settings {
	if settings == nil {
		return Settings{}
	}
	if _, hasSteering := settings["steeringMode"]; !hasSteering {
		if queue, ok := settings["queueMode"]; ok {
			settings["steeringMode"] = queue
			delete(settings, "queueMode")
		}
	}
	if _, hasTransport := settings["transport"]; !hasTransport {
		if raw, ok := settings["websockets"]; ok {
			var enabled bool
			if json.Unmarshal(raw, &enabled) == nil {
				encoded, _ := json.Marshal(map[bool]string{true: "websocket", false: "sse"}[enabled])
				settings["transport"] = encoded
			}
			delete(settings, "websockets")
		}
	}
	return settings
}

// SettingsManager loads, merges and persists layered settings.
type SettingsManager struct {
	mu sync.Mutex

	storage        SettingsStorage
	paths          map[SettingsScope]string
	projectTrusted bool

	global    Settings
	project   Settings
	overrides Settings

	globalLoadError  error
	projectLoadError error
	errors           []SettingsError

	pendingGlobal  bool
	pendingProject bool
}

// SettingsManagerCreate loads global and project settings from files.
func SettingsManagerCreate(cwd, agentDir string, options SettingsManagerCreateOptions) *SettingsManager {
	storage := NewFileSettingsStorage(cwd, agentDir)
	paths := map[SettingsScope]string{
		SettingsScopeGlobal:  storage.GlobalSettingsPath,
		SettingsScopeProject: storage.ProjectSettingsPath,
	}
	return newSettingsManager(storage, options, paths)
}

// SettingsManagerFromStorage loads settings from an arbitrary storage backend.
func SettingsManagerFromStorage(storage SettingsStorage, options SettingsManagerCreateOptions) *SettingsManager {
	return newSettingsManager(storage, options, nil)
}

// SettingsManagerInMemory builds an in-memory manager seeded with settings.
func SettingsManagerInMemory(settings Settings, options SettingsManagerCreateOptions) *SettingsManager {
	storage := NewInMemorySettingsStorage()
	seeded := migrateSettings(CloneSettings(settings))
	if seeded == nil {
		seeded = Settings{}
	}
	data, _ := json.MarshalIndent(seeded, "", "  ")
	_ = storage.WithLock(SettingsScopeGlobal, func([]byte) ([]byte, error) { return data, nil })
	return newSettingsManager(storage, options, nil)
}

func newSettingsManager(storage SettingsStorage, options SettingsManagerCreateOptions, paths map[SettingsScope]string) *SettingsManager {
	trusted := true
	if options.ProjectTrusted != nil {
		trusted = *options.ProjectTrusted
	}
	manager := &SettingsManager{storage: storage, paths: paths, projectTrusted: trusted}
	manager.global, manager.globalLoadError = loadSettingsFromStorage(storage, SettingsScopeGlobal, true)
	manager.project, manager.projectLoadError = loadSettingsFromStorage(storage, SettingsScopeProject, trusted)
	if manager.globalLoadError != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: SettingsScopeGlobal, Path: manager.path(SettingsScopeGlobal), Err: manager.globalLoadError})
	}
	if manager.projectLoadError != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: SettingsScopeProject, Path: manager.path(SettingsScopeProject), Err: manager.projectLoadError})
	}
	return manager
}

func (m *SettingsManager) path(scope SettingsScope) string {
	if m.paths == nil {
		return ""
	}
	return m.paths[scope]
}

func loadSettingsFromStorage(storage SettingsStorage, scope SettingsScope, trusted bool) (Settings, error) {
	if scope == SettingsScopeProject && !trusted {
		return Settings{}, nil
	}
	var content []byte
	err := storage.WithLock(scope, func(current []byte) ([]byte, error) {
		content = append([]byte(nil), current...)
		return nil, nil
	})
	if err != nil {
		return Settings{}, err
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return Settings{}, nil
	}
	var settings Settings
	if err := json.Unmarshal(stripBOM(content), &settings); err != nil {
		return Settings{}, err
	}
	if settings == nil {
		settings = Settings{}
	}
	return migrateSettings(settings), nil
}

// GetGlobalSettings returns a copy of the global layer.
func (m *SettingsManager) GetGlobalSettings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return CloneSettings(m.global)
}

// GetProjectSettings returns a copy of the project layer.
func (m *SettingsManager) GetProjectSettings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return CloneSettings(m.project)
}

// GetSettings returns the merged global < project < overrides document.
func (m *SettingsManager) GetSettings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MergeSettings(MergeSettings(m.global, m.project), m.overrides)
}

// IsProjectTrusted reports whether project settings are active.
func (m *SettingsManager) IsProjectTrusted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.projectTrusted
}

// SetProjectTrusted changes project trust and reloads or clears project state.
func (m *SettingsManager) SetProjectTrusted(trusted bool) {
	m.mu.Lock()
	m.projectTrusted = trusted
	m.mu.Unlock()
	if !trusted {
		m.mu.Lock()
		m.project = Settings{}
		m.projectLoadError = nil
		m.mu.Unlock()
		return
	}
	settings, err := loadSettingsFromStorage(m.storage, SettingsScopeProject, true)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.project = settings
	m.projectLoadError = err
	if err != nil {
		m.errors = append(m.errors, SettingsError{Scope: SettingsScopeProject, Path: m.path(SettingsScopeProject), Err: err})
	}
}

// ApplyOverrides merges caller overrides on top of the current merged view.
func (m *SettingsManager) ApplyOverrides(overrides Settings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides = MergeSettings(m.overrides, overrides)
}

// Set writes one key to the project layer (subject to project trust) and queues
// a persistence write. Use SetGlobal for the global layer.
func (m *SettingsManager) Set(key string, value any) {
	m.SetProject(key, value)
}

// SetGlobal writes one key to the global layer.
func (m *SettingsManager) SetGlobal(key string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.global == nil {
		m.global = Settings{}
	}
	m.global[key] = encodeSettingValue(value)
	m.pendingGlobal = true
}

// SetProject writes one key to the project layer.
func (m *SettingsManager) SetProject(key string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.projectTrusted {
		return
	}
	if m.project == nil {
		m.project = Settings{}
	}
	m.project[key] = encodeSettingValue(value)
	m.pendingProject = true
}

func encodeSettingValue(value any) json.RawMessage {
	if raw, ok := value.(json.RawMessage); ok {
		return append(json.RawMessage(nil), raw...)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}

// Flush persists any queued writes. It is the durability boundary.
func (m *SettingsManager) Flush() error {
	m.mu.Lock()
	global := CloneSettings(m.global)
	project := CloneSettings(m.project)
	writeGlobal := m.pendingGlobal && m.globalLoadError == nil
	writeProject := m.pendingProject && m.projectLoadError == nil && m.projectTrusted
	m.pendingGlobal = false
	m.pendingProject = false
	m.mu.Unlock()

	var firstErr error
	if writeGlobal {
		if err := m.persist(SettingsScopeGlobal, global); err != nil {
			m.recordError(SettingsScopeGlobal, err)
			firstErr = err
		}
	}
	if writeProject {
		if err := m.persist(SettingsScopeProject, project); err != nil {
			m.recordError(SettingsScopeProject, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (m *SettingsManager) persist(scope SettingsScope, settings Settings) error {
	return m.storage.WithLock(scope, func(current []byte) ([]byte, error) {
		existing := Settings{}
		if len(bytes.TrimSpace(current)) > 0 {
			if err := json.Unmarshal(stripBOM(current), &existing); err != nil {
				return nil, err
			}
		}
		merged := MergeSettings(existing, settings)
		encoded, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(encoded, '\n'), nil
	})
}

func (m *SettingsManager) recordError(scope SettingsScope, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors = append(m.errors, SettingsError{Scope: scope, Path: m.path(scope), Err: err})
}

// DrainErrors returns and clears accumulated load/persistence errors.
func (m *SettingsManager) DrainErrors() []SettingsError {
	m.mu.Lock()
	defer m.mu.Unlock()
	errors := m.errors
	m.errors = nil
	return errors
}

func (m *SettingsManager) decodeInto(key string, target any) bool {
	settings := m.GetSettings()
	raw, ok := settings[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return false
	}
	return json.Unmarshal(raw, target) == nil
}

// CompactionSettings returns the typed compaction section, if present.
func (m *SettingsManager) CompactionSettings() (CompactionSettings, bool) {
	var value CompactionSettings
	return value, m.decodeInto("compaction", &value)
}

// RetrySettings returns the typed retry section, if present.
func (m *SettingsManager) RetrySettings() (RetrySettings, bool) {
	var value RetrySettings
	return value, m.decodeInto("retry", &value)
}

// GetEnableInstallTelemetry reports the install telemetry preference
// (default true).
func (m *SettingsManager) GetEnableInstallTelemetry() bool {
	var enabled bool
	if m.decodeInto("enableInstallTelemetry", &enabled) {
		return enabled
	}
	return true
}

// SettingString returns a string setting.
func (m *SettingsManager) SettingString(key string) (string, bool) {
	var value string
	return value, m.decodeInto(key, &value)
}

// ---------------------------------------------------------------------------
// Settings diagnostics
// ---------------------------------------------------------------------------

// SettingsDiagnostic is a non-fatal configuration warning.
type SettingsDiagnostic struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// CollectSettingsDiagnostics converts manager errors into runtime diagnostics.
func CollectSettingsDiagnostics(manager *SettingsManager) []SettingsDiagnostic {
	if manager == nil {
		return nil
	}
	errs := manager.DrainErrors()
	diagnostics := make([]SettingsDiagnostic, 0, len(errs))
	for _, settingsError := range errs {
		message := fmt.Sprintf("Invalid %s settings: %v", settingsError.Scope, settingsError.Err)
		if settingsError.Path != "" {
			message = fmt.Sprintf("Invalid settings file %s: %v", settingsError.Path, settingsError.Err)
		}
		diagnostics = append(diagnostics, SettingsDiagnostic{Type: "warning", Message: message})
	}
	return diagnostics
}

// DeduplicateDiagnostics removes duplicate type/message pairs, preserving order.
func DeduplicateDiagnostics(diagnostics []SettingsDiagnostic) []SettingsDiagnostic {
	seen := make(map[string]struct{}, len(diagnostics))
	out := make([]SettingsDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		key := diagnostic.Type + "\x00" + diagnostic.Message
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, diagnostic)
	}
	return out
}

// ---------------------------------------------------------------------------
// Auth storage
// ---------------------------------------------------------------------------

// AuthStorageBackend provides locked access to the raw auth document.
type AuthStorageBackend interface {
	WithLock(fn func(current []byte) (next []byte, err error)) error
}

// FileAuthStorageBackend is a file-backed auth document.
type FileAuthStorageBackend struct {
	AuthPath string
}

// NewFileAuthStorageBackend builds file-backed auth storage.
func NewFileAuthStorageBackend(authPath string) *FileAuthStorageBackend {
	return &FileAuthStorageBackend{AuthPath: authPath}
}

// WithLock reads, transforms and atomically writes the auth file.
func (b *FileAuthStorageBackend) WithLock(fn func(current []byte) (next []byte, err error)) error {
	var current []byte
	if data, err := os.ReadFile(b.AuthPath); err == nil {
		current = stripBOM(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next, err := fn(current)
	if err != nil {
		return err
	}
	if next != nil {
		return writeFileAtomic(b.AuthPath, next, AuthFileWriteMode)
	}
	return nil
}

// InMemoryAuthStorageBackend is an in-memory auth document.
type InMemoryAuthStorageBackend struct {
	mu    sync.Mutex
	value []byte
}

// NewInMemoryAuthStorageBackend builds an empty backend.
func NewInMemoryAuthStorageBackend() *InMemoryAuthStorageBackend {
	return &InMemoryAuthStorageBackend{}
}

// WithLock reads, transforms and stores the auth document.
func (b *InMemoryAuthStorageBackend) WithLock(fn func(current []byte) (next []byte, err error)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	next, err := fn(append([]byte(nil), b.value...))
	if err != nil {
		return err
	}
	if next != nil {
		b.value = append([]byte(nil), next...)
	}
	return nil
}

func parseStoredCredential(raw json.RawMessage) (authtypes.Credential, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return nil, err
	}
	switch discriminator.Type {
	case authtypes.CredentialTypeAPIKey, "":
		var credential authtypes.ApiKeyCredential
		if err := json.Unmarshal(raw, &credential); err != nil {
			return nil, err
		}
		if credential.Type == "" {
			credential.Type = authtypes.CredentialTypeAPIKey
		}
		return &credential, nil
	case authtypes.CredentialTypeOAuth:
		var credential authtypes.OAuthCredential
		if err := json.Unmarshal(raw, &credential); err != nil {
			return nil, err
		}
		return &credential, nil
	default:
		return nil, fmt.Errorf("unknown credential type %q", discriminator.Type)
	}
}

func readAuthData(current []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(current)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(stripBOM(current), &data); err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]json.RawMessage{}
	}
	return data, nil
}

// ReadOnlyAuthStorage exposes reads over a backend and rejects writes.
type ReadOnlyAuthStorage struct {
	backend AuthStorageBackend
}

// NewReadOnlyAuthStorage builds a read-only view over a backend.
func NewReadOnlyAuthStorage(backend AuthStorageBackend) *ReadOnlyAuthStorage {
	return &ReadOnlyAuthStorage{backend: backend}
}

func (s *ReadOnlyAuthStorage) readData() (map[string]json.RawMessage, error) {
	var data map[string]json.RawMessage
	err := s.backend.WithLock(func(current []byte) ([]byte, error) {
		parsed, parseErr := readAuthData(current)
		data = parsed
		return nil, parseErr
	})
	return data, err
}

// Read returns a stored credential or nil.
func (s *ReadOnlyAuthStorage) Read(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) (authtypes.Credential, error) {
	if err := authSignal(ctx, options); err != nil {
		return nil, err
	}
	data, err := s.readData()
	if err != nil {
		return nil, err
	}
	return parseStoredCredential(data[providerID])
}

// List returns stored credential metadata.
func (s *ReadOnlyAuthStorage) List(ctx context.Context, options *authtypes.AuthOperationOptions) ([]authtypes.CredentialInfo, error) {
	if err := authSignal(ctx, options); err != nil {
		return nil, err
	}
	data, err := s.readData()
	if err != nil {
		return nil, err
	}
	return authInfos(data), nil
}

// Modify is not supported by read-only storage.
func (s *ReadOnlyAuthStorage) Modify(context.Context, string, func(authtypes.Credential) (authtypes.Credential, error), *authtypes.AuthOperationOptions) (authtypes.Credential, error) {
	return nil, errors.New("auth storage is read-only")
}

// Delete is not supported by read-only storage.
func (s *ReadOnlyAuthStorage) Delete(context.Context, string, *authtypes.AuthOperationOptions) error {
	return errors.New("auth storage is read-only")
}

func authInfos(data map[string]json.RawMessage) []authtypes.CredentialInfo {
	infos := make([]authtypes.CredentialInfo, 0, len(data))
	for providerID, raw := range data {
		credential, err := parseStoredCredential(raw)
		if err != nil || credential == nil {
			continue
		}
		infos = append(infos, authtypes.CredentialInfo{ProviderID: providerID, Type: credential.CredentialType()})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ProviderID < infos[j].ProviderID })
	return infos
}

func authSignal(ctx context.Context, options *authtypes.AuthOperationOptions) error {
	if options != nil && options.Signal != nil {
		return options.Signal.Err()
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

// AuthStorage is credential storage backed by a JSON document. Secret values
// are resolved from environment indirection only; shell snippets are never
// executed by default.
type AuthStorage struct {
	backend  AuthStorageBackend
	AuthPath string
}

// CreateAuthStorage builds file-backed credential storage.
func CreateAuthStorage(authPath string) *AuthStorage {
	return &AuthStorage{backend: NewFileAuthStorageBackend(authPath), AuthPath: authPath}
}

// AuthStorageFromBackend builds credential storage over a backend.
func AuthStorageFromBackend(backend AuthStorageBackend) *AuthStorage {
	return &AuthStorage{backend: backend}
}

// AuthStorageInMemory builds in-memory credential storage.
func AuthStorageInMemory(data map[string]authtypes.Credential) (*AuthStorage, error) {
	backend := NewInMemoryAuthStorageBackend()
	raw := map[string]json.RawMessage{}
	for providerID, credential := range data {
		encoded, err := json.Marshal(credential)
		if err != nil {
			return nil, err
		}
		raw[providerID] = encoded
	}
	serialized, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := backend.WithLock(func([]byte) ([]byte, error) { return serialized, nil }); err != nil {
		return nil, err
	}
	return &AuthStorage{backend: backend}, nil
}

// Read returns a stored credential, resolving api-key indirection but never
// executing shell snippets.
func (s *AuthStorage) Read(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) (authtypes.Credential, error) {
	if err := authSignal(ctx, options); err != nil {
		return nil, err
	}
	var credential authtypes.Credential
	err := s.backend.WithLock(func(current []byte) ([]byte, error) {
		data, parseErr := readAuthData(current)
		if parseErr != nil {
			return nil, parseErr
		}
		parsed, parseErr := parseStoredCredential(data[providerID])
		if parseErr != nil || parsed == nil {
			return nil, parseErr
		}
		credential = resolveCredentialSecret(parsed)
		return nil, nil
	})
	return credential, err
}

func resolveCredentialSecret(credential authtypes.Credential) authtypes.Credential {
	apiKey, ok := credential.(*authtypes.ApiKeyCredential)
	if !ok || apiKey == nil || apiKey.Key == nil {
		return credential
	}
	env := map[string]string{}
	for name, value := range apiKey.Env {
		if value != nil {
			env[name] = *value
		}
	}
	resolved := ResolveConfigValue(*apiKey.Key, env)
	if resolved == nil {
		return credential
	}
	return authtypes.NewApiKeyCredential(*resolved)
}

// List returns stored credential metadata.
func (s *AuthStorage) List(ctx context.Context, options *authtypes.AuthOperationOptions) ([]authtypes.CredentialInfo, error) {
	if err := authSignal(ctx, options); err != nil {
		return nil, err
	}
	var infos []authtypes.CredentialInfo
	err := s.backend.WithLock(func(current []byte) ([]byte, error) {
		data, parseErr := readAuthData(current)
		if parseErr != nil {
			return nil, parseErr
		}
		infos = authInfos(data)
		return nil, nil
	})
	return infos, err
}

// Modify is the only write path, serialized by the backend lock.
func (s *AuthStorage) Modify(ctx context.Context, providerID string, fn func(current authtypes.Credential) (authtypes.Credential, error), options *authtypes.AuthOperationOptions) (authtypes.Credential, error) {
	if err := authSignal(ctx, options); err != nil {
		return nil, err
	}
	var result authtypes.Credential
	err := s.backend.WithLock(func(current []byte) ([]byte, error) {
		data, parseErr := readAuthData(current)
		if parseErr != nil {
			return nil, parseErr
		}
		existing, parseErr := parseStoredCredential(data[providerID])
		if parseErr != nil {
			return nil, parseErr
		}
		next, fnErr := fn(existing)
		if fnErr != nil {
			return nil, fnErr
		}
		if next == nil {
			result = existing
			return nil, nil
		}
		encoded, encodeErr := json.Marshal(next)
		if encodeErr != nil {
			return nil, encodeErr
		}
		data[providerID] = encoded
		result = next
		serialized, encodeErr := json.MarshalIndent(data, "", "  ")
		if encodeErr != nil {
			return nil, encodeErr
		}
		return serialized, nil
	})
	return result, err
}

// Delete removes a credential.
func (s *AuthStorage) Delete(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) error {
	if err := authSignal(ctx, options); err != nil {
		return err
	}
	return s.backend.WithLock(func(current []byte) ([]byte, error) {
		data, parseErr := readAuthData(current)
		if parseErr != nil {
			return nil, parseErr
		}
		if _, exists := data[providerID]; !exists {
			return nil, nil
		}
		delete(data, providerID)
		return json.MarshalIndent(data, "", "  ")
	})
}

// ReadStoredCredential performs a one-off synchronous read without resolving
// configured key values.
func ReadStoredCredential(providerID, authPath string) authtypes.Credential {
	data, err := os.ReadFile(authPath)
	if err != nil {
		return nil
	}
	authData, err := readAuthData(data)
	if err != nil {
		return nil
	}
	credential, err := parseStoredCredential(authData[providerID])
	if err != nil {
		return nil
	}
	return credential
}

// RuntimeCredentials overlays non-persistent runtime API keys on a base store.
type RuntimeCredentials struct {
	mu        sync.Mutex
	store     authtypes.CredentialStore
	overrides map[string]string
}

// NewRuntimeCredentials wraps a credential store.
func NewRuntimeCredentials(store authtypes.CredentialStore) *RuntimeCredentials {
	if store == nil {
		store = auth.NewInMemoryCredentialStore()
	}
	return &RuntimeCredentials{store: store, overrides: map[string]string{}}
}

// SetRuntimeAPIKey installs a non-persistent key for a provider.
func (c *RuntimeCredentials) SetRuntimeAPIKey(providerID, apiKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.overrides[providerID] = apiKey
}

// RemoveRuntimeAPIKey removes a runtime key.
func (c *RuntimeCredentials) RemoveRuntimeAPIKey(providerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.overrides, providerID)
}

// HasRuntimeAPIKey reports whether a runtime key is installed.
func (c *RuntimeCredentials) HasRuntimeAPIKey(providerID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.overrides[providerID]
	return ok
}

// Read returns the runtime key or the stored credential.
func (c *RuntimeCredentials) Read(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) (authtypes.Credential, error) {
	if err := authSignal(ctx, options); err != nil {
		return nil, err
	}
	c.mu.Lock()
	override, ok := c.overrides[providerID]
	c.mu.Unlock()
	if ok {
		return authtypes.NewApiKeyCredential(override), nil
	}
	return c.store.Read(ctx, providerID, options)
}

// List merges stored metadata with runtime overrides.
func (c *RuntimeCredentials) List(ctx context.Context, options *authtypes.AuthOperationOptions) ([]authtypes.CredentialInfo, error) {
	stored, err := c.store.List(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := authSignal(ctx, options); err != nil {
		return nil, err
	}
	byProvider := map[string]authtypes.CredentialInfo{}
	for _, info := range stored {
		byProvider[info.ProviderID] = info
	}
	c.mu.Lock()
	for providerID := range c.overrides {
		byProvider[providerID] = authtypes.CredentialInfo{ProviderID: providerID, Type: authtypes.CredentialTypeAPIKey}
	}
	c.mu.Unlock()
	out := make([]authtypes.CredentialInfo, 0, len(byProvider))
	for _, info := range byProvider {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderID < out[j].ProviderID })
	return out, nil
}

// Modify delegates to the base store.
func (c *RuntimeCredentials) Modify(ctx context.Context, providerID string, fn func(current authtypes.Credential) (authtypes.Credential, error), options *authtypes.AuthOperationOptions) (authtypes.Credential, error) {
	return c.store.Modify(ctx, providerID, fn, options)
}

// Delete removes the stored credential and any runtime override.
func (c *RuntimeCredentials) Delete(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) error {
	if err := authSignal(ctx, options); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.overrides, providerID)
	c.mu.Unlock()
	return c.store.Delete(ctx, providerID, options)
}

// ---------------------------------------------------------------------------
// Config value resolution
// ---------------------------------------------------------------------------

// ConfigValueCommandExecutor is the opt-in hook for `!command` config values.
// It is nil by default, so the SDK never executes a shell snippet from
// credentials or config implicitly.
var ConfigValueCommandExecutor func(command string) (string, bool)

var configValueCache = struct {
	mu      sync.Mutex
	results map[string]*string
}{results: map[string]*string{}}

var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var envVarNamePrefixRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)

type configValuePart struct {
	literal string
	env     string
	isEnv   bool
}

func parseConfigValueTemplate(config string) []configValuePart {
	var parts []configValuePart
	appendLiteral := func(value string) {
		if value == "" {
			return
		}
		if len(parts) > 0 && !parts[len(parts)-1].isEnv {
			parts[len(parts)-1].literal += value
			return
		}
		parts = append(parts, configValuePart{literal: value})
	}

	index := 0
	for index < len(config) {
		dollarIndex := strings.IndexByte(config[index:], '$')
		if dollarIndex < 0 {
			appendLiteral(config[index:])
			break
		}
		dollarIndex += index
		appendLiteral(config[index:dollarIndex])
		var nextChar byte
		if dollarIndex+1 < len(config) {
			nextChar = config[dollarIndex+1]
		}
		if nextChar == '$' || nextChar == '!' {
			appendLiteral(string(nextChar))
			index = dollarIndex + 2
			continue
		}
		if nextChar == '{' {
			endRelative := strings.IndexByte(config[dollarIndex+2:], '}')
			if endRelative < 0 {
				appendLiteral("$")
				index = dollarIndex + 1
				continue
			}
			endIndex := dollarIndex + 2 + endRelative
			name := config[dollarIndex+2 : endIndex]
			if envVarNameRe.MatchString(name) {
				parts = append(parts, configValuePart{env: name, isEnv: true})
			} else {
				appendLiteral(config[dollarIndex : endIndex+1])
			}
			index = endIndex + 1
			continue
		}
		match := envVarNamePrefixRe.FindString(config[dollarIndex+1:])
		if match != "" {
			parts = append(parts, configValuePart{env: match, isEnv: true})
			index = dollarIndex + 1 + len(match)
			continue
		}
		appendLiteral("$")
		index = dollarIndex + 1
	}
	return parts
}

func parseConfigValueReference(config string) (bool, []configValuePart, string) {
	if strings.HasPrefix(config, "!") {
		return true, nil, config
	}
	return false, parseConfigValueTemplate(config), ""
}

func resolveEnvConfigValue(name string, env map[string]string) *string {
	if env != nil {
		if value, ok := env[name]; ok && value != "" {
			return &value
		}
	}
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return &value
	}
	return nil
}

func templateEnvNames(parts []configValuePart) []string {
	var names []string
	seen := map[string]struct{}{}
	for _, part := range parts {
		if !part.isEnv {
			continue
		}
		if _, ok := seen[part.env]; ok {
			continue
		}
		seen[part.env] = struct{}{}
		names = append(names, part.env)
	}
	return names
}

func resolveTemplate(parts []configValuePart, env map[string]string) *string {
	var builder strings.Builder
	for _, part := range parts {
		if !part.isEnv {
			builder.WriteString(part.literal)
			continue
		}
		value := resolveEnvConfigValue(part.env, env)
		if value == nil {
			return nil
		}
		builder.WriteString(*value)
	}
	resolved := builder.String()
	return &resolved
}

// GetConfigValueEnvVarName returns the single env var name for a bare
// `$NAME`/`${NAME}` reference.
func GetConfigValueEnvVarName(config string) *string {
	isCommand, parts, _ := parseConfigValueReference(config)
	if isCommand {
		return nil
	}
	if len(parts) == 1 && parts[0].isEnv {
		return &parts[0].env
	}
	return nil
}

// GetConfigValueEnvVarNames returns every env var referenced by a template.
func GetConfigValueEnvVarNames(config string) []string {
	isCommand, parts, _ := parseConfigValueReference(config)
	if isCommand {
		return nil
	}
	return templateEnvNames(parts)
}

// GetMissingConfigValueEnvVarNames returns referenced env vars that are unset.
func GetMissingConfigValueEnvVarNames(config string, env map[string]string) []string {
	var missing []string
	for _, name := range GetConfigValueEnvVarNames(config) {
		if resolveEnvConfigValue(name, env) == nil {
			missing = append(missing, name)
		}
	}
	return missing
}

// IsCommandConfigValue reports whether the value is a `!command` reference.
func IsCommandConfigValue(config string) bool {
	isCommand, _, _ := parseConfigValueReference(config)
	return isCommand
}

// IsConfigValueConfigured reports whether every referenced env var is set.
func IsConfigValueConfigured(config string, env map[string]string) bool {
	return len(GetMissingConfigValueEnvVarNames(config, env)) == 0
}

func executeCommand(commandConfig string) *string {
	if !IsCommandConfigValue(commandConfig) {
		return nil
	}
	if ConfigValueCommandExecutor == nil {
		return nil
	}
	configValueCache.mu.Lock()
	if cached, ok := configValueCache.results[commandConfig]; ok {
		configValueCache.mu.Unlock()
		return cached
	}
	configValueCache.mu.Unlock()
	value, ok := ConfigValueCommandExecutor(strings.TrimPrefix(commandConfig, "!"))
	var result *string
	if ok {
		result = &value
	}
	configValueCache.mu.Lock()
	configValueCache.results[commandConfig] = result
	configValueCache.mu.Unlock()
	return result
}

// ResolveConfigValue resolves a template or literal. Command values are only
// executed when ConfigValueCommandExecutor is explicitly installed.
func ResolveConfigValue(config string, env map[string]string) *string {
	isCommand, parts, _ := parseConfigValueReference(config)
	if isCommand {
		return executeCommand(config)
	}
	return resolveTemplate(parts, env)
}

// ResolveConfigValueUncached behaves like ResolveConfigValue but bypasses the
// command result cache.
func ResolveConfigValueUncached(config string, env map[string]string) *string {
	isCommand, parts, command := parseConfigValueReference(config)
	if isCommand {
		if ConfigValueCommandExecutor == nil {
			return nil
		}
		value, ok := ConfigValueCommandExecutor(strings.TrimPrefix(command, "!"))
		if !ok {
			return nil
		}
		return &value
	}
	return resolveTemplate(parts, env)
}

// ResolveConfigValueOrThrow resolves a value or returns a descriptive error.
func ResolveConfigValueOrThrow(config, description string, env map[string]string) (string, error) {
	if value := ResolveConfigValueUncached(config, env); value != nil {
		return *value, nil
	}
	isCommand, _, command := parseConfigValueReference(config)
	if isCommand {
		return "", fmt.Errorf("failed to resolve %s from shell command: %s", description, strings.TrimPrefix(command, "!"))
	}
	missing := GetMissingConfigValueEnvVarNames(config, env)
	switch len(missing) {
	case 1:
		return "", fmt.Errorf("failed to resolve %s from environment variable: %s", description, missing[0])
	case 0:
	default:
		return "", fmt.Errorf("failed to resolve %s from environment variables: %s", description, strings.Join(missing, ", "))
	}
	return "", fmt.Errorf("failed to resolve %s", description)
}

// ResolveHeaders resolves every header value, dropping unresolved entries.
func ResolveHeaders(headers map[string]string, env map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	resolved := map[string]string{}
	for key, value := range headers {
		if result := ResolveConfigValue(value, env); result != nil && *result != "" {
			resolved[key] = *result
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

// ResolveHeadersOrThrow resolves every header value or fails.
func ResolveHeadersOrThrow(headers map[string]string, description string, env map[string]string) (map[string]string, error) {
	if headers == nil {
		return nil, nil
	}
	resolved := map[string]string{}
	for key, value := range headers {
		result, err := ResolveConfigValueOrThrow(value, fmt.Sprintf("%s header %q", description, key), env)
		if err != nil {
			return nil, err
		}
		resolved[key] = result
	}
	if len(resolved) == 0 {
		return nil, nil
	}
	return resolved, nil
}

// ClearConfigValueCache clears cached command results.
func ClearConfigValueCache() {
	configValueCache.mu.Lock()
	configValueCache.results = map[string]*string{}
	configValueCache.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Auth guidance
// ---------------------------------------------------------------------------

const unknownProvider = "unknown"

// GetProviderLoginHelp returns the login guidance text.
func GetProviderLoginHelp() string {
	return strings.Join([]string{
		"Use /login to log into a provider via OAuth or API key. See:",
		"  docs/providers.md",
		"  docs/models.md",
	}, "\n")
}

// FormatNoModelsAvailableMessage is the no-models guidance message.
func FormatNoModelsAvailableMessage() string {
	return "No models available. " + GetProviderLoginHelp()
}

// FormatNoModelSelectedMessage is the no-model-selected guidance message.
func FormatNoModelSelectedMessage() string {
	return "No model selected.\n\n" + GetProviderLoginHelp() + "\n\nThen use /model to select a model."
}

// FormatNoAPIKeyFoundMessage is the missing-key guidance message.
func FormatNoAPIKeyFoundMessage(provider string) string {
	display := provider
	if provider == unknownProvider {
		display = "the selected model"
	}
	return fmt.Sprintf("No API key found for %s.\n\n%s", display, GetProviderLoginHelp())
}

// ---------------------------------------------------------------------------
// HTTP dispatcher settings
// ---------------------------------------------------------------------------

// HTTPIdleTimeoutChoice is one labeled HTTP idle-timeout option.
type HTTPIdleTimeoutChoice struct {
	Label     string `json:"label"`
	TimeoutMs int    `json:"timeoutMs"`
}

// HTTPIdleTimeoutChoices lists the supported HTTP idle timeouts.
var HTTPIdleTimeoutChoices = []HTTPIdleTimeoutChoice{
	{Label: "30 sec", TimeoutMs: 30_000},
	{Label: "1 min", TimeoutMs: 60_000},
	{Label: "2 min", TimeoutMs: 120_000},
	{Label: "5 min", TimeoutMs: 300_000},
	{Label: "disabled", TimeoutMs: 0},
}

// ParseHTTPIdleTimeoutMs parses a timeout value. It returns nil for invalid
// input, and 0 for "disabled".
func ParseHTTPIdleTimeoutMs(value any) *int {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.EqualFold(trimmed, "disabled") {
			zero := 0
			return &zero
		}
		if trimmed == "" {
			return nil
		}
		var number float64
		if _, err := fmt.Sscanf(trimmed, "%f", &number); err != nil {
			return nil
		}
		return ParseHTTPIdleTimeoutMs(number)
	case float64:
		if typed < 0 {
			return nil
		}
		floored := int(typed)
		return &floored
	case int:
		if typed < 0 {
			return nil
		}
		value := typed
		return &value
	case *int:
		if typed == nil || *typed < 0 {
			return nil
		}
		floored := *typed
		return &floored
	default:
		return nil
	}
}

// FormatHTTPIdleTimeoutMs renders a timeout using its labeled choice.
func FormatHTTPIdleTimeoutMs(timeoutMs int) string {
	for _, choice := range HTTPIdleTimeoutChoices {
		if choice.TimeoutMs == timeoutMs {
			return choice.Label
		}
	}
	return fmt.Sprintf("%d sec", timeoutMs/1000)
}

// ApplyHTTPProxySettings applies a proxy URL to the process proxy variables
// only when they are not already set.
func ApplyHTTPProxySettings(httpProxy string) {
	proxy := strings.TrimSpace(httpProxy)
	if proxy == "" {
		return
	}
	if os.Getenv("HTTP_PROXY") == "" {
		_ = os.Setenv("HTTP_PROXY", proxy)
	}
	if os.Getenv("HTTPS_PROXY") == "" {
		_ = os.Setenv("HTTPS_PROXY", proxy)
	}
}

// ConfigureHTTPDispatcher validates and returns an HTTP idle-timeout setting.
// The Go SDK uses net/http, so this is a configuration boundary rather than an
// undici global dispatcher install. Callers that own an http.Client apply the
// returned setting to their transport. This function changes no process state
// and installs neither a transport nor a whole-request deadline.
func ConfigureHTTPDispatcher(timeoutMs int) (int, error) {
	normalized := ParseHTTPIdleTimeoutMs(timeoutMs)
	if normalized == nil {
		return 0, fmt.Errorf("invalid HTTP idle timeout: %v", timeoutMs)
	}
	return *normalized, nil
}

// ---------------------------------------------------------------------------
// Provider attribution
// ---------------------------------------------------------------------------

func matchesHost(baseURL, expectedHost string) bool {
	host := baseURL
	if index := strings.Index(host, "://"); index >= 0 {
		host = host[index+3:]
	}
	if index := strings.IndexAny(host, "/?#"); index >= 0 {
		host = host[:index]
	}
	if index := strings.Index(host, "@"); index >= 0 {
		host = host[index+1:]
	}
	if index := strings.Index(host, ":"); index >= 0 {
		host = host[:index]
	}
	return host == expectedHost
}

// MergeProviderAttributionHeaders merges provider-attribution and session
// headers with caller-supplied header sources (later sources win).
func MergeProviderAttributionHeaders(model aitypes.Model, settings *SettingsManager, sessionID string, sources ...aitypes.ProviderHeaders) aitypes.ProviderHeaders {
	merged := aitypes.ProviderHeaders{}
	if sessionID != "" && (model.Provider == "opencode" || model.Provider == "opencode-go" || matchesHost(model.BaseUrl, "opencode.ai")) {
		merged["x-opencode-session"] = stringPointer(sessionID)
		merged["x-opencode-client"] = stringPointer("pi")
	}
	if settings == nil || settings.GetEnableInstallTelemetry() {
		for key, value := range defaultAttributionHeaders(model) {
			merged[key] = stringPointer(value)
		}
	}
	for _, source := range sources {
		for key, value := range source {
			merged[key] = value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func defaultAttributionHeaders(model aitypes.Model) map[string]string {
	if model.Provider == "openrouter" || strings.Contains(model.BaseUrl, "openrouter.ai") {
		return map[string]string{
			"HTTP-Referer":            "https://pi.dev",
			"X-OpenRouter-Title":      "pi",
			"X-OpenRouter-Categories": "cli-agent",
		}
	}
	if model.Provider == "nvidia" || matchesHost(model.BaseUrl, "integrate.api.nvidia.com") {
		return map[string]string{"X-BILLING-INVOKE-ORIGIN": "Pi"}
	}
	if model.Provider == "cloudflare-workers-ai" || model.Provider == "cloudflare-ai-gateway" ||
		matchesHost(model.BaseUrl, "api.cloudflare.com") || matchesHost(model.BaseUrl, "gateway.ai.cloudflare.com") {
		return map[string]string{"User-Agent": "pi-coding-agent"}
	}
	return nil
}

func stringPointer(value string) *string { return &value }
