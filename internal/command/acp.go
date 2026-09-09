package command

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	acp "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"

	"github.com/cnjack/jcode/internal/agent"
	"github.com/cnjack/jcode/internal/artifact"
	"github.com/cnjack/jcode/internal/computer"
	"github.com/cnjack/jcode/internal/config"
	"github.com/cnjack/jcode/internal/flow"
	"github.com/cnjack/jcode/internal/handler"
	"github.com/cnjack/jcode/internal/hooks"
	mempipeline "github.com/cnjack/jcode/internal/memory/pipeline"
	"github.com/cnjack/jcode/internal/mode"
	internalmodel "github.com/cnjack/jcode/internal/model"
	"github.com/cnjack/jcode/internal/prompts"
	"github.com/cnjack/jcode/internal/providertools"
	"github.com/cnjack/jcode/internal/review"
	"github.com/cnjack/jcode/internal/runner"
	"github.com/cnjack/jcode/internal/session"
	"github.com/cnjack/jcode/internal/skills"
	"github.com/cnjack/jcode/internal/telemetry"
	"github.com/cnjack/jcode/internal/tools"
	util "github.com/cnjack/jcode/internal/util"
)

// acpSession bundles all per-session state created in NewSession.
// ACP mode IDs match mode.SessionMode.String().
const (
	acpModeApproval   acp.SessionModeId = "approval"
	acpModePlan       acp.SessionModeId = "plan"
	acpModeAuto       acp.SessionModeId = "auto"
	acpModeFullAccess acp.SessionModeId = "full_access"
)

// acpModeID maps a unified mode to its advertised ACP wire id.
func acpModeID(m mode.SessionMode) acp.SessionModeId {
	return acp.SessionModeId(m.String())
}

// acpModes returns the mode list advertised in NewSession/ResumeSession responses.
func acpModes(current acp.SessionModeId) *acp.SessionModeState {
	return &acp.SessionModeState{
		CurrentModeId: current,
		AvailableModes: []acp.SessionMode{
			{Id: acpModeApproval, Name: "Ask for approval", Description: acp.Ptr("Ask before each restricted tool call")},
			{Id: acpModePlan, Name: "Plan", Description: acp.Ptr("Read-only planning mode for analysis")},
			{Id: acpModeAuto, Name: "Auto", Description: acp.Ptr("LLM reviewer allows safe calls, escalates uncertain ones")},
			{Id: acpModeFullAccess, Name: "Full access", Description: acp.Ptr("Unrestricted execution without approval prompts")},
		},
	}
}

type acpSession struct {
	h             *handler.ACPHandler
	ag            *adk.ChatModelAgent
	env           *tools.Env
	approvalState *runner.ApprovalState
	history       []adk.Message
	rec           *session.Recorder
	todoStore     *tools.TodoStore
	tracer        *telemetry.LangfuseTracer
	tokenUsage    *internalmodel.TokenUsage
	cancel        context.CancelFunc
	mu            sync.Mutex

	// Mode switching support.
	mode         acp.SessionModeId
	createAgent  func(sysPrompt string, toolList []tool.BaseTool, planMode bool) (*adk.ChatModelAgent, error)
	allTools     []tool.BaseTool
	planTools    []tool.BaseTool
	normalPrompt string
	planPrompt   string
	skillLoader  *skills.Loader
	flowLoader   *flow.Loader

	// providerName/modelName label API errors so the user is told which model
	// failed — with several providers configured, "rate limited" alone does not
	// say which one to go look at.
	providerName string
	modelName    string
}

func (s *acpSession) prepareModeLocked(target mode.SessionMode) (*adk.ChatModelAgent, error) {
	if s.createAgent == nil {
		return nil, fmt.Errorf("session agent factory is unavailable")
	}
	prompt, toolList := s.normalPrompt, s.allTools
	if target.IsPlan() {
		prompt, toolList = s.planPrompt, s.planTools
	}
	candidate, err := s.createAgent(prompt, toolList, target.IsPlan())
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, fmt.Errorf("candidate agent is unavailable")
	}
	return candidate, nil
}

func (s *acpSession) publishModeLocked(target mode.SessionMode, candidate *adk.ChatModelAgent) {
	s.ag = candidate
	s.mode = acpModeID(target)
	s.approvalState.SetSessionMode(target)
}

// commitModeLocked is the ACP authorization transaction used by both an
// explicit session/set_mode request and the permission dialog's Allow All.
// The caller holds s.mu. No executable or advertised state is published until
// the candidate agent exists and the mode entry has been durably fsynced.
func (s *acpSession) commitModeLocked(target mode.SessionMode) error {
	if s.mode == acpModeID(target) {
		return nil
	}
	candidate, err := s.prepareModeLocked(target)
	if err != nil {
		return fmt.Errorf("prepare mode %s: %w", target.String(), err)
	}
	if s.rec == nil {
		return fmt.Errorf("session recorder is unavailable")
	}
	if err := s.rec.RecordModeChangeStrict(target.String()); err != nil {
		return fmt.Errorf("persist mode %s: %w", target.String(), err)
	}
	s.publishModeLocked(target, candidate)
	return nil
}

// restoreModeLocked applies already-durable authorization during ACP Resume.
// Strict journal parsing happens independently before this method is called;
// on corruption the supplied target is the fail-closed Approval mode.
func (s *acpSession) restoreModeLocked(target mode.SessionMode) error {
	if s.mode == acpModeID(target) {
		return nil
	}
	candidate, err := s.prepareModeLocked(target)
	if err != nil {
		// A reconnect must never retain a previously live Full access agent when
		// strict restoration cannot build the requested safe snapshot.
		s.ag = nil
		s.mode = acpModeApproval
		s.approvalState.SetSessionMode(mode.Approval)
		return err
	}
	s.publishModeLocked(target, candidate)
	return nil
}

// Close releases resources held by the session (recorder file handle, tracer).
// recentTranscript snapshots the tail of the conversation for the approval
// reviewer, under the session lock that guards history.
func (s *acpSession) recentTranscript() []review.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return review.MsgsFromHistory(s.history)
}

func (s *acpSession) Close() {
	s.mu.Lock()
	env := s.env
	if s.rec != nil {
		s.rec.Close()
		s.rec = nil
	}
	if s.tracer != nil {
		s.tracer.Flush()
		s.tracer = nil
	}
	s.mu.Unlock()
	if env != nil {
		// The Env owns this task's app grants and snapshot/uid bindings. The
		// process-wide Manager belongs to acpAgent and must remain alive for
		// every other ACP session.
		env.CloseComputer()
	}
}

