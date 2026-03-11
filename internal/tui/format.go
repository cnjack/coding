package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func formatToolArgs(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return truncate(argsJSON, 120)
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		val := truncate(fmt.Sprintf("%v", v), 60)
		parts = append(parts, fmt.Sprintf("%s=%s", k, val))
	}
	return truncate(strings.Join(parts, " "), 200)
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", "↲")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// formatToolResult returns styled output lines depending on the tool name.
func formatToolResult(toolName, output string, termWidth int) []string {
	switch toolName {
	case "execute":
		return formatExecuteOutput(output, termWidth)
	case "edit":
		return formatEditOutput(output, termWidth)
	default:
		return []string{fmt.Sprintf("   %s %s",
			toolSuccessStyle.Render("✓ Done:"),
			toolResultStyle.Render(truncate(output, maxToolOutputLen)))}
	}
}

// formatExecuteOutput shows the last 5 lines of command output in a bordered box.
func formatExecuteOutput(output string, termWidth int) []string {
	const tailLines = 5
	rawLines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	// Take last N lines
	start := 0
	if len(rawLines) > tailLines {
		start = len(rawLines) - tailLines
	}
	tail := rawLines[start:]

	var boxContent strings.Builder
	if start > 0 {
		boxContent.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Italic(true).
			Render(fmt.Sprintf("... (%d lines hidden)", start)))
		boxContent.WriteString("\n")
	}
	for i, line := range tail {
		boxContent.WriteString(line)
		if i < len(tail)-1 {
			boxContent.WriteString("\n")
		}
	}

	boxWidth := termWidth - 8
	if boxWidth < 30 {
		boxWidth = 30
	}

	box := outputBoxStyle.Width(boxWidth).Render(boxContent.String())
	return []string{
		fmt.Sprintf("   %s", toolSuccessStyle.Render("✓ Output:")),
		box,
	}
}

// formatEditOutput renders the edit result with colored diff lines.
func formatEditOutput(output string, termWidth int) []string {
	// Split output into status line and diff block
	parts := strings.SplitN(output, "\n\n", 2)
	statusLine := parts[0]

	result := []string{
		fmt.Sprintf("   %s %s", toolSuccessStyle.Render("✓"), toolResultStyle.Render(statusLine)),
	}

	if len(parts) < 2 {
		return result
	}

	// Parse the diff block (```diff ... ```)
	diffBlock := parts[1]
	diffBlock = strings.TrimPrefix(diffBlock, "```diff\n")
	diffBlock = strings.TrimSuffix(diffBlock, "```")
	diffBlock = strings.TrimRight(diffBlock, "\n")

	if diffBlock == "" {
		return result
	}

	var diffContent strings.Builder
	for _, line := range strings.Split(diffBlock, "\n") {
		if strings.HasPrefix(line, "+ ") {
			diffContent.WriteString(diffAddStyle.Render(line))
		} else if strings.HasPrefix(line, "- ") {
			diffContent.WriteString(diffRemoveStyle.Render(line))
		} else {
			diffContent.WriteString(line)
		}
		diffContent.WriteString("\n")
	}

	boxWidth := termWidth - 8
	if boxWidth < 30 {
		boxWidth = 30
	}

	diffBox := outputBoxStyle.Width(boxWidth).Render(strings.TrimRight(diffContent.String(), "\n"))
	result = append(result, diffBox)
	return result
}
