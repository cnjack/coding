package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Color palette
	colorPrimary   = lipgloss.Color("#7C3AED") // violet
	colorSecondary = lipgloss.Color("#06B6D4") // cyan
	colorSuccess   = lipgloss.Color("#10B981") // green
	colorError     = lipgloss.Color("#EF4444") // red
	colorWarning   = lipgloss.Color("#F59E0B") // amber
	colorMuted     = lipgloss.Color("#6B7280") // gray
	colorText      = lipgloss.Color("#E5E7EB") // light gray
	colorBg        = lipgloss.Color("#111827") // dark bg
	colorToolBg    = lipgloss.Color("#1F2937") // slightly lighter

	// Styles
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary).
			PaddingLeft(1)

	assistantLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorPrimary)

	toolLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSecondary)

	toolNameStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWarning)

	toolArgsStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	toolResultStyle = lipgloss.NewStyle().
			Foreground(colorText).
			PaddingLeft(4)

	toolSuccessStyle = lipgloss.NewStyle().
				Foreground(colorSuccess).
				Bold(true)

	toolErrorStyle = lipgloss.NewStyle().
			Foreground(colorError).
			Bold(true)

	toolBlockStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted).
			Padding(0, 1)

	outputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted).
			Foreground(colorText).
			Padding(0, 1).
			MarginLeft(3)

	toolMutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			PaddingLeft(4).
			Italic(true)

	diffAddStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	diffRemoveStyle = lipgloss.NewStyle().
			Foreground(colorError)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(colorSecondary)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorError).
			Bold(true)

	promptStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	userLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSecondary)

	inputStyle = lipgloss.NewStyle().
			Foreground(colorText)

	dividerStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	todoLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSecondary)

	todoCompletedStyle = lipgloss.NewStyle().
				Foreground(colorSuccess)

	todoInProgressStyle = lipgloss.NewStyle().
				Foreground(colorWarning).
				Bold(true)

	todoPendingStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	todoCancelledStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Italic(true)
)

func divider(width int) string {
	if width <= 0 {
		width = 80
	}
	line := ""
	for i := 0; i < width; i++ {
		line += "─"
	}
	return dividerStyle.Render(line)
}