// acpAgent implements the acp.Agent and acp.AgentLoader interfaces, exposing jcode as an ACP server.
type acpAgent struct {
	conn *acp.AgentSideConnection

	mu       sync.Mutex
	sessions map[acp.SessionId]*acpSession

	// Computer Use controls one physical desktop, so all ACP sessions in this
	// process must share the same Manager. Besides reusing one daemon connection,
	// this shares the UI serialization lock and mutation epoch that prevent one
	// task from acting on another task's stale observation.
	computerMu     sync.Mutex
	computerMgr    *computer.Manager
	computerClosed bool
}

// Ensure acpAgent implements acp.AgentLoader interface.
var _ acp.AgentLoader = (*acpAgent)(nil)

func NewACPCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "acp",
		Short:        "Start ACP JSON-RPC server (headless agent protocol)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			handleACPSubcommand()
			return nil
		},
	}
}

func handleACPSubcommand() {
	// Redirect default log to debug log so nothing corrupts stdio JSON-RPC.
	log.SetOutput(config.Logger().Writer())

	a := &acpAgent{
		sessions: make(map[acp.SessionId]*acpSession),
	}
	conn := acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	a.conn = conn

	config.Logger().Printf("[acp] ACP server started on stdio")
	<-conn.Done()
	a.close()

	config.Logger().Printf("[acp] ACP connection closed")
}

// sharedComputerManager returns the one process-wide Computer Use manager for
// ACP. A newly loaded config is published before the session receives tools, so
// existing sessions and the new one enforce the same current policy.
func (a *acpAgent) sharedComputerManager(cfg *config.Config) (*computer.Manager, error) {
	a.computerMu.Lock()
	defer a.computerMu.Unlock()
	if a.computerClosed {
		return nil, fmt.Errorf("ACP agent is closed")
	}
	if a.computerMgr == nil {
		a.computerMgr = newComputerManager(cfg, "")
		return a.computerMgr, nil
	}
	var stored *config.ComputerConfig
	if cfg != nil {
		stored = cfg.Computer
	}
	a.computerMgr.SetConfig(computer.FromConfig(stored))
	return a.computerMgr, nil
}

// close releases task-scoped sessions first, then the process-wide native
// helper. Keeping that order ensures no session can retain a live backend after
// the daemon has been torn down.
func (a *acpAgent) close() {
	a.mu.Lock()
	sessions := make([]*acpSession, 0, len(a.sessions))
	for id, sess := range a.sessions {
		sessions = append(sessions, sess)
		delete(a.sessions, id)
	}
	a.mu.Unlock()

	for _, sess := range sessions {
		sess.Close()
	}

	a.computerMu.Lock()
	a.computerClosed = true
	mgr := a.computerMgr
	a.computerMgr = nil
	a.computerMu.Unlock()
	if mgr != nil {
		_ = mgr.Close()
	}
}

// availableCommandList builds the slash commands advertised to ACP clients:
// the built-in /goal command plus any skill-based commands.
func availableCommandList(skillLoader *skills.Loader, flowLoader *flow.Loader) []acp.AvailableCommand {
	cmds := []acp.AvailableCommand{
		{
			Name:        "goal",
			Description: "Set a persistent objective the agent pursues across turns (or 'clear' / status)",
			Input: &acp.AvailableCommandInput{
				Unstructured: &acp.UnstructuredCommandInput{
					Hint: "<objective> | clear | status",
				},
			},
		},
	}
	if skillLoader != nil {
		for _, sk := range skillLoader.SlashCommands() {
			name := strings.TrimPrefix(sk.Slash, "/")
			// "/goal" is consumed by Prompt() before the skill lookup, so a
			// skill with that name could never run — don't advertise it.
			if name == "goal" {
				continue
			}
			cmds = append(cmds, acp.AvailableCommand{
				Name:        name,
				Description: sk.Description,
				Input: &acp.AvailableCommandInput{
					Unstructured: &acp.UnstructuredCommandInput{
						Hint: "additional instructions",
					},
				},
			})
		}
	}
	if flowLoader != nil {
		for _, fc := range flowLoader.SlashCommands() {
			name := strings.TrimPrefix(fc.Slash, "/")
			if name == "goal" {
				continue
			}
			// ACP has no "type" field, so mark workflows in the description so
			// editor command palettes distinguish them from skills.
			cmds = append(cmds, acp.AvailableCommand{
				Name:        name,
				Description: "workflow — " + fc.Description,
				Input: &acp.AvailableCommandInput{
					Unstructured: &acp.UnstructuredCommandInput{
						Hint: "args / context",
					},
				},
			})
		}
	}
	return cmds
}

// broadcastSlashCommands sends the available slash commands to the client via
// an available_commands_update session notification.
func (a *acpAgent) broadcastSlashCommands(sessionID acp.SessionId, sess *acpSession) {
	cmds := availableCommandList(sess.skillLoader, sess.flowLoader)
	if len(cmds) == 0 {
		return
	}

	if err := a.conn.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: sessionID,
		Update: acp.SessionUpdate{
			AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
				AvailableCommands: cmds,
			},
		},
	}); err != nil {
		config.Logger().Printf("[acp] broadcastSlashCommands error: %v", err)
	}
}

// scheduleSlashCommandsBroadcast sends slash commands after the session
// response has had a chance to reach the client. Some ACP clients ignore
// session updates that arrive before the new/load/resume response is fully
// processed, which makes slash commands appear only after a later message.
func (a *acpAgent) scheduleSlashCommandsBroadcast(sessionID acp.SessionId, sess *acpSession) {
	go func() {
		time.Sleep(50 * time.Millisecond)

		a.mu.Lock()
		current, ok := a.sessions[sessionID]
		a.mu.Unlock()
		if !ok || current != sess {
			return
		}

		a.broadcastSlashCommands(sessionID, sess)
	}()
}

// --- acp.Agent interface ---

