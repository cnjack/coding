package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	sshagent "golang.org/x/crypto/ssh/agent"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cnjack/coding/internal/agent"
	"github.com/cnjack/coding/internal/config"
	internalmodel "github.com/cnjack/coding/internal/model"
	"github.com/cnjack/coding/internal/prompts"
	"github.com/cnjack/coding/internal/tools"
	"github.com/cnjack/coding/internal/tui"
	util "github.com/cnjack/coding/internal/util"
)

func main() {
	var prompt string
	flag.StringVar(&prompt, "prompt", "", "The prompt to send to the coding assistant")
	flag.StringVar(&prompt, "p", "", "The prompt to send to the coding assistant (shorthand)")
	flag.Parse()
	prompt = strings.TrimSpace(prompt)
	hasPrompt := prompt != ""

	// Setup wizard if config is missing
	if config.NeedsSetup() {
		ok, err := tui.RunSetupTUI()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Setup error: %v\n", err)
			os.Exit(1)
		}
		if !ok {
			os.Exit(0)
		}
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Config file: %s\n", config.ConfigPath())
		os.Exit(1)
	}

	ctx := context.Background()
	pwd := util.GetWorkDir()
	platform := util.GetSystemInfo()
	systemPrompt := prompts.GetSystemPrompt(platform, pwd)

	providerCfg := cfg.Models[cfg.Provider]
	if providerCfg == nil {
		fmt.Fprintf(os.Stderr, "Provider %q not found in config\n", cfg.Provider)
		os.Exit(1)
	}

	chatModel, err := internalmodel.NewChatModel(ctx, &internalmodel.ChatModelConfig{
		Model: cfg.Model, APIKey: providerCfg.APIKey, BaseURL: providerCfg.BaseURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating model: %v\n", err)
		os.Exit(1)
	}

	env := tools.NewEnv(pwd, platform)
	toolList := []tool.BaseTool{
		env.NewReadTool(), env.NewEditTool(), env.NewWriteTool(),
		env.NewExecuteTool(), env.NewGrepTool(),
	}

	ag, err := agent.NewAgent(ctx, chatModel, toolList, systemPrompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating agent: %v\n", err)
		os.Exit(1)
	}

	p, _ := tui.RunTUI(hasPrompt)

	go func() {
		var history []adk.Message

		if hasPrompt {
			p.Send(tui.UserPromptMsg{Prompt: prompt})
			history = append(history, schema.UserMessage(prompt))
			resp := runAgent(ctx, ag, history, p)
			if resp != "" {
				history = append(history, &schema.Message{Role: schema.Assistant, Content: resp})
			}
		}

		// Multi-turn loop: listen to both prompt and SSH channels
		promptCh := tui.GetPromptChannel()
		sshCh := tui.GetSSHChannel()
		configCh := tui.GetConfigChannel()
		for {
			select {
			case cfgMsg := <-configCh:
				providerCfg := cfgMsg.Models[cfgMsg.Provider]
				if providerCfg != nil {
					newChatModel, err := internalmodel.NewChatModel(ctx, &internalmodel.ChatModelConfig{
						Model: cfgMsg.Model, APIKey: providerCfg.APIKey, BaseURL: providerCfg.BaseURL,
					})
					if err == nil {
						chatModel = newChatModel
						newAg, err := agent.NewAgent(ctx, chatModel, toolList, systemPrompt)
						if err == nil {
							ag = newAg
						}
					}
				}
			case userPrompt := <-promptCh:
				history = append(history, schema.UserMessage(userPrompt))
				resp := runAgent(ctx, ag, history, p)
				if resp != "" {
					history = append(history, &schema.Message{Role: schema.Assistant, Content: resp})
				}

			case connMsg := <-sshCh:
				switch msg := connMsg.(type) {
				case tui.SSHConnectMsg:
					handleSSHConnect(ctx, env, msg.Addr, msg.Path, p, &systemPrompt, &ag, chatModel, toolList)
				case tui.SSHListDirReqMsg:
					handleSSHListDir(ctx, env, msg.Path, p)
				}
			}
		}
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

// handleSSHConnect connects to a remote machine via SSH and reconfigures the env.
func handleSSHConnect(ctx context.Context, env *tools.Env, addr, path string, p *tea.Program,
	systemPrompt *string, ag **adk.ChatModelAgent,
	chatModel einomodel.ToolCallingChatModel, toolList []tool.BaseTool) {

	// Parse user@host
	user := "root"
	host := addr
	if parts := strings.SplitN(addr, "@", 2); len(parts) == 2 {
		user = parts[0]
		host = parts[1]
	}

	authMethods := buildSSHAuthMethods()

	executor, err := tools.NewSSHExecutor(host, user, authMethods)
	if err != nil {
		p.Send(tui.SSHStatusMsg{Success: false, Err: err})
		return
	}

	// Temporarily set the executor into env so handleSSHListDir can use it, if it's the interactive setup.
	env.SetSSH(executor, "/root")

	if path == "?" {
		// Discover initial home dir and trigger the path picker UI
		remotePwd := "/root"
		if stdout, _, execErr := executor.Exec(ctx, "pwd", "", 5*1e9); execErr == nil {
			if trimmed := strings.TrimSpace(stdout); trimmed != "" {
				remotePwd = trimmed
			}
		}
		handleSSHListDir(ctx, env, remotePwd, p)
		return // Do not initialize agent yet
	}

	// Detect remote home dir as default working directory if no path specified
	remotePwd := "/root"
	if path != "" {
		remotePwd = path
	} else {
		if stdout, _, execErr := executor.Exec(ctx, "pwd", "", 5*1e9); execErr == nil {
			if trimmed := strings.TrimSpace(stdout); trimmed != "" {
				remotePwd = trimmed
			}
		}
	}

	// Switch env to SSH
	env.SetSSH(executor, remotePwd)

	// Update system prompt with remote context
	*systemPrompt = prompts.GetSystemPrompt(executor.Platform(), remotePwd)

	// Rebuild agent with new system prompt
	newAg, agErr := agent.NewAgent(ctx, chatModel, toolList, *systemPrompt)
	if agErr == nil {
		*ag = newAg
	}

	p.Send(tui.SSHStatusMsg{
		Success: true,
		Label:   fmt.Sprintf("%s@%s (pwd: %s)", user, host, remotePwd),
	})
}

// handleSSHListDir runs `ls` remotely to fetch directory contents for the TUI picker.
func handleSSHListDir(ctx context.Context, env *tools.Env, path string, p *tea.Program) {
	cmd := fmt.Sprintf("ls -F -1 %s", tools.ShellQuote(path))
	stdout, stderr, err := env.Exec.Exec(ctx, cmd, "", 10*1e9)
	if err != nil {
		p.Send(tui.SSHDirResultsMsg{Err: fmt.Errorf("ls failed: %v\nstderr: %s", err, truncate(stderr, 100))})
		return
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var dirs []string

	// Check if parent directory should be added (if not root)
	if path != "/" {
		// Just a heuristic, actual parent logic can be handled client side 
		// but providing ".." gives user an easy way back.
		dirs = append(dirs, "..")
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// In `ls -F`, directories end with '/'
		if strings.HasSuffix(line, "/") {
			dirs = append(dirs, line[:len(line)-1]) // remove trailing slash
		}
	}

	p.Send(tui.SSHDirResultsMsg{
		Path:  path,
		Items: dirs,
	})
}

func truncate(s string, l int) string {
	if len(s) > l {
		return s[:l] + "..."
	}
	return s
}

func buildSSHAuthMethods() []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	// Try SSH agent
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(sshagent.NewClient(conn).Signers))
		}
	}

	// Try common key files
	keyPaths := []string{
		os.Getenv("HOME") + "/.ssh/id_rsa",
		os.Getenv("HOME") + "/.ssh/id_ed25519",
		os.Getenv("HOME") + "/.ssh/id_ecdsa",
	}
	for _, keyPath := range keyPaths {
		key, err := os.ReadFile(keyPath)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	return methods
}

