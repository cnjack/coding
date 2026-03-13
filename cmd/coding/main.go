package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/cnjack/coding/internal/agent"
	"github.com/cnjack/coding/internal/config"
	internalmodel "github.com/cnjack/coding/internal/model"
	"github.com/cnjack/coding/internal/prompts"
	"github.com/cnjack/coding/internal/runner"
	"github.com/cnjack/coding/internal/session"
	"github.com/cnjack/coding/internal/telemetry"
	"github.com/cnjack/coding/internal/tools"
	"github.com/cnjack/coding/internal/tui"
	util "github.com/cnjack/coding/internal/util"
)

var (
	Version   = "0.2.0"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func main() {
	// Handle mcp subcommand before standard flag parsing.
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		handleMCPSubcommand(os.Args[2:])
		return
	}

	var prompt string
	flag.StringVar(&prompt, "prompt", "", "The prompt to send to the coding assistant")
	flag.StringVar(&prompt, "p", "", "The prompt to send to the coding assistant (shorthand)")
	var isDoctor bool
	flag.BoolVar(&isDoctor, "doctor", false, "Run system check to test model and MCP connections")
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "Show version information")
	var resumeUUID string
	flag.StringVar(&resumeUUID, "resume", "", "Resume a previous session by UUID")
	var listSessions bool
	flag.BoolVar(&listSessions, "session", false, "List sessions for the current project and exit")
	flag.Parse()
	prompt = strings.TrimSpace(prompt)
	hasPrompt := prompt != ""

	if showVersion {
		printVersion()
		return
	}
	if isDoctor {
		runDoctorMode()
		return
	}
	if listSessions {
		handleListSessions()
		return
	}

	// Disable default log output to prevent background libraries from corrupting TUI
	log.SetOutput(io.Discard)

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
		env.NewTodoWriteTool(), env.NewTodoReadTool(),
	}

	var mcpStatuses []tui.MCPStatusItem
	if len(cfg.MCPServers) > 0 {
		var mcpTools []tool.BaseTool
		var internalStatuses []tools.MCPStatus
		mcpTools, internalStatuses = tools.LoadMCPTools(ctx, cfg.MCPServers)
		toolList = append(toolList, mcpTools...)
		for _, st := range internalStatuses {
			errMsg := ""
			if st.Error != nil {
				errMsg = st.Error.Error()
			}
			mcpStatuses = append(mcpStatuses, tui.MCPStatusItem{
				Name: st.Name, ToolCount: st.ToolCount, Running: st.Running, ErrMsg: errMsg,
			})
		}
	}

	approvalState := &runner.ApprovalState{}

	// Setup Langfuse tracer if telemetry is configured.
	var langfuseTracer *telemetry.LangfuseTracer
	if cfg.Telemetry != nil && cfg.Telemetry.Langfuse != nil {
		langfuseTracer = telemetry.NewLangfuseTracer(cfg.Telemetry.Langfuse)
	}

	p, _ := tui.RunTUI(hasPrompt, pwd, env.TodoStore)
	approvalState.SetProgram(p)

	createAgent := func() (*adk.ChatModelAgent, error) {
		var middlewares []adk.AgentMiddleware
		if langfuseTracer != nil {
			middlewares = append(middlewares, langfuseTracer.AgentMiddleware())
		}
		return agent.NewAgent(ctx, chatModel, toolList, systemPrompt, approvalState.RequestApproval, middlewares...)
	}

	ag, err := createAgent()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating agent: %v\n", err)
		os.Exit(1)
	}

	// Load a previous session if --resume was requested.
	var initialHistory []adk.Message
	var initialResumeUUID string
	var initialResumeEntries []tui.SessionEntry
	if resumeUUID != "" {
		entries, loadErr := session.LoadSession(resumeUUID)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "Cannot load session: %v\n", loadErr)
			os.Exit(1)
		}
		initialHistory = session.ReconstructHistory(entries)
		initialResumeUUID = resumeUUID
		initialResumeEntries = tui.ConvertSessionEntries(entries)
		hasPrompt = false // treat as interactive, not one-shot
	}

	go func() {
		rec, _ := session.NewRecorder(pwd, cfg.Provider, cfg.Model)
		defer func() {
			if rec != nil {
				rec.Close()
			}
			if langfuseTracer != nil {
				langfuseTracer.Flush()
			}
		}()

		if len(mcpStatuses) > 0 {
			p.Send(tui.MCPStatusMsg{Statuses: mcpStatuses})
		}
		if agentsMdPath := prompts.HasAgentsMd(pwd); agentsMdPath != "" {
			p.Send(tui.AgentsMdMsg{Found: true, Path: agentsMdPath})
		}

		history := initialHistory
		if initialResumeUUID != "" {
			p.Send(tui.SessionResumedMsg{UUID: initialResumeUUID, Entries: initialResumeEntries})
		}

		if hasPrompt {
			p.Send(tui.UserPromptMsg{Prompt: prompt})
			if rec != nil {
				rec.RecordUser(prompt)
			}
			history = append(history, schema.UserMessage(prompt))
			resp := runner.Run(ctx, ag, history, p, rec, env.TodoStore, langfuseTracer)
			if resp != "" {
				history = append(history, &schema.Message{Role: schema.Assistant, Content: resp})
			}
		}

		promptCh := tui.GetPromptChannel()
		pendingPromptCh := tui.GetPendingPromptChannel()
		sshCh := tui.GetSSHChannel()
		configCh := tui.GetConfigChannel()
		addModelCh := tui.GetAddModelChannel()
		resumeCh := tui.GetResumeChannel()
		autoApproveCh := tui.GetAutoApproveChannel()
		for {
			select {
			case enabled := <-autoApproveCh:
				approvalState.SetSessionApproval(enabled)

			case cfgMsg := <-configCh:
				providerCfg := cfgMsg.Models[cfgMsg.Provider]
				if providerCfg != nil {
					newChatModel, err := internalmodel.NewChatModel(ctx, &internalmodel.ChatModelConfig{
						Model: cfgMsg.Model, APIKey: providerCfg.APIKey, BaseURL: providerCfg.BaseURL,
					})
					if err == nil {
						chatModel = newChatModel
						if newAg, err := createAgent(); err == nil {
							ag = newAg
						}
					}
				}

			case userPrompt := <-promptCh:
				if rec != nil {
					rec.RecordUser(userPrompt)
				}
				history = append(history, schema.UserMessage(userPrompt))
				resp := runner.Run(ctx, ag, history, p, rec, env.TodoStore, langfuseTracer)
				if resp != "" {
					history = append(history, &schema.Message{Role: schema.Assistant, Content: resp})
				}

			case pendingPrompt := <-pendingPromptCh:
				if rec != nil {
					rec.RecordUser(pendingPrompt)
				}
				p.Send(tui.UserPromptMsg{Prompt: pendingPrompt})
				history = append(history, schema.UserMessage(pendingPrompt))
				resp := runner.Run(ctx, ag, history, p, rec, env.TodoStore, langfuseTracer)
				if resp != "" {
					history = append(history, &schema.Message{Role: schema.Assistant, Content: resp})
				}

			case uuid := <-resumeCh:
				entries, loadErr := session.LoadSession(uuid)
				if loadErr != nil {
					p.Send(tui.AgentDoneMsg{Err: fmt.Errorf("load session: %w", loadErr)})
					break
				}
				history = session.ReconstructHistory(entries)
				approvalState.SetSessionApproval(false)
				p.Send(tui.SessionResumedMsg{UUID: uuid, Entries: tui.ConvertSessionEntries(entries)})

			case connMsg := <-sshCh:
				switch msg := connMsg.(type) {
				case tui.SSHConnectMsg:
					handleSSHConnect(ctx, env, msg.Addr, msg.Path, p, &systemPrompt, &ag, chatModel, createAgent)
				case tui.SSHListDirReqMsg:
					handleSSHListDir(ctx, env, msg.Path, p)
				case tui.SSHCancelMsg:
					_ = msg
					env.ResetToLocal(pwd, platform)
					systemPrompt = prompts.GetSystemPrompt(platform, pwd)
					if newAg, err := createAgent(); err == nil {
						ag = newAg
					}
				}

			case <-addModelCh:
				p.ReleaseTerminal()
				ok, setupErr := tui.RunSetupTUI()
				p.RestoreTerminal()
				if setupErr != nil {
					p.Send(tui.AgentDoneMsg{Err: fmt.Errorf("Setup error: %w", setupErr)})
				} else if ok {
					newCfg, loadErr := config.LoadConfig()
					if loadErr == nil {
						providerCfg := newCfg.Models[newCfg.Provider]
						if providerCfg != nil {
							newChatModel, cmErr := internalmodel.NewChatModel(ctx, &internalmodel.ChatModelConfig{
								Model: newCfg.Model, APIKey: providerCfg.APIKey, BaseURL: newCfg.Models[newCfg.Provider].BaseURL,
							})
							if cmErr == nil {
								chatModel = newChatModel
								if newAg, agErr := createAgent(); agErr == nil {
									ag = newAg
								}
							}
						}
						p.Send(tui.ConfigUpdatedMsg{
							Provider: newCfg.Provider,
							Model:    newCfg.Model,
							Message:  fmt.Sprintf("✅ Added model: %s - %s\n", newCfg.Provider, newCfg.Model),
						})
					}
				}
			}
		}
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
