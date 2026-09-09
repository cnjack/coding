package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/cnjack/jcode/internal/config"
	"github.com/cnjack/jcode/internal/flow"
	"github.com/cnjack/jcode/internal/memory"
	"github.com/cnjack/jcode/internal/mode"
	"github.com/cnjack/jcode/internal/tools"
)

// commandSuggestion represents a single slash command suggestion item.
type commandSuggestion struct {
	cmd  string // e.g. "/setting"
	desc string // e.g. "Settings menu"
}

// getAllCommands returns all available slash commands (built-in + skill).
func (m Model) getAllCommands() []commandSuggestion {
	commands := []commandSuggestion{
		{"/setting", "Settings menu"},
		{"/model", "Switch model"},
		{"/theme", "Switch color theme"},
		{"/ssh", "SSH connection"},
		{"/resume", "Resume a previous session"},
		{"/compact", "Compress conversation context"},
		{"/copy", "Copy response, code block or blockquote"},
		{"/goal", "Set a persistent objective (auto-continues)"},
		{"/bg", "List background tasks"},
		{"/channel", "Manage channels (WeChat etc.)"},
		{"/mcp", "List MCP servers / log in (/mcp login <name>)"},
		{"/browser", "Browser use status (/browser on|off)"},
		{"/computer", "Computer use status (/computer on|off|grant)"},
		{"/memory", "Project memory status (/memory sync|clear)"},
		{"/help", "Show keyboard shortcuts"},
	}
	for _, sc := range m.skillSlashCommands {
		commands = append(commands, commandSuggestion{sc.Slash, sc.Description})
	}
	for _, fc := range m.flowSlashCommands {
		commands = append(commands, commandSuggestion{fc.Slash, fc.Description})
	}
	return commands
}

func (m *Model) handleRemovedSessionToolsInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	m.lines = append(m.lines, textLine("  "+toolLabelStyle.Render("Tools:")+" unknown command"))
	m.refreshViewport()
	return m, tea.Batch(cmds...)
}

// isFlowSlash reports whether cmd (e.g. "/repo-audit") is a workflow slash
// command, used to mark it distinctly in the suggestion list.
func (m Model) isFlowSlash(cmd string) bool {
	for _, fc := range m.flowSlashCommands {
		if fc.Slash == cmd {
			return true
		}
	}
	return false
}

// filterCommands returns commands that match the given prefix.
func filterCommands(commands []commandSuggestion, prefix string) []commandSuggestion {
	var matches []commandSuggestion
	for _, c := range commands {
		if strings.HasPrefix(c.cmd, prefix) {
			matches = append(matches, c)
		}
	}
	return matches
}

// updateSuggestions updates the command suggestion state based on current input.
func (m *Model) updateSuggestions() {
	val := m.textarea.Value()
	if !strings.HasPrefix(val, "/") {
		m.cmdSuggestionActive = false
		m.cmdSuggestions = nil
		m.cmdSuggestionIndex = 0
		return
	}
	all := m.getAllCommands()
	matches := filterCommands(all, val)
	if len(matches) == 0 || (len(matches) == 1 && matches[0].cmd == val) {
		m.cmdSuggestionActive = false
		m.cmdSuggestions = nil
		m.cmdSuggestionIndex = 0
		return
	}
	m.cmdSuggestions = matches
	m.cmdSuggestionActive = true
	if m.cmdSuggestionIndex >= len(matches) {
		m.cmdSuggestionIndex = 0
	}
}