func (a *acpAgent) Initialize(_ context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	config.Logger().Printf("[acp] Initialize: client=%v, protocol=%d",
		params.ClientInfo, params.ProtocolVersion)

	// Check if the configured model supports image input.
	imageSupport := false
	if cfg, err := config.LoadConfig(); err == nil {
		providerName, modelName := cfg.GetProviderModel()
		registry := internalmodel.NewModelRegistryWithConfig(cfg)
		if _, m, ok := registry.LookupModel(providerName, modelName); ok && m != nil {
			imageSupport = m.SupportsImageInput()
		}
	}

	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			PromptCapabilities: acp.PromptCapabilities{
				EmbeddedContext: true,
				Image:           imageSupport,
			},
			LoadSession: true,
			SessionCapabilities: acp.SessionCapabilities{
				List:   &acp.SessionListCapabilities{},
				Close:  &acp.SessionCloseCapabilities{},
				Resume: &acp.SessionResumeCapabilities{},
			},
		},
		AgentInfo: &acp.Implementation{
			Name:    "jcode",
			Title:   acp.Ptr("JCODE — Coding Assistant"),
			Version: Version, // set from internal/command/version.go
		},
	}, nil
}

func (a *acpAgent) Authenticate(_ context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *acpAgent) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	config.Logger().Printf("[acp] NewSession: cwd=%s, mcpServers=%d", params.Cwd, len(params.McpServers))

	cfg, err := config.LoadConfig()
	if err != nil {
		return acp.NewSessionResponse{}, fmt.Errorf("config error: %w", err)
	}

	pwd := params.Cwd
	if pwd == "" {
		pwd = util.GetWorkDir()
	}

	// Seed the shared managed deny-read policy from the user-owned global
	// config (never project-mergeable; reloads only strengthen it).
	tools.InitManagedDenyRead(cfg)

	// Apply project-level config overlay (walk-up .jcode/config.json + mcp.json).
	config.ApplyProjectOverlay(cfg, pwd)

	providerName, modelName := cfg.GetProviderModel()
	rec, _ := session.NewRecorder(pwd, providerName, modelName)
	// LLM session titles ride the small model (checked at fire time).
	attachTitleRefiner(ctx, rec)
	sessionID := acp.SessionId(fmt.Sprintf("sess_%s", rec.UUID()))

	sess, err := a.buildAgentSession(
		ctx, cfg, pwd, sessionID, rec, nil, "", resolveStartupMode(cfg, false),
	)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}

	// Background memory distillation on session start (gates inside).
	mempipeline.MaybeStartBackground(cfg, pwd)

	a.mu.Lock()
	a.sessions[sessionID] = sess
	a.mu.Unlock()

	config.Logger().Printf("[acp] Session created: %s", sessionID)

	// Broadcast available slash commands for this session after the response
	// is returned so clients have registered the session before the update.
	a.scheduleSlashCommandsBroadcast(sessionID, sess)

	return acp.NewSessionResponse{
		SessionId:     sessionID,
		Modes:         acpModes(sess.mode),
		ConfigOptions: acpSessionConfigOptions(sess),
	}, nil
}

