package tui

import (
	"encoding/json"
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
)

const maxToolOutputLen = 500

// promptCh is used to pass the user's prompt from the TUI to the main goroutine.
var promptCh = make(chan string, 1)

// sshCh is used to pass SSH connection requests and directory browsing signals to the main goroutine.
var sshCh = make(chan interface{}, 1)

// GetPromptChannel returns the channel that receives the user's submitted prompt.
func GetPromptChannel() <-chan string {
	return promptCh
}

// GetSSHChannel returns the channel that receives SSH connection requests.
func GetSSHChannel() <-chan interface{} {
	return sshCh
}

// configCh is used to notify main goroutine of config changes.
var configCh = make(chan *config.Config, 1)

// GetConfigChannel returns the channel that receives configuration changes.
func GetConfigChannel() <-chan *config.Config {
	return configCh
}

// --- Messages ---

type AgentTextMsg struct{ Text string }
type ToolCallMsg struct{ Name, Args string }
type ToolResultMsg struct {
	Name, Output string
	Err          error
}
type AgentDoneMsg struct{ Err error }
type PromptSubmitMsg struct{ Prompt string }
type UserPromptMsg struct{ Prompt string }

// SSHConnectMsg is sent when user initially requests connection
type SSHConnectMsg struct {
	Addr string // user@host
	Path string // remote working dir (optional)
}

// SSHListDirReqMsg is sent when TUI needs to list a directory on the remote machine
type SSHListDirReqMsg struct {
	Path string
}

// SSHDirResultsMsg is sent from main to TUI with directory contents
type SSHDirResultsMsg struct {
	Path  string
	Items []string
	Err   error
}

// SSHStatusMsg carries the result of an SSH connection attempt.
type SSHStatusMsg struct {
	Success bool
	Label   string // e.g. "root@myserver:22"
	Err     error
}

// --- Model ---

type Mode int