func (m Model) inputAreaView() string {
	var parts []string

	// Determine sidebar visibility locally (width-only, consistent with View())
	showSidebar := m.width >= minWidthForSidebar

	// 1. Unified mode pill line
	parts = append(parts, m.renderModePills())

	// 1b. Active goal indicator (only while a goal exists).
	if m.goalStore != nil {
		if line := m.goalStore.StatusLine(); line != "" {
			parts = append(parts, lipgloss.NewStyle().Foreground(colorPrimary).Render(line))
		}
	}

	// 2. Input content: textarea or special prompts
	switch {
	case m.planReviewActive:
		parts = append(parts, m.planReviewPromptView())
	case m.askUserActive:
		parts = append(parts, m.askUserPromptView())
	default:
		if m.cmdSuggestionActive && len(m.cmdSuggestions) > 0 {
			suggestionView := m.renderCommandSuggestions()
			if suggestionView != "" {
				parts = append(parts, suggestionView)
			}
		}
		// Clean textarea — no background, no border, with subtle vertical padding
		taView := strings.TrimRight(m.textarea.View(), "\n")
		parts = append(parts, lipgloss.NewStyle().PaddingTop(1).PaddingBottom(1).Render(taView))
	}

	// 3. Status bar (minimal for wide screen, fallback for narrow)
	if showSidebar {
		if status := m.renderMinimalStatusBar(); status != "" {
			parts = append(parts, status)
		}
	} else {
		parts = append(parts, m.renderFallbackStatusBar())
	}

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderCommandSuggestions renders the interactive command suggestion list with
// the currently selected item highlighted. The user can navigate with ↑/↓ and
// select with Tab or Enter.
func (m Model) renderCommandSuggestions() string {
	maxVisible := 5
	suggestions := m.cmdSuggestions
	total := len(suggestions)
	if total == 0 {
		return ""
	}

	// Calculate the visible window around the selected index
	start := 0
	if m.cmdSuggestionIndex >= maxVisible {
		start = m.cmdSuggestionIndex - maxVisible + 1
	}
	if start+maxVisible > total {
		start = total - maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > total {
		end = total
	}

	var lines []string
	for i := start; i < end; i++ {
		s := suggestions[i]
		cmdText := s.cmd
		descText := s.desc
		isFlow := m.isFlowSlash(s.cmd)
		if i == m.cmdSuggestionIndex {
			// Highlighted item
			cmdStyled := lipgloss.NewStyle().Bold(true).Foreground(colorOnPrimary).Background(colorPrimary).Render(cmdText)
			descStyled := lipgloss.NewStyle().Foreground(colorOnPrimary).Background(colorPrimary).Render(" " + descText)
			tag := ""
			if isFlow {
				tag = lipgloss.NewStyle().Italic(true).Foreground(colorOnPrimary).Background(colorPrimary).Render(" workflow")
			}
			// Indicator
			indicator := lipgloss.NewStyle().Foreground(colorPrimary).Render("❯")
			lines = append(lines, fmt.Sprintf("  %s %s%s%s", indicator, cmdStyled, tag, descStyled))
		} else {
			cmdStyled := lipgloss.NewStyle().Foreground(colorText).Render(cmdText)
			descStyled := lipgloss.NewStyle().Foreground(colorMuted).Render(" " + descText)
			tag := ""
			if isFlow {
				tag = lipgloss.NewStyle().Italic(true).Foreground(colorPrimary).Render(" workflow")
			}
			indicator := lipgloss.NewStyle().Foreground(colorMuted).Render(" ")
			lines = append(lines, fmt.Sprintf("  %s %s%s%s", indicator, cmdStyled, tag, descStyled))
		}
	}

	if total > maxVisible {
		remaining := total - end
		if remaining > 0 {
			lines = append(lines, lipgloss.NewStyle().PaddingLeft(3).Foreground(colorMuted).Italic(true).
				Render(fmt.Sprintf("  ... and %d more (↓ to scroll)", remaining)))
		}
	}

	return strings.Join(lines, "\n")
}

// --- Legacy hint function removed; replaced by interactive suggestion list.

// handleChannelInput shows the channel management panel.
func (m *Model) handleChannelInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	items := []list.Item{
		channelItem{title: "💬 WeChat", desc: m.channelStateDesc("wechat"), key: "wechat"},
	}
	m.channelMenu.SetItems(items)
	m.showingChannel = true
	m.textarea.Blur()
	return m, tea.Batch(cmds...)
}

// channelStateDesc returns a human-readable description for the channel state.
func (m Model) channelStateDesc(channelID string) string {
	switch m.channelStates[channelID] {
	case "enabled":
		return "Enabled"
	case "disabled":
		return "Disabled"
	default:
		return "Not connected"
	}
}

// handleSettingInput shows the setting menu.
func (m *Model) handleSettingInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	items := []list.Item{
		settingItem{title: "🔀 Switch Model", desc: "Switch to a different configured model", key: "switch_model"},
		settingItem{title: "⚙  Manage Models", desc: "Choose which models appear in the model selector", key: "manage_models"},
		settingItem{title: "➕ Add New Provider", desc: "Add a new model provider via setup wizard", key: "add_model"},
		settingItem{title: "📝 Edit Config File", desc: "Manually edit " + config.ConfigPath(), key: "edit_config"},
	}
	m.settingMenu.SetItems(items)
	m.showingSetting = true
	m.textarea.Blur()
	return m, tea.Batch(cmds...)
}

