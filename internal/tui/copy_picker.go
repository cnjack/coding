package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// copySourceText returns the assistant text the /copy picker and Ctrl+Y
// operate on: the in-flight streaming text when a response is being produced,
// otherwise the last completed response. The bytes are identical whether the
// session runs locally, over SSH or behind the cloud relay — extraction always
// happens locally on the transcript content, never on rendered output.
func (m *Model) copySourceText() string {
	if m.currentText != nil {
		if text := m.currentText.String(); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return m.lastAssistantRawText
}

// handleCopyInput opens the /copy target picker. Targets are extracted from a
// snapshot of the current response taken here, so later streaming cannot shift
// the selection or copy text from a different turn.
func (m *Model) handleCopyInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	targets := analyzeCopyTargets(m.copySourceText())
	if len(targets) == 0 {
		m.lines = append(m.lines, textLine(toolErrorStyle.Render("✗ Nothing to copy yet — no assistant response in this view")))
		m.refreshViewport()
		return m, tea.Batch(cmds...)
	}

	items := make([]list.Item, 0, len(targets))
	for _, t := range targets {
		items = append(items, copyItem{target: t})
	}
	del := list.NewDefaultDelegate()
	del.SetSpacing(0)
	m.copyPicker = list.New(items, del, 60, 15)
	m.copyPicker.Title = "↑/↓ select · Enter copy · Esc cancel"
	m.copyPicker.SetShowHelp(false)
	m.copyPicker.SetShowStatusBar(false)
	m.copyPicker.SetShowPagination(true)
	m.copyPicker.SetFilteringEnabled(true)
	m.copyPicker.Select(0)
	m.copyTargets = targets
	m.pickingCopy = true
	m.textarea.Blur()
	return m, tea.Batch(cmds...)
}

// closeCopyPicker dismisses the picker without copying.
func (m *Model) closeCopyPicker() {
	m.copyPicker.ResetFilter()
	m.pickingCopy = false
	m.copyTargets = nil
	m.textarea.Focus()
	m.refreshViewport()
}

// copySelectedTarget copies the highlighted target and reports what was
// copied in the transcript (label + size). Clipboard writes ride the
// terminal's OSC52 protocol, which not every terminal supports — the
// transcript line names the target so an empty paste is diagnosable. An
// empty target is an explicit error, never a silent no-op.
func (m *Model) copySelectedTarget(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	sel := m.copyPicker.SelectedItem()
	m.closeCopyPicker()
	if sel == nil {
		return m, tea.Batch(cmds...)
	}
	target, ok := sel.(copyItem)
	if !ok {
		return m, tea.Batch(cmds...)
	}
	if strings.TrimSpace(target.target.text) == "" {
		m.lines = append(m.lines, textLine(toolErrorStyle.Render("✗ Copy failed — target is empty")))
		return m, tea.Batch(cmds...)
	}
	cmds = append(cmds, tea.SetClipboard(target.target.text))
	m.lines = append(m.lines, textLine(fmt.Sprintf("  %s %s — %s",
		toolSuccessStyle.Render("✓ Copied"),
		toolNameStyle.Render(target.target.label),
		target.target.detail)))
	return m, tea.Batch(cmds...)
}

// handleCopyPickerKey processes key input while the /copy picker is open.
func (m *Model) handleCopyPickerKey(msg tea.KeyPressMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	// While the list is actively filtering, forward all keys to it.
	if m.copyPicker.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.copyPicker, cmd = m.copyPicker.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}

	switch msg.String() {
	case "enter":
		return m.copySelectedTarget(cmds)
	case "ctrl+c", "esc":
		m.closeCopyPicker()
		return m, tea.Batch(cmds...)
	}

	var cmd tea.Cmd
	m.copyPicker, cmd = m.copyPicker.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// copyPickerView renders the /copy target picker overlay.
func (m Model) copyPickerView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	contentW := w - 12
	if contentW > 70 {
		contentW = 70
	}
	if contentW < 30 {
		contentW = 30
	}
	listH := h - 10
	if listH < 4 {
		listH = 4
	}

	boxStyle := dialogBoxStyle.Width(contentW)

	headerText := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).
		Render("📋  Copy to clipboard")
	subtitle := lipgloss.NewStyle().Foreground(colorDimText).
		Render(fmt.Sprintf("Current response · %d targets (snapshot taken when opened)", len(m.copyTargets)))

	m.copyPicker.SetSize(contentW-4, listH)
	m.copyPicker.SetShowHelp(false)
	m.copyPicker.SetShowPagination(true)

	parts := []string{headerText, subtitle, "", m.copyPicker.View()}

	footer := lipgloss.NewStyle().Foreground(colorDimText).
		Render("  ↑/↓ select · Enter copy · Esc cancel · / filter")
	parts = append(parts, footer)

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}