const (
	ModeWelcome Mode = iota
	ModeAgent
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

	// SSH setup wizard state
	sshStep int        // 0=none, 1=waiting for host, 2=waiting for response, 3=picking dir
	sshAddr string     // addr stored from step 1
	sshPath string     // current remote dir being listed
	dirList list.Model // the bubbles/list model for directory selection

	// Model picker
	modelPicker  list.Model
	pickingModel bool

	// History
	history      []string
	historyIndex int
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

func NewModel(hasPrompt bool) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	md, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)

	mode := ModeWelcome
	thinking := false
	if hasPrompt {
		mode = ModeAgent
		thinking = true
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

	m := Model{
		mode:        mode,
		spinner:     s,
		thinking:    thinking,
		mdRenderer:  md,
		textarea:    newTextarea(),
		currentText: &strings.Builder{},
		dirList:     l,
		modelPicker: ml,
		history:     loadHistory(),
	}
	m.historyIndex = len(m.history)
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

// inputActive returns true when the text input should be visible and accepting keys.
func (m Model) inputActive() bool {
	return (m.mode == ModeWelcome || (m.mode == ModeAgent && m.agentDone) || m.sshStep > 0) && !m.pickingModel
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.KeyMsg:
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
				// Cancel SSH step
				m.sshStep = 0
				m.sshPath = ""
				m.sshAddr = ""
				m.textarea.Placeholder = "Type your prompt here..."
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
			case "enter":
				prompt := strings.TrimSpace(m.textarea.Value())
				if prompt != "" {
					appendHistory(prompt)
					if len(m.history) == 0 || m.history[len(m.history)-1] != prompt {
						m.history = append(m.history, prompt)
					}
					m.historyIndex = len(m.history)

					m.textarea.Reset()

					if prompt == "/setting" {
						m.lines = append(m.lines, toolLabelStyle.Render("⚙ Settings: ")+"Please edit "+config.ConfigPath())
						m.refreshViewport()
						return m, tea.Batch(cmds...)
					}
					if prompt == "/model" {
						return m.handleModelInput(cmds)
					}

					// Handle /ssh command
					if prompt == "/ssh" || strings.HasPrefix(prompt, "/ssh ") {
						return m.handleSSHInput(prompt, cmds)
					}

					// Handle SSH setup steps
					if m.sshStep > 0 {
						return m.handleSSHStep(prompt, cmds)
					}

					// Regular prompt
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
				return m, tea.Batch(cmds...)
			case "up":
				if m.historyIndex > 0 {
					m.historyIndex--
					m.textarea.SetValue(m.history[m.historyIndex])
					m.textarea.CursorEnd()
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
			m.dirList.Title = fmt.Sprintf("Dir: %s (Enter to browse, or select '✅ Use this dir')", msg.Path)
			
			// Build list items
			items := []list.Item{
				dirItem{title: "✅ Use this directory (" + msg.Path + ")", desc: "Connect using " + msg.Path, isDirectory: true, isSelectBtn: true},
			}
			if msg.Path != "/" && msg.Path != "" {
				// We don't have perfect path.Dir awareness without 'path' package, but remote side could list '..'
				// For simplicity, we just add the items returned by main
			}
			for _, name := range msg.Items {
				fullPath := path.Join(msg.Path, name)
				items = append(items, dirItem{title: fullPath, name: name, desc: "Folder", isDirectory: true})
			}
			m.dirList.SetItems(items)
		}
		m.refreshViewport()

	case SSHStatusMsg:
		m.thinking = false
		if msg.Success {
			m.lines = append(m.lines, fmt.Sprintf("   %s Connected to %s",
				toolSuccessStyle.Render("✓"), toolNameStyle.Render(msg.Label)))
		} else {
			m.lines = append(m.lines, fmt.Sprintf("   %s %s",
				toolErrorStyle.Render("✗ SSH Error:"),
				toolResultStyle.Render(msg.Err.Error())))
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
		m.thinking = false
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
		// Resize viewport to make room for input
		if m.ready {
			m.viewport.Height = m.calcViewportHeight(true)
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
		}
	}

	if m.ready && m.mode == ModeAgent {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

const inputAreaLines = 4 // divider + textarea(1) + divider + padding

func (m Model) calcViewportHeight(withInput bool) int {
	headerHeight := 3
	footerHeight := 2
	if withInput {
		footerHeight = inputAreaLines + 1
	}
	h := m.height - headerHeight - footerHeight
	if h < 3 {
		h = 3
	}
	return h
}

func (m Model) View() string {
	if m.pickingModel {
		return m.modelPickerView()
	}

	if m.mode == ModeWelcome {
		return m.welcomeView()
	}

	if !m.ready {
		return "\n  Initializing..."
	}

	if m.sshStep == 3 {
		return m.dirPickerView()
	}

	header := titleStyle.Render("🚀 Little Jack — Coding Assistant")
	headerLine := divider(m.width)

	var footer string
	if m.agentDone {
		footer = m.inputAreaView()
	} else if m.thinking {
		if m.pendingTool != "" {
			footer = fmt.Sprintf("  %s Running %s...", m.spinner.View(), toolNameStyle.Render(m.pendingTool))
		} else {
			footer = fmt.Sprintf("  %s Thinking...", m.spinner.View())
		}
	} else {
		footer = dividerStyle.Render("  ↑/↓ scroll • Ctrl+C quit")
	}

	return fmt.Sprintf("%s\n%s\n%s\n%s", header, headerLine, m.viewport.View(), footer)
}

func (m Model) modelPickerView() string {
	w, h := m.width, m.height
	if w <= 0 { w = 80 }
	if h <= 0 { h = 24 }

	modW, modH := w-8, h-4
	if modW > 120 { modW = 120 }

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1).
		Width(modW).
		Height(modH)

	headerText := fmt.Sprintf(" %s ", toolNameStyle.Render("Select Model"))
	
	m.modelPicker.SetSize(modW-6, modH-8)
	m.modelPicker.Title = "Select model (↑/↓ to navigate, Enter to confirm, Esc to cancel)"
	m.modelPicker.SetShowHelp(false)
	m.modelPicker.SetShowStatusBar(true)

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Padding(0, 1).Render(headerText),
		"",
		m.modelPicker.View(),
	)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m Model) dirPickerView() string {
	w, h := m.width, m.height
	if w <= 0 { w = 80 }
	if h <= 0 { h = 24 }

	modW, modH := w-8, h-4
	if modW > 120 { modW = 120 }

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Padding(0, 1).
		Width(modW).
		Height(modH)

	headerText := fmt.Sprintf(" Open Folder: %s ", toolNameStyle.Render(m.sshAddr))
	
	pathBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colorSecondary).
		Padding(0, 1).
		Foreground(colorText).
		Width(modW - 4).
		Render(m.sshPath)

	m.dirList.SetSize(modW-6, modH-8)
	m.dirList.Title = "Select directory (↑/↓ to navigate, Enter to explore or finalize)"
	m.dirList.SetShowHelp(false)
	m.dirList.SetShowStatusBar(true)

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Padding(0, 1).Render(headerText),
		pathBox,
		"",
		m.dirList.View(),
	)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxStyle.Render(content))
}

func (m Model) inputAreaView() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		divider(m.width),
		lipgloss.NewStyle().PaddingLeft(1).PaddingRight(2).Render(m.textarea.View()),
		divider(m.width),
	)
}

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