// buildAgentSession creates the env, tools, middlewares, and agent shared by
// NewSession and LoadSession. The caller is responsible for creating the
// Recorder (so the session ID can be derived from it) and for storing the
// returned session in a.sessions.
func (a *acpAgent) buildAgentSession(
	ctx context.Context,
	cfg *config.Config,
	pwd string,
	sessionID acp.SessionId,
	rec *session.Recorder,
	history []*schema.Message,
	agentRoleName string,
	startupMode mode.SessionMode,
) (*acpSession, error) {
	platform := util.GetSystemInfo()
	envInfo := util.CollectEnvInfo(pwd)

	skillLoader := skills.NewLoader()
	skillLoader.ScanProjectSkills(pwd)

	flowLoader := flow.NewLoader()
	flowLoader.LoadProject(pwd)

	providerName, modelName := cfg.GetProviderModel()
	var selectedRole config.AgentRoleConfig
	if agentRoleName != "" {
		role, ok := config.LoadAgentRoles(pwd)[agentRoleName]
		if !ok {
			config.Logger().Printf(
				"[acp] custom agent %q is unavailable; resuming with Default", agentRoleName,
			)
			agentRoleName = ""
		} else {
			selectedRole = role
			var resolveErr error
			providerName, modelName, resolveErr = resolveCustomAgentModel(
				role, cfg, providerName, modelName,
			)
			if resolveErr != nil {
				return nil, fmt.Errorf("custom agent %q: %w", agentRoleName, resolveErr)
			}
		}
	}
	providers := cfg.GetProviders()
	providerCfg := providers[providerName]
	if providerCfg == nil {
		return nil, fmt.Errorf("provider %q not found in config", providerName)
	}

	registry := internalmodel.NewModelRegistryWithConfig(cfg)
	baseURL := providerCfg.BaseURL
	if baseURL == "" {
		baseURL = registry.GetProviderAPI(providerName)
	}

	// Apply a per-model reasoning-effort override (set from the chat picker)
	// over the provider-level default before constructing the model.
	acpEffortCfg := *providerCfg
	acpEffortCfg.ReasoningEffort = config.ResolveEffort(providerName, modelName, providerCfg.ReasoningEffort)
	chatModel, err := internalmodel.NewChatModelFromProvider(ctx, providerName, modelName, baseURL, &acpEffortCfg)
	if err != nil {
		return nil, fmt.Errorf("error creating model: %w", err)
	}

	env := tools.NewEnv(pwd, platform)
	if rec != nil {
		env.SessionIDFn = rec.UUID
	}
	config.Logger().Printf("[acp] using LocalExecutor for session %s", sessionID)
	bgManager := tools.NewBackgroundManager(env)

	// Per-session token tracker for usage display (goal status, reminders,
	// token updates).
	tokenUsage := &internalmodel.TokenUsage{}

	// Load the raw MCP catalog. Provider-owned endpoints are re-wrapped against
	// this recorder and its durable usage ledger when the current chat provider
	// configuration resolves the exact capability.
	providerToolCfg := *cfg
	projectActiveChatModel(&providerToolCfg, providerName, modelName)
	providerRuntimeLoader := activeChatProviderRuntimeConfigLoader(
		projectProviderRuntimeConfigLoader(pwd), providerName, modelName,
	)
	var rawMCPTools []tool.BaseTool
	effectiveMCPServers := providertools.EffectiveMCPServers(&providerToolCfg)
	if len(effectiveMCPServers) > 0 {
		rawMCPTools, _ = tools.LoadMCPTools(ctx, effectiveMCPServers)
	}
	rawMCPCatalog := newProviderSearchMCPCatalog(&providerToolCfg, rawMCPTools)
	providerSearchLedger, providerSearchLedgerErr := newProviderSearchUsageLedger(rec)
	if providerSearchLedgerErr != nil {
		config.Logger().Printf("[provider-search] initialize ACP usage ledger: %v", providerSearchLedgerErr)
	}
	mcpTools, providerSearchWrapErr := configuredProviderMCPTools(
		ctx, &providerToolCfg, rec, providerSearchLedger,
		rawMCPCatalog,
		false, true, providerRuntimeLoader,
	)
	if providerSearchWrapErr != nil {
		config.Logger().Printf("[provider-search] initialize ACP MCP tools: %v", providerSearchWrapErr)
	}

	// The ACPHandler is created before the tool list so the subagent tool can
	// bridge its lifecycle/progress callbacks into ACP tool_call_update
	// notifications (parity with the TUI notifier and the web SSE events).
	acpHandler := handler.NewACPHandler(a.conn, sessionID, pwd)

	// Publish the developer logging toggle (Settings → Developer) before the
	// first logger / tracer is built. Tracing also respects the developer
	// toggle; both take effect on this startup only.
	config.SetLoggingEnabled(config.LoggingEnabled(cfg))

	// Langfuse tracer — created before the tools so subagent runs nest child
	// traces under the session trace.
	var tracer *telemetry.LangfuseTracer
	if config.TracingEnabled(cfg) && cfg.Telemetry != nil && cfg.Telemetry.Langfuse != nil {
		tracer = telemetry.NewLangfuseTracer(cfg.Telemetry.Langfuse)
	}

	// One factory serves subagent + workflow model overrides (incl. the
	// "small" alias); fallback is this session's current model.
	factory := internalmodel.NewModelFactory(cfg, chatModel)
	agentRoles := config.LoadAgentRoles(env.Pwd())
	allTools := []tool.BaseTool{
		// load_skill: ACP puts the skill list in the system prompt (see
		// skillLoader.Descriptions() below) and the slash-command path literally
		// instructs the model to "use the load_skill tool" — but the tool itself
		// was never registered here, unlike in interactive and web.
		//
		// The model therefore saw skills advertised, was told to load them, and
		// had no way to. Observed in a live campaign: it spent 300s and 122 tool
		// calls trying, degenerating into `echo load_skill` with descriptions
		// like "Please work" and "Enough", generating enough traffic to trip the
		// provider's 60 RPM limit and time out. 4 of 400 runs died this way.
		skills.NewLoadSkillTool(skillLoader),
		env.NewReadTool(), env.NewEditTool(), env.NewWriteTool(),
		env.NewExecuteTool(bgManager), env.NewGrepTool(),
		env.NewTodoWriteTool(), env.NewTodoReadTool(),
		env.NewGoalSetTool(), env.NewGoalGetTool(), env.NewGoalUpdateTool(),
		env.NewAutomationCreateTool(),
		env.NewAutomationListTool(),
		env.NewAutomationDeleteTool(),
		env.NewSwitchEnvTool(),
		env.NewCheckBackgroundTool(bgManager),
		env.NewSubagentTool(&tools.SubagentDeps{
			ChatModel:    chatModel,
			ModelFactory: factory,
			Recorder:     rec,
			Tracer:       tracer,
			Notifier:     acpHandler.OnSubagentEvent,
			ProgressFn:   acpHandler.OnSubagentProgress,
			AgentRoles:   agentRoles,
		}),
		env.NewWorkflowRunTool(&tools.WorkflowToolDeps{
			ModelFactory: factory,
			Recorder:     rec,
			Tracer:       tracer,
			Loader:       flowLoader,
			AgentRoles:   agentRoles,
		}),
	}
	artifactService := artifact.NewService(session.LoadArtifactRecords, time.Now)
	acpHandler.SetArtifactPathResolver(func(ctx context.Context, ref handler.ArtifactRef) (string, error) {
		if rec == nil {
			return "", fmt.Errorf("session recorder is unavailable")
		}
		_, enginePath, resolveErr := artifactService.Resolve(ctx, rec.UUID(), pwd, ref.ID)
		return enginePath, resolveErr
	})
	imageLedger, imageLedgerErr := newImageUsageLedger(rec)
	if imageLedgerErr != nil {
		config.Logger().Printf("[image] initialize ACP usage ledger: %v", imageLedgerErr)
	}
	var sessionImageTool tool.BaseTool
	if imageGenerationEnabled(cfg, false, true) {
		if imageTool, imageErr := configuredGenerateImageTool(
			cfg, artifactService, rec, imageLedger,
			projectProviderRuntimeConfigLoader(pwd), acpHandler, nil,
		); imageErr == nil {
			sessionImageTool = imageTool
		} else if cfg.ImageModel != "" {
			config.Logger().Printf("[image] ACP generate_image unavailable: %v", imageErr)
		}
	}
	if config.MemoryEnabled(cfg) {
		allTools = append(allTools, env.NewMemoryNoteTool(&tools.MemoryNoteDeps{
			SessionIDFn: func() string {
				if rec != nil {
					return rec.UUID()
				}
				return ""
			},
		}))
	}
	// Computer-use manager. Off unless config enables it; when it is off,
	// NewComputerTools returns nil and the tools are simply absent.
	//
	// (Browser-use is deliberately not wired here: its extension backend needs
	// the web server, and the managed backend would launch a Chrome nobody can
	// see from an ACP client. Computer use has no such dependency.)
	computerMgr, err := a.sharedComputerManager(cfg)
	if err != nil {
		return nil, err
	}
	env.Computer = computerMgr
	allTools = append(allTools, env.NewComputerTools()...)

	// Plan mode tools: read-only subset. Goal tools are included — like the
	// todo tools they only mutate session metadata, and the continuation
	// guard runs in every mode, so the agent must be able to inspect and
	// complete/block an active goal here too.
	planTools := []tool.BaseTool{
		env.NewReadTool(),
		env.NewPlanExecuteTool(),
		env.NewGrepTool(),
		env.NewTodoWriteTool(), env.NewTodoReadTool(),
		env.NewGoalSetTool(), env.NewGoalGetTool(), env.NewGoalUpdateTool(),
		env.NewAutomationListTool(),
	}
	planTools = append(planTools, env.NewComputerPlanTools()...)

	normalPrompt := prompts.GetSystemPrompt(platform, pwd, "local", envInfo, skillLoader.Descriptions())
	planPrompt := prompts.GetPlanSystemPrompt(platform, pwd, "local", envInfo)
	if agentRoleName != "" {
		normalPrompt = withCustomAgentPrompt(normalPrompt, agentRoleName, selectedRole)
		planPrompt = withCustomAgentPrompt(planPrompt, agentRoleName, selectedRole)
	}
	if rec != nil {
		rec.SetAgent(agentRoleName)
		rec.SetProviderModel(providerName, modelName)
	}
	approvalState := runner.NewApprovalStateWithMode(pwd, startupMode)
	approvalState.SetComputerPermFunc(func(bundleID, class string) bool {
		return computerMgr != nil && computerMgr.Preapproved(bundleID, class)
	})
	approvalState.SetComputerAppFunc(env.CurrentComputerApp)

	approvalState.SetHandler(acpHandler)

	// Wire up environment change callback so switch_env properly restores
	// the original local executor when switching back from SSH. The agent
	// rebuild hook is installed below after the per-session agent factory exists.
	var rebuildForEnvironment func()
	env.OnEnvChange = func(label string, isLocal bool, envErr error) {
		if envErr != nil {
			config.Logger().Printf("[acp] env change error: %v", envErr)
			return
		}
		if isLocal {
			env.ResetToLocal(pwd, platform)
			config.Logger().Printf("[acp] env reset to original executor: %s", env.Exec.Label())
		} else {
			config.Logger().Printf("[acp] env changed to: %s", label)
		}
		if rebuildForEnvironment != nil {
			rebuildForEnvironment()
		}
	}

	if rec != nil {
		env.TodoStore.OnUpdate = func(items []tools.TodoItem) {
			snapItems := make([]session.TodoSnapshotItem, len(items))
			for i, it := range items {
				snapItems[i] = session.TodoSnapshotItem{
					ID: it.ID, Title: it.Title, Status: string(it.Status),
				}
			}
			rec.RecordTodoSnapshot(snapItems)
		}
		env.GoalStore.OnUpdate = tools.GoalRecorderHook(rec)
	}

	// Build agent with middlewares
	contextLimit := internalmodel.ResolveContextLimit(registry, cfg, providerName, modelName)
	compactThreshold := cfg.CompactionThreshold()

	var handlers []adk.ChatModelAgentMiddleware

	summMw, err := summarization.New(ctx, &summarization.Config{
		Model: chatModel,
		Trigger: &summarization.TriggerCondition{
			ContextTokens: int(float64(contextLimit) * compactThreshold),
		},
		TranscriptFilePath: filepath.Join(config.ConfigDir(), "transcript.txt"),
	})
	if err == nil {
		handlers = append(handlers, summMw)
	}

	reductionMw, err := reduction.New(ctx, agent.BuildReductionConfig(
		filepath.Join(config.ConfigDir(), "reduction"),
		contextLimit,
		compactThreshold,
		internalmodel.NewCalibratedCounter(tokenUsage).Count,
	))
	if err != nil {
		config.Logger().Printf("[acp] reduction middleware init error: %v", err)
	} else {
		handlers = append(handlers, reductionMw)
	}
	// Aggregate cap on one turn's NEW tool results: reduction only caps each
	// result individually (50k), so N parallel calls could still flood a single
	// request. Registered after reduction so per-result truncation runs first.
	handlers = append(handlers, agent.NewTurnToolResultBudgetMiddleware(0))

	reminderMw := agent.NewReminderMiddleware(agent.ReminderConfig{
		TodoStore:    env.TodoStore,
		GoalStore:    env.GoalStore,
		EnvLabel:     "local",
		IsRemote:     env.IsRemote(),
		ContextLimit: contextLimit,
		FileTracker:  env.FileTracker,
		Env:          env,
		Pwd:          pwd,
		Platform:     platform,
		EnvSnapshot:  prompts.SerializeEnvInfo(platform, pwd, "local", envInfo),
	}, tokenUsage)
	handlers = append(handlers, reminderMw)

	// Seed the initial agent from the resolved startup mode (Plan uses the
	// read-only tool/prompt set; Approval/Full access share the full set and differ only
	// on the approval axis, already seeded in approvalState above).
	initialPrompt, initialTools := normalPrompt, allTools
	if startupMode.IsPlan() {
		initialPrompt, initialTools = planPrompt, planTools
	}
	buildSessionAgent := func(
		agentCtx context.Context,
		sysPrompt string,
		toolList []tool.BaseTool,
		planMode bool,
		candidateMCPTools []tool.BaseTool,
		candidateImageTool tool.BaseTool,
	) (*adk.ChatModelAgent, error) {
		effectiveTools := toolList
		effectiveMCPTools := candidateMCPTools
		// Image generation runs in the local JCode engine and remains available
		// when the coding executor points at SSH. Plan mode still excludes it.
		if !planMode && candidateImageTool != nil {
			effectiveTools = append(append([]tool.BaseTool(nil), toolList...), candidateImageTool)
		}
		switch {
		case planMode:
			effectiveMCPTools = nil
		case env.IsRemote():
			generic, _, identifyErr := splitProviderSearchMCPTools(agentCtx, effectiveMCPTools)
			if identifyErr != nil {
				config.Logger().Printf("[provider-search] filter remote ACP catalog: %v", identifyErr)
			}
			effectiveMCPTools = generic
		}
		if !config.ToolSearchEnabled(cfg) {
			staticTools := effectiveTools
			if !planMode {
				staticTools = append(append([]tool.BaseTool(nil), staticTools...), effectiveMCPTools...)
			}
			return agent.NewAgent(
				agentCtx, chatModel, staticTools, sysPrompt,
				approvalState.RequestApproval, nil, handlers,
				agent.WithMaxIterations(cfg.MaxIterations),
			)
		}
		toolMode := agent.ToolModeNormal
		if planMode {
			toolMode = agent.ToolModePlan
		}
		toolPlan, planErr := buildCommandToolPlan(
			agentCtx, effectiveTools, effectiveMCPTools, agent.ToolTransportACP, toolMode,
		)
		if planErr != nil {
			return nil, fmt.Errorf("build ACP tool plan: %w", planErr)
		}
		return agent.NewAgentWithToolPlan(
			agentCtx, chatModel, toolPlan, sysPrompt,
			approvalState.RequestApproval, nil, handlers,
			agent.WithMaxIterations(cfg.MaxIterations),
		)
	}
	newSessionAgent := func(
		agentCtx context.Context,
		sysPrompt string,
		toolList []tool.BaseTool,
		planMode bool,
	) (*adk.ChatModelAgent, error) {
		return buildSessionAgent(
			agentCtx, sysPrompt, toolList, planMode, mcpTools, sessionImageTool,
		)
	}

	ag, err := newSessionAgent(ctx, initialPrompt, initialTools, startupMode.IsPlan())
	if err != nil {
		return nil, fmt.Errorf("error creating agent: %w", err)
	}

	// createAgent closure for mode switching — rebuilds agent with different prompt/tools.
	// Uses context.Background() so the agent survives beyond the original request context.
	makeAgent := func(sysPrompt string, toolList []tool.BaseTool, planMode bool) (*adk.ChatModelAgent, error) {
		return newSessionAgent(context.Background(), sysPrompt, toolList, planMode)
	}

	sess := &acpSession{
		h:             acpHandler,
		ag:            ag,
		env:           env,
		approvalState: approvalState,
		rec:           rec,
		todoStore:     env.TodoStore,
		tracer:        tracer,
		tokenUsage:    tokenUsage,
		history:       history,
		mode:          acpModeID(startupMode),
		createAgent:   makeAgent,
		allTools:      allTools,
		planTools:     planTools,
		providerName:  providerName,
		modelName:     modelName,
		normalPrompt:  normalPrompt,
		planPrompt:    planPrompt,
		skillLoader:   skillLoader,
		flowLoader:    flowLoader,
	}
	rebuildForEnvironment = func() {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		planMode := mode.Parse(string(sess.mode)).IsPlan()
		prompt, sessionTools := sess.normalPrompt, sess.allTools
		if planMode {
			prompt, sessionTools = sess.planPrompt, sess.planTools
		}
		newAgent, rebuildErr := sess.createAgent(prompt, sessionTools, planMode)
		if rebuildErr != nil {
			// The previous local agent may still contain billable tools. Never
			// retain it after entering a remote environment when rebuilding the
			// filtered catalog fails.
			sess.ag = nil
			config.Logger().Printf("[acp] fail-closed env agent rebuild failed: %v", rebuildErr)
			return
		}
		sess.ag = newAgent
	}

	// Provide the config/platform needed to lazily build the LLM reviewer when
	// the session enters Auto mode.
	approvalState.SetReviewerConfig(cfg, platform)
	approvalState.SetTranscriptFunc(sess.recentTranscript)

	// Reconcile the session's advertised mode when the handler promotes to
	// Full access via "Allow All", so sess.mode never drifts from the approval
	// state's source of truth.
	acpHandler.SetModeChangeCallback(func(m mode.SessionMode) error {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		if err := sess.commitModeLocked(m); err != nil {
			config.Logger().Printf("[acp] allow-all mode commit failed: %v", err)
			return err
		}
		return nil
	})

	return sess, nil
}

