package tui

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/cnjack/coding/internal/config"
	"github.com/cnjack/coding/internal/session"
	"github.com/cnjack/coding/internal/tools"
)

// --- Model ---

type Mode int

const (
	ModeAgent Mode = iota
)

type Model struct {
	mode      Mode
	agentDone bool

	lines       []string
	currentText *strings.Builder

	viewport viewport.Model
	ready    bool

	spinner  spinner.Model
	thinking bool

	width  int
	height int

	mdRenderer  *glamour.TermRenderer
	pendingTool string
	textarea    textarea.Model
	mcpStatuses []MCPStatusItem

	sshStep int
	sshAddr string
	sshPath string
	dirList list.Model

	modelPicker  list.Model
	pickingModel bool

	settingMenu    list.Model
	showingSetting bool

	sshAliasPicker  list.Model
	pickingSSHAlias bool

	sshSavePrompt bool
	sshSaveAddr   string
	sshSavePath   string

	history      []string
	historyIndex int

	sessionPicker  list.Model
	pickingSession bool

	agentsMdFound bool

	pwd string

	activeProvider string
	activeModel    string
	textareaLines  int

	todoStore *tools.TodoStore

	promptTokens      int64
	completionTokens  int64
	totalTokens       int64
	modelContextLimit int

	pendingPrompts []string
	autoApprove    bool

	approvalPending  bool
	approvalToolName string
	approvalToolArgs string
	approvalRespChan chan ToolApprovalResponse
}

// dirItem implements list.Item
type dirItem struct {
	title       string
	name        string
	desc        string
	isDirectory bool
	isSelectBtn bool
}

func (i dirItem) Title() string       { return i.title }
func (i dirItem) Description() string { return i.desc }
func (i dirItem) FilterValue() string { return i.title }

type modelItem struct {
	provider string
	model    string
	title    string
	desc     string
}

func (i modelItem) Title() string       { return i.title }
func (i modelItem) Description() string { return i.desc }
func (i modelItem) FilterValue() string { return i.title }

// settingItem is used for the /setting menu
type settingItem struct {
	title string
	desc  string
	key   string // action key
}

func (i settingItem) Title() string       { return i.title }
func (i settingItem) Description() string { return i.desc }
func (i settingItem) FilterValue() string { return i.title }

// sessionListItem implements list.Item for session picking.
type sessionListItem struct {
	meta session.SessionMeta
}

func (i sessionListItem) Title() string {
	ts := i.meta.StartTime
	if len(ts) >= 16 {
		ts = ts[:16]
	}
	return fmt.Sprintf("%s  %s / %s", ts, i.meta.Provider, i.meta.Model)
}
func (i sessionListItem) Description() string { return i.meta.UUID }
func (i sessionListItem) FilterValue() string { return i.meta.StartTime + i.meta.UUID }

// sshAliasItem for the SSH alias picker
type sshAliasItem struct {
	title string
	desc  string
	addr  string
	path  string
	isNew bool // "Connect new SSH" option
}

func (i sshAliasItem) Title() string       { return i.title }
func (i sshAliasItem) Description() string { return i.desc }
func (i sshAliasItem) FilterValue() string { return i.title }

func newTextarea() textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Type your prompt here..."
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	ta.Prompt = "> "
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	ta.Focus()
	return ta
}

