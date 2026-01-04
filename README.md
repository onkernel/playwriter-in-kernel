# playwriter-in-kernel

Run [Playwriter](https://github.com/anthropics/playwriter) browser automation tasks via `cursor-agent` inside a [Kernel](https://onkernel.com) cloud browser.

This tool creates a Kernel browser session with the Playwriter Chrome extension, installs Cursor, and executes prompts that can control the browser using natural language.

## Prerequisites

- **Go 1.22+**
- **Kernel API Key** - Get from [Kernel Dashboard](https://dashboard.onkernel.com)
- **Cursor API Key** - From your Cursor subscription
- **Playwriter extension** uploaded to Kernel (one-time setup, see below)

## One-Time Setup

Upload the Playwriter extension to your Kernel account:

```bash
# Install Kernel CLI if needed
brew install onkernel/tap/kernel

# Download and upload the extension
kernel extensions download-web-store \
  "https://chromewebstore.google.com/detail/playwriter-mcp/jfeammnjpkecdekppnclgkkffahnhfhe" \
  --to ./playwriter-ext

kernel extensions upload ./playwriter-ext --name playwriter
```

## Installation

```bash
go build -o playwriter-in-kernel .
```

## Usage

```bash
export KERNEL_API_KEY="your-kernel-api-key"
export CURSOR_API_KEY="your-cursor-api-key"

./playwriter-in-kernel -p "use playwriter to search google for the weather in NYC"
```

### Options

| Flag | Description | Default |
|------|-------------|---------|
| `-p` | Prompt to send to cursor-agent (required) | |
| `-s` | Reuse an existing browser session ID | |
| `-m` | Model to use (e.g., `opus-4.5`, `sonnet-4`, `gpt-5`) | `opus-4.5` |
| `-timeout-seconds` | Browser session timeout | 600 |
| `-agent-timeout` | Hard timeout for cursor-agent (0 = no limit) | 0 |
| `-d` | Delete browser session on exit | false |

### Examples

```bash
# Basic usage - creates a new browser, runs the task
./playwriter-in-kernel -p "use playwriter to navigate to news.ycombinator.com and tell me the top 3 stories"

# Reuse an existing session (faster for multiple prompts)
./playwriter-in-kernel -s abc123xyz -p "click the first link"

# Auto-cleanup after running
./playwriter-in-kernel -d -p "take a screenshot of example.com"

# Set a timeout to prevent hanging
./playwriter-in-kernel -agent-timeout 120 -p "search for recent news"
```

## How It Works

1. **Creates a Kernel browser** with the Playwriter extension pre-loaded
2. **Pins the extension** to the Chrome toolbar
3. **Installs Cursor** and configures it with the Playwriter MCP server
4. **Activates Playwriter** by clicking the extension icon
5. **Runs cursor-agent** with your prompt, streaming output in real-time
6. **Displays results** including tool calls and assistant responses

### Technical Notes

- **PTY Requirement**: `cursor-agent` requires a pseudo-terminal to produce output. The tool uses `script -q -c ... /dev/null` to allocate one.
- **HOME Environment**: Kernel's process exec defaults to `HOME=/`. The tool explicitly sets `HOME=/home/kernel` for all commands.
- **Extension ID**: The Chrome internal extension ID (`hnenofdplkoaanpegekhdmbpckgdecba`) differs from the Web Store ID.

## MCP Configuration

The tool creates this configuration at `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "playwriter": {
      "command": "npx",
      "args": ["playwriter@latest"]
    }
  }
}
```

## Session Reuse

When you run without `-d`, the browser session stays alive. You can reuse it for faster subsequent runs:

```bash
# First run - note the session ID in the output
./playwriter-in-kernel -p "navigate to github.com"
# Output: Reuse session: playwriter-in-kernel -s abc123xyz -p "..."

# Subsequent runs - skip setup, go straight to the prompt
./playwriter-in-kernel -s abc123xyz -p "click on Explore"
```

## Links

- [Playwriter](https://github.com/anthropics/playwriter) - Browser automation MCP server
- [Kernel](https://onkernel.com) - Cloud browser infrastructure
- [Cursor](https://cursor.com) - AI-powered code editor