// handleSSHInput parses the /ssh command and begins the guided flow.
// Formats: /ssh | /ssh user@host | /ssh user@host:/path
func (m Model) handleSSHInput(input string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(input, "/ssh"))

	// /ssh with no args → step 1: ask for host
	if arg == "" {
		m.sshStep = 1
		m.lines = append(m.lines, toolLabelStyle.Render("🔗 SSH Setup"))
		m.textarea.Placeholder = "Enter SSH address (e.g. root@hostname)..."
		m.refreshViewport()
		return m, tea.Batch(cmds...)
	}

	// Check if path is included: user@host:/path
	if colonIdx := strings.LastIndex(arg, ":"); colonIdx > 0 {
		// Make sure it's not just user@host (no path after colon)
		possiblePath := arg[colonIdx+1:]
		if strings.HasPrefix(possiblePath, "/") {
			addr := arg[:colonIdx]
			path := possiblePath
			return m.startSSHConnect(addr, path, cmds)
		}
	}

	// /ssh user@host → ask for path interactively
	return m.startSSHConnect(arg, "?", cmds)
}

// handleSSHStep handles input during the SSH setup wizard.
func (m Model) handleSSHStep(input string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch m.sshStep {
	case 1: // User entered host
		addr := input
		// Check for addr:/path format
		if colonIdx := strings.LastIndex(addr, ":"); colonIdx > 0 {
			possiblePath := addr[colonIdx+1:]
			if strings.HasPrefix(possiblePath, "/") {
				m.sshStep = 0
				return m.startSSHConnect(addr[:colonIdx], possiblePath, cmds)
			}
		}
		// Trigger interactive picker
		m.sshStep = 0
		return m.startSSHConnect(addr, "?", cmds)
	}

	// Should not happen, reset
	m.sshStep = 0
	m.textarea.Placeholder = "Type your prompt here..."
	return m, tea.Batch(cmds...)
}

// startSSHConnect sends the connect message and updates the UI.
func (m Model) startSSHConnect(addr, path string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	m.sshStep = 0
	m.sshAddr = ""
	m.mode = ModeAgent
	m.agentDone = false
	m.thinking = true
	m.textarea.Placeholder = "Type your prompt here..."

	display := addr
	if path != "" {
		display = addr + ":" + path
	}
	m.lines = append(m.lines, fmt.Sprintf("%s Connecting to %s...",
		toolLabelStyle.Render("🔗 SSH:"), toolNameStyle.Render(display)))
	m.refreshViewport()

	cmds = append(cmds, func() tea.Msg {
		return SSHConnectMsg{Addr: addr, Path: path}
	})
	cmds = append(cmds, m.spinner.Tick)
	return m, tea.Batch(cmds...)
}

// refreshViewport updates the viewport content if ready.
func (m *Model) refreshViewport() {
	if m.ready {
		m.viewport.SetContent(m.renderContent())
		m.viewport.GotoBottom()
	}
}

func (m Model) welcomeView() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height
	if h <= 0 {
		h = 24
	}

	logo := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("🚀 Little Jack")
	subtitle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("Coding Assistant")
	headerBlock := lipgloss.NewStyle().Width(w).Align(lipgloss.Center).PaddingTop(1).
		Render(lipgloss.JoinVertical(lipgloss.Center, logo, subtitle))

	tips := []string{
		"💡  Describe a task and I'll help you code it",
		"📁  I can read, write, and edit files in your project",
		"🔍  I can search through your codebase with grep",
		"⚡  I can execute shell commands for you",
	}
	var tipsBlock strings.Builder
	for _, tip := range tips {
		tipsBlock.WriteString(lipgloss.NewStyle().Foreground(colorText).PaddingLeft(4).Render(tip))
		tipsBlock.WriteString("\n")
	}
	tipsRendered := lipgloss.NewStyle().Width(w).Align(lipgloss.Center).PaddingTop(1).
		Render(tipsBlock.String())

	inputArea := m.inputAreaView()

	topContent := lipgloss.JoinVertical(lipgloss.Left, headerBlock, tipsRendered)
	gap := h - lipgloss.Height(topContent) - lipgloss.Height(inputArea)
	if gap < 1 {
		gap = 1
	}

	return lipgloss.JoinVertical(lipgloss.Left, topContent, strings.Repeat("\n", gap), inputArea)
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
	return sb.String()
}

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

func RunTUI(hasPrompt bool) (*tea.Program, Model) {
	m := NewModel(hasPrompt)
	p := tea.NewProgram(m, tea.WithAltScreen())
	return p, m
}

func HeaderView() string {
	return lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).
		Render("🚀 Little Jack — Coding Assistant")
}