func (a *acpAgent) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	config.Logger().Printf("[acp] Prompt: session=%s, blocks=%d", params.SessionId, len(params.Prompt))

	a.mu.Lock()
	sess, ok := a.sessions[params.SessionId]
	a.mu.Unlock()
	if !ok {
		return acp.PromptResponse{}, fmt.Errorf("unknown session: %s", params.SessionId)
	}

	// Extract text and images from prompt content blocks.
	var userText strings.Builder
	var imageParts []schema.MessageInputPart
	for _, block := range params.Prompt {
		if block.Text != nil {
			userText.WriteString(block.Text.Text)
		}
		if block.Image != nil && block.Image.Data != "" {
			data := block.Image.Data
			imageParts = append(imageParts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						MIMEType:   block.Image.MimeType,
						Base64Data: &data,
					},
				},
			})
		}
		if block.ResourceLink != nil {
			fmt.Fprintf(&userText, "\n[Resource: %s (%s)]", block.ResourceLink.Name, block.ResourceLink.Uri)
		}
	}

	prompt := userText.String()
	if prompt == "" && len(imageParts) == 0 {
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	// Handle slash commands.
	if strings.HasPrefix(prompt, "/") {
		cmd := strings.TrimPrefix(prompt, "/")
		parts := strings.SplitN(cmd, " ", 2)
		cmdName := parts[0]

		// Built-in /goal command. Status and clear are answered locally —
		// the goal state lives in GoalStore, so no agent run is needed (and
		// running one would let the continuation guard auto-continue an
		// active goal from a mere status query).
		if cmdName == "goal" {
			rest := ""
			if len(parts) > 1 {
				rest = parts[1]
			}
			var goalStore *tools.GoalStore
			if sess.env != nil {
				goalStore = sess.env.GoalStore
			}
			action := tools.ParseGoalCommand(rest)
			switch action.Kind {
			case "status":
				line := "🎯 No goal set. Use /goal <objective> to set one."
				if goalStore != nil && goalStore.Has() {
					line = goalStore.StatusLine()
				}
				sess.h.OnAgentText(line)
				return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
			case "clear":
				if goalStore != nil {
					goalStore.Clear()
				}
				sess.h.OnAgentText("🎯 Goal cleared.")
				return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
			default: // "set"
				objective, err := tools.ValidateGoalObjective(action.Objective)
				if err != nil {
					sess.h.OnAgentText("🎯 " + err.Error())
					return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
				}
				if goalStore != nil {
					goalStore.Set(objective)
				}
				prompt = tools.GoalKickoffPrompt(objective)
			}
		}

		// Check if it's a skill slash command.
		matchedSkill := false
		if sess.skillLoader != nil {
			if sk := sess.skillLoader.GetBySlash("/" + cmdName); sk != nil {
				userInput := ""
				if len(parts) > 1 {
					userInput = parts[1]
				}
				prompt = fmt.Sprintf("Use the load_skill tool with name=%q and follow its instructions. %s", sk.Name, userInput)
				matchedSkill = true
			}
		}

		// Otherwise check workflow slash commands (e.g. /repo-audit).
		if !matchedSkill && sess.flowLoader != nil {
			if wf, ok := sess.flowLoader.GetBySlash("/" + cmdName); ok {
				userInput := ""
				if len(parts) > 1 {
					userInput = parts[1]
				}
				prompt = flow.SlashRunPrompt(wf.Meta.Name, userInput)
			}
		}
	}

	sess.mu.Lock()
	ag := sess.ag
	if ag == nil {
		sess.mu.Unlock()
		return acp.PromptResponse{}, fmt.Errorf("session agent is unavailable after a fail-closed rebuild")
	}
	if sess.rec != nil {
		var entryImages []session.EntryImage
		for _, p := range imageParts {
			if p.Image != nil && p.Image.Base64Data != nil {
				entryImages = append(entryImages, session.EntryImage{
					MimeType: p.Image.MIMEType,
					Data:     *p.Image.Base64Data,
				})
			}
		}
		sess.rec.RecordUser(prompt, entryImages...)
	}

	// Build user message — include images as multimodal content if provided.
	var userMsg *schema.Message
	if len(imageParts) > 0 {
		parts := make([]schema.MessageInputPart, 0, len(imageParts)+1)
		if prompt != "" {
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: prompt,
			})
		}
		parts = append(parts, imageParts...)
		userMsg = &schema.Message{
			Role:                  schema.User,
			Content:               prompt,
			UserInputMultiContent: parts,
		}
	} else {
		userMsg = schema.UserMessage(prompt)
	}
	sess.history = append(sess.history, userMsg)

	// Create a cancellable context for this prompt turn.
	promptCtx, cancel := context.WithCancel(ctx)
	sess.cancel = cancel
	history := make([]adk.Message, len(sess.history))
	copy(history, sess.history)
	sess.mu.Unlock()

	// Inject the hook dispatcher so PreToolUse/PostToolUse/Stop hooks run on ACP
	// too (parity with the TUI); reloaded per turn so hooks.json edits hot-apply.
	// The recorder is optional here, so fall back to the ACP session id.
	hookSessionID := string(params.SessionId)
	if sess.rec != nil {
		hookSessionID = sess.rec.UUID()
	}
	promptCtx = hooks.WithDispatcher(promptCtx, hooks.NewSessionDispatcher(config.ConfigDir(), sess.env.Pwd(), hookSessionID, config.Logger().Printf))
	// Reset per-turn approval-reviewer state (denial circuit breaker) at the
	// start of each user turn.
	sess.approvalState.OnTurnStart()
	result := runner.Run(promptCtx, ag, history, sess.h, sess.rec, sess.todoStore, sess.env.GoalStore, sess.tracer, sess.tokenUsage)

	sess.mu.Lock()
	if len(result.Messages) > 0 {
		sess.history = append(sess.history, result.Messages...)
	}
	sess.cancel = nil
	sess.mu.Unlock()

	if promptCtx.Err() != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}

	// A turn that died on an API error is not an end_turn. Reporting one is how a
	// 402 came back as a clean, empty, successful-looking turn — see
	// ACPHandler.OnAgentDone. Tell the user what happened, in words they can act
	// on, and end the turn with a reason that is not "success".
	if turnErr := sess.h.TakeTurnError(); turnErr != nil {
		friendly := runner.FormatRunError(turnErr, sess.providerName, sess.modelName)
		config.Logger().Printf("[acp] turn failed: %v", turnErr)
		sess.h.OnAgentText("\n" + friendly)
		return acp.PromptResponse{StopReason: acp.StopReasonRefusal}, nil
	}

	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *acpAgent) Cancel(_ context.Context, params acp.CancelNotification) error {
	config.Logger().Printf("[acp] Cancel: session=%s", params.SessionId)

	a.mu.Lock()
	sess, ok := a.sessions[params.SessionId]
	a.mu.Unlock()
	if !ok {
		return nil
	}

	sess.mu.Lock()
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.mu.Unlock()
	return nil
}

