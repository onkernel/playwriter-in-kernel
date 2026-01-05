// Package stream provides utilities for parsing and displaying agent output streams.
package stream

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"playwriter-setup/agent"
)

// Output styles
var (
	DimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	ToolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	AssistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
)

// Parser handles parsing and displaying agent stream output
type Parser struct {
	lastPrintedMessage string
}

// NewParser creates a new stream parser
func NewParser() *Parser {
	return &Parser{}
}

// ParseLine parses a single line of JSON output and returns a StreamEvent
func (p *Parser) ParseLine(line string) (*agent.StreamEvent, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "[?") || strings.HasPrefix(line, "\x1b[") {
		return nil, nil
	}

	var event agent.StreamEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return nil, err
	}

	return &event, nil
}

// ProcessEvent handles a stream event and prints appropriate output
func (p *Parser) ProcessEvent(event agent.StreamEvent) {
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
					fmt.Println(ToolStyle.Render("[tool] "+toolName+": ") + DimStyle.Render(code))
				} else {
					fmt.Println(ToolStyle.Render("[tool] " + toolName))
				}
			}
		}
	case "assistant":
		for _, c := range event.Message.Content {
			text := strings.TrimSpace(c.Text)
			if text != "" && text != p.lastPrintedMessage {
				// Collapse multiple consecutive newlines to single newlines
				for strings.Contains(text, "\n\n") {
					text = strings.ReplaceAll(text, "\n\n", "\n")
				}
				// Single-line messages are typically planning/thinking, multi-line are final responses
				if strings.Contains(text, "\n") {
					fmt.Println(AssistantStyle.Render(text))
				} else {
					fmt.Println(DimStyle.Render("> ") + AssistantStyle.Render(text))
				}
				p.lastPrintedMessage = text
			}
		}
	}
}

// ProcessLine parses and processes a single line, printing output as needed
// Returns true if the line was valid JSON, false otherwise
func (p *Parser) ProcessLine(line string) bool {
	event, err := p.ParseLine(line)
	if err != nil {
		// Non-JSON output - print it directly if not a control sequence
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "[?") {
			fmt.Println(line)
		}
		return false
	}
	if event != nil {
		p.ProcessEvent(*event)
	}
	return true
}
