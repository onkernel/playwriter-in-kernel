// playwriter-in-kernel runs cursor-agent with the Playwriter MCP server
// inside a Kernel browser environment.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/onkernel/kernel-go-sdk"
	"github.com/onkernel/kernel-go-sdk/option"
	"github.com/onkernel/kernel-go-sdk/shared"
)

const (
	// Chrome internal extension ID for Playwriter (different from Web Store ID)
	playwriterExtensionID = "hnenofdplkoaanpegekhdmbpckgdecba"

	// Paths and settings for Kernel browser environment
	kernelPreferencesPath = "/home/kernel/user-data/Default/Preferences"
	kernelHome            = "/home/kernel"

	// Extension icon position in toolbar (1920x1080 resolution)
	extensionIconX = 1775
	extensionIconY = 55
)

// Output styles
var (
	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	successStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warningStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	assistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
)

// MCPConfig represents Cursor's MCP server configuration
type MCPConfig struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

type MCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// StreamEvent represents a JSON event from cursor-agent's stream output
type StreamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Message struct {
		Content []struct {
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

// lastPrintedMessage tracks output to avoid duplicates
var lastPrintedMessage string

func decodeB64(s string) string {
	decoded, _ := base64.StdEncoding.DecodeString(s)
	return string(decoded)
}

// pinExtension adds an extension to Chrome's pinned toolbar extensions
func pinExtension(ctx context.Context, client kernel.Client, sessionID, extensionID string) error {
	resp, err := client.Browsers.Fs.ReadFile(ctx, sessionID, kernel.BrowserFReadFileParams{
		Path: kernelPreferencesPath,
	})
	if err != nil {
		return fmt.Errorf("read preferences: %w", err)
	}
	defer resp.Body.Close()

	prefsData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	var prefs map[string]any
	if err := json.Unmarshal(prefsData, &prefs); err != nil {
		return fmt.Errorf("parse preferences: %w", err)
	}

	extensions, _ := prefs["extensions"].(map[string]any)
	if extensions == nil {
		extensions = make(map[string]any)
		prefs["extensions"] = extensions
	}

	var pinned []string
	if existing, ok := extensions["pinned_extensions"].([]any); ok {
		for _, id := range existing {
			if s, ok := id.(string); ok {
				if s == extensionID {
					return nil // Already pinned
				}
				pinned = append(pinned, s)
			}
		}
	}

	pinned = append(pinned, extensionID)
	extensions["pinned_extensions"] = pinned

	newPrefs, _ := json.Marshal(prefs)
	return client.Browsers.Fs.WriteFile(ctx, sessionID, bytes.NewReader(newPrefs), kernel.BrowserFWriteFileParams{
		Path: kernelPreferencesPath,
	})
}

// setupBrowser creates and configures a new browser session
func setupBrowser(ctx context.Context, client kernel.Client, timeoutSeconds int64, showReuseHint bool) (sessionID, liveViewURL string, err error) {
	fmt.Println(headerStyle.Render("Creating browser session..."))

	browser, err := client.Browsers.New(ctx, kernel.BrowserNewParams{
		Headless:       kernel.Opt(false),
		TimeoutSeconds: kernel.Opt(timeoutSeconds),
		Extensions:     []shared.BrowserExtensionParam{{Name: kernel.Opt("playwriter")}},
	})
	if err != nil {
		return "", "", fmt.Errorf("create browser: %w", err)
	}

	sessionID = browser.SessionID
	liveViewURL = browser.BrowserLiveViewURL

	fmt.Println(successStyle.Render("Browser created: ") + sessionID)
	fmt.Println(dimStyle.Render("Live view: ") + liveViewURL)
	if showReuseHint {
		fmt.Println(dimStyle.Render("Reuse session: ") + "playwriter-in-kernel -session " + sessionID + " -p \"...\"")
	}

	// Wait for browser to initialize
	time.Sleep(5 * time.Second)

	// Pin extension (requires stopping Chrome temporarily)
	fmt.Println(headerStyle.Render("Pinning Playwriter extension..."))
	proc := client.Browsers.Process

	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "supervisorctl", Args: []string{"stop", "chromium"},
		AsRoot: kernel.Opt(true), TimeoutSec: kernel.Opt(int64(30)),
	})
	time.Sleep(2 * time.Second)

	if err := pinExtension(ctx, client, sessionID, playwriterExtensionID); err != nil {
		fmt.Println(warningStyle.Render("Warning: Failed to pin extension: " + err.Error()))
	}

	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "chown", Args: []string{"kernel:kernel", kernelPreferencesPath},
		AsRoot: kernel.Opt(true), TimeoutSec: kernel.Opt(int64(10)),
	})

	proc.Spawn(ctx, sessionID, kernel.BrowserProcessSpawnParams{
		Command: "supervisorctl", Args: []string{"start", "chromium"},
		AsRoot: kernel.Opt(true),
	})
	time.Sleep(5 * time.Second)

	// Navigate to a clean page
	fmt.Println(headerStyle.Render("Setting up browser..."))
	client.Browsers.Playwright.Execute(ctx, sessionID, kernel.BrowserPlaywrightExecuteParams{
		Code: `
			const pages = context.pages();
			for (let i = 1; i < pages.length; i++) await pages[i].close();
			if (pages.length > 0) await pages[0].goto('https://duckduckgo.com');
		`,
		TimeoutSec: kernel.Opt(int64(30)),
	})
	time.Sleep(2 * time.Second)

	// Install Cursor
	fmt.Println(headerStyle.Render("Installing Cursor..."))
	result, err := proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "export HOME=/home/kernel && curl -fsSL https://cursor.com/install | bash"},
		TimeoutSec: kernel.Opt(int64(300)),
	})
	if err != nil {
		return "", "", fmt.Errorf("install cursor: %w", err)
	}
	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("cursor install failed (exit %d)", result.ExitCode)
	}

	// Configure MCP
	fmt.Println(headerStyle.Render("Configuring MCP..."))
	mcpConfig := MCPConfig{
		MCPServers: map[string]MCPServer{
			"playwriter": {Command: "npx", Args: []string{"playwriter@latest"}},
		},
	}
	mcpJSON, _ := json.MarshalIndent(mcpConfig, "", "  ")

	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "mkdir -p /home/kernel/.cursor /home/kernel/.config/cursor"},
	})

	for _, path := range []string{"/home/kernel/.cursor/mcp.json", "/home/kernel/.config/cursor/mcp.json"} {
		proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
			Command: "bash",
			Args:    []string{"-c", fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", path, mcpJSON)},
		})
	}

	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args:    []string{"-c", "chown -R kernel:kernel /home/kernel/.cursor /home/kernel/.config/cursor"},
		AsRoot:  kernel.Opt(true),
	})

	fmt.Println(successStyle.Render("Setup complete"))

	return sessionID, liveViewURL, nil
}