func runAgent(ctx context.Context, ag *adk.ChatModelAgent, messages []adk.Message, p *tea.Program) string {
	input := &adk.AgentInput{
		Messages:        messages,
		EnableStreaming: true,
	}

	var assistantText strings.Builder

	iterator := ag.Run(ctx, input)
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			p.Send(tui.AgentDoneMsg{Err: event.Err})
			return assistantText.String()
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mo := event.Output.MessageOutput

		if mo.Role == schema.Tool {
			toolName := mo.ToolName
			if !mo.IsStreaming && mo.Message != nil {
				p.Send(tui.ToolResultMsg{Name: toolName, Output: mo.Message.Content})
			} else if mo.IsStreaming {
				var sb strings.Builder
				for {
					chunk, err := mo.MessageStream.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						p.Send(tui.ToolResultMsg{Name: toolName, Err: err})
						break
					}
					if chunk != nil {
						sb.WriteString(chunk.Content)
					}
				}
				p.Send(tui.ToolResultMsg{Name: toolName, Output: sb.String()})
			}
			continue
		}

		if mo.Role != schema.Assistant {
			continue
		}

		if mo.IsStreaming {
			for {
				chunk, err := mo.MessageStream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					break
				}
				if chunk == nil {
					continue
				}
				if len(chunk.ToolCalls) > 0 {
					for _, tc := range chunk.ToolCalls {
						if tc.Function.Name != "" {
							p.Send(tui.ToolCallMsg{Name: tc.Function.Name, Args: tc.Function.Arguments})
						}
					}
				}
				if chunk.Content != "" {
					assistantText.WriteString(chunk.Content)
					p.Send(tui.AgentTextMsg{Text: chunk.Content})
				}
			}
		} else if mo.Message != nil {
			if len(mo.Message.ToolCalls) > 0 {
				for _, tc := range mo.Message.ToolCalls {
					p.Send(tui.ToolCallMsg{Name: tc.Function.Name, Args: tc.Function.Arguments})
				}
			}
			if mo.Message.Content != "" {
				assistantText.WriteString(mo.Message.Content)
				p.Send(tui.AgentTextMsg{Text: mo.Message.Content})
			}
		}
	}

	p.Send(tui.AgentDoneMsg{})
	return assistantText.String()
}