func (a *acpAgent) SetSessionMode(_ context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	config.Logger().Printf("[acp] SetSessionMode: session=%s, mode=%s", params.SessionId, params.ModeId)

	a.mu.Lock()
	sess, ok := a.sessions[params.SessionId]
	a.mu.Unlock()
	if !ok {
		return acp.SetSessionModeResponse{}, fmt.Errorf("unknown session: %s", params.SessionId)
	}

	sess.mu.Lock()

	// Accept only the four canonical ids.
	switch params.ModeId {
	case acpModeApproval, acpModePlan, acpModeAuto, acpModeFullAccess:
	default:
		sess.mu.Unlock()
		return acp.SetSessionModeResponse{}, fmt.Errorf("unknown mode: %s", params.ModeId)
	}
	sm := mode.Parse(string(params.ModeId))
	newMode := acpModeID(sm)
	if newMode == sess.mode {
		sess.mu.Unlock()
		return acp.SetSessionModeResponse{}, nil
	}
	if err := sess.commitModeLocked(sm); err != nil {
		sess.mu.Unlock()
		config.Logger().Printf("[acp] mode switch to %s failed: %v", newMode, err)
		return acp.SetSessionModeResponse{}, fmt.Errorf("failed to switch session mode safely")
	}
	sess.mu.Unlock()
	config.Logger().Printf("[acp] agent recreated for mode %s", newMode)

	// Notify the client of the mode change (outside of lock).
	if a.conn != nil {
		if err := a.conn.SessionUpdate(context.Background(), acp.SessionNotification{
			SessionId: params.SessionId,
			Update: acp.SessionUpdate{
				CurrentModeUpdate: &acp.SessionCurrentModeUpdate{
					CurrentModeId: newMode,
				},
			},
		}); err != nil {
			config.Logger().Printf("[acp] SetSessionMode update error: %v", err)
		}
	}

	return acp.SetSessionModeResponse{}, nil
}

