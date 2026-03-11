package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cnjack/coding/internal/config"
	"github.com/cnjack/coding/internal/session"
)

func (m Model) handleModelInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	cfg, err := config.LoadConfig()
	if err != nil {
		m.lines = append(m.lines, toolErrorStyle.Render("✗ Failed to load config: "+err.Error()))
		return m, tea.Batch(cmds...)
	}

	var items []list.Item
	for provider, pCfg := range cfg.Models {
		for _, modelName := range pCfg.Models {
			desc := "Provider: " + provider
			if provider == cfg.Provider && modelName == cfg.Model {
				desc += " (Current)"
			}
			items = append(items, modelItem{
				provider: provider,
				model:    modelName,
				title:    provider + " - " + modelName,
				desc:     desc,
			})
		}
	}
	m.modelPicker.SetItems(items)
	m.pickingModel = true
	m.textarea.Blur()
	m.modelPicker.Title = "Select Model"
	return m, tea.Batch(cmds...)
}

// handleResumeInput parses the /resume command.
// /resume           — shows session picker for current project
// /resume <UUID>    — resumes specific session directly
func (m Model) handleResumeInput(input string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(input, "/resume"))

	if arg != "" {
		// Direct UUID resume
		m.lines = append(m.lines, toolLabelStyle.Render("📂 Loading session..."))
		m.thinking = true
		m.mode = ModeAgent
		m.agentDone = false
		uuid := arg
		cmds = append(cmds, m.spinner.Tick)
		cmds = append(cmds, func() tea.Msg {
			return ResumeRequestMsg{UUID: uuid}
		})
		return m, tea.Batch(cmds...)
	}

	// No UUID — show session picker
	metas, err := session.ListSessions(m.pwd)
	if err != nil || len(metas) == 0 {
		msg := "No sessions found for this project."
		if err != nil {
			msg = fmt.Sprintf("Error loading sessions: %v", err)
		}
		m.lines = append(m.lines, toolLabelStyle.Render("📂 Resume:")+" "+msg)
		m.refreshViewport()
		return m, tea.Batch(cmds...)
	}

	var items []list.Item
	// Show newest first
	for i := len(metas) - 1; i >= 0; i-- {
		items = append(items, sessionListItem{meta: metas[i]})
	}
	m.sessionPicker.SetItems(items)
	m.pickingSession = true
	m.textarea.Blur()
	return m, tea.Batch(cmds...)
}

func (m Model) settingMenuView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	modW, modH := w-8, h-4
	if modW > 120 {
		modW = 120
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1).
		Width(modW)

	headerText := fmt.Sprintf(" %s ", toolNameStyle.Render("⚙ Settings"))

	m.settingMenu.SetSize(modW-6, modH-6)
	m.settingMenu.Title = "Settings (↑/↓ to navigate, Enter to confirm, Esc to cancel)"
	m.settingMenu.SetShowHelp(false)
	m.settingMenu.SetShowStatusBar(true)
	m.settingMenu.SetShowPagination(false)

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Padding(0, 1).Render(headerText),
		"",
		m.settingMenu.View(),
	)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m Model) sshAliasPickerView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	modW, modH := w-8, h-4
	if modW > 120 {
		modW = 120
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1).
		Width(modW)

	headerText := fmt.Sprintf(" %s ", toolNameStyle.Render("🔗 SSH Connections"))

	m.sshAliasPicker.SetSize(modW-6, modH-6)
	m.sshAliasPicker.Title = "Select SSH connection (↑/↓ to navigate, Enter to connect, Esc to cancel)"
	m.sshAliasPicker.SetShowHelp(false)
	m.sshAliasPicker.SetShowStatusBar(true)
	m.sshAliasPicker.SetShowPagination(false)

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Padding(0, 1).Render(headerText),
		"",
		m.sshAliasPicker.View(),
	)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m Model) modelPickerView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	modW, modH := w-8, h-4
	if modW > 120 {
		modW = 120
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1).
		Width(modW)

	headerText := fmt.Sprintf(" %s ", toolNameStyle.Render("Select Model"))

	m.modelPicker.SetSize(modW-6, modH-6)
	m.modelPicker.Title = "Select model (↑/↓ to navigate, Enter to confirm, Esc to cancel)"
	m.modelPicker.SetShowHelp(false)
	m.modelPicker.SetShowStatusBar(true)
	m.modelPicker.SetShowPagination(false)

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Padding(0, 1).Render(headerText),
		"",
		m.modelPicker.View(),
	)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m Model) dirPickerView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	modW, modH := w-8, h-4
	if modW > 120 {
		modW = 120
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1).
		Width(modW)

	headerText := fmt.Sprintf(" Open Folder: %s ", toolNameStyle.Render(m.sshAddr))

	pathBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colorSecondary).
		Padding(0, 1).
		Foreground(colorText).
		Width(modW - 4).
		Render(m.sshPath)

	m.dirList.SetSize(modW-6, modH-10)
	m.dirList.Title = "↑/↓ navigate · Enter browse · Tab open folder · Esc cancel"
	m.dirList.SetShowHelp(false)
	m.dirList.SetShowStatusBar(true)
	m.dirList.SetShowPagination(false)

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Padding(0, 1).Render(headerText),
		pathBox,
		"",
		m.dirList.View(),
	)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m Model) approvalDialogView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	modW := 60
	if modW > w-8 {
		modW = w - 8
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWarning).
		Padding(1, 2).
		Width(modW)

	headerText := toolNameStyle.Render("⚠️ Tool Approval Required")

	argsDisplay := m.approvalToolArgs
	if len(argsDisplay) > 200 {
		argsDisplay = argsDisplay[:200] + "..."
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Render(headerText),
		"",
		fmt.Sprintf("Tool: %s", toolNameStyle.Render(m.approvalToolName)),
		"",
		lipgloss.NewStyle().Foreground(colorMuted).Render("Arguments:"),
		lipgloss.NewStyle().Foreground(colorText).Render(argsDisplay),
		"",
		lipgloss.NewStyle().Foreground(colorMuted).Render("[y] Approve  [n] Reject"),
	)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m Model) sessionPickerView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	modW, modH := w-8, h-4
	if modW > 120 {
		modW = 120
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1).
		Width(modW)

	headerText := fmt.Sprintf(" %s ", toolNameStyle.Render("📂 Resume Session"))

	m.sessionPicker.SetSize(modW-6, modH-6)
	m.sessionPicker.Title = "Select session (↑/↓ navigate · Enter resume · Esc cancel)"
	m.sessionPicker.SetShowHelp(false)
	m.sessionPicker.SetShowStatusBar(true)
	m.sessionPicker.SetShowPagination(false)

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Padding(0, 1).Render(headerText),
		"",
		m.sessionPicker.View(),
	)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}
