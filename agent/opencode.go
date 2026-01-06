package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/onkernel/kernel-go-sdk"
)

// OpenCodeAgent implements the Agent interface for OpenCode CLI
type OpenCodeAgent struct{}

// NewOpenCodeAgent creates a new OpenCode agent
func NewOpenCodeAgent() *OpenCodeAgent {
	return &OpenCodeAgent{}
}

// Name returns the agent identifier
func (a *OpenCodeAgent) Name() string {
	return "opencode"
}

// RequiredEnvVar returns empty string since OpenCode supports multiple providers.
// Use ProviderEnvVars() to get all supported provider env vars.
func (a *OpenCodeAgent) RequiredEnvVar() string {
	return ""
}

// DefaultModel returns the default model for OpenCode
func (a *OpenCodeAgent) DefaultModel() string {
	return "anthropic/claude-opus-4-5"
}

// OpenCodeProviderEnvVars lists all environment variables that OpenCode recognizes
// for provider authentication. These are forwarded to the Kernel environment.
var OpenCodeProviderEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"GOOGLE_API_KEY",
	"GOOGLE_GENERATIVE_AI_API_KEY",
	"AZURE_OPENAI_API_KEY",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_PROFILE",
	"AWS_REGION",
	"AWS_BEARER_TOKEN_BEDROCK",
	"OPENROUTER_API_KEY",
	"GROQ_API_KEY",
	"MISTRAL_API_KEY",
	"PERPLEXITY_API_KEY",
	"TOGETHER_API_KEY",
	"XAI_API_KEY",
	"DEEPSEEK_API_KEY",
	"FIREWORKS_API_KEY",
	"CEREBRAS_API_KEY",
	"SAMBANOVA_API_KEY",
}

// ProviderEnvVars returns all provider env vars that OpenCode supports
func (a *OpenCodeAgent) ProviderEnvVars() []string {
	return OpenCodeProviderEnvVars
}

// Install installs OpenCode in the browser environment
func (a *OpenCodeAgent) Install(ctx context.Context, client kernel.Client, sessionID string) error {
	fmt.Println(HeaderStyle.Render("Installing OpenCode..."))

	proc := client.Browsers.Process

	// Install opencode
	result, err := proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "export HOME=/home/kernel && curl -fsSL https://opencode.ai/install | bash"},
		TimeoutSec: kernel.Opt(int64(300)),
	})
	if err != nil {
		return fmt.Errorf("install opencode: %w", err)
	}
	if result.ExitCode != 0 {
		stderr := DecodeB64(result.StderrB64)
		return fmt.Errorf("opencode install failed (exit %d): %s", result.ExitCode, stderr)
	}

	// Fix ownership so kernel user can run opencode
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "chown -R kernel:kernel /home/kernel/.opencode /home/kernel/.local/share/opencode 2>/dev/null || true"},
		AsRoot:  kernel.Opt(true),
	})

	fmt.Println(SuccessStyle.Render("OpenCode installed"))
	return nil
}

// ConfigureMCP sets up the MCP server configuration for OpenCode
func (a *OpenCodeAgent) ConfigureMCP(ctx context.Context, client kernel.Client, sessionID string, config MCPConfig) error {
	fmt.Println(HeaderStyle.Render("Configuring MCP..."))

	proc := client.Browsers.Process

	// Create .config/opencode directory
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "mkdir -p /home/kernel/.config/opencode"},
	})

	// Convert MCPConfig to OpenCode format
	// OpenCode uses: {"mcp": {"name": {"type": "local", "command": [...], "enabled": true}}}
	opencodeMCP := make(map[string]any)
	mcpServers := make(map[string]any)

	for name, server := range config.MCPServers {
		// Build command array: [command, ...args]
		cmdArray := append([]string{server.Command}, server.Args...)
		mcpServers[name] = map[string]any{
			"type":    "local",
			"command": cmdArray,
			"enabled": true,
		}
	}
	opencodeMCP["mcp"] = mcpServers

	mcpJSON, _ := json.MarshalIndent(opencodeMCP, "", "  ")
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", fmt.Sprintf("cat > /home/kernel/.config/opencode/opencode.json << 'EOF'\n%s\nEOF", mcpJSON)},
	})

	// Fix ownership
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "chown -R kernel:kernel /home/kernel/.config/opencode"},
		AsRoot:  kernel.Opt(true),
	})

	fmt.Println(SuccessStyle.Render("MCP configured"))
	return nil
}

// OpenCodeStreamEvent represents a JSON event from OpenCode's stream output
type OpenCodeStreamEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	SessionID string `json:"sessionID"`
	Part      struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
		Tool string `json:"tool,omitempty"`
		// For tool_use events
		State struct {
			Status string `json:"status,omitempty"`
			Input  struct {
				Code string `json:"code,omitempty"`
			} `json:"input,omitempty"`
		} `json:"state,omitempty"`
	} `json:"part,omitempty"`
}

