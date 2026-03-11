package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cnjack/coding/internal/config"
)

type SetupDoneMsg struct{}

type ProviderProfile struct {
	ID      string
	Name    string
	BaseURL string
	Models  []string
	NeedURL bool // if true, prompt for URL
	NeedKey bool // if true, prompt for API Key
}

var DefaultProviders = []ProviderProfile{
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Models: []string{"gpt-5.4", "gpt-5.4-pro", "gpt-5-mini", "gpt-5-nano", "gpt-5", "gpt-4.1"}, NeedKey: true}, // doc: https://developers.openai.com/api/docs/models
	{ID: "openai-compatible", Name: "OpenAI Compatible", BaseURL: "", Models: []string{"custom"}, NeedURL: true, NeedKey: true},
	{ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", Models: []string{"anthropic/claude-3.5-sonnet", "google/gemini-1.5-pro", "meta-llama/llama-3.1-405b"}, NeedKey: true},
	{ID: "ollama-cloud", Name: "Ollama Cloud", BaseURL: "", Models: []string{"llama3", "llama3.1", "qwen2.5", "mistral"}, NeedURL: true, NeedKey: true},
	{ID: "ollama", Name: "Ollama (Local)", BaseURL: "http://localhost:11434/v1", Models: []string{"llama3", "llama3.1", "qwen2.5", "mistral", "gemma2"}, NeedKey: false},
	{ID: "minimax", Name: "MiniMax", BaseURL: "https://api.minimaxi.com/v1", Models: []string{"MiniMax M2.5-highspeed", "MiniMax M2.5", "MiniMax M2.1", "MiniMax M2"}, NeedKey: true},                                                                                 // doc: https://platform.minimaxi.com/docs/api-reference/api-overview
	{ID: "bigmodel", Name: "BigModel (Zhipu)", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Models: []string{"glm-5", "glm-4.7", "glm-4-7-flashx", "glm-4.6"}, NeedKey: true},                                                                                     // doc: https://docs.bigmodel.cn/cn/guide/models/text/glm-5
	{ID: "bigmodel-plan", Name: "BigModel Plan", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", Models: []string{"glm-5", "glm-4.7"}, NeedKey: true},                                                                                                         // doc: https://docs.bigmodel.cn/cn/coding-plan/overview
	{ID: "z.ai", Name: "Z.AI", BaseURL: "https://api.z.ai/v1", Models: []string{"glm-5", "glm-4.7", "glm-4-7-flashx", "glm-4.6"}, NeedKey: true},                                                                                                                      // same as the bigmodel
	{ID: "z.ai-plan", Name: "Z.AI Plan", BaseURL: "https://api.z.ai/v1", Models: []string{"glm-5", "glm-4.7"}, NeedKey: true},                                                                                                                                         // same as the bigmodel
	{ID: "moonshot-cn", Name: "Moonshot CN (Kimi)", BaseURL: "https://api.moonshot.cn/v1", Models: []string{"kimi-k2.5", "kimi-k2-turbo-preview", "kimi-k2-thinking", "kimi-k2-thinking-turbo"}, NeedKey: true},                                                       // doc: https://platform.moonshot.cn/docs/pricing/chat
	{ID: "moonshot-global", Name: "Moonshot Global", BaseURL: "https://api.moonshot.ai/v1", Models: []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"}, NeedKey: true},                                                                                 // reviewed
	{ID: "kimi-plan", Name: "Kimi Plan", BaseURL: "https://api.kimi.com/coding/v1", Models: []string{"kimi-for-coding"}, NeedKey: true},                                                                                                                               // doc: https://www.kimi.com/code/docs/en/more/third-party-agents.html
	{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com", Models: []string{"deepseek-chat", "deepseek-reasoner"}, NeedKey: true},                                                                                                                    // doc: https://api-docs.deepseek.com/zh-cn/quick_start/pricing
	{ID: "bailian", Name: "Bailian (Aliyun)", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Models: []string{"qwen-plus", "qwen-max", "qwen-turbo"}, NeedKey: true},                                                                                   // doc: https://bailian.console.aliyun.com/cn-beijing/?tab=doc&spm=0.0.0.i0#/doc/?type=model&url=2840914
	{ID: "bailian-plan", Name: "Bailian Plan", BaseURL: "https://coding.dashscope.aliyuncs.com/v1", Models: []string{"qwen3.5-plus", "kimi-k2.5", "glm-5", "MiniMax-M2.5", "qwen3-max-2026-01-23", "qwen3-coder-next", "qwen3-coder-plus", "glm-4.7"}, NeedKey: true}, //doc: https://bailian.console.aliyun.com/cn-beijing/?tab=doc&spm=0.0.0.i0#/doc/?type=model&url=3005961
	{ID: "siliconflow", Name: "硅基流动 (SiliconFlow)", BaseURL: "https://api.siliconflow.cn/v1", Models: []string{"deepseek-ai/DeepSeek-V2.5", "Qwen/Qwen2.5-72B-Instruct"}, NeedKey: true},
	{ID: "magicark", Name: "魔力方舟", BaseURL: "https://api.gitee.com/v1", Models: []string{"qwen", "moonshot", "deepseek"}, NeedKey: true},
	{ID: "groq", Name: "Groq", BaseURL: "https://api.groq.com/openai/v1", Models: []string{"llama3-8b-8192", "llama3-70b-8192", "mixtral-8x7b-32768"}, NeedKey: true},
	{ID: "together", Name: "Together AI", BaseURL: "https://api.together.xyz/v1", Models: []string{"meta-llama/Llama-3-70b-chat-hf", "mistralai/Mixtral-8x7B-Instruct-v0.1"}, NeedKey: true},
	{ID: "tencent", Name: "腾讯混元 (Tencent)", BaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Models: []string{"hunyuan-pro", "hunyuan-standard", "hunyuan-lite"}, NeedKey: true},
}

type providerItem struct {
	profile    ProviderProfile
	configured bool // this exact provider has an API key in config
}

func (i providerItem) Title() string { return i.profile.Name }
func (i providerItem) Description() string {
	if i.configured {
		return "✓ Configured · " + i.profile.BaseURL
	}
	if i.profile.BaseURL != "" {
		return i.profile.BaseURL
	}
	return i.profile.ID
}
func (i providerItem) FilterValue() string { return i.profile.Name + " " + i.profile.ID }

type modelListItem struct {
	name string
	desc string
}

func (i modelListItem) Title() string       { return i.name }
func (i modelListItem) Description() string { return i.desc }
func (i modelListItem) FilterValue() string { return i.name }

type SetupState int

const (
	StateProvider SetupState = iota
	StateModel
	StateCustomModel
	StateURL
	StateAPIKey
)

type SetupModel struct {
	state         SetupState
	providerList  list.Model
	modelList     list.Model
	customModelIn textinput.Model
	urlIn         textinput.Model
	keyIn         textinput.Model

	selectedProvider *ProviderProfile
	selectedModel    string
	finalURL         string
	finalKey         string

	width  int
	height int
	err    string
	done   bool
}

func NewSetupModel() SetupModel {
	m := SetupModel{
		state: StateProvider,
	}

	// Build a set of configured providers
	configuredProviders := make(map[string]bool)
	if cfg, err := config.LoadConfig(); err == nil && cfg != nil {
		for _, dp := range DefaultProviders {
			if pCfg, ok := cfg.Models[dp.ID]; ok && pCfg.APIKey != "" {
				configuredProviders[dp.ID] = true
			}
		}
	}

	pItems := make([]list.Item, len(DefaultProviders))
	for i, p := range DefaultProviders {
		item := providerItem{profile: p}
		if configuredProviders[p.ID] {
			item.configured = true
		}
		pItems[i] = item
	}
	del := list.NewDefaultDelegate()
	del.SetSpacing(0)
	pl := list.New(pItems, del, 60, 15)
	pl.Title = "Select LLM Provider (↑/↓ to navigate, Enter to confirm)"
	pl.SetShowHelp(false)
	m.providerList = pl

	ml := list.New([]list.Item{}, del, 60, 15)
	ml.SetShowHelp(false)
	m.modelList = ml

	m.customModelIn = textinput.New()
	m.customModelIn.Placeholder = "Enter custom model name..."
	m.customModelIn.Prompt = "Model Name: "
	m.customModelIn.Width = 50

	m.urlIn = textinput.New()
	m.urlIn.Placeholder = "https://your-base-url/v1"
	m.urlIn.Prompt = "Base URL: "
	m.urlIn.Width = 50

	m.keyIn = textinput.New()
	m.keyIn.Placeholder = "sk-..."
	m.keyIn.Prompt = "API Key: "
	m.keyIn.EchoMode = textinput.EchoPassword
	m.keyIn.EchoCharacter = '•'
	m.keyIn.Width = 50

	return m
}

func (m SetupModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink)
}

func (m SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

		switch m.state {
		case StateProvider:
			if msg.String() == "enter" {
				sel := m.providerList.SelectedItem()
				if sel != nil {
					p := sel.(providerItem).profile
					m.selectedProvider = &p

					var mItems []list.Item
					for _, mod := range p.Models {
						mItems = append(mItems, modelListItem{name: mod, desc: "Built-in model"})
					}
					mItems = append(mItems, modelListItem{name: "Custom...", desc: "Enter a custom model name"})
					m.modelList.SetItems(mItems)
					m.modelList.Title = "Select Model (" + p.Name + ")"

					m.state = StateModel
					return m, nil
				}
			}
			var cmd tea.Cmd
			m.providerList, cmd = m.providerList.Update(msg)
			cmds = append(cmds, cmd)

		case StateModel:
			if msg.String() == "enter" {
				sel := m.modelList.SelectedItem()
				if sel != nil {
					name := sel.(modelListItem).name
					if name == "Custom..." {
						m.state = StateCustomModel
						m.customModelIn.Focus()
					} else {
						m.selectedModel = name
						return m.advanceAfterModel()
					}
					return m, nil
				}
			} else if msg.String() == "esc" {
				m.state = StateProvider
				return m, nil
			}
			var cmd tea.Cmd
			m.modelList, cmd = m.modelList.Update(msg)
			cmds = append(cmds, cmd)

		case StateCustomModel:
			if msg.String() == "enter" {
				val := strings.TrimSpace(m.customModelIn.Value())
				if val != "" {
					m.selectedModel = val
					return m.advanceAfterModel()
				}
			} else if msg.String() == "esc" {
				m.state = StateModel
				return m, nil
			}
			var cmd tea.Cmd
			m.customModelIn, cmd = m.customModelIn.Update(msg)
			cmds = append(cmds, cmd)

		case StateURL:
			if msg.String() == "enter" {
				val := strings.TrimSpace(m.urlIn.Value())
				if val == "" && m.selectedProvider.BaseURL != "" {
					val = m.selectedProvider.BaseURL
				}
				if val != "" {
					m.finalURL = val
					return m.advanceAfterURL()
				}
			} else if msg.String() == "esc" {
				if m.selectedModel == "Custom..." {
					m.state = StateCustomModel
				} else {
					m.state = StateModel
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.urlIn, cmd = m.urlIn.Update(msg)
			cmds = append(cmds, cmd)

		case StateAPIKey:
			if msg.String() == "enter" {
				val := strings.TrimSpace(m.keyIn.Value())
				if val != "" || !m.selectedProvider.NeedKey {
					m.finalKey = val
					return m.submit()
				} else if m.selectedProvider.NeedKey && val == "" {
					m.err = "API Key is required"
				}
			} else if msg.String() == "esc" {
				if m.selectedProvider.NeedURL || m.selectedProvider.BaseURL == "" {
					m.state = StateURL
					m.urlIn.Focus()
				} else if m.selectedModel == "Custom..." {
					m.state = StateCustomModel
					m.customModelIn.Focus()
				} else {
					m.state = StateModel
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.keyIn, cmd = m.keyIn.Update(msg)
			cmds = append(cmds, cmd)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.providerList.SetSize(msg.Width-4, 15)
		m.modelList.SetSize(msg.Width-4, 15)
	}

	return m, tea.Batch(cmds...)
}

func (m SetupModel) advanceAfterModel() (tea.Model, tea.Cmd) {
	if m.selectedProvider.NeedURL || m.selectedProvider.BaseURL == "" {
		m.state = StateURL
		if m.selectedProvider.BaseURL != "" {
			m.urlIn.Placeholder = m.selectedProvider.BaseURL
		}
		m.urlIn.Focus()
		return m, nil
	} else {
		m.finalURL = m.selectedProvider.BaseURL
		return m.advanceAfterURL()
	}
}

func (m SetupModel) advanceAfterURL() (tea.Model, tea.Cmd) {
	if m.selectedProvider.NeedKey {
		// Check if this exact provider already has an API key in config
		if existingKey := m.findProviderAPIKey(); existingKey != "" {
			// Auto-use the existing key, skip the input step
			m.finalKey = existingKey
			return m.submit()
		}
		m.state = StateAPIKey
		m.keyIn.Focus()
		return m, nil
	} else {
		// skip key
		m.finalKey = ""
		return m.submit()
	}
}

// findProviderAPIKey checks existing config for an API key for the selected provider.
func (m SetupModel) findProviderAPIKey() string {
	if cfg, err := config.LoadConfig(); err == nil && cfg != nil {
		if pCfg, ok := cfg.Models[m.selectedProvider.ID]; ok && pCfg.APIKey != "" {
			return pCfg.APIKey
		}
	}
	return ""
}

func (m SetupModel) submit() (tea.Model, tea.Cmd) {
	cfg, err := config.LoadConfig()
	if err != nil {
		// New config
		cfg = &config.Config{
			Models:        make(map[string]*config.ProviderConfig),
			MaxIterations: 1000,
		}
	}

	pID := m.selectedProvider.ID

	// Create or update provider config
	if cfg.Models == nil {
		cfg.Models = make(map[string]*config.ProviderConfig)
	}

	pCfg, exists := cfg.Models[pID]
	if !exists {
		pCfg = &config.ProviderConfig{
			Models: []string{},
		}
		cfg.Models[pID] = pCfg
	}

	pCfg.APIKey = m.finalKey
	pCfg.BaseURL = m.finalURL

	// Add model to the history if not present
	found := false
	for _, mod := range pCfg.Models {
		if mod == m.selectedModel {
			found = true
			break
		}
	}
	if !found {
		pCfg.Models = append(pCfg.Models, m.selectedModel)
	}

	cfg.Provider = pID
	cfg.Model = m.selectedModel

	if err := config.SaveConfig(cfg); err != nil {
		m.err = fmt.Sprintf("Failed to save config: %v", err)
		return m, nil
	}

	m.done = true
	return m, tea.Quit
}

func (m SetupModel) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}

	logo := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("🚀 Little Jack — Setup")
	subtitle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("Follow the wizard to configure your LLM")
	header := lipgloss.JoinVertical(lipgloss.Center, logo, subtitle)
	header = lipgloss.NewStyle().Width(w).Align(lipgloss.Center).PaddingTop(2).PaddingBottom(1).Render(header)

	var content string
	switch m.state {
	case StateProvider:
		content = m.providerList.View()
	case StateModel:
		content = m.modelList.View()
	case StateCustomModel:
		content = m.customModelIn.View()
	case StateURL:
		content = m.urlIn.View()
		if m.selectedProvider.BaseURL != "" {
			content += "\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("(Leave empty to use default: "+m.selectedProvider.BaseURL+")")
		}
	case StateAPIKey:
		content = m.keyIn.View()
	}

	if m.state != StateProvider && m.state != StateModel {
		content = "\n" + content + "\n\n  Press Enter to submit, Esc to go back."
	}

	errLine := ""
	if m.err != "" {
		errLine = "\n" + lipgloss.NewStyle().PaddingLeft(2).Foreground(colorError).Bold(true).Render("  ⚠ "+m.err)
	}

	helpLine := lipgloss.NewStyle().Foreground(colorMuted).PaddingLeft(2).Render("  Ctrl+C quit")
	cfgPath := lipgloss.NewStyle().Foreground(colorMuted).Italic(true).PaddingLeft(2).Render("  Config: " + config.ConfigPath())

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		divider(w-4),
		"\n",
		lipgloss.NewStyle().PaddingLeft(2).Render(content),
		errLine,
		"\n",
		divider(w-4),
		helpLine,
		cfgPath,
	)
}

func (m SetupModel) IsDone() bool {
	return m.done
}

func RunSetupTUI() (bool, error) {
	m := NewSetupModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return false, err
	}
	if sm, ok := finalModel.(SetupModel); ok {
		return sm.IsDone(), nil
	}
	return false, nil
}
