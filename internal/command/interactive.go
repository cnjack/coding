package command

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/cnjack/jcode/internal/agent"
	"github.com/cnjack/jcode/internal/artifact"
	"github.com/cnjack/jcode/internal/browser"
	"github.com/cnjack/jcode/internal/channel"
	"github.com/cnjack/jcode/internal/computer"
	"github.com/cnjack/jcode/internal/config"
	"github.com/cnjack/jcode/internal/flow"
	"github.com/cnjack/jcode/internal/handler"
	"github.com/cnjack/jcode/internal/hooks"
	mempipeline "github.com/cnjack/jcode/internal/memory/pipeline"
	"github.com/cnjack/jcode/internal/mode"
	internalmodel "github.com/cnjack/jcode/internal/model"
	weixin "github.com/cnjack/jcode/internal/pkg/weixin"
	"github.com/cnjack/jcode/internal/prompts"
	"github.com/cnjack/jcode/internal/providertools"
	"github.com/cnjack/jcode/internal/review"
	"github.com/cnjack/jcode/internal/runner"
	"github.com/cnjack/jcode/internal/session"
	"github.com/cnjack/jcode/internal/skills"
	"github.com/cnjack/jcode/internal/team"
	"github.com/cnjack/jcode/internal/telemetry"
	"github.com/cnjack/jcode/internal/toolpolicy"
	"github.com/cnjack/jcode/internal/tools"
	"github.com/cnjack/jcode/internal/tui"
	util "github.com/cnjack/jcode/internal/util"
)

// interactiveState holds all shared state for the interactive TUI event loop.
type interactiveState struct {
	ctx                  context.Context
	p                    *tea.Program
	cfg                  *config.Config
	chatModel            einomodel.ToolCallingChatModel
	ag                   *adk.ChatModelAgent
	history              []adk.Message
	env                  *tools.Env
	bgManager            *tools.BackgroundManager
	approvalState        *runner.ApprovalState
	artifactService      *artifact.Service
	imageLedger          *toolpolicy.UsageLedger
	providerSearchLedger *toolpolicy.UsageLedger
	activeProvider       string
	activeModel          string
	rec                  *session.Recorder
	planStore            *tools.PlanStore
	summCapture          *agent.SummarizationCapture
	systemPrompt         string
	toolList             []tool.BaseTool
	agentMode            tui.AgentMode
	agentRoleName        string
	agentRole            *config.AgentRoleConfig
	envInfo              *util.EnvInfo
	pwd                  string
	platform             string
	registry             *internalmodel.ModelRegistry
	skillLoader          *skills.Loader
	flowLoader           *flow.Loader
	langfuseTracer       *telemetry.LangfuseTracer
	h                    handler.AgentEventHandler
	askUserDeps          *tools.AskUserDeps
	teamManager          *team.Manager
	rawMCPTools          []tool.BaseTool
	rawMCPConfigEpoch    string
	mcpTools             []tool.BaseTool
	mcpReloadMu          sync.Mutex
	modeSwitchMu         sync.Mutex
	agentTokenUsage      *internalmodel.TokenUsage

	sessionResumeWarning  string
	sessionBaselineCommit string

	// hookDisp fires user-configured hooks at agent-loop lifecycle points. Always
	// non-nil (a no-op dispatcher when no hooks are configured).
	hookDisp hooks.Dispatcher
	// hookStartContext holds SessionStart additionalContext, prepended to the
	// first user prompt then cleared.
	hookStartContext string

	// WeChat channel
	wechatClient *weixin.Client
	agentRunning atomic.Bool

	// Agent cancellation
	cancelFunc context.CancelFunc // used to cancel a running agent job
	runCtx     context.Context    // per-run context, non-nil while agent is running
}

func teamChildUsesPlanProfile(agentType, permission string) bool {
	return agentType == team.AgentTypeExplore || permission == team.PermissionPlan
}

// buildTeamChildTools is the single hard boundary for teammate capabilities.
// Explore agents and every Plan-mode teammate get only inspection tools plus
// the endpoint-enforced Plan execute variant. A write-capable profile is
// available only to normalized general/coder teammates in normal or auto mode.
func buildTeamChildTools(childEnv any, agentType, permission string) []tool.BaseTool {
	e, ok := childEnv.(*tools.Env)
	if !ok || e == nil {
		return nil
	}
	normalizedType, err := team.NormalizeAgentType(agentType)
	if err != nil {
		return nil
	}
	normalizedPermission, err := team.NormalizePermission(permission)
	if err != nil {
		return nil
	}
	if teamChildUsesPlanProfile(normalizedType, normalizedPermission) {
		return []tool.BaseTool{
			e.NewReadTool(),
			e.NewGrepTool(),
			e.NewPlanExecuteTool(),
		}
	}
	return []tool.BaseTool{
		e.NewReadTool(), e.NewEditTool(), e.NewWriteTool(),
		e.NewExecuteTool(nil), e.NewGrepTool(),
		e.NewTodoWriteTool(), e.NewTodoReadTool(),
	}
}

func buildTeamChildPrompt(agentType, permission, platform, pwd string) string {
	normalizedType, err := team.NormalizeAgentType(agentType)
	if err != nil {
		return ""
	}
	normalizedPermission, err := team.NormalizePermission(permission)
	if err != nil {
		return ""
	}
	if teamChildUsesPlanProfile(normalizedType, normalizedPermission) {
		return prompts.GetPlanSystemPrompt(platform, pwd, "local", nil)
	}
	return prompts.GetSystemPrompt(platform, pwd, "local", nil, "")
}

func (s *interactiveState) buildAllTools() []tool.BaseTool {
	// One factory serves subagent + workflow model overrides (incl. the
	// "small" alias); fallback is the current session model, so it must be
	// rebuilt here on every agent rebuild (model switches re-enter this func).
	factory := internalmodel.NewModelFactory(s.cfg, s.chatModel)
	agentRoles := config.LoadAgentRoles(s.env.Pwd())
	all := []tool.BaseTool{
		s.env.NewReadTool(), s.env.NewEditTool(), s.env.NewWriteTool(),
		s.env.NewExecuteTool(s.bgManager), s.env.NewGrepTool(),
		s.env.NewTodoWriteTool(), s.env.NewTodoReadTool(),
		s.env.NewGoalSetTool(), s.env.NewGoalGetTool(), s.env.NewGoalUpdateTool(),
		s.env.NewAutomationCreateTool(),
		s.env.NewAutomationListTool(),
		s.env.NewAutomationDeleteTool(),
		s.env.NewCheckBackgroundTool(s.bgManager),
		s.env.NewSubagentTool(&tools.SubagentDeps{
			ChatModel:    s.chatModel,
			ModelFactory: factory,
			Notifier:     s.subagentNotifier,
			ProgressFn:   s.subagentProgress,
			TokenFn:      s.subagentTokenFn,
			Recorder:     s.rec,
			Tracer:       s.langfuseTracer,
			AgentRoles:   agentRoles,
		}),
		s.env.NewWorkflowRunTool(&tools.WorkflowToolDeps{
			ModelFactory: factory,
			Recorder:     s.rec,
			Tracer:       s.langfuseTracer,
			Loader:       s.flowLoader,
			AgentRoles:   agentRoles,
		}),
		tools.NewAskUserTool(s.askUserDeps),
		skills.NewLoadSkillTool(s.skillLoader),
		tools.NewTeamCreateTool(s.teamManager),
		tools.NewTeamSpawnTool(s.teamManager),
		tools.NewTeamSendMessageTool(s.teamManager),
		tools.NewTeamListTool(s.teamManager),
		tools.NewTeamDeleteTool(s.teamManager),
	}
	// Image generation always executes in the local JCode engine and stores a
	// managed local artifact, even while the coding executor targets SSH.
	if imageGenerationEnabled(s.cfg, false, true) {
		if imageTool, err := configuredGenerateImageTool(
			s.cfg, s.artifactService, s.rec, s.imageLedger,
			projectProviderRuntimeConfigLoader(s.pwd), s.h, nil,
		); err == nil {
			all = append(all, imageTool)
		} else if s.cfg != nil && s.cfg.ImageModel != "" {
			config.Logger().Printf("[image] generate_image unavailable: %v", err)
		}
	}
	// Only add switch_env tool if SSH aliases are configured
	if s.cfg != nil && len(s.cfg.SSHAliases) > 0 {
		all = append(all, s.env.NewSwitchEnvTool())
	}
	if config.MemoryEnabled(s.cfg) {
		all = append(all, s.env.NewMemoryNoteTool(&tools.MemoryNoteDeps{
			SessionIDFn: func() string {
				if s.rec != nil {
					return s.rec.UUID()
				}
				return ""
			},
		}))
	}
	all = append(all, s.env.NewBrowserTools()...)
	all = append(all, s.env.NewComputerTools()...)
	return all
}

func (s *interactiveState) buildPlanTools() []tool.BaseTool {
	plan := []tool.BaseTool{
		s.env.NewReadTool(),
		s.env.NewPlanExecuteTool(),
		s.env.NewGrepTool(),
		s.env.NewTodoWriteTool(), s.env.NewTodoReadTool(),
		s.env.NewGoalSetTool(), s.env.NewGoalGetTool(), s.env.NewGoalUpdateTool(),
		s.env.NewAutomationListTool(),
		tools.NewAskUserTool(s.askUserDeps),
	}
	plan = append(plan, s.env.NewBrowserPlanTools()...)
	return append(plan, s.env.NewComputerPlanTools()...)
}

func (s *interactiveState) setTopLevelAgent(name string) error {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "default") {
		name = ""
	}
	if name == "" {
		s.agentRoleName = ""
		s.agentRole = nil
		return nil
	}
	role, ok := config.LoadAgentRoles(s.pwd)[name]
	if !ok {
		return fmt.Errorf("unknown custom agent %q", name)
	}
	s.agentRoleName = name
	s.agentRole = &role
	return nil
}

func (s *interactiveState) withTopLevelAgentPrompt(base string) string {
	if s.agentRole == nil {
		return base
	}
	return withCustomAgentPrompt(base, s.agentRoleName, *s.agentRole)
}

func (s *interactiveState) buildTopLevelTools() []tool.BaseTool {
	if s.agentMode == tui.ModePlanning {
		return s.buildPlanTools()
	}
	return s.buildAllTools()
}

func (s *interactiveState) refreshTopLevelPromptAndTools(envLabel string, envInfo *util.EnvInfo) {
	if s.agentMode == tui.ModePlanning {
		s.systemPrompt = s.withTopLevelAgentPrompt(
			prompts.GetPlanSystemPrompt(s.platform, s.pwd, envLabel, envInfo),
		)
	} else {
		s.systemPrompt = s.withTopLevelAgentPrompt(
			prompts.GetSystemPrompt(s.platform, s.pwd, envLabel, envInfo, s.skillLoader.Descriptions()),
		)
	}
	s.toolList = s.buildTopLevelTools()
}