// Run executes a prompt using OpenCode
func (a *OpenCodeAgent) Run(ctx context.Context, client kernel.Client, sessionID string, opts RunOptions, handler StreamHandler) (int64, error) {
	if opts.AgentTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(opts.AgentTimeout)*time.Second)
		defer cancel()
	}

	fmt.Println(HeaderStyle.Render("Running OpenCode..."))
	fmt.Println()

	// Escape prompt for shell
	escaped := strings.ReplaceAll(opts.Prompt, "'", "'\"'\"'")
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)

	// Build model argument
	modelArg := ""
	if opts.Model != "" {
		modelArg = fmt.Sprintf(" -m %s", opts.Model)
	}

	// Build environment variable exports from the EnvVars map
	var envExports strings.Builder
	for key, value := range opts.EnvVars {
		if value != "" {
			// Escape single quotes in values
			escapedValue := strings.ReplaceAll(value, "'", "'\"'\"'")
			envExports.WriteString(fmt.Sprintf("export %s='%s'\n", key, escapedValue))
		}
	}

	// OpenCode flags:
	// - run: non-interactive mode
	// - --format json: JSON streaming output
	// OpenCode supports multiple providers via environment variables
	// Note: opencode installs to ~/.opencode/bin/opencode
	script := fmt.Sprintf(`#!/bin/bash
export HOME=/home/kernel
export PATH="$HOME/.opencode/bin:$HOME/.local/bin:$PATH"
%scd /home/kernel
/home/kernel/.opencode/bin/opencode run --format json%s "%s"
`, envExports.String(), modelArg, escaped)

	// Write script and run as kernel user with PTY (using 'script' command)
	cmd := fmt.Sprintf(
		`cat > /tmp/run_opencode.sh << 'SCRIPT'
%s
SCRIPT
chmod +x /tmp/run_opencode.sh
script -q -c "su - kernel -c '/tmp/run_opencode.sh'" /dev/null`,
		script,
	)

	spawn, err := client.Browsers.Process.Spawn(ctx, sessionID, kernel.BrowserProcessSpawnParams{
		Command: "bash", Args: []string{"-c", cmd},
	})
	if err != nil {
		return 1, fmt.Errorf("spawn opencode: %w", err)
	}

	stream := client.Browsers.Process.StdoutStreamStreaming(ctx, spawn.ProcessID, kernel.BrowserProcessStdoutStreamParams{
		ID: sessionID,
	})

	var jsonBuffer strings.Builder
	var exitCode int64
	decoder := json.NewDecoder(strings.NewReader(""))

	for stream.Next() {
		event := stream.Current()

		if event.Event == kernel.BrowserProcessStdoutStreamResponseEventExit {
			exitCode = event.ExitCode
			break
		}

		if event.DataB64 != "" {
			data := DecodeB64(event.DataB64)
			jsonBuffer.WriteString(data)

			// Try to parse all complete JSON objects from buffer
			decoder = json.NewDecoder(strings.NewReader(jsonBuffer.String()))
			var consumed int
			for {
				var ocEvent OpenCodeStreamEvent
				if err := decoder.Decode(&ocEvent); err != nil {
					break // incomplete JSON, wait for more data
				}
				// Convert OpenCode event to common StreamEvent format
				streamEvent := a.convertEvent(ocEvent)
				handler(streamEvent)
				consumed = int(decoder.InputOffset())
			}
			// Keep only unparsed data in buffer
			if consumed > 0 {
				remaining := jsonBuffer.String()[consumed:]
				jsonBuffer.Reset()
				jsonBuffer.WriteString(remaining)
			}
		}
	}

	// Process any remaining complete JSON in buffer
	decoder = json.NewDecoder(strings.NewReader(jsonBuffer.String()))
	for {
		var ocEvent OpenCodeStreamEvent
		if err := decoder.Decode(&ocEvent); err != nil {
			break
		}
		streamEvent := a.convertEvent(ocEvent)
		handler(streamEvent)
	}

	if err := stream.Err(); err != nil {
		return 1, fmt.Errorf("stream error: %w", err)
	}

	return exitCode, nil
}

// convertEvent converts an OpenCode stream event to the common StreamEvent format
func (a *OpenCodeAgent) convertEvent(ocEvent OpenCodeStreamEvent) StreamEvent {
	var streamEvent StreamEvent

	switch ocEvent.Type {
	case "text":
		streamEvent.Type = "assistant"
		if ocEvent.Part.Text != "" {
			streamEvent.Message.Content = []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: ocEvent.Part.Text},
			}
		}
	case "tool_use":
		streamEvent.Type = "tool_call"
		// Mark as started if status is not completed
		if ocEvent.Part.State.Status != "completed" {
			streamEvent.Subtype = "started"
		}
		streamEvent.ToolCall.MCPToolCall.Args.Name = ocEvent.Part.Tool
		streamEvent.ToolCall.MCPToolCall.Args.Args.Code = ocEvent.Part.State.Input.Code
	default:
		// Pass through other event types
		streamEvent.Type = ocEvent.Type
	}

	return streamEvent
}