// handleBgInput handles `/bg` command to show background task status.
func (m *Model) handleBgInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	prompt := "Use the check_background tool to list all background tasks and report their status."
	m.mode = ModeAgent
	m.agentDone = false
	m.thinking = true
	m.lines = append(m.lines, textLine(fmt.Sprintf("%s /bg",
		userLabelStyle.Render("👤 You:"))))
	if m.ready {
		m.viewport.SetHeight(m.calcViewportHeight(false))
		m.viewport.SetContent(m.renderViewportContent())
		m.viewport.GotoBottom()
	}
	cmds = append(cmds, func() tea.Msg {
		return PromptSubmitMsg{Prompt: prompt}
	})
	cmds = append(cmds, m.spinner.Tick)
	return m, tea.Batch(cmds...)
}

// handleMemoryInput handles `/memory` (status), `/memory clear` and
// `/memory sync`. Status/clear are local filesystem operations; sync defers
// to the background pipeline.
func (m *Model) handleMemoryInput(prompt string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	refresh := func() {
		if m.ready {
			m.viewport.SetHeight(m.calcViewportHeight(m.inputActive()))
			m.viewport.SetContent(m.renderViewportContent())
			m.viewport.GotoBottom()
		}
	}
	arg := strings.TrimSpace(strings.TrimPrefix(prompt, "/memory"))
	root := memory.ProjectRoot(m.pwd)
	switch arg {
	case "clear":
		busy, err := memory.ClearScope(root)
		switch {
		case busy:
			m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🧠 memory pipeline is running; try /memory clear again shortly")))
		case err != nil:
			m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🧠 memory clear failed: "+err.Error())))
		default:
			m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🧠 Project memory cleared: "+root)))
		}
	case "sync":
		m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🧠 Run `jcode memory sync --wait` in a terminal to run the distillation pipeline.")))
	default:
		cfg, _ := config.LoadConfig()
		if !config.MemoryEnabled(cfg) {
			m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🧠 Memory is disabled (memory.enabled=false).")))
			break
		}
		notes := memory.RecentNotes(root, 5)
		summary := "none yet"
		if st, err := os.Stat(filepath.Join(root, memory.SummaryFile)); err == nil {
			summary = fmt.Sprintf("%d bytes", st.Size())
		}
		m.lines = append(m.lines, textLine(toolLabelStyle.Render(fmt.Sprintf("  🧠 Memory: %s", root))))
		m.lines = append(m.lines, textLine(toolLabelStyle.Render(fmt.Sprintf("     summary: %s · inbox notes: %d", summary, len(memory.RecentNotes(root, 0))))))
		for _, n := range notes {
			first := n.Text
			if i := strings.IndexByte(first, '\n'); i > 0 {
				first = first[:i]
			}
			first = memory.TruncateRunes(first, 80, "…")
			m.lines = append(m.lines, textLine(toolLabelStyle.Render("     - ["+n.Kind+"] "+first)))
		}
		m.lines = append(m.lines, textLine(toolLabelStyle.Render("     /memory sync · /memory clear")))
	}
	refresh()
	return m, tea.Batch(cmds...)
}

// handleCompactInput handles `/compact` by sending a compact request to the main goroutine.
func (m *Model) handleCompactInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	m.lines = append(m.lines, textLine(toolLabelStyle.Render("  ⏳ Compacting context...")))
	m.thinking = true
	m.agentDone = false
	if m.ready {
		m.viewport.SetContent(m.renderViewportContent())
		m.viewport.GotoBottom()
	}

	select {
	case compactCh <- struct{}{}:
	default:
	}

	cmds = append(cmds, m.spinner.Tick)
	return m, tea.Batch(cmds...)
}

