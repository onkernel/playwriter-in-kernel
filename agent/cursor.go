package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/onkernel/kernel-go-sdk"
)

// CursorAgent implements the Agent interface for Cursor's cursor-agent CLI
type CursorAgent struct{}

// NewCursorAgent creates a new Cursor agent
func NewCursorAgent() *CursorAgent {
	return &CursorAgent{}
}

// Name returns the agent identifier
func (a *CursorAgent) Name() string {
	return "cursor"
}

// RequiredEnvVar returns the environment variable name for the API key
func (a *CursorAgent) RequiredEnvVar() string {
	return "CURSOR_API_KEY"
}

// DefaultModel returns the default model for Cursor
func (a *CursorAgent) DefaultModel() string {
	return "opus-4.5"
}

// Install installs cursor-agent in the browser environment
func (a *CursorAgent) Install(ctx context.Context, client kernel.Client, sessionID string) error {
	fmt.Println(HeaderStyle.Render("Installing Cursor..."))

	result, err := client.Browsers.Process.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "export HOME=/home/kernel && curl -fsSL https://cursor.com/install | bash"},
		TimeoutSec: kernel.Opt(int64(300)),
	})
	if err != nil {
		return fmt.Errorf("install cursor: %w", err)
	}
	if result.ExitCode != 0 {
		stderr := DecodeB64(result.StderrB64)
		return fmt.Errorf("cursor install failed (exit %d): %s", result.ExitCode, stderr)
	}

	fmt.Println(SuccessStyle.Render("Cursor installed"))
	return nil
}

// ConfigureMCP sets up the MCP server configuration for Cursor
func (a *CursorAgent) ConfigureMCP(ctx context.Context, client kernel.Client, sessionID string, config MCPConfig) error {
	fmt.Println(HeaderStyle.Render("Configuring MCP..."))

	mcpJSON, _ := json.MarshalIndent(config, "", "  ")
	proc := client.Browsers.Process

	// Create config directories
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "mkdir -p /home/kernel/.cursor /home/kernel/.config/cursor"},
	})

	// Write MCP config to both possible locations
	for _, path := range []string{"/home/kernel/.cursor/mcp.json", "/home/kernel/.config/cursor/mcp.json"} {
		proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
			Command: "bash",
			Args:    []string{"-c", fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", path, mcpJSON)},
		})
	}

	// Fix ownership
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "chown -R kernel:kernel /home/kernel/.cursor /home/kernel/.config/cursor"},
		AsRoot:  kernel.Opt(true),
	})

	fmt.Println(SuccessStyle.Render("MCP configured"))
	return nil
}

// Run executes a prompt using cursor-agent
func (a *CursorAgent) Run(ctx context.Context, client kernel.Client, sessionID string, opts RunOptions, handler StreamHandler) (int64, error) {
	if opts.AgentTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(opts.AgentTimeout)*time.Second)
		defer cancel()
	}

	fmt.Println(HeaderStyle.Render("Running cursor-agent..."))
	fmt.Println()

	// Escape prompt for shell
	escaped := strings.ReplaceAll(opts.Prompt, "'", "'\"'\"'")
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)

	// Build command with optional model flag
	modelArg := ""
	if opts.Model != "" {
		modelArg = fmt.Sprintf(" --model %s", opts.Model)
	}

	// cursor-agent requires a PTY, so we use 'script' to allocate one
	cmd := fmt.Sprintf(
		`export HOME=/home/kernel && export PATH="$HOME/.local/bin:$PATH" && export CURSOR_API_KEY='%s' && script -q -c "cursor-agent -f --approve-mcps --output-format stream-json%s -p \"%s\"" /dev/null`,
		opts.APIKey, modelArg, escaped,
	)

	spawn, err := client.Browsers.Process.Spawn(ctx, sessionID, kernel.BrowserProcessSpawnParams{
		Command: "bash", Args: []string{"-c", cmd},
	})
	if err != nil {
		return 1, fmt.Errorf("spawn cursor-agent: %w", err)
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