func NewModel(hasPrompt bool, pwd string, todoStore *tools.TodoStore) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	md, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)

	mode := ModeAgent
	thinking := false
	var initialLines []string
	if hasPrompt {
		thinking = true
	} else {
		initialLines = []string{
			lipgloss.NewStyle().Foreground(colorMuted).Render("Welcome to Little Jack. How can I help you today?"),
			"",
			lipgloss.NewStyle().Foreground(colorText).PaddingLeft(2).Render("💡 Describe a task and I'll help you code it"),
			lipgloss.NewStyle().Foreground(colorText).PaddingLeft(2).Render("📁 I can read, write, and edit files in your project"),
			lipgloss.NewStyle().Foreground(colorText).PaddingLeft(2).Render("⚡ I can execute shell commands for you"),
			"",
		}
	}

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)

	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Select Remote Directory"
	l.SetShowHelp(false)

	modelDel := list.NewDefaultDelegate()
	modelDel.SetSpacing(0)
	ml := list.New([]list.Item{}, modelDel, 0, 0)
	ml.Title = "Select Model"
	ml.SetShowHelp(false)

	// Setting menu list
	settingDel := list.NewDefaultDelegate()
	settingDel.SetSpacing(0)
	sl := list.New([]list.Item{}, settingDel, 0, 0)
	sl.Title = "Settings"
	sl.SetShowHelp(false)

	// SSH alias picker list
	sshAliasDel := list.NewDefaultDelegate()
	sshAliasDel.SetSpacing(0)
	sal := list.New([]list.Item{}, sshAliasDel, 0, 0)
	sal.Title = "SSH Connections"
	sal.SetShowHelp(false)

	// Session picker list
	sessionDel := list.NewDefaultDelegate()
	sessionDel.SetSpacing(0)
	sesl := list.New([]list.Item{}, sessionDel, 0, 0)
	sesl.Title = "Sessions"
	sesl.SetShowHelp(false)

	m := Model{
		mode:           mode,
		spinner:        s,
		thinking:       thinking,
		mdRenderer:     md,
		textarea:       newTextarea(),
		textareaLines:  1,
		currentText:    &strings.Builder{},
		dirList:        l,
		modelPicker:    ml,
		settingMenu:    sl,
		sshAliasPicker: sal,
		sessionPicker:  sesl,
		pwd:            pwd,
		history:        loadHistory(),
		todoStore:      todoStore,
		lines:          initialLines,
	}
	m.historyIndex = len(m.history)

	if cfg, err := config.LoadConfig(); err == nil {
		m.activeProvider = cfg.Provider
		m.activeModel = cfg.Model
	}

	return m
}

func loadHistory() []string {
	hPath, err := config.HistoryFilePath()
	if err != nil {
		return nil
	}
	content, err := os.ReadFile(hPath)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	var history []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			history = append(history, l)
		}
	}
	return history
}

func appendHistory(prompt string) {
	hPath, err := config.HistoryFilePath()
	if err != nil {
		return
	}
	f, err := os.OpenFile(hPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(prompt + "\n")
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, textarea.Blink)
}

