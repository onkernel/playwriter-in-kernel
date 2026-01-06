// Package browser provides utilities for setting up and configuring
// Kernel browser sessions for agent automation with Playwriter.
package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/onkernel/kernel-go-sdk"
	"github.com/onkernel/kernel-go-sdk/shared"
)

const (
	// PlaywriterExtensionID is Chrome's internal ID for the Playwriter extension.
	// This ID is derived from the extension's public key in manifest.json and is
	// consistent across all users who upload the same extension to Kernel.
	// (Note: This differs from the Chrome Web Store listing ID which is jfeammnjpkecdekppnclgkkffahnhfhe)
	PlaywriterExtensionID = "hnenofdplkoaanpegekhdmbpckgdecba"

	// PreferencesPath is the Chrome preferences file in Kernel
	PreferencesPath = "/home/kernel/user-data/Default/Preferences"

	// KernelHome is the home directory for the kernel user
	KernelHome = "/home/kernel"

	// Extension icon position in toolbar (1920x1080 resolution)
	// This is where the pinned Playwriter extension appears
	ExtensionIconX = 1775
	ExtensionIconY = 55
)

// Output styles
var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// decodeB64 decodes a base64 string
func decodeB64(s string) string {
	decoded, _ := base64.StdEncoding.DecodeString(s)
	return string(decoded)
}

// SetupOptions contains options for browser setup
type SetupOptions struct {
	TimeoutSeconds int64
	ShowReuseHint  bool
}

// SetupResult contains the result of browser setup
type SetupResult struct {
	SessionID   string
	LiveViewURL string
}

// Setup creates and configures a new browser session with the Playwriter extension.
func Setup(ctx context.Context, client kernel.Client, opts SetupOptions) (*SetupResult, error) {
	fmt.Println(headerStyle.Render("Creating browser session..."))

	browser, err := client.Browsers.New(ctx, kernel.BrowserNewParams{
		Headless:       kernel.Opt(false),
		TimeoutSeconds: kernel.Opt(opts.TimeoutSeconds),
		Extensions:     []shared.BrowserExtensionParam{{Name: kernel.Opt("playwriter")}},
	})
	if err != nil {
		return nil, fmt.Errorf("create browser: %w", err)
	}

	result := &SetupResult{
		SessionID:   browser.SessionID,
		LiveViewURL: browser.BrowserLiveViewURL,
	}

	fmt.Println(successStyle.Render("Browser created: ") + result.SessionID)
	fmt.Println(dimStyle.Render("Live view: ") + result.LiveViewURL)
	if opts.ShowReuseHint {
		fmt.Println(dimStyle.Render("Reuse: ") + "playwriter-in-kernel -s " + result.SessionID + " -p \"...\"")
	}

	// Pin extension (requires stopping Chrome temporarily)
	fmt.Println(headerStyle.Render("Pinning Playwriter extension..."))
	proc := client.Browsers.Process

	proc.Exec(ctx, result.SessionID, kernel.BrowserProcessExecParams{
		Command: "supervisorctl", Args: []string{"stop", "chromium"},
		AsRoot: kernel.Opt(true), TimeoutSec: kernel.Opt(int64(30)),
	})
	time.Sleep(2 * time.Second)

	if err := pinExtension(ctx, client, result.SessionID, PlaywriterExtensionID); err != nil {
		fmt.Println(warningStyle.Render("Warning: Failed to pin extension: " + err.Error()))
	}

	proc.Exec(ctx, result.SessionID, kernel.BrowserProcessExecParams{
		Command: "chown", Args: []string{"kernel:kernel", PreferencesPath},
		AsRoot: kernel.Opt(true), TimeoutSec: kernel.Opt(int64(10)),
	})

	proc.Spawn(ctx, result.SessionID, kernel.BrowserProcessSpawnParams{
		Command: "supervisorctl", Args: []string{"start", "chromium"},
		AsRoot: kernel.Opt(true),
	})
	time.Sleep(5 * time.Second)

	// Navigate to a clean page
	fmt.Println(headerStyle.Render("Setting up browser..."))
	client.Browsers.Playwright.Execute(ctx, result.SessionID, kernel.BrowserPlaywrightExecuteParams{
		Code: `
			const pages = context.pages();
			for (let i = 1; i < pages.length; i++) await pages[i].close();
			if (pages.length > 0) await pages[0].goto('https://duckduckgo.com');
		`,
		TimeoutSec: kernel.Opt(int64(30)),
	})
	time.Sleep(2 * time.Second)

	return result, nil
}

// pinExtension adds an extension to Chrome's pinned toolbar extensions
func pinExtension(ctx context.Context, client kernel.Client, sessionID, extensionID string) error {
	resp, err := client.Browsers.Fs.ReadFile(ctx, sessionID, kernel.BrowserFReadFileParams{
		Path: PreferencesPath,
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
		Path: PreferencesPath,
	})
}

