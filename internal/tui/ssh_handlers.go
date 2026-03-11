package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/cnjack/coding/internal/config"
)

// handleSSHInput parses the /ssh command and begins the guided flow.
// Formats: /ssh | /ssh user@host | /ssh user@host:/path
func (m Model) handleSSHInput(input string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(input, "/ssh"))

	// /ssh with no args → show saved aliases or new connection option
	if arg == "" {
		cfg, _ := config.LoadConfig()
		var items []list.Item
		if cfg != nil && len(cfg.SSHAliases) > 0 {
			for _, alias := range cfg.SSHAliases {
				desc := alias.Addr
				if alias.Path != "" {
					desc += ":" + alias.Path
				}
				items = append(items, sshAliasItem{
					title: "🔗 " + alias.Name,
					desc:  desc,
					addr:  alias.Addr,
					path:  alias.Path,
				})
			}
		}
		items = append(items, sshAliasItem{
			title: "➕ Connect New SSH",
			desc:  "Enter a new SSH address",
			isNew: true,
		})
		m.sshAliasPicker.SetItems(items)
		m.pickingSSHAlias = true
		m.textarea.Blur()
		return m, tea.Batch(cmds...)
	}

	// Check if path is included: user@host:/path
	if colonIdx := strings.LastIndex(arg, ":"); colonIdx > 0 {
		// Make sure it's not just user@host (no path after colon)
		possiblePath := arg[colonIdx+1:]
		if strings.HasPrefix(possiblePath, "/") {
			addr := arg[:colonIdx]
			path := possiblePath
			// Remember for save prompt
			m.sshSaveAddr = addr
			m.sshSavePath = path
			return m.startSSHConnect(addr, path, cmds)
		}
	}

	// /ssh user@host → ask for path interactively, remember addr for save
	m.sshSaveAddr = arg
	m.sshSavePath = ""
	return m.startSSHConnect(arg, "?", cmds)
}

// handleSSHSaveAlias handles the user's response to the alias save prompt.
func (m Model) handleSSHSaveAlias(input string, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	m.sshSavePrompt = false
	m.textarea.Placeholder = "Type your prompt here..."

	input = strings.TrimSpace(input)
	if input == "" || strings.ToLower(input) == "n" || strings.ToLower(input) == "no" {
		m.lines = append(m.lines, toolLabelStyle.Render("⚙ SSH:")+" Alias not saved.")
		m.refreshViewport()
		return m, tea.Batch(cmds...)
	}

	// Save the alias
	aliasName := input
	cfg, err := config.LoadConfig()
	if err != nil {
		cfg = &config.Config{
			Models:        make(map[string]*config.ProviderConfig),
			MaxIterations: 1000,
		}
	}

	// Check for duplicate name and replace
	found := false
	for i, a := range cfg.SSHAliases {
		if a.Name == aliasName {
			cfg.SSHAliases[i].Addr = m.sshSaveAddr
			cfg.SSHAliases[i].Path = m.sshSavePath
			found = true
			break
		}
	}
	if !found {
		cfg.SSHAliases = append(cfg.SSHAliases, config.SSHAlias{
			Name: aliasName,
			Addr: m.sshSaveAddr,
			Path: m.sshSavePath,
		})
	}

	if err := config.SaveConfig(cfg); err != nil {
		m.lines = append(m.lines, toolErrorStyle.Render("✗ Failed to save alias: "+err.Error()))
	} else {
		m.lines = append(m.lines, toolLabelStyle.Render("⚙ SSH:")+" Saved alias '"+aliasName+"' → "+m.sshSaveAddr)
	}
	m.sshSaveAddr = ""
	m.sshSavePath = ""
	m.refreshViewport()
	return m, tea.Batch(cmds...)
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
	m.sshAddr = addr
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
