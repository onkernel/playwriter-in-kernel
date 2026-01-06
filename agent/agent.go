// Package agent provides an abstraction for AI coding agents that can run
// prompts with MCP tools in a Kernel browser environment.
package agent

import (
	"context"
	"encoding/base64"

	"github.com/charmbracelet/lipgloss"
	"github.com/onkernel/kernel-go-sdk"
)

// Shared output styles
var (
	HeaderStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	SuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	DimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// MCPServer represents a single MCP server configuration
type MCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// MCPConfig represents MCP server configuration for an agent
type MCPConfig struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

// PlaywriterMCPConfig returns the standard MCP config for playwriter built from source
func PlaywriterMCPConfig() MCPConfig {
	return MCPConfig{
		MCPServers: map[string]MCPServer{
			"playwriter": {
				Command: "node",
				Args:    []string{"/home/kernel/playwriter/playwriter/dist/cli.js"},
			},
		},
	}
}

// RunOptions contains options for running an agent
type RunOptions struct {
	Prompt       string
	Model        string
	APIKey       string            // Primary API key (for agents with single provider)
	EnvVars      map[string]string // Additional env vars to forward (for multi-provider agents)
	AgentTimeout int64             // Hard timeout in seconds (0 = no limit)
}

// StreamHandler is called for each event from the agent's output stream
type StreamHandler func(event StreamEvent)

// StreamEvent represents a JSON event from an agent's stream output
type StreamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message,omitempty"`
	ToolCall struct {
		MCPToolCall struct {
			Args struct {
				Name     string `json:"name"`
				ToolName string `json:"toolName"`
				Args     struct {
					Code string `json:"code"`
				} `json:"args"`
			} `json:"args"`
		} `json:"mcpToolCall"`
	} `json:"tool_call,omitempty"`
}

// Agent represents an AI coding agent that can run prompts with MCP tools
type Agent interface {
	// Name returns the agent identifier (e.g., "cursor", "claude", "opencode")
	Name() string

	// Install installs the agent CLI in the browser environment
	Install(ctx context.Context, client kernel.Client, sessionID string) error

	// ConfigureMCP sets up the MCP server configuration
	ConfigureMCP(ctx context.Context, client kernel.Client, sessionID string, config MCPConfig) error

	// Run executes a prompt and returns the exit code
	// The handler is called for each event in the output stream
	Run(ctx context.Context, client kernel.Client, sessionID string, opts RunOptions, handler StreamHandler) (exitCode int64, err error)

	// RequiredEnvVar returns the name of the environment variable needed for the API key.
	// Returns empty string if no single env var is required (e.g., multi-provider agents).
	RequiredEnvVar() string

	// ProviderEnvVars returns a list of environment variable names that should be
	// forwarded to the agent for provider authentication. Returns nil for agents
	// that only need a single API key (use RequiredEnvVar instead).
	ProviderEnvVars() []string

	// DefaultModel returns the default model to use if none is specified
	DefaultModel() string
}

// DecodeB64 decodes a base64 string, returning empty string on error
func DecodeB64(s string) string {
	decoded, _ := base64.StdEncoding.DecodeString(s)
	return string(decoded)
}