func (m Model) inputActive() bool {
	return (m.mode == ModeAgent || m.sshStep > 0 || m.sshSavePrompt) && !m.pickingModel && !m.showingSetting && !m.pickingSSHAlias && !m.pickingSession && !m.approvalPending
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.MouseMsg:
		if m.pickingSession {
			var cmd tea.Cmd
			m.sessionPicker, cmd = m.sessionPicker.Update(msg)
			cmds = append(cmds, cmd)
		} else if m.showingSetting {
			var cmd tea.Cmd
			m.settingMenu, cmd = m.settingMenu.Update(msg)
			cmds = append(cmds, cmd)
		} else if m.pickingSSHAlias {
			var cmd tea.Cmd
			m.sshAliasPicker, cmd = m.sshAliasPicker.Update(msg)
			cmds = append(cmds, cmd)
		} else if m.pickingModel {
			var cmd tea.Cmd
			m.modelPicker, cmd = m.modelPicker.Update(msg)
			cmds = append(cmds, cmd)
		} else if m.sshStep == 3 {
			var cmd tea.Cmd
			m.dirList, cmd = m.dirList.Update(msg)
			cmds = append(cmds, cmd)
		} else if m.ready {
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			cmds = append(cmds, vpCmd)
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		// Tool approval dialog handling
		if m.approvalPending {
			switch msg.String() {
			case "y", "Y":
				m.approvalPending = false
				m.autoApprove = true
				if m.approvalRespChan != nil {
					m.approvalRespChan <- ToolApprovalResponse{Approved: true}
				}
				select {
				case autoApproveCh <- true:
				default:
				}
				m.textarea.Focus()
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			case "n", "N", "esc":
				m.approvalPending = false
				if m.approvalRespChan != nil {
					m.approvalRespChan <- ToolApprovalResponse{Approved: false}
				}
				m.textarea.Focus()
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
			return m, tea.Batch(cmds...)
		}

		// Session picker handling
		if m.pickingSession {
			switch msg.String() {
			case "enter":
				selected := m.sessionPicker.SelectedItem()
				if selected != nil {
					selItem := selected.(sessionListItem)
					m.pickingSession = false
					m.textarea.Focus()
					m.lines = append(m.lines, toolLabelStyle.Render("📂 Loading session..."))
					m.thinking = true
					m.mode = ModeAgent
					m.agentDone = false
					uuid := selItem.meta.UUID
					cmds = append(cmds, m.spinner.Tick)
					cmds = append(cmds, func() tea.Msg {
						return ResumeRequestMsg{UUID: uuid}
					})
					return m, tea.Batch(cmds...)
				}
			case "ctrl+c", "esc":
				m.pickingSession = false
				m.textarea.Focus()
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
			var cmd tea.Cmd
			m.sessionPicker, cmd = m.sessionPicker.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		// Setting menu handling
		if m.showingSetting {
			switch msg.String() {
			case "enter":
				selected := m.settingMenu.SelectedItem()
				if selected != nil {
					selItem := selected.(settingItem)
					switch selItem.key {
					case "switch_model":
						m.showingSetting = false
						return m.handleModelInput(cmds)
					case "add_model":
						m.showingSetting = false
						m.textarea.Focus()
						// Signal to launch the setup TUI
						cmds = append(cmds, func() tea.Msg {
							return AddModelMsg{}
						})
						return m, tea.Batch(cmds...)
					case "edit_config":
						m.showingSetting = false
						m.textarea.Focus()
						m.lines = append(m.lines, toolLabelStyle.Render("⚙ Settings:")+" Please edit "+config.ConfigPath())
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
				}
			case "ctrl+c", "esc":
				m.showingSetting = false
				m.textarea.Focus()
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
			var cmd tea.Cmd
			m.settingMenu, cmd = m.settingMenu.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		// SSH alias picker handling
		if m.pickingSSHAlias {
			switch msg.String() {
			case "enter":
				selected := m.sshAliasPicker.SelectedItem()
				if selected != nil {
					selItem := selected.(sshAliasItem)
					m.pickingSSHAlias = false
					m.textarea.Focus()
					if selItem.isNew {
						// Start new SSH connection wizard
						m.sshStep = 1
						m.lines = append(m.lines, toolLabelStyle.Render("🔗 SSH Setup"))
						m.textarea.Placeholder = "Enter SSH address (e.g. root@hostname)..."
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
					// Connect using saved alias
					path := selItem.path
					if path == "" {
						path = "?"
					}
					return m.startSSHConnect(selItem.addr, path, cmds)
				}
			case "ctrl+c", "esc":
				m.pickingSSHAlias = false
				m.textarea.Focus()
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
			var cmd tea.Cmd
			m.sshAliasPicker, cmd = m.sshAliasPicker.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		if m.pickingModel {
			switch msg.String() {
			case "enter":
				selected := m.modelPicker.SelectedItem()
				if selected != nil {
					selItem := selected.(modelItem)
					cfg, err := config.LoadConfig()
					if err == nil {
						cfg.Provider = selItem.provider
						cfg.Model = selItem.model
						config.SaveConfig(cfg)
						m.activeProvider = selItem.provider
						m.activeModel = selItem.model
						select {
						case configCh <- cfg:
						default:
						}
					}
					m.pickingModel = false
					m.lines = append(m.lines, toolLabelStyle.Render("⚙ Setup:")+" Switched to "+selItem.provider+" - "+selItem.model)
					m.textarea.Focus()
					m.refreshViewport()
					return m, tea.Batch(cmds...)
				}
			case "ctrl+c", "esc":
				m.pickingModel = false
				m.textarea.Focus()
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			}
			var cmd tea.Cmd
			m.modelPicker, cmd = m.modelPicker.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		if m.sshStep == 3 {
			switch msg.String() {
			case "tab":
				// Tab = confirm current directory (Open Folder)
				return m.startSSHConnect(m.sshAddr, m.sshPath, cmds)
			case "enter":
				selected := m.dirList.SelectedItem()
				if selected != nil {
					selItem := selected.(dirItem)
					if selItem.isSelectBtn {
						// Finalize dir selection
						return m.startSSHConnect(m.sshAddr, m.sshPath, cmds)
					}
					// Otherwise, list this new dir
					m.thinking = true
					m.sshPath = path.Join(m.sshPath, selItem.name)
					if m.sshPath == "" {
						m.sshPath = "/"
					}
					cmds = append(cmds, m.spinner.Tick)
					cmds = append(cmds, func() tea.Msg {
						return SSHListDirReqMsg{Path: m.sshPath}
					})
					return m, tea.Batch(cmds...)
				}
			case "ctrl+c", "esc":
				// Cancel SSH step — notify main to restore local env
				m.sshStep = 0
				m.sshPath = ""
				m.sshAddr = ""
				m.sshSaveAddr = ""
				m.sshSavePath = ""
				m.textarea.Placeholder = "Type your prompt here..."
				m.lines = append(m.lines, toolLabelStyle.Render("🔗 SSH:")+" Cancelled.")
				m.refreshViewport()
				cmds = append(cmds, func() tea.Msg {
					return SSHCancelMsg{}
				})
				return m, tea.Batch(cmds...)
			}

			// Update list
			var cmd tea.Cmd
			m.dirList, cmd = m.dirList.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		if m.inputActive() {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "shift+tab":
				m.autoApprove = !m.autoApprove
				select {
				case autoApproveCh <- m.autoApprove:
				default:
				}
				m.refreshViewport()
				return m, tea.Batch(cmds...)
			case "enter":
				prompt := strings.TrimSpace(m.textarea.Value())
				if prompt != "" {
					appendHistory(prompt)
					if len(m.history) == 0 || m.history[len(m.history)-1] != prompt {
						m.history = append(m.history, prompt)
					}
					m.historyIndex = len(m.history)

					m.textarea.Reset()
					m.textareaLines = 1
					m.textarea.SetHeight(1)

					if prompt == "/setting" {
						return m.handleSettingInput(cmds)
					}
					if prompt == "/model" {
						return m.handleModelInput(cmds)
					}

					if prompt == "/ssh" || strings.HasPrefix(prompt, "/ssh ") {
						return m.handleSSHInput(prompt, cmds)
					}

					if prompt == "/resume" || strings.HasPrefix(prompt, "/resume ") {
						return m.handleResumeInput(prompt, cmds)
					}

					if m.sshSavePrompt {
						return m.handleSSHSaveAlias(prompt, cmds)
					}

					if m.sshStep > 0 {
						return m.handleSSHStep(prompt, cmds)
					}

					if m.lines != nil && len(m.lines) > 0 {
						// Check if the lines are the initial welcome message, we clear it.
						if strings.Contains(m.lines[0], "Welcome to Little Jack") {
							m.lines = nil
						}
					}

					if !m.agentDone && m.thinking {
						m.pendingPrompts = append(m.pendingPrompts, prompt)
						m.lines = append(m.lines, fmt.Sprintf("%s %s",
							userLabelStyle.Render("👤 You (queued):"), prompt))
						if m.ready {
							m.viewport.Height = m.calcViewportHeight(true)
							m.viewport.SetContent(m.renderContent())
							m.viewport.GotoBottom()
						}
						return m, tea.Batch(cmds...)
					}

					m.mode = ModeAgent
					m.agentDone = false
					m.thinking = true
					m.lines = append(m.lines, fmt.Sprintf("%s %s",
						userLabelStyle.Render("👤 You:"), prompt))
					if m.ready {
						m.viewport.Height = m.calcViewportHeight(false)
						m.viewport.SetContent(m.renderContent())
						m.viewport.GotoBottom()
					}
					cmds = append(cmds, func() tea.Msg {
						return PromptSubmitMsg{Prompt: prompt}
					})
					cmds = append(cmds, m.spinner.Tick)
				}
				return m, tea.Batch(cmds...)
			case "shift+enter":
				// Insert newline into textarea by forwarding a plain enter key
				var cmd tea.Cmd
				m.textarea, cmd = m.textarea.Update(tea.KeyMsg{Type: tea.KeyEnter})
				cmds = append(cmds, cmd)
				m.textareaLines = recalcLines(m.textarea.Value())
				m.textarea.SetHeight(m.textareaLines)
				if m.ready {
					m.viewport.Height = m.calcViewportHeight(m.inputActive())
				}
				return m, tea.Batch(cmds...)
			case "up":
				if m.historyIndex > 0 {
					m.historyIndex--
					m.textarea.SetValue(m.history[m.historyIndex])
					m.textarea.CursorEnd()
					m.textareaLines = recalcLines(m.textarea.Value())
					m.textarea.SetHeight(m.textareaLines)
					if m.ready {
						m.viewport.Height = m.calcViewportHeight(m.inputActive())
					}
				}
				return m, tea.Batch(cmds...)
			case "down":
				if m.historyIndex < len(m.history)-1 {
					m.historyIndex++
					m.textarea.SetValue(m.history[m.historyIndex])
					m.textarea.CursorEnd()
				} else if m.historyIndex == len(m.history)-1 {
					m.historyIndex++
					m.textarea.SetValue("")
				}
				m.textareaLines = recalcLines(m.textarea.Value())
				m.textarea.SetHeight(m.textareaLines)
				if m.ready {
					m.viewport.Height = m.calcViewportHeight(m.inputActive())
				}
				return m, tea.Batch(cmds...)
			case "pgup", "pgdown":
				if m.ready && m.mode == ModeAgent {
					var vpCmd tea.Cmd
					m.viewport, vpCmd = m.viewport.Update(msg)
					cmds = append(cmds, vpCmd)
				}
				return m, tea.Batch(cmds...)
			}
			// Forward other keys to textarea
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			cmds = append(cmds, cmd)
			m.textareaLines = recalcLines(m.textarea.Value())
			m.textarea.SetHeight(m.textareaLines)
			if m.ready {
				m.viewport.Height = m.calcViewportHeight(m.inputActive())
			}
			return m, tea.Batch(cmds...)
		}
		// Agent running — only ctrl+c
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		inputWidth := msg.Width - 6
		if inputWidth < 20 {
			inputWidth = 20
		}
		m.textarea.SetWidth(inputWidth)

		vpH := m.calcViewportHeight(m.inputActive())
		if !m.ready {
			m.viewport = viewport.New(msg.Width, vpH)
			m.viewport.HighPerformanceRendering = false
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = vpH
		}
		m.dirList.SetSize(msg.Width, vpH)
		m.settingMenu.SetSize(msg.Width, vpH)
		m.sshAliasPicker.SetSize(msg.Width, vpH)
		m.sessionPicker.SetSize(msg.Width, vpH)
		m.viewport.SetContent(m.renderContent())

	case spinner.TickMsg:
		if m.thinking {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case PromptSubmitMsg:
		promptCh <- msg.Prompt

	case SSHConnectMsg:
		sshCh <- msg

	case SSHListDirReqMsg:
		sshCh <- msg

	case SSHCancelMsg:
		sshCh <- msg

	case ConfigUpdatedMsg:
		m.activeProvider = msg.Provider
		m.activeModel = msg.Model
		if msg.Message != "" {
			m.lines = append(m.lines, msg.Message)
			if m.ready {
				m.viewport.SetContent(m.renderContent())
				m.viewport.GotoBottom()
			}
		}

	case MCPStatusMsg:
		m.mcpStatuses = msg.Statuses
		m.refreshViewport()

	case TodoUpdateMsg:
		m.refreshViewport()

	case AddModelMsg:
		select {
		case addModelCh <- struct{}{}:
		default:
		}

	case ResumeRequestMsg:
		select {
		case resumeCh <- msg.UUID:
		default:
		}

	case AgentsMdMsg:
		m.agentsMdFound = msg.Found
		m.refreshViewport()

	case SessionResumedMsg:
		m.autoApprove = false
		m.thinking = false
		m.mode = ModeAgent
		m.agentDone = true
		m.lines = nil
		m.currentText.Reset()
		m.lines = append(m.lines, toolLabelStyle.Render("📂 Session resumed: ")+msg.UUID)
		m.lines = append(m.lines, "")
		for _, e := range msg.Entries {
			switch e.Type {
			case string(session.EntryUser):
				m.lines = append(m.lines, fmt.Sprintf("%s %s",
					userLabelStyle.Render("👤 You:"), e.Content))
			case string(session.EntryAssistant):
				if e.Content != "" {
					rendered := e.Content
					if m.mdRenderer != nil {
						if md, err := m.mdRenderer.Render(e.Content); err == nil {
							rendered = md
						}
					}
					m.lines = append(m.lines, assistantLabelStyle.Render("🤖 Assistant:"))
					m.lines = append(m.lines, rendered)
				}
			case string(session.EntryToolCall):
				m.lines = append(m.lines, fmt.Sprintf("%s %s %s",
					toolLabelStyle.Render("🔧 Tool:"),
					toolNameStyle.Render(e.Name),
					toolArgsStyle.Render(truncate(e.Args, 100)),
				))
			case string(session.EntryToolResult):
				if e.Error != "" {
					m.lines = append(m.lines, fmt.Sprintf("   %s %s",
						toolErrorStyle.Render("✗ Error:"),
						toolResultStyle.Render(truncate(e.Error, 200))))
				} else {
					m.lines = append(m.lines, formatToolResult(e.Name, e.Output, m.width)...)
				}
			}
		}
		m.lines = append(m.lines, "")
		m.lines = append(m.lines, divider(m.width-4))
		if m.ready {
			m.viewport.Height = m.calcViewportHeight(true)
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
		}
		m.textarea.Focus()

	case SSHDirResultsMsg:
		m.thinking = false
		if msg.Err != nil {
			m.lines = append(m.lines, fmt.Sprintf("   %s Failed to list directory: %v",
				toolErrorStyle.Render("✗ SSH Error:"), msg.Err))
			m.sshStep = 0
			m.textarea.Placeholder = "Type your prompt here..."
		} else {
			m.sshStep = 3
			m.sshPath = msg.Path
			m.dirList.Title = fmt.Sprintf("Dir: %s", msg.Path)

			// Build list items: .. first, then subdirs, then ✅ at the bottom
			var items []list.Item
			for _, name := range msg.Items {
				if name == ".." {
					items = append(items, dirItem{title: "📁 ..", name: "..", desc: "Parent directory", isDirectory: true})
				}
			}
			for _, name := range msg.Items {
				if name == ".." {
					continue
				}
				fullPath := path.Join(msg.Path, name)
				items = append(items, dirItem{title: "📁 " + fullPath, name: name, desc: "Folder", isDirectory: true})
			}
			items = append(items, dirItem{title: "✅ Use this directory (" + msg.Path + ")", desc: "Open folder here", isDirectory: true, isSelectBtn: true})
			m.dirList.SetItems(items)
		}
		m.refreshViewport()

	case SSHStatusMsg:
		m.thinking = false
		if msg.Success {
			m.lines = append(m.lines, fmt.Sprintf("   %s Connected to %s",
				toolSuccessStyle.Render("✓"), toolNameStyle.Render(msg.Label)))
			// If this was a direct /ssh user@host connection, offer to save alias
			if m.sshSaveAddr != "" {
				// Update sshSavePath from the actual connected path in label
				if m.sshSavePath == "" || m.sshSavePath == "?" {
					// Extract path from label like "user@host (pwd: /path)"
					if idx := strings.Index(msg.Label, "pwd: "); idx >= 0 {
						end := strings.Index(msg.Label[idx:], ")")
						if end > 0 {
							m.sshSavePath = msg.Label[idx+5 : idx+end]
						}
					}
				}
				m.sshSavePrompt = true
				m.lines = append(m.lines, toolLabelStyle.Render("⚙ SSH:")+" Save as alias? Enter alias name (or press Enter/type 'n' to skip)")
				m.textarea.Placeholder = "Enter alias name (e.g. my-server)..."
			}
		} else {
			m.lines = append(m.lines, fmt.Sprintf("   %s %s",
				toolErrorStyle.Render("✗ SSH Error:"),
				toolResultStyle.Render(msg.Err.Error())))
			m.sshSaveAddr = ""
			m.sshSavePath = ""
		}
		m.agentDone = true
		m.textarea.Focus()
		if m.ready {
			m.viewport.Height = m.calcViewportHeight(true)
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
		}

	case UserPromptMsg:
		m.lines = append(m.lines, fmt.Sprintf("%s %s",
			userLabelStyle.Render("👤 You:"), msg.Prompt))
		if m.ready {
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
		}

	case AgentTextMsg:
		m.currentText.WriteString(msg.Text)
		if m.ready {
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
		}

	case ToolCallMsg:
		m.thinking = true
		m.flushText()
		m.pendingTool = msg.Name
		argsDisplay := formatToolArgs(msg.Args)
		m.lines = append(m.lines, fmt.Sprintf("%s %s %s",
			toolLabelStyle.Render("🔧 Tool:"),
			toolNameStyle.Render(msg.Name),
			toolArgsStyle.Render(argsDisplay),
		))
		if m.ready {
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
		}
		cmds = append(cmds, m.spinner.Tick)

	case ToolResultMsg:
		m.thinking = true
		m.pendingTool = ""
		if msg.Err != nil {
			m.lines = append(m.lines, fmt.Sprintf("   %s %s",
				toolErrorStyle.Render("✗ Error:"),
				toolResultStyle.Render(truncate(msg.Err.Error(), maxToolOutputLen))))
		} else {
			m.lines = append(m.lines, formatToolResult(msg.Name, msg.Output, m.width)...)
		}
		if m.ready {
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
		}
		cmds = append(cmds, m.spinner.Tick)

	case TokenUpdateMsg:
		m.promptTokens = msg.PromptTokens
		m.completionTokens = msg.CompletionTokens
		m.totalTokens = msg.TotalTokens
		m.modelContextLimit = msg.ModelContextLimit

	case AgentDoneMsg:
		m.thinking = false
		m.flushText()
		if msg.Err != nil {
			m.lines = append(m.lines, errorStyle.Render("Error: "+msg.Err.Error()))
		}
		m.lines = append(m.lines, "")
		m.lines = append(m.lines, divider(m.width-4))
		m.agentDone = true
		m.textarea.Focus()
		if m.ready {
			m.viewport.Height = m.calcViewportHeight(true)
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
		}

		if len(m.pendingPrompts) > 0 {
			first := m.pendingPrompts[0]
			m.pendingPrompts = m.pendingPrompts[1:]
			select {
			case pendingPromptCh <- first:
			default:
			}
		}

	case ToolApprovalRequestMsg:
		m.approvalPending = true
		m.approvalToolName = msg.Name
		m.approvalToolArgs = msg.Args
		m.approvalRespChan = msg.Resp
		m.textarea.Blur()
		m.refreshViewport()

	}

	if m.ready && m.mode == ModeAgent {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

const maxTextareaLines = 5

func recalcLines(s string) int {
	n := strings.Count(s, "\n") + 1
	if n < 1 {
		n = 1
	}
	if n > maxTextareaLines {
		n = maxTextareaLines
	}
	return n
}

func (m Model) inputAreaHeight() int {
	lines := m.textareaLines
	if lines < 1 {
		lines = 1
	}
	h := lines + 3
	if m.todoStore != nil && m.todoStore.HasItems() {
		h += m.todoBarHeight()
	}
	val := m.textarea.Value()
	if strings.HasPrefix(val, "/") && m.getCommandHints(val) != "" {
		h += 1
	}
	return h
}

func (m Model) calcViewportHeight(withInput bool) int {
	headerHeight := 3
	footerHeight := 1
	if withInput {
		footerHeight = m.inputAreaHeight()
	}
	h := m.height - headerHeight - footerHeight
	if h < 3 {
		h = 3
	}
	return h
}

func (m Model) View() string {
	if m.showingSetting {
		return m.settingMenuView()
	}

	if m.pickingSSHAlias {
		return m.sshAliasPickerView()
	}

	if m.pickingModel {
		return m.modelPickerView()
	}

	if m.pickingSession {
		return m.sessionPickerView()
	}

	if !m.ready {
		return "\n  Initializing..."
	}

	if m.sshStep == 3 {
		return m.dirPickerView()
	}

	if m.approvalPending {
		return m.approvalDialogView()
	}

	header := titleStyle.Render("🚀 Little Jack — Coding Assistant")
	headerLine := divider(m.width)
	headerHeight := lipgloss.Height(header) + lipgloss.Height(headerLine)

	footer := m.inputAreaView()
	footerHeight := lipgloss.Height(footer)

	if m.ready {
		m.viewport.Height = m.height - headerHeight - footerHeight
		if m.viewport.Height < 3 {
			m.viewport.Height = 3
		}
		m.viewport.SetContent(strings.TrimRight(m.renderContent(), "\n"))
	}

	mainView := lipgloss.JoinVertical(lipgloss.Left, header, headerLine, m.viewport.View(), footer)
	return mainView
}

// refreshViewport updates the viewport content if ready.
func (m *Model) refreshViewport() {
	if m.ready {
		m.viewport.SetContent(m.renderContent())
		m.viewport.GotoBottom()
	}
}

// --- Helpers ---

func (m *Model) flushText() {
	text := m.currentText.String()
	if text == "" {
		return
	}
	m.currentText.Reset()
	rendered := text
	if m.mdRenderer != nil {
		if md, err := m.mdRenderer.Render(text); err == nil {
			rendered = md
		}
	}
	m.lines = append(m.lines, assistantLabelStyle.Render("🤖 Assistant:"))
	m.lines = append(m.lines, rendered)
}

func (m *Model) renderContent() string {
	var sb strings.Builder
	for _, line := range m.lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if m.currentText.Len() > 0 {
		sb.WriteString(assistantLabelStyle.Render("🤖 Assistant:"))
		sb.WriteString("\n")
		sb.WriteString(m.currentText.String())
		sb.WriteString("\n")
	}
	if m.thinking && !m.agentDone {
		var statusLine string
		if m.pendingTool != "" {
			statusLine = fmt.Sprintf("  %s Running %s...", m.spinner.View(), toolNameStyle.Render(m.pendingTool))
		} else {
			statusLine = fmt.Sprintf("  %s Thinking...", m.spinner.View())
		}
		sb.WriteString(statusLine)
		sb.WriteString("\n")
	}
	return sb.String()
}

func RunTUI(hasPrompt bool, pwd string, todoStore *tools.TodoStore) (*tea.Program, Model) {
	m := NewModel(hasPrompt, pwd, todoStore)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	return p, m
}

func HeaderView() string {
	return lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).
		Render("🚀 Little Jack — Coding Assistant")
}