func (s *interactiveState) subagentNotifier(name, agentType string, done bool, result string, err error) {
	if s.p == nil {
		return
	}
	if !done {
		s.p.Send(tui.SubagentStartMsg{Name: name, Type: agentType})
	} else {
		s.p.Send(tui.SubagentDoneMsg{Name: name, Result: result, Err: err})
	}
}

func (s *interactiveState) subagentProgress(agentName, event, toolName, detail string) {
	if s.p == nil {
		return
	}
	s.p.Send(tui.SubagentProgressMsg{
		AgentName: agentName,
		Event:     event,
		ToolName:  toolName,
		Detail:    detail,
	})
}

func (s *interactiveState) subagentTokenFn(agentName string, totalTokens int64) {
	if s.p != nil {
		s.p.Send(tui.SubagentTokenUpdateMsg{Name: agentName, TotalTokens: totalTokens})
	}
}

func (s *interactiveState) createAgent() (*adk.ChatModelAgent, error) {
	return s.createAgentWithModeCatalog(
		s.agentMode, s.systemPrompt, s.toolList, s.mcpTools,
	)
}

// createAgentWithModeCatalog builds an unpublished agent for a target tool and
// prompt axis. Explicit mode changes use it before writing the authorization
// journal, so a failed build cannot alter the live agent or selector state.
func (s *interactiveState) createAgentWithModeCatalog(
	agentMode tui.AgentMode,
	systemPrompt string,
	toolList []tool.BaseTool,
	mcpTools []tool.BaseTool,
) (*adk.ChatModelAgent, error) {
	var middlewares []adk.ChatModelAgentMiddleware
	if s.langfuseTracer != nil {
		middlewares = append(middlewares, s.langfuseTracer.AgentMiddleware())
	}

	var handlers []adk.ChatModelAgentMiddleware

	providerName, modelName := s.activeProvider, s.activeModel
	if providerName == "" || modelName == "" {
		providerName, modelName = s.cfg.GetProviderModel()
	}
	contextLimit := internalmodel.ResolveContextLimit(s.registry, s.cfg, providerName, modelName)
	// effLimit reserves output/summary headroom so trigger math never lets the
	// real window overflow before compaction fires. The reminder middleware
	// keeps the raw limit (occupancy display semantics, not trigger budget).
	effLimit := internalmodel.EffectiveContextLimit(contextLimit)
	// compactThreshold drives summarization + compaction; the reduction (lighter,
	// earlier tool-output clearing) threshold derives from it inside
	// agent.BuildReductionConfig.
	compactThreshold := s.cfg.CompactionThreshold()

	summMw, err := summarization.New(s.ctx, &summarization.Config{
		Model: s.chatModel,
		Trigger: &summarization.TriggerCondition{
			ContextTokens: int(float64(effLimit) * compactThreshold),
		},
		TranscriptFilePath: filepath.Join(config.ConfigDir(), "transcript.txt"),
		Finalize: func(ctx context.Context, originalMsgs []adk.Message, summary adk.Message) ([]adk.Message, error) {
			var systemMsgs []adk.Message
			var contextN int
			for _, msg := range originalMsgs {
				if msg.Role == schema.System {
					systemMsgs = append(systemMsgs, msg)
				} else {
					contextN++
				}
			}
			s.summCapture.Capture(summary.Content, contextN)
			config.Logger().Printf("[summarization] Finalize: compacted %d context messages", contextN)
			if s.agentTokenUsage != nil {
				s.agentTokenUsage.ResetContext()
			}
			if s.p != nil {
				s.p.Send(tui.CompactDoneMsg{})
			}
			return append(systemMsgs, summary), nil
		},
	})
	if err != nil {
		config.Logger().Printf("[agent] summarization middleware init error: %v", err)
		// Fallback only: when the high-fidelity eino summarizer is unavailable,
		// register the lossy in-run compaction middleware as a window guardrail.
		// The two are mutually exclusive — never both in the same run — because
		// this one truncates messages to 500 chars and its result is not synced
		// back to s.history across turns.
		compactionStrategy := agent.NewThresholdCompactionStrategy(compactThreshold, s.chatModel, 6)
		compactionMw := agent.NewCompactionMiddleware(compactionStrategy, effLimit, s.agentTokenUsage, func(savedTokens int) {
			if s.agentTokenUsage != nil {
				s.agentTokenUsage.ResetContext()
			}
			if s.p != nil {
				s.p.Send(tui.CompactDoneMsg{OldTokens: 0, NewTokens: 0})
			}
		})
		handlers = append(handlers, compactionMw)
	} else {
		handlers = append(handlers, summMw)
	}

	reductionMw, err := reduction.New(s.ctx, agent.BuildReductionConfig(
		filepath.Join(config.ConfigDir(), "reduction"),
		contextLimit,
		compactThreshold,
		internalmodel.NewCalibratedCounter(s.agentTokenUsage).Count,
	))
	if err != nil {
		config.Logger().Printf("[agent] reduction middleware init error: %v", err)
	} else {
		handlers = append(handlers, reductionMw)
	}
	// Aggregate cap on one turn's NEW tool results: reduction only caps each
	// result individually (50k), so N parallel calls could still flood a single
	// request. Registered after reduction so per-result truncation runs first.
	handlers = append(handlers, agent.NewTurnToolResultBudgetMiddleware(0))

	// Env-drift/AGENTS.md refresh only makes sense against the local
	// executor: the middleware collects local state, so leave Pwd empty
	// (feature off) when this agent is rebuilt for a remote env.
	reminderPwd, reminderSnapshot := "", ""
	if !s.env.IsRemote() {
		reminderPwd = s.pwd
		reminderSnapshot = prompts.SerializeEnvInfo(s.platform, s.pwd, "local", s.envInfo)
	}
	reminderMw := agent.NewReminderMiddleware(agent.ReminderConfig{
		TodoStore:    s.env.TodoStore,
		GoalStore:    s.env.GoalStore,
		PlanStore:    s.planStore,
		EnvLabel:     "local",
		IsRemote:     s.env.IsRemote(),
		ContextLimit: contextLimit,
		FileTracker:  s.env.FileTracker,
		Env:          s.env,
		Pwd:          reminderPwd,
		Platform:     s.platform,
		EnvSnapshot:  reminderSnapshot,
	}, s.agentTokenUsage)
	handlers = append(handlers, reminderMw)

	// Wire up budget middleware (per-agent tracker).
	if s.cfg.Budget != nil {
		inputPer1M, outputPer1M := s.registry.GetModelCost(providerName, modelName)
		cacheReadPer1M, _ := s.registry.GetModelCacheCost(providerName, modelName)
		pricing := internalmodel.ModelPricing{InputPer1M: inputPer1M, OutputPer1M: outputPer1M, CacheReadPer1M: cacheReadPer1M}
		budgetManager := agent.NewBudgetManager(s.cfg.Budget, pricing)
		budgetMw := agent.NewBudgetMiddleware(budgetManager, s.agentTokenUsage, func(status agent.BudgetStatus) {
			config.Logger().Printf("[budget] warning level=%d cost=%.4f", status.WarningLevel, status.EstimatedCost)
		})
		handlers = append([]adk.ChatModelAgentMiddleware{budgetMw}, handlers...)
	}

	effectiveMCPTools := mcpTools
	if agentMode == tui.ModePlanning {
		effectiveMCPTools = nil
	} else if s.env.IsRemote() {
		generic, _, identifyErr := splitProviderSearchMCPTools(s.ctx, effectiveMCPTools)
		if identifyErr != nil {
			config.Logger().Printf("[provider-search] filter remote TUI catalog: %v", identifyErr)
		}
		effectiveMCPTools = generic
	}

	if !config.ToolSearchEnabled(s.cfg) {
		staticTools := toolList
		if agentMode != tui.ModePlanning {
			staticTools = append(append([]tool.BaseTool(nil), staticTools...), effectiveMCPTools...)
		}
		return agent.NewAgent(
			s.ctx, s.chatModel, staticTools, systemPrompt,
			s.approvalState.RequestApproval, middlewares, handlers,
		)
	}

	toolMode := agent.ToolModeNormal
	if agentMode == tui.ModePlanning {
		toolMode = agent.ToolModePlan
	}
	toolPlan, err := buildCommandToolPlan(
		s.ctx, toolList, effectiveMCPTools, agent.ToolTransportTUI, toolMode,
	)
	if err != nil {
		return nil, fmt.Errorf("build TUI tool plan: %w", err)
	}
	return agent.NewAgentWithToolPlan(
		s.ctx, s.chatModel, toolPlan, systemPrompt,
		s.approvalState.RequestApproval, middlewares, handlers,
	)
}

// mcpStatusItems converts internal MCP statuses to TUI status items.
func mcpStatusItems(internalStatuses []tools.MCPStatus) []tui.MCPStatusItem {
	items := make([]tui.MCPStatusItem, 0, len(internalStatuses))
	for _, st := range internalStatuses {
		errMsg := ""
		if st.Error != nil {
			errMsg = st.Error.Error()
		}
		items = append(items, tui.MCPStatusItem{
			Name: st.Name, ToolCount: st.ToolCount, Running: st.Running,
			ErrMsg: errMsg, NeedsAuth: st.NeedsAuth,
		})
	}
	return items
}

func (s *interactiveState) configureProviderMCPTools() error {
	configured, err := s.providerMCPToolsFor()
	s.mcpTools = configured
	return err
}

func (s *interactiveState) providerMCPToolsFor() ([]tool.BaseTool, error) {
	providerToolCfg := *s.cfg
	projectActiveChatModel(&providerToolCfg, s.activeProvider, s.activeModel)
	return configuredProviderMCPTools(
		s.ctx,
		&providerToolCfg,
		s.rec,
		s.providerSearchLedger,
		&providerSearchMCPCatalog{
			Tools:       append([]tool.BaseTool(nil), s.rawMCPTools...),
			ConfigEpoch: s.rawMCPConfigEpoch,
		},
		false,
		true,
		activeChatProviderRuntimeConfigLoader(
			projectProviderRuntimeConfigLoader(s.pwd), s.activeProvider, s.activeModel,
		),
	)
}

func (s *interactiveState) failClosedAgentRebuild(scope string, err error) {
	s.ag = nil
	config.Logger().Printf("[%s] fail-closed agent rebuild: %v", scope, err)
	if s.p == nil {
		return
	}
	s.p.Send(tui.AgentDoneMsg{Err: fmt.Errorf("%s safely: %w", scope, err)})
	if s.cancelFunc != nil {
		s.cancelFunc()
	}
	s.p.Quit()
}

