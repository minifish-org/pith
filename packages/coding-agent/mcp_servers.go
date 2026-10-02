// MCP server configuration for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/mcp-servers.ts and the
// configuration half of packages/coding-agent/src/extensions/mcp/config.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. Configuration is
// caller-supplied and cwd-scoped: the SDK never discovers a global mcp.json or
// prompts interactively. Only validation and the namespace/exposure rules are
// shared with the runtime.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"fmt"
	"regexp"
	"strings"
)

// MCP exposure values decide how a server's tools reach the model.
const (
	// MCPExposureCodemode tools are callable from Codemode scripts but are not
	// declared to the model. The alias codemode-deferred maps here.
	MCPExposureCodemode = "codemode"
	// MCPExposureDeferred tools stay out of the model declarations until
	// tool_search loads them.
	MCPExposureDeferred = "deferred"
	// MCPExposureDirect tools are declared like any other tool.
	MCPExposureDirect = "direct"
	// MCPExposureHidden tools are registered but unreachable.
	MCPExposureHidden = "hidden"
)

// MCPExposureAliases maps older exposure names to their current name.
var MCPExposureAliases = map[string]string{
	"codemode-deferred": MCPExposureCodemode,
}

var mcpServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// MCPAuthProviderRef names a Pi provider whose token is sent to an HTTP server.
type MCPAuthProviderRef struct {
	Provider string
}

// MCPServerConfig is the validated configuration of one MCP server. Type is
// "stdio" (default) or "http".
type MCPServerConfig struct {
	Name string
	Type string

	// stdio transport.
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string

	// http transport.
	URL      string
	Headers  map[string]string
	OAuth    *MCPOAuthConfig
	Auth     *MCPAuthProviderRef
	AuthName string

	// Shared.
	Exposure     string
	Description  string
	ToolExposure map[string]string
	Enabled      *bool
	Timeout      float64
}

// MCPEnabled reports whether a config should connect. A nil Enabled defaults to
// true.
func (c MCPServerConfig) MCPEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// MCPNamespace returns the tool namespace of a server: `mcp__<server>` with
// dashes replaced by underscores.
func MCPNamespace(server string) string {
	return "mcp__" + strings.ReplaceAll(server, "-", "_")
}

// NormalizeMCPExposure returns the canonical exposure name, applying aliases
// and defaulting to codemode.
func NormalizeMCPExposure(exposure string) string {
	trimmed := strings.TrimSpace(exposure)
	if trimmed == "" {
		return MCPExposureCodemode
	}
	if alias, ok := MCPExposureAliases[trimmed]; ok {
		return alias
	}
	switch trimmed {
	case MCPExposureCodemode, MCPExposureDeferred, MCPExposureDirect, MCPExposureHidden:
		return trimmed
	default:
		return trimmed
	}
}

// ValidateMCPServerConfig validates one config, returning a reason for the first
// invalid field. An empty return means the config is valid.
func ValidateMCPServerConfig(config MCPServerConfig) error {
	if !mcpServerNamePattern.MatchString(config.Name) {
		return fmt.Errorf("server name must match %s", mcpServerNamePattern.String())
	}
	switch config.Type {
	case "", "stdio":
		if strings.TrimSpace(config.Command) == "" {
			return fmt.Errorf("stdio server %q requires a command", config.Name)
		}
	case "http":
		if strings.TrimSpace(config.URL) == "" {
			return fmt.Errorf("http server %q requires a url", config.Name)
		}
	default:
		return fmt.Errorf("server %q has unsupported type %q", config.Name, config.Type)
	}
	switch NormalizeMCPExposure(config.Exposure) {
	case MCPExposureCodemode, MCPExposureDeferred, MCPExposureDirect, MCPExposureHidden:
	default:
		return fmt.Errorf("server %q has unsupported exposure %q", config.Name, config.Exposure)
	}
	for pattern, exposure := range config.ToolExposure {
		switch NormalizeMCPExposure(exposure) {
		case MCPExposureCodemode, MCPExposureDeferred, MCPExposureDirect, MCPExposureHidden:
		default:
			return fmt.Errorf("server %q tool exposure %q has unsupported exposure %q", config.Name, pattern, exposure)
		}
	}
	if config.Type == "http" {
		if reason := validateMCPOAuth(config.OAuth); reason != "" {
			return fmt.Errorf("server %q: %s", config.Name, reason)
		}
	}
	return nil
}

// MCPExposureForTool resolves the exposure of one server tool. An exact name
// wins over patterns; among patterns the first matching key in ToolExposure
// order is used. A hidden override removes the tool.
func MCPExposureForTool(config MCPServerConfig, toolName string) string {
	if config.ToolExposure != nil {
		if exposure, ok := config.ToolExposure[toolName]; ok {
			return NormalizeMCPExposure(exposure)
		}
		// Map iteration order is random, so match patterns against a stable
		// key order derived from sorted keys.
		for _, pattern := range sortedKeys(config.ToolExposure) {
			if mcpGlobMatch(pattern, toolName) {
				return NormalizeMCPExposure(config.ToolExposure[pattern])
			}
		}
	}
	return NormalizeMCPExposure(config.Exposure)
}

// mcpGlobMatch reports whether a `*`-style glob matches a tool name.
func mcpGlobMatch(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		found := strings.Index(name[position:], part)
		if found < 0 {
			return false
		}
		if index == 0 && found != 0 {
			return false
		}
		position += found + len(part)
	}
	if last := parts[len(parts)-1]; last != "" {
		return strings.HasSuffix(name, last)
	}
	return true
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
