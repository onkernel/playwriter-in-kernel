package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/onkernel/kernel-go-sdk"
)

// ClaudeAgent implements the Agent interface for Anthropic's Claude Code CLI
type ClaudeAgent struct{}

// NewClaudeAgent creates a new Claude agent
func NewClaudeAgent() *ClaudeAgent {
	return &ClaudeAgent{}
}

// Name returns the agent identifier
func (a *ClaudeAgent) Name() string {
	return "claude"
}

// RequiredEnvVar returns the environment variable name for the API key
func (a *ClaudeAgent) RequiredEnvVar() string {
	return "ANTHROPIC_API_KEY"
}

// DefaultModel returns the default model for Claude
func (a *ClaudeAgent) DefaultModel() string {
	return "opus-4.5"
}

// Install installs Claude Code in the browser environment
func (a *ClaudeAgent) Install(ctx context.Context, client kernel.Client, sessionID string) error {
	fmt.Println(HeaderStyle.Render("Installing Claude Code..."))

	result, err := client.Browsers.Process.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "export HOME=/home/kernel && npm install -g @anthropic-ai/claude-code"},
		TimeoutSec: kernel.Opt(int64(300)),
	})
	if err != nil {
		return fmt.Errorf("install claude code: %w", err)
	}
	if result.ExitCode != 0 {
		stderr := DecodeB64(result.StderrB64)
		return fmt.Errorf("claude code install failed (exit %d): %s", result.ExitCode, stderr)
	}

	fmt.Println(SuccessStyle.Render("Claude Code installed"))
	return nil
}

// ConfigureMCP sets up the MCP server configuration for Claude Code
func (a *ClaudeAgent) ConfigureMCP(ctx context.Context, client kernel.Client, sessionID string, config MCPConfig) error {
	fmt.Println(HeaderStyle.Render("Configuring MCP..."))

	proc := client.Browsers.Process

	// Create .claude directory
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "mkdir -p /home/kernel/.claude"},
	})

	// Write MCP config (used via --mcp-config flag at runtime)
	mcpJSON, _ := json.MarshalIndent(config, "", "  ")
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", fmt.Sprintf("cat > /home/kernel/.mcp.json << 'EOF'\n%s\nEOF", mcpJSON)},
	})

	// Fix ownership
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "chown -R kernel:kernel /home/kernel/.claude /home/kernel/.mcp.json"},
		AsRoot:  kernel.Opt(true),
	})

	fmt.Println(SuccessStyle.Render("MCP configured"))
	return nil
}

// Run executes a prompt using Claude Code
func (a *ClaudeAgent) Run(ctx context.Context, client kernel.Client, sessionID string, opts RunOptions, handler StreamHandler) (int64, error) {
	if opts.AgentTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(opts.AgentTimeout)*time.Second)
		defer cancel()
	}

	fmt.Println(HeaderStyle.Render("Running Claude Code..."))
	fmt.Println()

	// Escape prompt for shell
	escaped := strings.ReplaceAll(opts.Prompt, "'", "'\"'\"'")
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)

	// Build model argument
	modelArg := ""
	if opts.Model != "" {
		modelArg = fmt.Sprintf(" --model %s", opts.Model)
	}

	// Claude Code flags:
	// - -p (--print): non-interactive mode
	// - --verbose: required for stream-json output
	// - --output-format stream-json: streaming JSON output
	// - --dangerously-skip-permissions: allow MCP tools without prompting
	// - --mcp-config: load MCP config from file
	// Must run as 'kernel' user (--dangerously-skip-permissions fails as root)
	script := fmt.Sprintf(`#!/bin/bash
export HOME=/home/kernel
export ANTHROPIC_API_KEY='%s'
cd /home/kernel
/usr/local/bin/claude --mcp-config /home/kernel/.mcp.json -p --verbose --output-format stream-json --dangerously-skip-permissions%s "%s"
`, opts.APIKey, modelArg, escaped)

	// Write script and run as kernel user with PTY (using 'script' command)
	cmd := fmt.Sprintf(
		`cat > /tmp/run_claude.sh << 'SCRIPT'
%s
SCRIPT
chmod +x /tmp/run_claude.sh
script -q -c "su - kernel -c '/tmp/run_claude.sh'" /dev/null`,
		script,
	)

	spawn, err := client.Browsers.Process.Spawn(ctx, sessionID, kernel.BrowserProcessSpawnParams{
		Command: "bash", Args: []string{"-c", cmd},
	})
	if err != nil {
		return 1, fmt.Errorf("spawn claude: %w", err)
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
				var streamEvent StreamEvent
				if err := decoder.Decode(&streamEvent); err != nil {
					break // incomplete JSON, wait for more data
				}
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
		var streamEvent StreamEvent
		if err := decoder.Decode(&streamEvent); err != nil {
			break
		}
		handler(streamEvent)
	}

	if err := stream.Err(); err != nil {
		return 1, fmt.Errorf("stream error: %w", err)
	}

	return exitCode, nil
}