// reloadMCP reloads MCP servers from the current config, rebuilds the agent
// tool set in place, and pushes a fresh status to the TUI. Used after an
// OAuth login or MCP config change so tools become usable without a restart.
func (s *interactiveState) reloadMCP() {
	s.mcpReloadMu.Lock()
	defer s.mcpReloadMu.Unlock()
	latest, err := config.LoadConfig()
	if err != nil {
		config.Logger().Printf("[mcp] reload: config load failed: %v", err)
		// Keep unrelated MCP tools, but immediately remove any previously
		// configured provider-search endpoint. Continuing to use the old agent
		// would retain a credential-bearing tool after its policy became
		// unreadable.
		generic, _, identifyErr := splitProviderSearchMCPTools(s.ctx, s.rawMCPTools)
		if identifyErr != nil {
			config.Logger().Printf("[mcp] reload: filter provider search tools: %v", identifyErr)
		}
		s.rawMCPTools = generic
		s.rawMCPConfigEpoch = ""
		s.mcpTools = generic
		if s.agentMode != tui.ModePlanning {
			s.toolList = s.buildTopLevelTools()
			if newAg, rebuildErr := s.createAgent(); rebuildErr == nil {
				s.ag = newAg
			} else {
				config.Logger().Printf("[mcp] reload: fail-closed agent rebuild failed: %v", rebuildErr)
				s.p.Send(tui.AgentDoneMsg{Err: fmt.Errorf("reload MCP tools safely: %w", rebuildErr)})
				if s.cancelFunc != nil {
					s.cancelFunc()
				}
				s.p.Quit()
			}
		}
		return
	}
	// Hot-reload: re-merge the managed deny-read rules from the fresh global
	// config. Union semantics — newly added rules apply immediately; removed
	// rules stay in force for this session (managed deny-read outranks later
	// relaxed settings).
	tools.InitManagedDenyRead(latest)
	config.ApplyProjectOverlay(latest, s.pwd)
	s.cfg = latest
	rawMCPTools, statuses := tools.LoadMCPTools(s.ctx, providertools.EffectiveMCPServers(latest))
	rawCatalog := newProviderSearchMCPCatalog(latest, rawMCPTools)
	s.rawMCPTools = rawCatalog.Tools
	s.rawMCPConfigEpoch = rawCatalog.ConfigEpoch
	if s.providerSearchLedger == nil {
		s.providerSearchLedger, err = newProviderSearchUsageLedger(s.rec)
		if err != nil {
			config.Logger().Printf("[mcp] reload: initialize provider search ledger: %v", err)
		}
	}
	wrapErr := s.configureProviderMCPTools()
	if wrapErr != nil {
		config.Logger().Printf("[mcp] reload: provider search unavailable: %v", wrapErr)
	}
	if s.agentMode != tui.ModePlanning {
		s.toolList = s.buildTopLevelTools()
		if newAg, err := s.createAgent(); err == nil {
			s.ag = newAg
		} else {
			config.Logger().Printf("[mcp] reload: agent rebuild failed: %v", err)
			s.p.Send(tui.AgentDoneMsg{Err: fmt.Errorf("reload MCP tools safely: %w", err)})
			if s.cancelFunc != nil {
				s.cancelFunc()
			}
			s.p.Quit()
		}
	}
	if s.p != nil {
		s.p.Send(tui.MCPStatusMsg{Statuses: mcpStatusItems(statuses)})
	}
}

// modeAfterToolSwitch returns the unified session mode that a tool-axis switch
// leaves the session in. Leaving the plan tool set — which is what an approved
// plan does on its way to execution — must also leave Plan, or applyModeSwitch
// would record and display a read-only mode while the agent already holds the
// full tool set. Approval is the safe landing spot, matching how resume
// normalizes a saved Plan. Every other mode survives the switch untouched.
func modeAfterToolSwitch(current mode.SessionMode, newMode tui.AgentMode) mode.SessionMode {
	if newMode != tui.ModePlanning && current == mode.Plan {
		return mode.Approval
	}
	return current
}

type preparedTUISessionMode struct {
	agentMode    tui.AgentMode
	systemPrompt string
	tools        []tool.BaseTool
	agent        *adk.ChatModelAgent
}

// prepareTUISessionMode completes every fallible part of an explicit mode
// switch against an unpublished snapshot. In particular, entering Plan builds
// the read-only prompt/tool agent before the authorization journal is touched.
func (s *interactiveState) prepareTUISessionMode(
	target mode.SessionMode,
) (preparedTUISessionMode, error) {
	prepared := preparedTUISessionMode{agentMode: tui.ModeNormal}
	envLabel := s.env.Exec.Label()
	if target.IsPlan() {
		prepared.agentMode = tui.ModePlanning
		prepared.systemPrompt = s.withTopLevelAgentPrompt(
			prompts.GetPlanSystemPrompt(s.platform, s.pwd, envLabel, s.envInfo),
		)
		prepared.tools = s.buildPlanTools()
	} else {
		prepared.systemPrompt = s.withTopLevelAgentPrompt(
			prompts.GetSystemPrompt(
				s.platform, s.pwd, envLabel, s.envInfo, s.skillLoader.Descriptions(),
			),
		)
		prepared.tools = s.buildAllTools()
	}
	candidate, err := s.createAgentWithModeCatalog(
		prepared.agentMode, prepared.systemPrompt, prepared.tools, s.mcpTools,
	)
	if err != nil {
		return preparedTUISessionMode{}, err
	}
	if candidate == nil {
		return preparedTUISessionMode{}, fmt.Errorf("candidate agent is unavailable")
	}
	prepared.agent = candidate
	return prepared, nil
}

// commitTUISessionMode is the TUI authorization transaction:
// prepare candidate -> fsync journal -> publish backend state. It deliberately
// never calls Program.Send: Approve All invokes it from BubbleTea Model.Update,
// where a synchronous send to the same unbuffered event loop would deadlock.
// The explicit selector caller acknowledges success after this method returns;
// Approve All updates its Model locally. On error both paths remain unchanged.
func (s *interactiveState) commitTUISessionMode(target mode.SessionMode) error {
	s.modeSwitchMu.Lock()
	defer s.modeSwitchMu.Unlock()

	if s.approvalState.GetSessionMode() == target {
		return nil
	}
	prepared, err := s.prepareTUISessionMode(target)
	if err != nil {
		return fmt.Errorf("prepare mode %s: %w", target.String(), err)
	}
	return s.persistAndPublishTUISessionMode(target, prepared)
}

func (s *interactiveState) persistAndPublishTUISessionMode(
	target mode.SessionMode,
	prepared preparedTUISessionMode,
) error {
	if s.rec == nil {
		return fmt.Errorf("session recorder is unavailable")
	}
	if err := s.rec.RecordModeChangeStrict(target.String()); err != nil {
		return fmt.Errorf("persist mode %s: %w", target.String(), err)
	}

	s.approvalState.SetSessionMode(target)
	s.agentMode = prepared.agentMode
	s.systemPrompt = prepared.systemPrompt
	s.toolList = prepared.tools
	s.ag = prepared.agent
	if s.agentTokenUsage != nil {
		s.agentTokenUsage.ResetContext()
	}
	return nil
}

func (s *interactiveState) applyModeSwitch(newMode tui.AgentMode) {
	s.agentMode = newMode
	config.Logger().Printf("[plan] mode switch to %d (0=normal, 1=plan)", newMode)

	// The unified session mode is the source of truth; it survives transient
	// approval-axis changes. Leaving the plan tool set is the one exception —
	// see modeAfterToolSwitch.
	currentMode := s.approvalState.GetSessionMode()
	if next := modeAfterToolSwitch(currentMode, newMode); next != currentMode {
		currentMode = next
		s.approvalState.SetSessionMode(currentMode)
	}

	if s.rec != nil {
		// Record the unified session mode so resume round-trips the selector.
		s.rec.RecordModeChange(currentMode.String())
	}

	s.refreshTopLevelPromptAndTools(s.env.Exec.Label(), s.envInfo)
	config.Logger().Printf("[plan] built tools: %d tools", len(s.toolList))
	if newAg, err := s.createAgent(); err == nil {
		s.ag = newAg
		config.Logger().Printf("[plan] agent recreated successfully")
	} else {
		s.failClosedAgentRebuild("switch session mode", err)
	}
	if s.agentTokenUsage != nil {
		s.agentTokenUsage.ResetContext()
	}
	// Sync the TUI mode pill with the resulting unified mode (covers the
	// plan-completion revert to Normal, which the user did not trigger directly).
	if s.p != nil {
		s.p.Send(tui.ModeSelectedMsg{Mode: currentMode})
	}
}

// applySessionMode is the explicit selector entry point. It intentionally does
// not optimistically mutate either axis; commitTUISessionMode publishes only
// after the candidate agent and durable journal both succeed.
func (s *interactiveState) applySessionMode(m mode.SessionMode) error {
	if err := s.commitTUISessionMode(m); err != nil {
		return err
	}
	// Explicit selector requests are consumed by the command event loop, not
	// BubbleTea Model.Update, so acknowledging via Program.Send cannot self-lock.
	if s.p != nil {
		s.p.Send(tui.ModeSelectedMsg{Mode: m})
	}
	return nil
}

func (s *interactiveState) drainModeSwitch(modeSelectCh <-chan mode.SessionMode) {
	for {
		select {
		case sm := <-modeSelectCh:
			if err := s.applySessionMode(sm); err != nil {
				config.Logger().Printf("[mode] TUI mode switch failed: %v", err)
				s.p.Send(tui.CommandNoticeMsg{Label: "Mode", Text: "unchanged (could not save mode change)"})
			}
		default:
			return
		}
	}
}

// recentTranscript snapshots the tail of the conversation for the approval
// reviewer. Called only during a turn, when st.history is not being mutated
// concurrently (handlePrompt blocks on runner.Run while approvals happen).
func (s *interactiveState) recentTranscript() []review.Msg {
	return review.MsgsFromHistory(s.history)
}