// processStreamLine parses and displays a single line of cursor-agent output
func processStreamLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "[?") || strings.HasPrefix(line, "\x1b[") {
		return
	}

	var event StreamEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		// Non-JSON output
		if !strings.HasPrefix(line, "[?") {
			fmt.Println(line)
		}
		return
	}

	switch event.Type {
	case "system", "user", "thinking", "result":
		// Skip these event types
	case "tool_call":
		if event.Subtype == "started" {
			toolName := event.ToolCall.MCPToolCall.Args.Name
			if toolName == "" {
				toolName = event.ToolCall.MCPToolCall.Args.ToolName
			}
			if toolName != "" {
				// Show code preview for playwriter-execute
				code := event.ToolCall.MCPToolCall.Args.Args.Code
				if code != "" {
					// Truncate and clean up the code for display
					code = strings.ReplaceAll(code, "\n", " ")
					code = strings.Join(strings.Fields(code), " ") // collapse whitespace
					if len(code) > 80 {
						code = code[:77] + "..."
					}
					fmt.Println(toolStyle.Render("[tool] "+toolName+": ") + dimStyle.Render(code))
				} else {
					fmt.Println(toolStyle.Render("[tool] " + toolName))
				}
			}
		}
	case "assistant":
		for _, c := range event.Message.Content {
			text := strings.TrimSpace(c.Text)
			if text != "" && text != lastPrintedMessage {
				// Collapse multiple consecutive newlines to single newlines
				for strings.Contains(text, "\n\n") {
					text = strings.ReplaceAll(text, "\n\n", "\n")
				}
				// Single-line messages are typically planning/thinking, multi-line are final responses
				if strings.Contains(text, "\n") {
					fmt.Println(assistantStyle.Render(text))
				} else {
					fmt.Println(dimStyle.Render("> ") + assistantStyle.Render(text))
				}
				lastPrintedMessage = text
			}
		}
	}
}