func (a *acpAgent) ListSessions(_ context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	cwd := util.GetWorkDir()
	if params.Cwd != nil && *params.Cwd != "" {
		cwd = *params.Cwd
	}
	config.Logger().Printf("[acp] ListSessions: cwd=%s", cwd)

	metas, err := session.ListSessions(cwd)
	if err != nil {
		config.Logger().Printf("[acp] ListSessions error: %v", err)
		return acp.ListSessionsResponse{Sessions: []acp.SessionInfo{}}, nil
	}

	sessions := make([]acp.SessionInfo, 0, len(metas))
	for _, m := range metas {
		var title *string
		if m.Title != "" {
			title = &m.Title
		}
		updatedAt := m.StartTime
		sessions = append(sessions, acp.SessionInfo{
			SessionId: acp.SessionId(fmt.Sprintf("sess_%s", m.UUID)),
			Title:     title,
			Cwd:       cwd,
			UpdatedAt: &updatedAt,
		})
	}
	return acp.ListSessionsResponse{Sessions: sessions}, nil
}

func (a *acpAgent) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	config.Logger().Printf("[acp] SetSessionConfigOption")
	if params.Boolean == nil {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("unsupported session config option value")
	}
	return a.setSessionToolConfigOption(ctx, params.Boolean)
}

// CloseSession implements the session/close method.
// It cancels any ongoing work and releases session resources.
func (a *acpAgent) CloseSession(_ context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	config.Logger().Printf("[acp] CloseSession: session=%s", params.SessionId)

	a.mu.Lock()
	sess, ok := a.sessions[params.SessionId]
	if ok {
		delete(a.sessions, params.SessionId)
	}
	a.mu.Unlock()

	if ok {
		// Cancel any ongoing prompt, then release resources.
		sess.mu.Lock()
		if sess.cancel != nil {
			sess.cancel()
		}
		sess.mu.Unlock()
		sess.Close()
	}

	return acp.CloseSessionResponse{}, nil
}