func (s *interactiveState) handlePrompt(userPrompt string) {
	s.agentRunning.Store(true)
	defer s.agentRunning.Store(false)

	// Create a per-run cancellable context so cancelling one run
	// does not prevent future runs.
	runCtx, runCancel := context.WithCancel(s.ctx)
	s.cancelFunc = runCancel
	s.runCtx = runCtx
	defer func() {
		runCancel()
		s.runCtx = nil
	}()

	if s.sessionResumeWarning != "" {
		userPrompt = s.sessionResumeWarning + "\n\n" + userPrompt
		s.sessionResumeWarning = ""
	}
	// UserPromptSubmit hook: may block this prompt or inject extra context. Fires
	// BEFORE anything is recorded so a denied prompt leaves the transcript and the
	// in-memory history consistent (neither contains it).
	if s.hookDisp != nil && s.hookDisp.Configured(hooks.UserPromptSubmit) {
		dec := s.hookDisp.Fire(runCtx, hooks.UserPromptSubmit, hooks.Payload{Prompt: userPrompt})
		if dec.Denied() {
			msg := "Your message was blocked by a hook policy."
			if dec.Reason != "" {
				msg += " Reason: " + dec.Reason
			}
			s.h.OnAgentText(msg + "\n")
			s.h.OnAgentDone(nil)
			return
		}
		if dec.AdditionalContext != "" {
			userPrompt = userPrompt + "\n\n" + dec.AdditionalContext
		}
	}
	// Prepend one-shot SessionStart context to the first prompt of the session.
	if s.hookStartContext != "" {
		userPrompt = s.hookStartContext + "\n\n" + userPrompt
		s.hookStartContext = ""
	}

	// Record after the hooks so transcript and history reflect the same final
	// prompt (and nothing is recorded when the prompt is denied above).
	if s.rec != nil {
		s.rec.RecordUser(userPrompt)
	}

	if s.agentTokenUsage == nil {
		s.agentTokenUsage = &internalmodel.TokenUsage{}
	}
	s.history = append(s.history, schema.UserMessage(userPrompt))
	s.history = agent.DrainBgNotifications(s.bgManager, s.history)
	s.approvalState.OnTurnStart() // reset the per-turn reviewer denial breaker
	result := runner.Run(runCtx, s.ag, s.history, s.h, s.rec, s.env.TodoStore, s.env.GoalStore, s.langfuseTracer, s.agentTokenUsage)
	if len(result.Messages) > 0 {
		s.history = append(s.history, result.Messages...)
	}
	s.history = agent.SyncSummarization(s.summCapture, s.history, s.rec)
	s.handlePlanCompletion(result)
}

func (s *interactiveState) handlePlanCompletion(planResult runner.RunResult) {
	if s.agentMode != tui.ModePlanning || planResult.Response == "" || planResult.Err != nil {
		return
	}
	resp := planResult.Response

	s.planStore.Submit("Plan", resp)
	config.Logger().Printf("[plan] plan submitted for review (%d chars)", len(resp))
	if s.rec != nil {
		s.rec.RecordPlanUpdate("submitted", "Plan", resp, "")
	}

	s.p.Send(tui.PlanApprovalMsg{PlanContent: resp, PlanPath: "Plan"})

	planRespCh := tui.GetPlanResponseChannel()
	planResp := <-planRespCh

	if !planResp.Approved {
		feedback := planResp.Feedback
		s.planStore.Reject(feedback)
		config.Logger().Printf("[plan] plan rejected: %s", feedback)
		if s.rec != nil {
			s.rec.RecordPlanUpdate("rejected", "", "", feedback)
		}

		revisePrompt := "Your plan was rejected."
		if feedback != "" {
			revisePrompt += " Feedback: " + feedback
		}
		revisePrompt += "\nPlease revise your plan based on this feedback."
		s.p.Send(tui.UserPromptMsg{Prompt: revisePrompt})
		if s.rec != nil {
			s.rec.RecordUser(revisePrompt)
		}
		s.history = append(s.history, schema.UserMessage(revisePrompt))
		result := runner.Run(s.runCtx, s.ag, s.history, s.h, s.rec, s.env.TodoStore, s.env.GoalStore, s.langfuseTracer, s.agentTokenUsage)
		if len(result.Messages) > 0 {
			s.history = append(s.history, result.Messages...)
		}
		s.history = agent.SyncSummarization(s.summCapture, s.history, s.rec)
		s.handlePlanCompletion(result)
		return
	}

	s.planStore.Approve()
	config.Logger().Printf("[plan] plan approved, transitioning to execution mode")
	if s.rec != nil {
		s.rec.RecordPlanUpdate("approved", s.planStore.Title(), s.planStore.Content(), "")
	}

	todos := tools.ExtractTodosFromPlan(s.planStore.Content())
	if len(todos) > 0 {
		s.env.TodoStore.Update(todos)
		s.p.Send(tui.TodoUpdateMsg{})
		config.Logger().Printf("[plan] populated %d todos from plan", len(todos))
	}

	s.applyModeSwitch(tui.ModeExecuting)

	execPrompt := "Your plan has been approved. Execute it step by step, tracking progress with the todo list. Mark each step complete as you finish it."
	s.p.Send(tui.UserPromptMsg{Prompt: execPrompt})
	if s.rec != nil {
		s.rec.RecordUser(execPrompt)
	}
	s.history = append(s.history, schema.UserMessage(execPrompt))
	result := runner.Run(s.ctx, s.ag, s.history, s.h, s.rec, s.env.TodoStore, s.env.GoalStore, s.langfuseTracer, s.agentTokenUsage)
	if len(result.Messages) > 0 {
		s.history = append(s.history, result.Messages...)
	}
	s.history = agent.SyncSummarization(s.summCapture, s.history, s.rec)

	if s.env.TodoStore.HasItems() && !s.env.TodoStore.HasIncomplete() {
		config.Logger().Printf("[plan] all todos complete, switching to normal mode")
		s.planStore.Clear()
		s.applyModeSwitch(tui.ModeNormal)
	}
}

func (s *interactiveState) attemptSSHResume(target string) string {
	if target == "local" || target == "" {
		return ""
	}
	var alias *config.SSHAlias
	for _, a := range s.cfg.SSHAliases {
		if a.Name == target {
			alias = &a
			break
		}
	}
	if alias == nil {
		return fmt.Sprintf("[System Note: The session was previously connected to SSH alias '%s', but it no longer exists in config. Environment dropped to 'local'.]", target)
	}

	authMethods := tools.BuildSSHAuthMethods()
	user := ""
	host := alias.Addr
	if idx := strings.Index(host, "@"); idx > 0 {
		user = host[:idx]
		host = host[idx+1:]
	}

	sshExec, err := tools.NewSSHExecutor(host, user, authMethods)
	if err != nil {
		return fmt.Sprintf("[System Note: The session attempted to reconnect to SSH alias '%s' (%s) but failed: %v. Environment dropped to 'local'.]", target, alias.Addr, err)
	}

	s.env.SetSSH(sshExec, s.env.Pwd())
	label := sshExec.Label()
	if s.env.OnEnvChange != nil {
		s.env.OnEnvChange(label, false, nil)
	}
	return ""
}

func (s *interactiveState) handleResume(uuid string) {
	entries, loadErr := session.LoadSession(uuid)
	if loadErr != nil {
		s.p.Send(tui.AgentDoneMsg{Err: fmt.Errorf("load session: %w", loadErr)})
		return
	}
	st := session.ReconstructState(entries)
	s.history = session.PruneOldToolOutputs(st.History, 2)
	// Authorization restore is intentionally independent from the tolerant
	// conversational replay above. A malformed line could be a newer Approval
	// revoke, so the strict reader fails closed instead of reviving Full access.
	restoredMode := restoredSessionMode(uuid, "tui-resume")
	s.approvalState.SetSessionMode(restoredMode)
	s.agentMode = tui.ModeNormal
	s.rec.SetUUID(uuid)
	if ledgerErr := resetImageUsageLedger(s.imageLedger, s.rec); ledgerErr != nil {
		config.Logger().Printf("[image] switch TUI usage ledger: %v", ledgerErr)
		s.imageLedger = nil
	}
	if s.providerSearchLedger == nil {
		s.providerSearchLedger, loadErr = newProviderSearchUsageLedger(s.rec)
	} else {
		loadErr = resetProviderSearchUsageLedger(s.providerSearchLedger, s.rec)
	}
	if loadErr != nil {
		config.Logger().Printf("[provider-search] switch TUI usage ledger: %v", loadErr)
		s.providerSearchLedger = nil
	}
	if configureErr := s.configureProviderMCPTools(); configureErr != nil {
		config.Logger().Printf("[provider-search] switch TUI MCP catalog: %v", configureErr)
	}
	s.p.Send(tui.SessionResumedMsg{UUID: uuid, Entries: tui.ConvertSessionEntries(entries)})
	// Sync the mode pill with the restored mode (SessionResumedMsg resets the
	// pill to Approval; this overrides it with what the session was actually saved in).
	s.p.Send(tui.ModeSelectedMsg{Mode: restoredMode})

	// Restore stored system prompt for KV-cache-friendly resume.
	if st.SystemPrompt != "" {
		s.systemPrompt = st.SystemPrompt
		envDiff := prompts.BuildEnvDiff(st.EnvInfo, s.platform, s.pwd, s.env.Exec.Label(), s.envInfo)
		if envDiff != "" {
			s.history = append(s.history, &schema.Message{
				Role:    schema.System,
				Content: envDiff,
			})
		}
	}

	if st.Plan != nil {
		switch st.Plan.Status {
		case "approved":
			s.planStore.Submit(st.Plan.Title, st.Plan.Content)
			s.planStore.Approve()
		case "submitted":
			s.planStore.Submit(st.Plan.Title, st.Plan.Content)
		case "rejected":
			s.planStore.SetDraft(st.Plan.Title, st.Plan.Content)
		}
	}

	if len(st.Todos) > 0 {
		todoItems := make([]tools.TodoItem, len(st.Todos))
		for i, t := range st.Todos {
			todoItems[i] = tools.TodoItem{
				ID: t.ID, Title: t.Title, Status: tools.TodoStatus(t.Status),
			}
		}
		s.env.TodoStore.Update(todoItems)
		s.p.Send(tui.TodoUpdateMsg{})
	}

	// Restore the resumed session's goal — or reset the store when it has
	// none, so a goal from the previously open session does not leak across.
	s.env.GoalStore.RestoreFromSnapshot(st.Goal)

	if targetEnv := st.EnvTarget; targetEnv != "local" {
		s.sessionResumeWarning = s.attemptSSHResume(targetEnv)
	}
	s.toolList = s.buildTopLevelTools()
	if newAgent, agentErr := s.createAgent(); agentErr == nil {
		s.ag = newAgent
	} else {
		config.Logger().Printf("[session] fail-closed agent rebuild after session switch: %v", agentErr)
		s.p.Send(tui.AgentDoneMsg{Err: fmt.Errorf("resume session safely: %w", agentErr)})
		if s.cancelFunc != nil {
			s.cancelFunc()
		}
		s.p.Quit()
	}
}