// InstallPlaywriterFromSource clones the playwriter repo, patches the extension ID
// allowlist to include the Kernel extension, builds it, and creates a launch script.
// This is needed because the npm package is outdated.
func InstallPlaywriterFromSource(ctx context.Context, client kernel.Client, sessionID string) error {
	fmt.Println(headerStyle.Render("Installing Playwriter from source..."))

	proc := client.Browsers.Process

	// Clone the playwriter repo
	fmt.Println(dimStyle.Render("Cloning repository..."))
	result, err := proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args: []string{"-c", `
cd /home/kernel
rm -rf playwriter 2>/dev/null
git clone --depth 1 https://github.com/remorses/playwriter.git
`},
		TimeoutSec: kernel.Opt(int64(120)),
	})
	if err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("clone failed (exit %d): %s", result.ExitCode, decodeB64(result.StderrB64))
	}

	// Add the Kernel extension ID to the allowed list.
	// The relay has a hardcoded list of allowed extension IDs, but our Kernel extension
	// ID (hnenofdplkoaanpegekhdmbpckgdecba) isn't in that list.
	fmt.Println(dimStyle.Render("Patching extension allowlist..."))
	result, err = proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args: []string{"-c", `
cd /home/kernel/playwriter/playwriter
# Add Kernel extension ID to the allowed list
sed -i "/elnnakgjclnapgflmidlpobefkdmapdm/a\\    '` + PlaywriterExtensionID + `', // Kernel extension" src/cdp-relay.ts
`},
		TimeoutSec: kernel.Opt(int64(30)),
	})
	if err != nil {
		return fmt.Errorf("patch: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("patch failed (exit %d): %s", result.ExitCode, decodeB64(result.StderrB64))
	}

	// Install pnpm
	fmt.Println(dimStyle.Render("Installing pnpm..."))
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "npm install -g pnpm 2>/dev/null || true"},
		TimeoutSec: kernel.Opt(int64(60)),
	})

	// Install bun
	fmt.Println(dimStyle.Render("Installing bun..."))
	result, err = proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "export HOME=/home/kernel && curl -fsSL https://bun.sh/install | bash"},
		TimeoutSec: kernel.Opt(int64(120)),
	})
	if err != nil {
		return fmt.Errorf("bun install: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("bun install failed (exit %d): %s", result.ExitCode, decodeB64(result.StderrB64))
	}

	// Install dependencies
	fmt.Println(dimStyle.Render("Installing dependencies..."))
	result, err = proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "cd /home/kernel/playwriter && pnpm install --ignore-scripts"},
		TimeoutSec: kernel.Opt(int64(180)),
	})
	if err != nil {
		return fmt.Errorf("pnpm install: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("pnpm install failed (exit %d): %s", result.ExitCode, decodeB64(result.StderrB64))
	}

	// Build playwriter
	fmt.Println(dimStyle.Render("Building..."))
	result, err = proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "export PATH=\"/home/kernel/.bun/bin:$PATH\" && cd /home/kernel/playwriter/playwriter && pnpm run build"},
		TimeoutSec: kernel.Opt(int64(120)),
	})
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("build failed (exit %d): %s", result.ExitCode, decodeB64(result.StderrB64))
	}

	// Create launch script
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command: "bash",
		Args: []string{"-c", `
cat > /home/kernel/start-playwriter-relay.sh << 'EOF'
#!/bin/bash
cd /home/kernel/playwriter/playwriter
exec node dist/start-relay-server.js
EOF
chmod +x /home/kernel/start-playwriter-relay.sh
chown kernel:kernel /home/kernel/start-playwriter-relay.sh
chown -R kernel:kernel /home/kernel/playwriter
`},
		AsRoot:     kernel.Opt(true),
		TimeoutSec: kernel.Opt(int64(30)),
	})

	fmt.Println(successStyle.Render("Playwriter installed"))
	return nil
}

// StartPlaywriterRelay starts the playwriter relay server in the background.
func StartPlaywriterRelay(ctx context.Context, client kernel.Client, sessionID string) error {
	fmt.Println(headerStyle.Render("Starting Playwriter relay..."))

	proc := client.Browsers.Process

	// Kill any existing relay
	proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "pkill -f 'start-relay-server' 2>/dev/null || true"},
		TimeoutSec: kernel.Opt(int64(5)),
	})
	time.Sleep(1 * time.Second)

	// Start the relay
	proc.Spawn(ctx, sessionID, kernel.BrowserProcessSpawnParams{
		Command: "bash",
		Args:    []string{"-c", "su - kernel -c '/home/kernel/start-playwriter-relay.sh' &"},
	})

	// Wait for relay to start
	time.Sleep(3 * time.Second)

	// Verify it's running
	result, _ := proc.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "curl -s http://127.0.0.1:19988/version || echo 'not running'"},
		TimeoutSec: kernel.Opt(int64(5)),
	})
	stdout := decodeB64(result.StdoutB64)
	if result.ExitCode != 0 || stdout == "not running" {
		return fmt.Errorf("relay failed to start")
	}

	fmt.Println(successStyle.Render("Relay started: " + stdout))
	return nil
}

// ActivatePlaywriter clicks on the Playwriter extension icon to activate it
func ActivatePlaywriter(ctx context.Context, client kernel.Client, sessionID string) error {
	fmt.Println(headerStyle.Render("Activating Playwriter extension..."))
	client.Browsers.Computer.ClickMouse(ctx, sessionID, kernel.BrowserComputerClickMouseParams{
		X: ExtensionIconX, Y: ExtensionIconY,
	})
	time.Sleep(2 * time.Second)
	return nil
}

// IsPlaywriterConnected checks if the extension is connected to the relay
func IsPlaywriterConnected(ctx context.Context, client kernel.Client, sessionID string) bool {
	result, err := client.Browsers.Process.Exec(ctx, sessionID, kernel.BrowserProcessExecParams{
		Command:    "bash",
		Args:       []string{"-c", "netstat -tn 2>/dev/null | grep -q ':19988.*ESTABLISHED' && echo connected"},
		TimeoutSec: kernel.Opt(int64(5)),
	})
	if err != nil {
		return false
	}
	stdout := decodeB64(result.StdoutB64)
	return stdout == "connected\n" || stdout == "connected"
}