func (a *acpAgent) LoadSession(ctx context.Context, params acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	config.Logger().Printf("[acp] LoadSession: sessionId=%s", params.SessionId)

	// Extract the internal session UUID from the ACP session ID.
	resumeUUID := strings.TrimPrefix(string(params.SessionId), "sess_")

	cfg, err := config.LoadConfig()
	if err != nil {
		return acp.LoadSessionResponse{}, fmt.Errorf("config error: %w", err)
	}

	// Load session history from disk.
	entries, err := session.LoadSession(resumeUUID)
	if err != nil {
		return acp.LoadSessionResponse{}, fmt.Errorf("failed to load session: %w", err)
	}

	pwd := params.Cwd
	if pwd == "" {
		pwd = util.GetWorkDir()
	}

	// Seed the shared managed deny-read policy from the user-owned global
	// config (never project-mergeable; reloads only strengthen it).
	tools.InitManagedDenyRead(cfg)

	// Apply project-level config overlay (walk-up .jcode/config.json + mcp.json).
	config.ApplyProjectOverlay(cfg, pwd)

	providerName, modelName := cfg.GetProviderModel()
	rec, _ := session.NewRecorder(pwd, providerName, modelName)
	if rec != nil {
		rec.SetUUID(resumeUUID)
	}

	// Reconstruct full message history (including tool calls/results).
	resumeState := session.ReconstructState(entries)
	history := session.PruneOldToolOutputs(resumeState.History, 2)
	// Read authorization independently from tolerant history reconstruction.
	// A malformed trailing revoke must never revive a previous Full access mode.
	restoredMode := restoredSessionMode(resumeUUID, "acp-load")

	sess, err := a.buildAgentSession(
		ctx, cfg, pwd, params.SessionId, rec, history, resumeState.Agent, restoredMode,
	)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	sess.env.GoalStore.RestoreFromSnapshot(resumeState.Goal)

	a.mu.Lock()
	if old, ok := a.sessions[params.SessionId]; ok {
		old.Close()
	}
	a.sessions[params.SessionId] = sess
	a.mu.Unlock()

	config.Logger().Printf("[acp] Session loaded: %s, messages=%d", params.SessionId, len(sess.history))

	// Broadcast available slash commands for the loaded session after the
	// response is returned so clients have registered the session first.
	a.scheduleSlashCommandsBroadcast(params.SessionId, sess)

	return acp.LoadSessionResponse{
		Modes: acpModes(sess.mode), ConfigOptions: acpSessionConfigOptions(sess),
	}, nil
}

// ResumeSession implements the session/resume method.
// It resumes an existing session without returning previous messages.
func (a *acpAgent) ResumeSession(ctx context.Context, params acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	config.Logger().Printf("[acp] ResumeSession: session=%s", params.SessionId)

	// Extract the internal session UUID from the ACP session ID.
	resumeUUID := strings.TrimPrefix(string(params.SessionId), "sess_")
	restoredMode := restoredSessionMode(resumeUUID, "acp-resume")

	a.mu.Lock()
	if sess, ok := a.sessions[params.SessionId]; ok {
		sess.mu.Lock()
		if err := sess.restoreModeLocked(restoredMode); err != nil {
			sess.mu.Unlock()
			a.mu.Unlock()
			return acp.ResumeSessionResponse{}, fmt.Errorf("restore session mode safely: %w", err)
		}
		sess.mu.Unlock()
		// Session already in memory — broadcast slash commands after the
		// response so the reconnecting client receives the update.
		s := sess
		a.mu.Unlock()
		a.scheduleSlashCommandsBroadcast(params.SessionId, s)
		return acp.ResumeSessionResponse{
			Modes: acpModes(s.mode), ConfigOptions: acpSessionConfigOptions(s),
		}, nil
	}
	a.mu.Unlock()

	// Session not in memory — reload from disk so the agent has conversation context.
	cfg, err := config.LoadConfig()
	if err != nil {
		return acp.ResumeSessionResponse{}, fmt.Errorf("config error: %w", err)
	}

	pwd := params.Cwd
	if pwd == "" {
		pwd = util.GetWorkDir()
	}

	// Seed the shared managed deny-read policy from the user-owned global
	// config (never project-mergeable; reloads only strengthen it).
	tools.InitManagedDenyRead(cfg)

	// Apply project-level config overlay (walk-up .jcode/config.json + mcp.json).
	config.ApplyProjectOverlay(cfg, pwd)

	providerName, modelName := cfg.GetProviderModel()
	rec, _ := session.NewRecorder(pwd, providerName, modelName)
	// Reuse the original session UUID so transcript entries are written to
	// the same session file (same pattern as LoadSession).
	if rec != nil {
		rec.SetUUID(resumeUUID)
	}

	// Load history from disk so the agent has conversation context.
	var history []*schema.Message
	var goalSnap *session.GoalSnapshot
	var resumedAgent string
	if entries, err := session.LoadSession(resumeUUID); err == nil {
		resumeState := session.ReconstructState(entries)
		history = session.PruneOldToolOutputs(resumeState.History, 2)
		goalSnap = resumeState.Goal
		resumedAgent = resumeState.Agent
		config.Logger().Printf("[acp] ResumeSession: loaded %d history messages for %s", len(history), params.SessionId)
	} else {
		config.Logger().Printf("[acp] ResumeSession: could not load history for %s: %v", params.SessionId, err)
	}

	sess, err := a.buildAgentSession(
		ctx, cfg, pwd, params.SessionId, rec, history, resumedAgent, restoredMode,
	)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	sess.env.GoalStore.RestoreFromSnapshot(goalSnap)
	a.mu.Lock()
	a.sessions[params.SessionId] = sess
	a.mu.Unlock()

	config.Logger().Printf("[acp] Session resumed: %s", params.SessionId)
	a.scheduleSlashCommandsBroadcast(params.SessionId, sess)

	return acp.ResumeSessionResponse{
		Modes: acpModes(sess.mode), ConfigOptions: acpSessionConfigOptions(sess),
	}, nil
}

// Logout implements the logout method.
// Terminates the current authenticated session (no-op for jcode, which doesn't require auth).
func (a *acpAgent) Logout(_ context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	config.Logger().Printf("[acp] Logout")
	return acp.LogoutResponse{}, nil
}