func (s *interactiveState) handleConfig(cfgMsg *config.Config) {
	// Update stored config
	s.cfg = cfgMsg
	newProvName, newModelName := cfgMsg.GetProviderModel()
	s.activeProvider, s.activeModel = newProvName, newModelName
	if configureErr := s.configureProviderMCPTools(); configureErr != nil {
		// The raw provider endpoint is tied to the epoch captured when its MCP
		// transport connected. A config/key/policy change strips it until an
		// explicit MCP reload reconnects with the new epoch.
		config.Logger().Printf("[provider-search] config rebuild filtered MCP catalog: %v", configureErr)
	}

	newProviders := cfgMsg.GetProviders()
	newProvCfg := newProviders[newProvName]
	if newProvCfg == nil {
		s.failClosedAgentRebuild("apply config", fmt.Errorf("provider %q is unavailable", newProvName))
		return
	}

	// Refresh registry so new custom models / providers are available.
	s.registry = internalmodel.NewModelRegistryWithConfig(cfgMsg)

	newBaseURL := newProvCfg.BaseURL
	if newBaseURL == "" {
		newBaseURL = s.registry.GetProviderAPI(newProvName)
	}
	// Apply a per-model reasoning-effort override (set from the chat picker)
	// over the provider-level default before constructing the model.
	newEffortCfg := *newProvCfg
	newEffortCfg.ReasoningEffort = config.ResolveEffort(newProvName, newModelName, newProvCfg.ReasoningEffort)
	newChatModel, err := internalmodel.NewChatModelFromProvider(s.ctx, newProvName, newModelName, newBaseURL, &newEffortCfg)
	if err != nil {
		s.failClosedAgentRebuild("apply config", err)
		return
	}
	s.chatModel = newChatModel
	// Attribute subsequent usage to the newly selected model.
	if s.rec != nil {
		s.rec.SetProviderModel(newProvName, newModelName)
	}

	// Rebuild system prompt and tools to reflect config changes (e.g., SSH aliases)
	s.refreshTopLevelPromptAndTools(s.env.Exec.Label(), s.envInfo)

	if newAg, err := s.createAgent(); err == nil {
		s.ag = newAg
	} else {
		s.failClosedAgentRebuild("apply config", err)
	}
}

func (s *interactiveState) handleCompact() {
	var oldTokens int64
	if s.agentTokenUsage != nil {
		_, _, oldTokens = s.agentTokenUsage.Get()
	}
	oldLen := len(s.history)
	compactCtx := s.ctx
	if s.rec != nil && s.rec.UUID() != "" {
		compactCtx = internalmodel.WithProviderSessionID(compactCtx, s.rec.UUID())
	}
	s.history = agent.CompactHistory(compactCtx, s.chatModel, s.history)
	var newTokens int64
	if s.agentTokenUsage != nil {
		_, _, newTokens = s.agentTokenUsage.Get()
	}
	if s.rec != nil && len(s.history) < oldLen && len(s.history) > 0 {
		// history[0] is the summary system message; everything after it is the
		// tail kept verbatim, which resume re-attaches via KeptN.
		s.rec.RecordCompact(s.history[0].Content, oldLen-len(s.history), len(s.history)-1)
	}
	if s.agentTokenUsage != nil {
		s.agentTokenUsage.ResetContext()
	}
	s.p.Send(tui.CompactDoneMsg{
		OldTokens: oldTokens,
		NewTokens: newTokens,
	})
}

func (s *interactiveState) handleAddModel() {
	_ = s.p.ReleaseTerminal()
	ok, setupErr := tui.RunSetupTUI()
	_ = s.p.RestoreTerminal()
	if setupErr != nil {
		s.p.Send(tui.AgentDoneMsg{Err: fmt.Errorf("setup error: %w", setupErr)})
		return
	}
	if !ok {
		return
	}
	newCfg, loadErr := config.LoadConfig()
	if loadErr != nil {
		return
	}
	newProvName, newModelName := newCfg.GetProviderModel()
	newProviders := newCfg.GetProviders()
	newProvCfg := newProviders[newProvName]
	if newProvCfg == nil {
		s.failClosedAgentRebuild("add model", fmt.Errorf("provider %q is unavailable", newProvName))
		return
	}
	// Refresh registry so new custom models / providers are available.
	s.cfg = newCfg
	if configureErr := s.configureProviderMCPTools(); configureErr != nil {
		config.Logger().Printf("[provider-search] add-model rebuild filtered MCP catalog: %v", configureErr)
	}
	s.registry = internalmodel.NewModelRegistryWithConfig(newCfg)

	newBaseURL := newProvCfg.BaseURL
	if newBaseURL == "" {
		newBaseURL = s.registry.GetProviderAPI(newProvName)
	}
	newEffortCfg2 := *newProvCfg
	newEffortCfg2.ReasoningEffort = config.ResolveEffort(newProvName, newModelName, newProvCfg.ReasoningEffort)
	newChatModel, cmErr := internalmodel.NewChatModelFromProvider(s.ctx, newProvName, newModelName, newBaseURL, &newEffortCfg2)
	if cmErr != nil {
		s.failClosedAgentRebuild("add model", cmErr)
		return
	}
	s.chatModel = newChatModel
	if s.rec != nil {
		s.rec.SetProviderModel(newProvName, newModelName)
	}
	if newAg, agErr := s.createAgent(); agErr == nil {
		s.ag = newAg
	} else {
		s.failClosedAgentRebuild("add model", agErr)
		return
	}
	s.p.Send(tui.ConfigUpdatedMsg{
		Provider: newProvName,
		Model:    newModelName,
		Message:  fmt.Sprintf("✅ Added model: %s/%s\n", newProvName, newModelName),
	})
}

func (s *interactiveState) handleSSH(connMsg interface{}) {
	switch msg := connMsg.(type) {
	case tui.SSHConnectMsg:
		HandleSSHConnect(s.ctx, s.env, msg.Addr, msg.Path, s.p, &s.systemPrompt,
			&s.ag, s.chatModel, s.createAgent, s.skillLoader.Descriptions(),
			msg.AcceptHostKey, msg.HostKeyFingerprint)
	case tui.SSHListDirReqMsg:
		HandleSSHListDir(s.ctx, s.env, msg.Path, s.p)
	case tui.SSHCancelMsg:
		s.env.ResetToLocal(s.pwd, s.platform)
		s.refreshTopLevelPromptAndTools("local", s.envInfo)
		if newAg, err := s.createAgent(); err == nil {
			s.ag = newAg
		} else {
			s.failClosedAgentRebuild("leave remote environment", err)
		}
	}
}

// runEventLoop is the main goroutine that processes TUI events and drives
// the agent loop. It is started from RunInteractive after TUI setup.
func (s *interactiveState) runEventLoop(initialHistory []adk.Message, initialResumeUUID string,
	initialResumeEntries []tui.SessionEntry, hasPrompt bool, prompt string, mcpStatuses []tui.MCPStatusItem) {
	defer func() {
		if s.rec != nil {
			s.rec.Close()
		}
		if s.langfuseTracer != nil {
			s.langfuseTracer.Flush()
		}
	}()

	s.p.Send(team.SetTeamManagerMsg{Manager: s.teamManager})

	if len(mcpStatuses) > 0 {
		s.p.Send(tui.MCPStatusMsg{Statuses: mcpStatuses})
	}
	if agentsMdPath := prompts.HasAgentsMd(s.pwd); agentsMdPath != "" {
		s.p.Send(tui.AgentsMdMsg{Found: true, Path: agentsMdPath})
	}

	if slashSkills := s.skillLoader.SlashCommands(); len(slashSkills) > 0 {
		var slashInfos []tui.SkillSlashInfo
		for _, sk := range slashSkills {
			slashInfos = append(slashInfos, tui.SkillSlashInfo{
				Slash:       sk.Slash,
				Description: sk.Description,
			})
		}
		s.p.Send(tui.SkillsLoadedMsg{SlashCommands: slashInfos})
	}

	if s.flowLoader != nil {
		if slashFlows := s.flowLoader.SlashCommands(); len(slashFlows) > 0 {
			var flowInfos []tui.FlowSlashInfo
			for _, fc := range slashFlows {
				flowInfos = append(flowInfos, tui.FlowSlashInfo{
					Slash:       fc.Slash,
					Description: fc.Description,
				})
			}
			s.p.Send(tui.FlowsLoadedMsg{SlashCommands: flowInfos})
		}
	}

	s.history = initialHistory
	if initialResumeUUID != "" {
		s.p.Send(tui.SessionResumedMsg{UUID: initialResumeUUID, Entries: initialResumeEntries})
	}

	if hasPrompt {
		s.p.Send(tui.UserPromptMsg{Prompt: prompt})
		if s.rec != nil {
			s.rec.RecordUser(prompt)
		}
		runCtx, runCancel := context.WithCancel(s.ctx)
		s.cancelFunc = runCancel
		s.runCtx = runCtx
		s.agentRunning.Store(true)
		s.history = append(s.history, schema.UserMessage(prompt))
		result := runner.Run(runCtx, s.ag, s.history, s.h, s.rec, s.env.TodoStore, s.env.GoalStore, s.langfuseTracer, s.agentTokenUsage)
		runCancel()
		s.runCtx = nil
		s.agentRunning.Store(false)
		if len(result.Messages) > 0 {
			s.history = append(s.history, result.Messages...)
		}
		s.history = agent.SyncSummarization(s.summCapture, s.history, s.rec)
	}

	promptCh := tui.GetPromptChannel()
	pendingPromptCh := tui.GetPendingPromptChannel()
	sshCh := tui.GetSSHChannel()
	configCh := tui.GetConfigChannel()
	addModelCh := tui.GetAddModelChannel()
	resumeCh := tui.GetResumeChannel()
	compactCh := tui.GetCompactChannel()
	modeSelectCh := tui.GetModeSelectChannel()
	channelActionCh := tui.GetChannelActionChannel()
	mcpLoginCh := tui.GetMCPLoginChannel()
	cancelAgentCh := tui.GetCancelAgentChannel()

	// Background goroutine to handle agent cancellation requests.
	// This is necessary because the main event loop blocks on handlePrompt/runner.Run,
	// so the cancel channel must be consumed independently.
	go func() {
		for range cancelAgentCh {
			if s.cancelFunc != nil {
				config.Logger().Printf("[interactive] cancelling agent job via Ctrl+C")
				s.cancelFunc()
			}
		}
	}()

	// Send initial WeChat state to TUI
	s.p.Send(tui.ChannelStateMsg{
		ChannelID: "wechat",
		State:     s.wechatClient.State().String(),
	})

	for {
		select {
		case sm := <-modeSelectCh:
			if err := s.applySessionMode(sm); err != nil {
				config.Logger().Printf("[mode] TUI mode switch failed: %v", err)
				s.p.Send(tui.CommandNoticeMsg{Label: "Mode", Text: "unchanged (could not save mode change)"})
			}

		case cfgMsg := <-configCh:
			s.handleConfig(cfgMsg)

		case userPrompt := <-promptCh:
			s.drainModeSwitch(modeSelectCh)
			s.handlePrompt(userPrompt)

		case pendingPrompt := <-pendingPromptCh:
			s.drainModeSwitch(modeSelectCh)
			s.p.Send(tui.UserPromptMsg{Prompt: pendingPrompt})
			s.handlePrompt(pendingPrompt)

		case uuid := <-resumeCh:
			s.handleResume(uuid)

		case connMsg := <-sshCh:
			s.handleSSH(connMsg)

		case <-compactCh:
			s.handleCompact()

		case <-addModelCh:
			s.handleAddModel()

		case action := <-channelActionCh:
			s.handleChannelAction(action)

		case name := <-mcpLoginCh:
			s.handleMCPLogin(name)

		}
	}
}