// handleGoalInput handles `/goal` (status), `/goal clear`, and
// `/goal <objective>` (set + start working toward it).
func (m *Model) handleGoalInput(prompt string) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	refresh := func() {
		if m.ready {
			m.viewport.SetHeight(m.calcViewportHeight(m.inputActive()))
			m.viewport.SetContent(m.renderViewportContent())
			m.viewport.GotoBottom()
		}
	}

	action := tools.ParseGoalCommand(strings.TrimPrefix(prompt, "/goal"))

	switch action.Kind {
	case "status":
		if m.goalStore == nil || !m.goalStore.Has() {
			m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🎯 No goal set. Use /goal <objective> to set one.")))
		} else {
			m.lines = append(m.lines, textLine(toolLabelStyle.Render("  "+m.goalStore.StatusLine())))
		}
		refresh()
		return m, tea.Batch(cmds...)

	case "clear":
		if m.goalStore == nil {
			m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🎯 Goals are not available in this session.")))
			refresh()
			return m, tea.Batch(cmds...)
		}
		m.goalStore.Clear()
		m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🎯 Goal cleared.")))
		refresh()
		return m, tea.Batch(cmds...)
	}

	if m.goalStore == nil {
		m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🎯 Goals are not available in this session.")))
		refresh()
		return m, tea.Batch(cmds...)
	}

	objective, err := tools.ValidateGoalObjective(action.Objective)
	if err != nil {
		m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🎯 "+err.Error())))
		refresh()
		return m, tea.Batch(cmds...)
	}

	m.goalStore.Set(objective)
	m.lines = append(m.lines, textLine(""))
	m.lines = append(m.lines, textLine(userPromptStyle.Render("🎯 "+prompt)))
	m.lines = append(m.lines, textLine(toolLabelStyle.Render("  "+m.goalStore.StatusLine())))

	// If the agent is already running, do not submit a kickoff: the in-flight
	// run's goal-continuation guard picks the new goal up as soon as the
	// current turn finishes (a blocking send to the prompt channel here could
	// also freeze the UI).
	if !m.agentDone && m.thinking {
		m.lines = append(m.lines, textLine(toolLabelStyle.Render("  🎯 Agent is running — it will continue toward this goal after the current turn.")))
		refresh()
		return m, tea.Batch(cmds...)
	}

	// Idle: start the agent working toward the goal immediately.
	m.mode = ModeAgent
	m.agentDone = false
	m.thinking = true
	m.promptStartTime = time.Now()
	refresh()

	kickoff := tools.GoalKickoffPrompt(objective)
	cmds = append(cmds, func() tea.Msg {
		return PromptSubmitMsg{Prompt: kickoff}
	})
	cmds = append(cmds, m.spinner.Tick)
	return m, tea.Batch(cmds...)
}

// matchSkillSlash checks if the prompt matches a registered skill slash command.
// Returns a SkillSlashMsg if matched, nil otherwise.
func (m Model) matchSkillSlash(prompt string) *SkillSlashMsg {
	for _, sc := range m.skillSlashCommands {
		if prompt == sc.Slash || strings.HasPrefix(prompt, sc.Slash+" ") {
			userInput := ""
			if strings.HasPrefix(prompt, sc.Slash+" ") {
				userInput = strings.TrimSpace(prompt[len(sc.Slash):])
			}
			skillName := strings.TrimPrefix(sc.Slash, "/")
			return &SkillSlashMsg{
				SkillName: skillName,
				UserInput: userInput,
			}
		}
	}
	return nil
}

// handleSkillSlashInput handles a skill slash command by sending a prompt that
// loads the skill and follows its instructions.
func (m *Model) handleSkillSlashInput(skillName, userInput string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	prompt := fmt.Sprintf("Use the load_skill tool with name=%q and follow its instructions.", skillName)
	if userInput != "" {
		prompt += fmt.Sprintf("\n\nAdditional context: %s", userInput)
	}

	displayLabel := "/" + skillName
	if userInput != "" {
		displayLabel += " " + userInput
	}

	m.mode = ModeAgent
	m.agentDone = false
	m.thinking = true
	m.lines = append(m.lines, textLine(fmt.Sprintf("%s %s",
		userLabelStyle.Render("🔧 Skill:"), displayLabel)))
	if m.ready {
		m.viewport.SetHeight(m.calcViewportHeight(false))
		m.viewport.SetContent(m.renderViewportContent())
		m.viewport.GotoBottom()
	}
	cmds = append(cmds, func() tea.Msg {
		return PromptSubmitMsg{Prompt: prompt}
	})
	cmds = append(cmds, m.spinner.Tick)
	return m, tea.Batch(cmds...)
}

