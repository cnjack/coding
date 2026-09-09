package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/cnjack/jcode/internal/tools"
)

func (m Model) newView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *Model) View() tea.View {
	viewStart := time.Now()
	m.renderPerf.viewCalls++
	defer func() {
		m.observeViewRender(time.Since(viewStart))
	}()

	if m.showingTranscript {
		return m.newView(m.transcriptView())
	}

	if m.showingHelp {
		return m.newView(m.helpPanelView())
	}

	if m.showingSetting {
		return m.newView(m.settingMenuView())
	}

	if m.showingChannel {
		return m.newView(m.channelPanelView())
	}

	if m.pickingSSHAlias {
		return m.newView(m.sshAliasPickerView())
	}

	if m.pickingModel {
		return m.newView(m.modelPickerView())
	}

	if m.managingModels {
		return m.newView(m.manageModelsView())
	}

	if m.pickingTheme {
		return m.newView(m.themePickerView())
	}

	if m.pickingCopy {
		return m.newView(m.copyPickerView())
	}

	if m.pickingSession {
		return m.newView(m.sessionPickerView())
	}

	if !m.ready {
		return m.newView("\n  Initializing...")
	}

	if m.sshStep == 3 {
		return m.newView(m.dirPickerView())
	}

	if m.approvalPending {
		return m.newView(m.approvalDialogView())
	}

	if m.cancelPending {
		return m.newView(m.cancelDialogView())
	}

	if m.exitPending {
		return m.newView(m.exitDialogView())
	}

	// ─── Determine sidebar visibility (local, don't modify Model in View) ───
	showSidebar := m.width >= minWidthForSidebar

	// ─── Footer (input area) — always full width ───
	footer := m.cachedInputAreaView()
	footerHeight := m.footerCacheH

	// ─── Team coordinator panel ───
	teamPanel := ""
	teamPanelHeight := 0
	if m.teamState.HasTeam() && m.teamState.PanelVisible {
		teamPanel = RenderCoordinatorPanel(&m.teamState, m.width)
		teamPanelHeight = lipgloss.Height(teamPanel)
	}

	// ─── Calculate main content area dimensions ───
	mainWidth := m.width
	if showSidebar {
		mainWidth = m.width - sidebarWidth
	}

	// ─── Viewport ───
	vpH := m.height - footerHeight - teamPanelHeight
	if vpH < 3 {
		vpH = 3
	}
	if m.ready {
		// Only re-render content when something changed. Stream text is
		// batched by BatchRenderMsg; spinner ticks explicitly dirty the status
		// line in Update.
		if m.contentDirty {
			stickToBottom := m.viewport.AtBottom()
			m.viewport.SetContent(m.renderViewportContent())
			if stickToBottom {
				m.viewport.GotoBottom()
			}
		}
	}

	vpView := m.viewport.View()

	// ─── Assemble layout ───
	if showSidebar {
		// Manual line-by-line join: viewport | divider | sidebar
		// This avoids JoinHorizontal's reliance on width calculation which
		// can misalign the │ when ANSI sequences or wide chars are present.
		sidebar := m.renderSidebarCached(vpH)
		contentRow := joinColumnsWithDivider(vpView, sidebar, mainWidth, vpH)
		parts := []string{contentRow}
		if teamPanel != "" {
			parts = append(parts, teamPanel)
		}
		parts = append(parts, footer)
		mainView := lipgloss.JoinVertical(lipgloss.Left, parts...)
		return m.newView(mainView)
	}

	// Single-column fallback
	parts := []string{vpView}
	if teamPanel != "" {
		parts = append(parts, teamPanel)
	}
	parts = append(parts, footer)
	mainView := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return m.newView(mainView)
}

// joinColumnsWithDivider manually joins the viewport and sidebar line-by-line
// with a "│ " divider. Each viewport line is padded to vpWidth using
// ansi.StringWidth (GraphemeWidth method), matching BubbleTea's internal
// cell buffer width calculation when Unicode Core mode is enabled.
func joinColumnsWithDivider(vpView, sidebar string, vpWidth, height int) string {
	vpLines := strings.Split(vpView, "\n")
	sbLines := strings.Split(sidebar, "\n")

	// Pre-allocate: estimate each line is vpWidth + divider + sidebarWidth.
	estimatedCap := height * (vpWidth + 2 + sidebarWidth + 1)
	var buf strings.Builder
	buf.Grow(estimatedCap)

	// Pre-build a padding string for slice-based padding (faster than loop).
	spaces := strings.Repeat(" ", vpWidth+2)

	for i := 0; i < height; i++ {
		var vl, sl string
		if i < len(vpLines) {
			vl = vpLines[i]
		}
		if i < len(sbLines) {
			sl = sbLines[i]
		}

		buf.WriteString(vl)
		if pad := vpWidth - ansi.StringWidth(vl); pad > 0 {
			buf.WriteString(spaces[:pad])
		}
		buf.WriteString("│ ")
		buf.WriteString(sl)

		if i < height-1 {
			buf.WriteByte('\n')
		}
	}
	return buf.String()
}

// renderSidebar renders the right sidebar panel.
func (m Model) renderSidebar(height int) string {
	if m.sidebarComp == nil {
		m.sidebarComp = NewSidebarComponent()
	}
	var todos []tools.TodoItem
	if m.todoStore != nil {
		todos = m.todoStore.Items()
	}
	activeTokens := m.totalTokens
	if m.teamState.ViewingAgent != "" {
		activeTokens = m.teammateTokens[m.teamState.ViewingAgent]
	}
	return m.sidebarComp.View(SidebarState{
		Width:             sidebarWidth - 2, // content width: total - leftBorder - leftPad
		Height:            height,
		TotalWidth:        sidebarWidth,
		EnvLabel:          m.envLabel,
		ActiveProvider:    m.activeProvider,
		ActiveModel:       m.activeModel,
		TotalTokens:       activeTokens,
		ModelContextLimit: m.modelContextLimit,
		TodoItems:         todos,
		TodoScrollOffset:  m.sidebarScrollOffset,
		MCPStatuses:       m.mcpStatuses,
		TeammateCount:     len(m.teamState.Teammates),
		BgRunning:         m.bgRunning,
		Version:           m.version,
	})
}

// renderSidebarCached returns the cached sidebar or rebuilds it when dirty or height changed.
func (m *Model) renderSidebarCached(height int) string {
	if !m.sidebarCacheDirty && m.sidebarCache != "" && m.sidebarCacheH == height {
		m.renderPerf.sidebarCacheHits++
		return m.sidebarCache
	}
	m.renderPerf.sidebarCacheMisses++
	m.sidebarCache = m.renderSidebar(height)
	m.sidebarCacheDirty = false
	m.sidebarCacheH = height
	return m.sidebarCache
}

// refreshViewport recalculates viewport height, updates content and scrolls to bottom.
func (m *Model) refreshViewport() {
	if m.ready {
		m.renderPerf.refreshes++
		m.contentDirty = true
		m.viewport.SetHeight(m.calcViewportHeight())
		m.viewport.SetContent(m.renderViewportContent())
		m.viewport.GotoBottom()
	}
}

func (m *Model) renderViewportContent() string {
	return strings.TrimRight(m.renderContent(), "\n")
}

// invalidateSidebarCache marks the sidebar cache as needing rebuild.
// Call this when sidebar-affecting state changes (tokens, model, todos, etc).
func (m *Model) invalidateSidebarCache() {
	m.sidebarCacheDirty = true
}