// handleMCPLogin runs an OAuth login for the named server in the background so
// the event loop is not blocked while the user completes the browser flow. On
// success it persists config and hot-reloads MCP tools.
func (s *interactiveState) handleMCPLogin(name string) {
	srv := s.cfg.MCPServers[name]
	if srv == nil {
		s.p.Send(tui.MCPNoticeMsg{Text: "server not found: " + name})
		return
	}
	if srv.URL == "" || (srv.Type != "http" && srv.Type != "sse") {
		s.p.Send(tui.MCPNoticeMsg{Text: "OAuth login only applies to http/sse servers"})
		return
	}
	if srv.OAuth == nil {
		srv.OAuth = &config.MCPOAuthConfig{Enabled: true}
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
		defer cancel()
		err := tools.PerformMCPOAuthLogin(ctx, name, srv, func(authURL string) {
			s.p.Send(tui.MCPNoticeMsg{Text: "opening browser…"})
			if err := util.OpenURL(authURL); err != nil {
				config.Logger().Printf("[mcp] open browser: %v", err)
			}
		})
		if err != nil {
			s.p.Send(tui.MCPNoticeMsg{Text: "login failed: " + err.Error()})
			return
		}
		if err := config.SaveConfig(s.cfg); err != nil {
			config.Logger().Printf("[mcp] save config after login: %v", err)
		}
		s.p.Send(tui.MCPNoticeMsg{Text: name + " authorized — reloading tools"})
		s.reloadMCP()
	}()
}

