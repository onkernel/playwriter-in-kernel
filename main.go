// playwriter-in-kernel runs AI coding agents with the Playwriter MCP server
// inside a Kernel browser environment.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/onkernel/kernel-go-sdk"
	"github.com/onkernel/kernel-go-sdk/option"

	"playwriter-setup/agent"
	"playwriter-setup/browser"
	"playwriter-setup/stream"
)

// Output styles
var (
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// getAgent returns the appropriate agent based on name
func getAgent(name string) (agent.Agent, error) {
	switch strings.ToLower(name) {
	case "cursor":
		return agent.NewCursorAgent(), nil
	case "claude":
		return agent.NewClaudeAgent(), nil
	default:
		return nil, fmt.Errorf("unknown agent: %s (supported: cursor, claude)", name)
	}
}

func main() {
	prompt := flag.String("p", "", "Prompt to send to the agent (required)")
	session := flag.String("s", "", "Reuse an existing browser session ID")
	timeout := flag.Int64("timeout-seconds", 600, "Browser session timeout in seconds")
	agentTimeout := flag.Int64("agent-timeout", 0, "Hard timeout for agent in seconds (0 = no limit)")
	model := flag.String("m", "", "Model to use (default depends on agent)")
	deleteBrowser := flag.Bool("d", false, "Delete browser session on exit")
	agentName := flag.String("agent", "", "Agent to use: cursor or claude (required)")
	flag.Parse()

	if *prompt == "" || *agentName == "" {
		fmt.Fprintln(os.Stderr, "Usage: playwriter-in-kernel -agent <cursor|claude> -p \"your prompt\" [options]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Options:")
		fmt.Fprintln(os.Stderr, "  -agent string       Agent to use: cursor or claude (required)")
		fmt.Fprintln(os.Stderr, "  -p string           Prompt to send to the agent (required)")
		fmt.Fprintln(os.Stderr, "  -s string           Reuse an existing browser session ID")
		fmt.Fprintln(os.Stderr, "  -m string           Model to use (default depends on agent)")
		fmt.Fprintln(os.Stderr, "  -timeout-seconds    Browser session timeout (default: 600)")
		fmt.Fprintln(os.Stderr, "  -agent-timeout      Hard timeout for agent (default: 0 = no limit)")
		fmt.Fprintln(os.Stderr, "  -d                  Delete browser session on exit")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Environment variables:")
		fmt.Fprintln(os.Stderr, "  KERNEL_API_KEY      Kernel API key (required)")
		fmt.Fprintln(os.Stderr, "  CURSOR_API_KEY      Cursor API key (required for cursor agent)")
		fmt.Fprintln(os.Stderr, "  ANTHROPIC_API_KEY   Anthropic API key (required for claude agent)")
		os.Exit(1)
	}

	// Get the agent
	ag, err := getAgent(*agentName)
	if err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render(err.Error()))
		os.Exit(1)
	}

	// Check environment variables
	kernelKey := os.Getenv("KERNEL_API_KEY")
	agentAPIKey := os.Getenv(ag.RequiredEnvVar())

	if kernelKey == "" {
		fmt.Fprintln(os.Stderr, errorStyle.Render("KERNEL_API_KEY environment variable is required"))
		os.Exit(1)
	}
	if agentAPIKey == "" {
		fmt.Fprintln(os.Stderr, errorStyle.Render(ag.RequiredEnvVar()+" environment variable is required"))
		os.Exit(1)
	}

	// Set default model if not specified
	modelToUse := *model
	if modelToUse == "" {
		modelToUse = ag.DefaultModel()
	}

	ctx := context.Background()
	client := kernel.NewClient(option.WithAPIKey(kernelKey))

	var sessionID, liveViewURL string
	var created bool

	if *session != "" {
		// Reuse existing session
		sessionID = *session
		browserInfo, err := client.Browsers.Get(ctx, sessionID)
		if err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render("Failed to get session: "+err.Error()))
			os.Exit(1)
		}
		liveViewURL = browserInfo.BrowserLiveViewURL
		fmt.Println(dimStyle.Render("Using session: ") + sessionID)
		fmt.Println(dimStyle.Render("Live view: ") + liveViewURL)
	} else {
		// Create new session with full setup
		result, err := browser.Setup(ctx, client, browser.SetupOptions{
			TimeoutSeconds: *timeout,
			ShowReuseHint:  !*deleteBrowser,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render("Browser setup failed: "+err.Error()))
			os.Exit(1)
		}
		sessionID = result.SessionID
		liveViewURL = result.LiveViewURL
		created = true

		// Install the agent CLI
		if err := ag.Install(ctx, client, sessionID); err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render("Agent install failed: "+err.Error()))
			os.Exit(1)
		}

		// Install playwriter from source (both agents use the same version)
		if err := browser.InstallPlaywriterFromSource(ctx, client, sessionID); err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render("Playwriter install failed: "+err.Error()))
			os.Exit(1)
		}

		// Start the relay
		if err := browser.StartPlaywriterRelay(ctx, client, sessionID); err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render("Relay start failed: "+err.Error()))
			os.Exit(1)
		}

		// Configure MCP with the locally built playwriter
		if err := ag.ConfigureMCP(ctx, client, sessionID, agent.PlaywriterMCPConfig()); err != nil {
			fmt.Fprintln(os.Stderr, errorStyle.Render("MCP configuration failed: "+err.Error()))
			os.Exit(1)
		}

		fmt.Println(successStyle.Render("Setup complete"))
		fmt.Println(strings.Repeat("-", 60))
		fmt.Println(dimStyle.Render("Session: ") + sessionID)
		fmt.Println(dimStyle.Render("Live view: ") + liveViewURL)
		fmt.Println(strings.Repeat("-", 60))
	}

	// Cleanup on exit if requested
	if created && *deleteBrowser {
		defer func() {
			fmt.Println()
			fmt.Println(dimStyle.Render("Cleaning up browser session..."))
			client.Browsers.DeleteByID(ctx, sessionID)
		}()
	}

	// Activate the extension (clicks the icon to trigger connection to relay)
	if browser.IsPlaywriterConnected(ctx, client, sessionID) {
		fmt.Println(dimStyle.Render("Playwriter extension already connected"))
	} else {
		browser.ActivatePlaywriter(ctx, client, sessionID)
	}

	// Create stream parser for output handling
	parser := stream.NewParser()

	// Run the agent
	exitCode, err := ag.Run(ctx, client, sessionID, agent.RunOptions{
		Prompt:       *prompt,
		Model:        modelToUse,
		APIKey:       agentAPIKey,
		AgentTimeout: *agentTimeout,
	}, func(event agent.StreamEvent) {
		parser.ProcessEvent(event)
	})

	if err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render(err.Error()))
		os.Exit(1)
	}

	fmt.Println()

	if exitCode != 0 {
		fmt.Fprintln(os.Stderr, errorStyle.Render(fmt.Sprintf("%s exited with code %d", ag.Name(), exitCode)))
		os.Exit(int(exitCode))
	}
}
