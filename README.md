# playwriter-in-kernel

Run [Playwriter](https://github.com/remorses/playwriter) browser automation tasks via AI coding agents inside a [Kernel](https://onkernel.com) cloud browser.

This tool creates a Kernel browser session with the Playwriter Chrome extension, installs an AI coding agent (Cursor, Claude Code, or OpenCode), builds Playwriter from source, and executes prompts that can control the browser using natural language.

## Prerequisites

- **Go 1.22+**
- **Kernel API Key** - Get from [Kernel Dashboard](https://dashboard.onkernel.com)
- **Agent API Key**:
  - For Cursor: `CURSOR_API_KEY` from your Cursor subscription
  - For Claude: `ANTHROPIC_API_KEY` from Anthropic
  - For OpenCode: `ANTHROPIC_API_KEY` from Anthropic (or configure other providers via opencode auth)
- **Playwriter extension** uploaded to Kernel (one-time setup, see below)

## One-Time Setup

Upload the Playwriter Chrome extension to your Kernel account:

```bash
# Install Kernel CLI if needed
brew install onkernel/tap/kernel

# Download the Chrome extension and upload to Kernel
kernel extensions download-web-store \
  "https://chromewebstore.google.com/detail/playwriter-mcp/jfeammnjpkecdekppnclgkkffahnhfhe" \
  --to ./playwriter-ext

kernel extensions upload ./playwriter-ext --name playwriter
```

> **Note**: Playwriter has two components: a Chrome extension (uploaded above) and a relay/MCP server. The Chrome extension is from the Web Store, but the relay is built from source during setup because the npm package is outdated.

## Installation

```bash
go build -o playwriter-in-kernel .
```

## Usage

```bash
export KERNEL_API_KEY="your-kernel-api-key"
export CURSOR_API_KEY="your-cursor-api-key"      # For cursor agent
export ANTHROPIC_API_KEY="your-anthropic-api-key" # For claude or opencode agent

# Using Cursor
./playwriter-in-kernel -agent cursor -p "use duckduckgo to find the latest news in NYC"

# Using Claude Code
./playwriter-in-kernel -agent claude -p "use playwriter to navigate to example.com"

# Using OpenCode
./playwriter-in-kernel -agent opencode -p "use playwriter to navigate to example.com and tell me the page title"
```

### Options

| Flag               | Description                                   | Default    |
| ------------------ | --------------------------------------------- | ---------- |
| `-p`               | Prompt to send to the agent (required)        |            |
| `-agent`           | Agent to use: `cursor`, `claude`, or `opencode` (required) |            |
| `-s`               | Reuse an existing browser session ID          |            |
| `-m`               | Model to use                                  | `opus-4.5` |
| `-timeout-seconds` | Browser session timeout                       | 600        |
| `-agent-timeout`   | Hard timeout for agent (0 = no limit)         | 0          |
| `-d`               | Delete browser session on exit                | false      |

### Examples

```bash
# Basic usage with Cursor
./playwriter-in-kernel -agent cursor -p "use playwriter to navigate to news.ycombinator.com and summarize the top 3 stories"

# Use Claude Code instead
./playwriter-in-kernel -agent claude -p "use playwriter to navigate to example.com and describe what you see"

# Reuse an existing session (faster for multiple prompts)
./playwriter-in-kernel -agent cursor -s f9v6br0tme7epagxtdss952x -p "click the first link"

# Auto-cleanup after running
./playwriter-in-kernel -agent cursor -d -p "navigate to example.com and tell me what the page says"

# Set a timeout to prevent hanging
./playwriter-in-kernel -agent-timeout 120 -p "search for recent news"

# Longer browser timeout for debugging (30 minutes)
./playwriter-in-kernel -timeout-seconds 1800 -p "explore the website"
```

## How It Works

1. **Creates a Kernel browser** with the Playwriter extension pre-loaded
2. **Pins the extension** to the Chrome toolbar
3. **Installs the agent** (Cursor or Claude Code)
4. **Builds Playwriter from source** with the extension allowlist disabled
5. **Starts the Playwriter relay** server
6. **Configures MCP** to use the locally built Playwriter
7. **Activates Playwriter** by clicking the extension icon
8. **Runs the agent** with your prompt, streaming output in real-time
9. **Displays results** including tool calls and assistant responses

## Architecture

The codebase uses an agent-agnostic interface so Cursor, Claude, and OpenCode all follow the same setup flow:

```
.
├── main.go           # CLI entrypoint and orchestration
├── agent/
│   ├── agent.go      # Agent interface and shared utilities
│   ├── cursor.go     # Cursor-agent implementation
│   ├── claude.go     # Claude Code implementation
│   └── opencode.go   # OpenCode implementation
├── browser/
│   └── setup.go      # Browser setup, Playwriter install, and activation
└── stream/
    └── parser.go     # Output stream parsing and display
```

### Agent Interface

All agents implement the Agent interface:

- Name() - Returns "cursor", "claude", or "opencode"
- Install() - Installs the agent CLI
- ConfigureMCP() - Sets up MCP server configuration
- Run() - Executes a prompt and streams output
- RequiredEnvVar() - Returns the API key env var name
- DefaultModel() - Returns the default model

### Playwriter Components

Playwriter has two parts:

1. **Chrome Extension** (from Web Store): Injected into the browser, captures CDP commands. You upload this to Kernel once during setup.

2. **Relay/MCP Server** (built from source): Bridges between the AI agent and the Chrome extension. Built automatically during each session setup because the npm package is outdated.

The relay build process:

1. Clone playwriter from GitHub (latest main branch)
2. Patch the relay to disable extension ID validation (it has a hardcoded allowlist that doesn't include the uploaded extension's ID)
3. Install dependencies with pnpm (required for workspace dependencies), run build (uses bun internally)
4. Start the relay server on port 19988
5. Configure MCP to use the locally built server

## Technical Notes

- **PTY Requirement**: All agents require a pseudo-terminal for output. The tool uses `script -q` to allocate one.
- **HOME Environment**: Kernel's process exec defaults to `HOME=/`. The tool explicitly sets `HOME=/home/kernel`.
- **Extension ID**: The Chrome extension ID (`hnenofdplkoaanpegekhdmbpckgdecba`) is derived from the extension's public key and is consistent across all Kernel users.
- **Extension allowlist**: The Playwriter relay has a hardcoded allowlist of known extension IDs. The extension ID when uploaded to Kernel isn't in this list, so we patch the relay to disable validation.
- **Claude as kernel user**: Claude Code refuses `--dangerously-skip-permissions` as root, so we use `su - kernel`.
- **Build from source**: The npm package is outdated, so we build the relay from source to get the `/extension` websocket endpoint.

## Session Reuse

When you run without -d, the browser session stays alive. You can reuse it for faster subsequent runs:

```bash
# First run - note the session ID in the output
./playwriter-in-kernel -agent cursor -p "navigate to github.com"
# Output: Reuse: playwriter-in-kernel -agent cursor -s f9v6br0tme7epagxtdss952x -p "..."

# Subsequent runs - skip setup, go straight to the prompt
./playwriter-in-kernel -agent cursor -s f9v6br0tme7epagxtdss952x -p "click on Explore"
```

## Links

- [Playwriter](https://github.com/remorses/playwriter) - Browser automation extension and MCP server
- [Kernel](https://onkernel.com) - Cloud browser infrastructure
- [Cursor CLI](https://cursor.com/cli)
- [Claude Code](https://code.claude.com/docs/en/overview)
- [OpenCode](https://opencode.ai) - Open source AI coding agent