// runCursorAgent executes cursor-agent with the given prompt
func runCursorAgent(ctx context.Context, client kernel.Client, sessionID, apiKey, prompt string, timeout int64) (int64, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	// Click extension to activate Playwriter
	fmt.Println(headerStyle.Render("Activating Playwriter extension..."))
	client.Browsers.Computer.ClickMouse(ctx, sessionID, kernel.BrowserComputerClickMouseParams{
		X: extensionIconX, Y: extensionIconY,
	})
	time.Sleep(2 * time.Second)

	fmt.Println(headerStyle.Render("Running cursor-agent..."))
	fmt.Println()

	lastPrintedMessage = ""

	// Escape prompt for shell
	escaped := strings.ReplaceAll(prompt, "'", "'\"'\"'")
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)

	// cursor-agent requires a PTY to produce output, so we use 'script' to allocate one
	cmd := fmt.Sprintf(
		`export HOME=/home/kernel && export PATH="$HOME/.local/bin:$PATH" && export CURSOR_API_KEY='%s' && script -q -c "cursor-agent -f --approve-mcps --output-format stream-json -p \"%s\"" /dev/null`,
		apiKey, escaped,
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

	var lineBuffer strings.Builder
	var exitCode int64

	for stream.Next() {
		event := stream.Current()

		if event.Event == kernel.BrowserProcessStdoutStreamResponseEventExit {
			exitCode = event.ExitCode
			break
		}

		if event.DataB64 != "" {
			data := decodeB64(event.DataB64)
			for _, ch := range data {
				if ch == '\n' {
					processStreamLine(lineBuffer.String())
					lineBuffer.Reset()
				} else {
					lineBuffer.WriteRune(ch)
				}
			}
		}
	}

	if lineBuffer.Len() > 0 {
		processStreamLine(lineBuffer.String())
	}

	if err := stream.Err(); err != nil {
		return 1, fmt.Errorf("stream error: %w", err)
	}

	return exitCode, nil
}

func main() {
	prompt := flag.String("p", "", "Prompt to send to cursor-agent (required)")
	session := flag.String("session", "", "Reuse an existing browser session ID")
	timeout := flag.Int64("timeout-seconds", 600, "Browser session timeout in seconds")
	agentTimeout := flag.Int64("agent-timeout", 0, "Hard timeout for cursor-agent in seconds (0 = no limit)")
	deleteBrowser := flag.Bool("d", false, "Delete browser session on exit")
	flag.Parse()

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "Usage: playwriter-in-kernel -p \"your prompt\" [options]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Options:")
		fmt.Fprintln(os.Stderr, "  -p string           Prompt to send to cursor-agent (required)")
		fmt.Fprintln(os.Stderr, "  -session string     Reuse an existing browser session ID")
		fmt.Fprintln(os.Stderr, "  -timeout-seconds    Browser session timeout (default: 600)")
		fmt.Fprintln(os.Stderr, "  -agent-timeout      Hard timeout for cursor-agent (default: 0 = no limit)")
		fmt.Fprintln(os.Stderr, "  -d                  Delete browser session on exit")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Environment variables:")
		fmt.Fprintln(os.Stderr, "  KERNEL_API_KEY      Kernel API key (required)")
		fmt.Fprintln(os.Stderr, "  CURSOR_API_KEY      Cursor API key (required)")
		os.Exit(1)
	}

	kernelKey := os.Getenv("KERNEL_API_KEY")
	cursorKey := os.Getenv("CURSOR_API_KEY")

	if kernelKey == "" {
		fmt.Fprintln(os.Stderr, errorStyle.Render("KERNEL_API_KEY environment variable is required"))
		os.Exit(1)
	}
	if cursorKey == "" {
		fmt.Fprintln(os.Stderr, errorStyle.Render("CURSOR_API_KEY environment variable is required"))
		os.Exit(1)
	}

	ctx := context.Background()
	client := kernel.NewClient(option.WithAPIKey(kernelKey))

	var sessionID, liveViewURL string
	var err error
	var created bool

	if *session != "" {
		sessionID = *session
		fmt.Println(dimStyle.Render("Using existing session: " + sessionID))
		fmt.Println()
	} else {
		sessionID, liveViewURL, err = setupBrowser(ctx, client, *timeout, !*deleteBrowser)
		if err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render("Setup failed: "+err.Error()))
			os.Exit(1)
		}
		created = true

		fmt.Println(strings.Repeat("-", 60))
		fmt.Println(dimStyle.Render("Session: ") + sessionID)
		fmt.Println(dimStyle.Render("Live view: ") + liveViewURL)
		fmt.Println(strings.Repeat("-", 60))
	}

	if created && *deleteBrowser {
		defer func() {
			fmt.Println()
			fmt.Println(dimStyle.Render("Cleaning up browser session..."))
			client.Browsers.DeleteByID(ctx, sessionID)
		}()
	}

	exitCode, err := runCursorAgent(ctx, client, sessionID, cursorKey, *prompt, *agentTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render(err.Error()))
		os.Exit(1)
	}

	fmt.Println()

	if exitCode != 0 {
		fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("cursor-agent exited with code %d", exitCode)))
		os.Exit(int(exitCode))
	}
}