// RunInteractive starts the interactive TUI session.
// The unsafe flag enables auto-approve for all tool calls and takes precedence over config.
func RunInteractive(prompt, resumeUUID, agentName string, unsafe bool) error {
	prompt = strings.TrimSpace(prompt)
	hasPrompt := prompt != ""
	agentFlagSet := strings.TrimSpace(agentName) != ""

	// Redirect default log output to the app error log so library diagnostics
	// (e.g. Langfuse upload errors) are visible without corrupting the TUI.
	log.SetOutput(config.Logger().Writer())

	// Setup wizard if config is missing.
	if config.NeedsSetup() {
		ok, err := tui.RunSetupTUI()
		if err != nil {
			return fmt.Errorf("setup error: %w", err)
		}
		if !ok {
			return nil
		}
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("config error: %v\nconfig file: %s", err, config.ConfigPath())
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()
	pwd := util.GetWorkDir()
	platform := util.GetSystemInfo()
	envInfo := util.CollectEnvInfo(pwd)

	// Seed the shared managed deny-read policy from the user-owned global
	// config before any project overlay — deny rules are never project-
	// mergeable, and runtime reloads only strengthen the live policy.
	tools.InitManagedDenyRead(cfg)

	// Apply project-level config overlay (walk-up .jcode/config.json + mcp.json).
	config.ApplyProjectOverlay(cfg, pwd)

	var resumeEntries []session.Entry
	var resumeState *session.SessionState
	if resumeUUID != "" {
		resumeEntries, err = session.LoadSession(resumeUUID)
		if err != nil {
			return fmt.Errorf("cannot load session: %w", err)
		}
		resumeState = session.ReconstructState(resumeEntries)
	}

	skillLoader := skills.NewLoaderWithDisabled(cfg.DisabledSkills)
	skillLoader.ScanProjectSkills(pwd)

	flowLoader := flow.NewLoader()
	flowLoader.LoadProject(pwd)

	// Memory distillation runs in the background on session start (design
	// §5.1); one-shot -p runs are excluded, gates (cooldown/budget/lock) are
	// inside the pipeline.
	if !hasPrompt {
		mempipeline.MaybeStartBackground(cfg, pwd)
	}

	systemPrompt := prompts.GetSystemPrompt(platform, pwd, "local", envInfo, skillLoader.Descriptions())

	providerName, modelName := cfg.GetProviderModel()
	selectedAgentName := strings.TrimSpace(agentName)
	if strings.EqualFold(selectedAgentName, "default") {
		selectedAgentName = ""
	}
	if !agentFlagSet && resumeState != nil {
		selectedAgentName = strings.TrimSpace(resumeState.Agent)
	}
	var resumeAgentWarning string
	if selectedAgentName != "" {
		role, ok := config.LoadAgentRoles(pwd)[selectedAgentName]
		if !ok {
			if agentFlagSet {
				return fmt.Errorf("unknown custom agent %q", selectedAgentName)
			}
			resumeAgentWarning = fmt.Sprintf(
				"Custom agent %q is no longer available; resumed with Default.", selectedAgentName,
			)
			selectedAgentName = ""
		} else {
			providerName, modelName, err = resolveCustomAgentModel(
				role, cfg, providerName, modelName,
			)
			if err != nil {
				return fmt.Errorf("custom agent %q: %w", selectedAgentName, err)
			}
		}
	}

	providers := cfg.GetProviders()
	providerCfg := providers[providerName]
	if providerCfg == nil {
		return fmt.Errorf("provider %q not found in config", providerName)
	}

	registry := internalmodel.NewModelRegistryWithConfig(cfg)
	baseURL := providerCfg.BaseURL
	if baseURL == "" {
		baseURL = registry.GetProviderAPI(providerName)
	}

	effortCfg := *providerCfg
	effortCfg.ReasoningEffort = config.ResolveEffort(providerName, modelName, providerCfg.ReasoningEffort)
	chatModel, err := internalmodel.NewChatModelFromProvider(ctx, providerName, modelName, baseURL, &effortCfg)
	if err != nil {
		return fmt.Errorf("error creating model: %w", err)
	}

	env := tools.NewEnv(pwd, platform)
	bgManager := tools.NewBackgroundManager(env)

	// Browser-use manager (managed Chrome backend; the extension backend needs a
	// server and is unavailable in the pure TUI). Shared with this session's env
	// so the browser_* tools work in the terminal.
	browserMgr := browser.NewManager(browser.FromConfig(cfg.Browser))
	env.Browser = browserMgr
	defer func() { _ = browserMgr.Close() }()

	// Computer-use manager (native desktop app control). Off unless config
	// enables it — unlike browser-use, this can reach anything on the machine.
	computerMgr := newComputerManager(cfg, "")
	env.Computer = computerMgr
	if computerMgr != nil {
		defer func() { _ = computerMgr.Close() }()
	}

	var rawMCPTools []tool.BaseTool
	var mcpStatuses []tui.MCPStatusItem
	effectiveMCPServers := providertools.EffectiveMCPServers(cfg)
	if len(effectiveMCPServers) > 0 {
		var internalStatuses []tools.MCPStatus
		rawMCPTools, internalStatuses = tools.LoadMCPTools(ctx, effectiveMCPServers)
		mcpStatuses = mcpStatusItems(internalStatuses)
	}
	rawMCPCatalog := newProviderSearchMCPCatalog(cfg, rawMCPTools)

	planStore := tools.NewPlanStore()

	rec, _ := session.NewRecorder(pwd, providerName, modelName)
	if resumeUUID != "" && rec != nil {
		rec.SetUUID(resumeUUID)
	}
	if rec != nil {
		env.SessionIDFn = rec.UUID
	}
	imageLedger, imageLedgerErr := newImageUsageLedger(rec)
	if imageLedgerErr != nil {
		config.Logger().Printf("[image] initialize TUI usage ledger: %v", imageLedgerErr)
	}
	providerSearchLedger, providerSearchLedgerErr := newProviderSearchUsageLedger(rec)
	if providerSearchLedgerErr != nil {
		config.Logger().Printf("[provider-search] initialize TUI usage ledger: %v", providerSearchLedgerErr)
	}
	providerToolCfg := *cfg
	projectActiveChatModel(&providerToolCfg, providerName, modelName)
	mcpTools, providerSearchWrapErr := configuredProviderMCPTools(
		ctx, &providerToolCfg, rec, providerSearchLedger,
		rawMCPCatalog,
		false, true, activeChatProviderRuntimeConfigLoader(
			projectProviderRuntimeConfigLoader(pwd), providerName, modelName,
		),
	)
	if providerSearchWrapErr != nil {
		config.Logger().Printf("[provider-search] initialize TUI MCP tools: %v", providerSearchWrapErr)
	}
	// LLM session titles ride the small model (checked at fire time).
	attachTitleRefiner(ctx, rec)

	// Build the transport-agnostic hook dispatcher and inject it into the context
	// so the tool hook middleware and the runner's continuation loop reach it
	// without signature changes. Project-level hooks are untrusted and load only
	// under JCODE_HOOKS_TRUST_PROJECT=1 (see hooks.NewSessionDispatcher).
	hookDisp := hooks.NewSessionDispatcher(config.ConfigDir(), pwd, rec.UUID(), config.Logger().Printf)
	ctx = hooks.WithDispatcher(ctx, hookDisp)

	env.TodoStore.OnUpdate = func(items []tools.TodoItem) {
		if rec != nil {
			snapItems := make([]session.TodoSnapshotItem, len(items))
			for i, it := range items {
				snapItems[i] = session.TodoSnapshotItem{
					ID: it.ID, Title: it.Title, Status: string(it.Status),
				}
			}
			rec.RecordTodoSnapshot(snapItems)
		}
	}

	env.GoalStore.OnUpdate = tools.GoalRecorderHook(rec)

	askUserCh := make(chan tools.AskUserResponse, 1)
	askUserDeps := &tools.AskUserDeps{
		ResponseCh: askUserCh,
	}

	st := &interactiveState{
		ctx:                  ctx,
		cancelFunc:           cancelFunc,
		cfg:                  cfg,
		chatModel:            chatModel,
		env:                  env,
		bgManager:            bgManager,
		planStore:            planStore,
		summCapture:          &agent.SummarizationCapture{},
		systemPrompt:         systemPrompt,
		agentMode:            tui.ModeNormal,
		envInfo:              envInfo,
		pwd:                  pwd,
		platform:             platform,
		registry:             registry,
		skillLoader:          skillLoader,
		flowLoader:           flowLoader,
		askUserDeps:          askUserDeps,
		rawMCPTools:          rawMCPCatalog.Tools,
		rawMCPConfigEpoch:    rawMCPCatalog.ConfigEpoch,
		mcpTools:             mcpTools,
		rec:                  rec,
		artifactService:      artifact.NewService(session.LoadArtifactRecords, time.Now),
		imageLedger:          imageLedger,
		providerSearchLedger: providerSearchLedger,
		activeProvider:       providerName,
		activeModel:          modelName,
		hookDisp:             hookDisp,
	}
	if err := st.setTopLevelAgent(selectedAgentName); err != nil {
		return err
	}
	st.sessionResumeWarning = resumeAgentWarning
	st.systemPrompt = st.withTopLevelAgentPrompt(systemPrompt)
	if rec != nil {
		rec.SetAgent(st.agentRoleName)
	}

	// SessionStart hook: fire once for a fresh session; stash any additionalContext
	// to prepend to the first prompt. A resumed session fires it later — after the
	// recorder UUID is restored — to avoid a double-fire and a wrong session_id.
	// Non-blocking.
	if resumeUUID == "" && hookDisp.Configured(hooks.SessionStart) {
		dec := hookDisp.Fire(ctx, hooks.SessionStart, hooks.Payload{})
		st.hookStartContext = dec.AdditionalContext
	}

	// Initialize WeChat channel
	st.wechatClient = weixin.NewClient()

	// Auto-enable WeChat if credentials exist
	if st.wechatClient.State() == channel.StateDisabled {
		st.wechatClient.SetOnMessage(func(from, text string) {
			if st.p != nil {
				// Notify user if agent is busy
				if st.agentRunning.Load() {
					_ = st.wechatClient.SendText(channel.BusyMessage())
				}
				st.p.Send(tui.ChannelInboundMsg{
					ChannelID: "wechat",
					From:      from,
					Text:      text,
				})
			}
		})
		if err := st.wechatClient.Enable(); err != nil {
			config.Logger().Printf("[wechat] auto-enable failed: %v", err)
		} else {
			config.Logger().Printf("[wechat] auto-enabled on startup")
			go func() {
				if err := st.wechatClient.SendText(channel.WelcomeMessage(time.Now())); err != nil {
					config.Logger().Printf("[wechat] failed to send welcome: %v", err)
				}
			}()
		}
	}

	// Publish the developer logging toggle (Settings → Developer) before the
	// first logger / tracer is built. Tracing also respects the developer
	// toggle; both take effect on this startup only.
	config.SetLoggingEnabled(config.LoggingEnabled(cfg))
	if config.TracingEnabled(cfg) && cfg.Telemetry != nil && cfg.Telemetry.Langfuse != nil {
		st.langfuseTracer = telemetry.NewLangfuseTracer(cfg.Telemetry.Langfuse)
	}

	// teamModelFactory serves per-teammate model overrides through the same
	// resolution path as subagents/workflows; the fallback (startup model) is
	// only reached when the "small" alias is unset/invalid.
	teamModelFactory := internalmodel.NewModelFactory(cfg, chatModel)
	teamAgentRoles := config.LoadAgentRoles(pwd)
	teamManager := team.NewManager(&team.ManagerDeps{
		DefaultModel: chatModel,
		EnvFactory: func(cwd string) any {
			return tools.NewEnv(cwd, platform)
		},
		ToolBuilder: buildTeamChildTools,
		// Route through the shared factory so teammates get the same model
		// semantics as subagents/workflows — incl. the "small" alias, baseURL
		// and effort resolution, and instance caching.
		ModelFactory: func(mCtx context.Context, mName string) (any, error) {
			return teamModelFactory.GetModel(mCtx, mName)
		},
		// Usage attribution: resolve the alias and strip the provider prefix so
		// team events land in the same stat bucket as every other writer.
		ResolveModelName: func(mName string) string {
			return internalmodel.BareModelID(teamModelFactory.ResolveRef(mName))
		},
		PromptBuilder: func(agentType, permission, agentPwd, _ string) string {
			return buildTeamChildPrompt(agentType, permission, platform, agentPwd)
		},
		LeaderSessionUUID: rec.UUID(),
		Tracer:            st.langfuseTracer,
		AgentRoles:        teamAgentRoles,
	})
	st.teamManager = teamManager
	st.toolList = st.buildTopLevelTools()

	// Resolve a fresh task's startup mode from CLI/config. A resumed task then
	// replaces it with its own strictly replayed authorization state.
	startupMode := resolveStartupMode(cfg, unsafe)
	if resumeUUID != "" {
		// A resumed task owns its authorization state. Do not inherit a global
		// Full access default (or --unsafe) when its strict journal says Approval;
		// an ambiguous/corrupt journal also fails closed to Approval.
		startupMode = restoredSessionMode(resumeUUID, "tui-startup-resume")
	}
	if startupMode.IsPlan() {
		st.agentMode = tui.ModePlanning
		st.systemPrompt = st.withTopLevelAgentPrompt(
			prompts.GetPlanSystemPrompt(platform, pwd, "local", envInfo),
		)
		st.toolList = st.buildPlanTools()
	}
	approvalState := runner.NewApprovalStateWithMode(pwd, startupMode)
	approvalState.SetBrowserPermFunc(func(origin, class string) bool {
		return browserSitePreapproved(cfg, origin, class)
	})
	approvalState.SetBrowserOriginFunc(env.CurrentBrowserOrigin)
	approvalState.SetComputerPermFunc(func(bundleID, class string) bool {
		return computerMgr != nil && computerMgr.Preapproved(bundleID, class)
	})
	approvalState.SetComputerAppFunc(env.CurrentComputerApp)
	st.approvalState = approvalState

	// Provide the config/platform needed to lazily build the LLM reviewer when
	// the session enters Auto mode. The transcript provider reads st.history,
	// which is only mutated between turns on the goroutine that blocks on
	// runner.Run — so reading it during an approval (which happens inside Run) is
	// race-free.
	approvalState.SetReviewerConfig(cfg, util.GetSystemInfo())
	approvalState.SetTranscriptFunc(st.recentTranscript)

	// Wire the `/browser` command to the browser-use subsystem.
	browserCtl := &tui.BrowserController{
		Status: func() tui.BrowserStatus {
			s := browserMgr.Status(context.Background())
			info := s.ChromeVersion
			if info == "" {
				info = s.ChromePath
			}
			return tui.BrowserStatus{
				Available:       true,
				Enabled:         s.Enabled,
				Backend:         s.Backend,
				ChromeFound:     s.ChromeFound,
				ChromeInfo:      info,
				ExtensionOnline: s.ExtensionOnline,
				DevMode:         s.DevMode,
			}
		},
		SetEnabled: func(enable bool) error {
			created := false
			if cfg.Browser == nil {
				cfg.Browser = &config.BrowserConfig{Backend: "auto"}
				created = true
			}
			previousEnabled := cfg.Browser.Enabled
			cfg.Browser.Enabled = enable
			if err := config.SaveConfig(cfg); err != nil {
				cfg.Browser.Enabled = previousEnabled
				if created {
					cfg.Browser = nil
				}
				return err
			}
			browserMgr.SetConfig(browser.FromConfig(cfg.Browser))
			// Tool schemas are fixed on an agent instance. Rebuild immediately so
			// /browser on|off changes the current task's model-visible tools.
			st.toolList = st.buildTopLevelTools()
			newAg, err := st.createAgent()
			if err != nil {
				return fmt.Errorf("saved setting but could not refresh agent tools: %w", err)
			}
			st.ag = newAg
			return nil
		},
	}

	computerCtl := &tui.ComputerController{
		Status: func() tui.ComputerStatus {
			if computerMgr == nil {
				return tui.ComputerStatus{
					Supported: false,
					Platform:  platform,
					Blocker:   "unsupported",
					Detail:    computer.UnsupportedReason(),
				}
			}
			s := computerMgr.Status(context.Background())
			return tui.ComputerStatus{
				Supported:       true,
				Platform:        platform,
				Available:       s.Available,
				Enabled:         s.Enabled,
				HelperInstalled: s.Helper.Installed,
				HelperConnected: s.Helper.Connected,
				HelperVersion:   s.Helper.Version,
				Accessibility:   string(s.AccessibilityPermission),
				ScreenRecording: string(s.ScreenRecordingPermission),
				Blocker:         s.Blocker,
				Detail:          s.Detail,
			}
		},
		SetEnabled: func(enable bool) error {
			if computerMgr == nil {
				return fmt.Errorf("%s", computer.UnsupportedReason())
			}
			created := false
			if cfg.Computer == nil {
				cfg.Computer = &config.ComputerConfig{}
				created = true
			}
			previousEnabled := cfg.Computer.Enabled
			cfg.Computer.Enabled = enable
			if err := config.SaveConfig(cfg); err != nil {
				// Disk is the commit point. Do not leave the live Manager or the
				// in-memory config claiming that a failed /computer toggle worked.
				cfg.Computer.Enabled = previousEnabled
				if created {
					cfg.Computer = nil
				}
				return err
			}
			// Publish only after the durable save. SetConfig waits for any
			// in-flight native action, so a disable/tightening is effective when
			// this command returns.
			computerMgr.SetConfig(computer.FromConfig(cfg.Computer))
			// Tool schemas are fixed on an agent instance. Rebuild immediately so
			// /computer on|off takes effect for the current task without restart.
			st.toolList = st.buildTopLevelTools()
			newAg, err := st.createAgent()
			if err != nil {
				return fmt.Errorf("saved setting but could not refresh agent tools: %w", err)
			}
			st.ag = newAg
			return nil
		},
		RequestPermissions: func() error {
			if computerMgr == nil {
				return fmt.Errorf("%s", computer.UnsupportedReason())
			}
			// Ask for both grants at once: the prompts are system dialogs the
			// user answers, and needing both is the normal case.
			_, err := computerMgr.RequestPermissions(context.Background(), true, true)
			return err
		},
	}

	p, _ := tui.RunTUI(hasPrompt, pwd, env.TodoStore, tui.WithVersion(Version), tui.WithGoalStore(env.GoalStore), tui.WithStartupMode(startupMode), tui.WithTheme(cfg.Theme), tui.WithBrowser(browserCtl), tui.WithComputer(computerCtl), tui.WithApprovalModeChange(func(enabled bool) error {
		target := mode.Approval
		if enabled {
			target = mode.FullAccess
		}
		if err := st.commitTUISessionMode(target); err != nil {
			config.Logger().Printf("[mode] TUI approve-all commit failed: %v", err)
			return fmt.Errorf("could not save mode change")
		}
		return nil
	}))
	st.p = p
	bgManager.SetNotifier(func(taskID, cmd, status string) {
		p.Send(tui.BgTaskDoneMsg{TaskID: taskID, Command: cmd, Status: status})
	})
	teamManager.SetTuiProgram(p)

	h := handler.NewTUIHandler(p)
	h.SetArtifactPathResolver(func(artifactID string) (string, error) {
		if st.rec == nil || st.artifactService == nil {
			return "", fmt.Errorf("artifact service is unavailable")
		}
		_, resolved, err := st.artifactService.Resolve(
			context.Background(), st.rec.UUID(), st.pwd, artifactID,
		)
		return resolved, err
	})

	// Wrap with notifying handler for WeChat push notifications
	notifyingH := handler.NewNotifyingHandler(h, 10*time.Second)
	notifyingH.SetApprovalNotifier(func(toolName, toolArgs string) {
		if st.wechatClient.State() == channel.StateEnabled {
			if err := st.wechatClient.SendText(channel.ApprovalMessage(toolName, toolArgs, "Please return to terminal")); err != nil {
				config.Logger().Printf("[wechat] failed to send approval notification: %v", err)
			}
		}
	})
	notifyingH.SetDoneNotifier(func(summary string, err error) {
		if st.wechatClient.State() == channel.StateEnabled {
			if sendErr := st.wechatClient.SendText(channel.DoneMessage(summary, err)); sendErr != nil {
				config.Logger().Printf("[wechat] failed to send done notification: %v", sendErr)
			}
		}
	})

	// Register WeChat as a notifier for working/idle status pushes.
	notifyingH.AddNotifier(channel.NewChannelNotifier(st.wechatClient))

	// BLE status pushes are a desktop-only feature (the desktop app bundles the
	// jcode-ble helper). The terminal/CLI does not spawn BLE.

	st.h = notifyingH
	approvalState.SetHandler(notifyingH)

	teamManager.SetHandlersFactory(func(workerName, workerColor, permission string) []adk.ChatModelAgentMiddleware {
		if permission == team.PermissionNormal {
			return agent.NewTeammateHandlers(
				approvalState.NewTeammateApprovalFunc(workerName, workerColor))
		}
		// Plan has an endpoint-enforced read-only tool set. Auto received its
		// one-time grant at team_spawn. Both retain safe panic/error folding.
		return agent.NewTeammateHandlers(nil)
	})

	askUserDeps.NotifyFn = func(question string, options []string) {
		p.Send(tui.AskUserQuestionMsg{Question: question, Options: options})
	}

	go func() {
		tuiAskCh := tui.GetAskUserResponseChannel()
		for resp := range tuiAskCh {
			askUserCh <- tools.AskUserResponse{Answer: resp.Answer}
		}
	}()

	// Rebind run-scoped callbacks (including image progress) now that the final
	// transport handler exists; the provisional tool list was built earlier.
	st.toolList = st.buildTopLevelTools()
	ag, err := st.createAgent()
	if err != nil {
		return fmt.Errorf("error creating agent: %w", err)
	}
	st.ag = ag

	// Record the system prompt and environment snapshot for KV-cache-friendly resume.
	if rec != nil {
		envSnapshot := prompts.SerializeEnvInfo(platform, pwd, "local", envInfo)
		rec.RecordSystemPrompt(st.systemPrompt, envSnapshot)
	}

	env.OnEnvChange = func(envLabel string, isLocal bool, envErr error) {
		if envErr != nil {
			p.Send(tui.SSHStatusMsg{Success: false, Err: envErr})
			return
		}
		if isLocal {
			approvalState.SetWorkpath(pwd)
			st.refreshTopLevelPromptAndTools("local", envInfo)
			if newAg, agErr := st.createAgent(); agErr == nil {
				st.ag = newAg
			} else {
				st.failClosedAgentRebuild("switch to local environment", agErr)
				return
			}
			p.Send(tui.SSHCancelMsg{})
			return
		}
		approvalState.SetWorkpath(env.Pwd())
		st.refreshTopLevelPromptAndTools(envLabel, nil)
		if newAg, agErr := st.createAgent(); agErr == nil {
			st.ag = newAg
		} else {
			st.failClosedAgentRebuild("switch to remote environment", agErr)
			return
		}
		p.Send(tui.SSHStatusMsg{Success: true, Label: envLabel})
	}

	// Load a previous session if --resume was requested.
	var initialHistory []adk.Message
	var initialResumeUUID string
	var initialResumeEntries []tui.SessionEntry
	if resumeUUID != "" {
		initialHistory = session.PruneOldToolOutputs(resumeState.History, 2)
		initialResumeUUID = resumeUUID
		initialResumeEntries = tui.ConvertSessionEntries(resumeEntries)
		hasPrompt = false

		restoreStoredPrompt := !agentFlagSet && resumeAgentWarning == "" && selectedAgentName == ""

		// Restore stored system prompt for KV-cache-friendly resume.
		if restoreStoredPrompt && resumeState.SystemPrompt != "" {
			systemPrompt = resumeState.SystemPrompt
			st.systemPrompt = systemPrompt
			// Inject environment diff as an additional system message.
			envDiff := prompts.BuildEnvDiff(resumeState.EnvInfo, platform, pwd, "local", envInfo)
			if envDiff != "" {
				initialHistory = append(initialHistory, &schema.Message{
					Role:    schema.System,
					Content: envDiff,
				})
			}
		}
		if resumeState.Plan != nil {
			switch resumeState.Plan.Status {
			case "approved":
				planStore.Submit(resumeState.Plan.Title, resumeState.Plan.Content)
				planStore.Approve()
			case "submitted":
				planStore.Submit(resumeState.Plan.Title, resumeState.Plan.Content)
			case "rejected":
				planStore.SetDraft(resumeState.Plan.Title, resumeState.Plan.Content)
			}
		}

		if len(resumeState.Todos) > 0 {
			todoItems := make([]tools.TodoItem, len(resumeState.Todos))
			for i, t := range resumeState.Todos {
				todoItems[i] = tools.TodoItem{
					ID: t.ID, Title: t.Title, Status: tools.TodoStatus(t.Status),
				}
			}
			env.TodoStore.Update(todoItems)
		}

		env.GoalStore.RestoreFromSnapshot(resumeState.Goal)

		if targetEnv := resumeState.EnvTarget; targetEnv != "local" {
			if envWarning := st.attemptSSHResume(targetEnv); envWarning != "" {
				if st.sessionResumeWarning != "" {
					st.sessionResumeWarning += "\n"
				}
				st.sessionResumeWarning += envWarning
			}
		}

		// Reuse the existing session UUID so new messages are appended to the same file
		if st.rec != nil {
			st.rec.SetUUID(resumeUUID)
			if ledgerErr := resetImageUsageLedger(st.imageLedger, st.rec); ledgerErr != nil {
				config.Logger().Printf("[image] restore TUI usage ledger: %v", ledgerErr)
				st.imageLedger = nil
			}
			var providerSearchLedgerErr error
			if st.providerSearchLedger == nil {
				st.providerSearchLedger, providerSearchLedgerErr = newProviderSearchUsageLedger(st.rec)
			} else {
				providerSearchLedgerErr = resetProviderSearchUsageLedger(st.providerSearchLedger, st.rec)
			}
			if providerSearchLedgerErr != nil {
				config.Logger().Printf("[provider-search] restore TUI usage ledger: %v", providerSearchLedgerErr)
				st.providerSearchLedger = nil
			}
			if configureErr := st.configureProviderMCPTools(); configureErr != nil {
				config.Logger().Printf("[provider-search] restore TUI MCP catalog: %v", configureErr)
			}
			// The dispatcher + SessionStart during setup bound to the throwaway UUID;
			// rebuild against the restored one so hook payloads carry the correct
			// session_id, then fire SessionStart now — once — for the real session.
			st.hookDisp = hooks.NewSessionDispatcher(config.ConfigDir(), pwd, st.rec.UUID(), config.Logger().Printf)
			st.ctx = hooks.WithDispatcher(st.ctx, st.hookDisp)
			if st.hookDisp.Configured(hooks.SessionStart) {
				dec := st.hookDisp.Fire(st.ctx, hooks.SessionStart, hooks.Payload{})
				st.hookStartContext = dec.AdditionalContext
			}
			st.toolList = st.buildTopLevelTools()
			resumedAgent, rebuildErr := st.createAgent()
			if rebuildErr != nil {
				return fmt.Errorf("rebuild resumed session agent: %w", rebuildErr)
			}
			st.ag = resumedAgent
		}
	}

	// Capture a git baseline so the exit summary only shows changes from this session.
	st.sessionBaselineCommit = computeGitBaseline()

	go st.runEventLoop(initialHistory, initialResumeUUID, initialResumeEntries, hasPrompt, prompt, mcpStatuses)

	if _, err := p.Run(); err != nil {
		if st.langfuseTracer != nil {
			st.langfuseTracer.Flush()
		}
		return fmt.Errorf("TUI error: %w", err)
	}

	// Send goodbye via WeChat if enabled
	if st.wechatClient.State() == channel.StateEnabled {
		// Best-effort, don't block exit
		go func() { _ = st.wechatClient.SendText(channel.GoodbyeMessage(time.Now())) }()
		time.Sleep(500 * time.Millisecond)
		_ = st.wechatClient.Disable()
	}

	// Close all notifiers (BLE, etc.)
	notifyingH.CloseNotifiers()

	if st.langfuseTracer != nil {
		st.langfuseTracer.Flush()
	}

	// Print session summary on exit.
	fmt.Println()
	fmt.Println("Session Summary")
	fmt.Println("===============")
	added, deleted := computeGitDiffStats(st.sessionBaselineCommit)
	fmt.Printf("Files changed: +%d/-%d lines\n", added, deleted)
	promptTokens, completionTokens, totalTokens := internalmodel.TokenTracker.Get()
	byModel := internalmodel.TokenTracker.GetByModel()
	if len(byModel) > 0 {
		for model, tokens := range byModel {
			fmt.Printf("Total tokens (%s): %d\n", model, tokens)
		}
	} else {
		fmt.Printf("Total tokens: %d (prompt: %d, completion: %d)\n", totalTokens, promptTokens, completionTokens)
	}
	if st.rec != nil && st.rec.HasRecording() {
		fmt.Printf("Resume: jcode --resume %s\n", st.rec.UUID())
	}
	fmt.Println()

	return nil
}

// resolveStartupMode picks the initial session mode from CLI flags and config.
// Precedence: --unsafe (forces Full access) > DefaultMode > legacy AutoApprove.
func resolveStartupMode(cfg *config.Config, unsafe bool) mode.SessionMode {
	if unsafe {
		return mode.FullAccess
	}
	if cfg != nil && cfg.DefaultMode != "" {
		return mode.Parse(cfg.DefaultMode)
	}
	if cfg != nil && cfg.AutoApprove { //nolint:staticcheck // intentional fallback to the deprecated field when DefaultMode is unset
		return mode.FullAccess
	}
	return mode.Approval
}

// computeGitBaseline creates a transient stash commit of the current working tree
// without modifying it. Returns the commit hash or empty string if there is nothing to stash.
func computeGitBaseline() string {
	cmd := exec.Command("git", "stash", "create", "jcode session baseline")
	cmd.Env = util.ScrubbedGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// computeGitDiffStats returns the total added and deleted lines since the session started.
// If baseline is non-empty, it diffs against that baseline commit; otherwise it falls back to git diff.
func computeGitDiffStats(baseline string) (added, deleted int) {
	var args []string
	if baseline != "" {
		args = []string{"diff", baseline, "--numstat"}
	} else {
		args = []string{"diff", "--numstat"}
	}
	cmd := exec.Command("git", args...)
	cmd.Env = util.ScrubbedGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			a, _ := strconv.Atoi(fields[0])
			d, _ := strconv.Atoi(fields[1])
			added += a
			deleted += d
		}
	}
	return added, deleted
}