// matchFlowSlash checks if the prompt matches a registered workflow slash command.
// Returns a FlowSlashMsg if matched, nil otherwise.
func (m Model) matchFlowSlash(prompt string) *FlowSlashMsg {
	for _, fc := range m.flowSlashCommands {
		if prompt == fc.Slash || strings.HasPrefix(prompt, fc.Slash+" ") {
			userInput := ""
			if strings.HasPrefix(prompt, fc.Slash+" ") {
				userInput = strings.TrimSpace(prompt[len(fc.Slash):])
			}
			return &FlowSlashMsg{
				FlowName:  strings.TrimPrefix(fc.Slash, "/"),
				UserInput: userInput,
			}
		}
	}
	return nil
}

// handleFlowSlashInput handles a workflow slash command by sending a prompt that
// runs the saved workflow via the workflow_run tool.
func (m *Model) handleFlowSlashInput(flowName, userInput string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	prompt := flow.SlashRunPrompt(flowName, userInput)

	displayLabel := "/" + flowName
	if userInput != "" {
		displayLabel += " " + userInput
	}

	m.mode = ModeAgent
	m.agentDone = false
	m.thinking = true
	m.lines = append(m.lines, textLine(fmt.Sprintf("%s %s",
		userLabelStyle.Render("Workflow:"), displayLabel)))
	if m.ready {
		m.viewport.SetHeight(m.calcViewportHeight(false))
		m.viewport.SetContent(m.renderViewportContent())
		m.viewport.GotoBottom()
	}
	cmds = append(cmds, func() tea.Msg {
		return PromptSubmitMsg{Prompt: prompt}
	})
	cmds = append(cmds, m.spinner.Tick)
	return m, tea.Batch(cmds...)
}

// renderModePills renders the unified mode selector line above the input. The
// four states map onto distinct pill styles; Shift+Tab cycles between them.
func (m Model) renderModePills() string {
	var modePill string
	switch m.selectorMode() {
	case mode.Plan:
		modePill = modePillPlanStyle.Render(" Plan ")
	case mode.Auto:
		modePill = modePillAutoStyle.Render(" Auto ")
	case mode.FullAccess:
		modePill = modePillFullAccessStyle.Render(" Full access ")
	default: // Ask for approval
		modePill = modePillApprovalStyle.Render(" Ask for approval ")
	}

	hint := modeSeparatorStyle.Render("shift+tab")
	leftPart := modePill + " " + hint + " "
	leftW := lipgloss.Width(leftPart)

	// Fill remaining width with dashes
	fillWidth := m.width - leftW
	if fillWidth < 0 {
		fillWidth = 0
	}
	fill := modeFillStyle.Render(strings.Repeat("─", fillWidth))

	return leftPart + fill
}

// renderMinimalStatusBar renders a minimal status bar for wide-screen mode (sidebar visible).
// Only shows copy notice and background tasks. Returns empty string when nothing to show.
func (m Model) renderMinimalStatusBar() string {
	var parts []string
	if m.bgRunning > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorWarning).Render(fmt.Sprintf("⏳ %d background", m.bgRunning)))
	}
	if m.teamState.HasTeam() {
		parts = append(parts, RenderTeamStatusPill(len(m.teamState.Teammates)))
	}

	if len(parts) == 0 {
		return ""
	}

	txt := strings.Join(parts, " │ ")
	txtW := lipgloss.Width(txt)
	fillW := m.width - txtW - 2
	if fillW < 0 {
		fillW = 0
	}
	fill := minimalStatusBarStyle.Render(strings.Repeat("─", fillW))
	return fill + " " + txt
}

// renderFallbackStatusBar renders a full status bar for narrow-screen mode (no sidebar).
func (m Model) renderFallbackStatusBar() string {
	sbComp := NewStatusBarComponent()
	teammateCount := len(m.teamState.Teammates)
	activeTokens := m.totalTokens
	if m.teamState.ViewingAgent != "" {
		activeTokens = m.teammateTokens[m.teamState.ViewingAgent]
	}
	return sbComp.View(StatusBarState{
		Width:             m.width,
		ActiveProvider:    m.activeProvider,
		ActiveModel:       m.activeModel,
		AutoApprove:       m.approvalMode == ModeAuto,
		TotalTokens:       activeTokens,
		ModelContextLimit: m.modelContextLimit,
		MCPStatuses:       m.mcpStatuses,
		Mode:              m.agentMode,
		BgRunning:         m.bgRunning,
		TeammateCount:     teammateCount,
	})
}
