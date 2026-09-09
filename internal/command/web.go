package command

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/spf13/cobra"

	"github.com/cnjack/jcode/internal/agent"
	"github.com/cnjack/jcode/internal/artifact"
	"github.com/cnjack/jcode/internal/automation"
	"github.com/cnjack/jcode/internal/browser"
	"github.com/cnjack/jcode/internal/channel"
	"github.com/cnjack/jcode/internal/channel/ble"
	"github.com/cnjack/jcode/internal/cloud"
	"github.com/cnjack/jcode/internal/computer"
	"github.com/cnjack/jcode/internal/config"
	"github.com/cnjack/jcode/internal/feature"
	"github.com/cnjack/jcode/internal/flow"
	"github.com/cnjack/jcode/internal/handler"
	mempipeline "github.com/cnjack/jcode/internal/memory/pipeline"
	"github.com/cnjack/jcode/internal/mode"
	internalmodel "github.com/cnjack/jcode/internal/model"
	weixin "github.com/cnjack/jcode/internal/pkg/weixin"
	"github.com/cnjack/jcode/internal/prompts"
	"github.com/cnjack/jcode/internal/providertools"
	"github.com/cnjack/jcode/internal/runner"
	"github.com/cnjack/jcode/internal/session"
	"github.com/cnjack/jcode/internal/skills"
	"github.com/cnjack/jcode/internal/telemetry"
	"github.com/cnjack/jcode/internal/tools"
	"github.com/cnjack/jcode/internal/usage"
	util "github.com/cnjack/jcode/internal/util"
	"github.com/cnjack/jcode/internal/web"
	managedworkspace "github.com/cnjack/jcode/internal/workspace"
)

// estimateToolTokens approximates a tool's contribution to the context window
// from its serialized schema (name + description + parameters). ToolInfo's
// MarshalJSON includes the JSON-schema params, so one marshal captures it all.
func estimateToolTokens(ctx context.Context, t tool.BaseTool) int {
	if t == nil {
		return 0
	}
	info, err := t.Info(ctx)
	if err != nil || info == nil {
		return 0
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return usage.EstimateBytes(len(info.Name) + len(info.Desc))
	}
	return usage.EstimateBytes(len(raw))
}

func NewWebCmd() *cobra.Command {
	var port int
	var host string
	var openBrowser bool
	var authToken string
	cmd := &cobra.Command{
		Use:          "web",
		Short:        "Start the web server",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWebServer(cmd.Context(), port, host, openBrowser, authToken)
		},
	}
	cmd.Flags().IntVar(&port, "port", 8080, "HTTP server port")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "HTTP server host")
	cmd.Flags().BoolVar(&openBrowser, "open", true, "Open browser after server starts")
	cmd.Flags().StringVar(&authToken, "auth-token", "", "Bearer token required when bound to a non-loopback host (auto-generated if empty). Can also be set via JCODE_WEB_TOKEN.")
	return cmd
}

// interactiveToolNames are the tools that require a live human to answer — they
// cannot run unattended. Automation runs (scheduled, and manual runs that may be
// headless) drop them via dropInteractiveTools so an agent calling ask_user in a
// run with no watching client can't block on the WS channel forever, stalling
// the run until the liveness ceiling cancels it. automation_create/delete and
// switch_env are also excluded: an unattended run must never recursively create
// enabled automations, remove existing ones, or wait forever on the
// always-interactive environment switch.
var interactiveToolNames = map[string]struct{}{
	"ask_user": {}, "generate_image": {}, "automation_create": {}, "automation_delete": {}, "switch_env": {},
}

// dropInteractiveTools returns tools minus any whose name is in
// interactiveToolNames. Tools whose Info() can't be read are kept (best-effort).
func dropInteractiveTools(tools []tool.BaseTool) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(tools))
	for _, t := range tools {
		if info, err := t.Info(context.Background()); err == nil {
			if _, drop := interactiveToolNames[info.Name]; drop {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// browserSitePreapproved reports whether an origin is pre-authorized for a
// browser action class ("navigate"/"interact") via config.browser.approval
// defaults or a per-site override. Empty origin never pre-approves.
func browserSitePreapproved(cfg *config.Config, origin, class string) bool {
	if cfg == nil || cfg.Browser == nil || origin == "" {
		return false
	}
	bc := cfg.Browser
	// Per-site override wins over the class default.
	for _, sp := range bc.SitePermissions {
		if sp.Origin != origin {
			continue
		}
		val := sp.Navigate
		if class == "interact" {
			val = sp.Interact
		}
		return val == "allow"
	}
	if bc.Approval != nil && bc.Approval[class] == "always_allow" {
		return true
	}
	return false
}

// resolveWebToken decides the web auth token and whether auth must be enforced.
//
// Auth is required when the bind host is non-loopback (exposed to the network),
// or when a token was explicitly supplied. Token source priority:
//  1. --auth-token flag / JCODE_WEB_TOKEN env — session-scoped, never written to disk
//  2. ~/.jcode/web_token — persisted (0600), reused across restarts
//  3. auto-generated (32 random bytes, base64url) when exposed and none of the
//     above; persisted to ~/.jcode/web_token so the token is stable across restarts
func resolveWebToken(host, flagToken string) (token string, requireAuth bool, err error) {
	explicit := flagToken
	if explicit == "" {
		explicit = os.Getenv("JCODE_WEB_TOKEN")
	}
	// Explicit token (flag/env): enforce auth, never touch disk (session-scoped).
	if explicit != "" {
		if !web.IsValidWSSubprotocolToken(explicit) {
			return "", true, fmt.Errorf("auth token must be printable ASCII with no spaces or separators (it is sent as a WebSocket subprotocol)")
		}
		return explicit, true, nil
	}
	// Loopback bind with no explicit token: keep the existing no-auth behaviour.
	if web.IsLoopbackBind(host) {
		return "", false, nil
	}
	// Exposed bind, no explicit token: reuse a persisted token or generate one.
	dir := config.ConfigDir()
	path := filepath.Join(dir, "web_token")
	if b, rerr := os.ReadFile(path); rerr == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, true, nil
		}
	}
	gen, gerr := generateWebToken()
	if gerr != nil {
		return "", true, fmt.Errorf("generate web token: %w", gerr)
	}
	// Ensure ~/.jcode exists before writing, otherwise a first remote start would
	// fail with ENOENT and silently fall back to a session-scoped token.
	if merr := os.MkdirAll(dir, 0o700); merr != nil {
		config.Logger().Printf("[web] could not create config dir %s: %v", dir, merr)
	} else if werr := os.WriteFile(path, []byte(gen), 0o600); werr != nil {
		// Non-fatal: fall back to a session-scoped token (auth still enforced).
		config.Logger().Printf("[web] could not persist web token to %s: %v", path, werr)
	}
	return gen, true, nil
}

// generateWebToken returns 32 cryptographically-random bytes as a URL-safe
// base64 string (no padding).
func generateWebToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// initChannelNotifiers creates the process-level WeChat client and BLE proxy,
// auto-enabling them when the config requests it.
func initChannelNotifiers(cfg *config.Config) (*weixin.Client, *ble.Proxy) {
	wechatClient := weixin.NewClient()
	if cfg.Channel != nil && cfg.Channel.WebEnabled && wechatClient.State() == channel.StateDisabled {
		if err := wechatClient.Enable(); err != nil {
			config.Logger().Printf("[wechat] web auto-enable failed: %v", err)
		} else {
			config.Logger().Printf("[wechat] web auto-enabled")
		}
	}
	// BLE is a desktop-only feature (compiled out of plain `jcode web`). The proxy
	// is added to every task's notifier chain; enabling/disabling it via the
	// settings toggle takes effect live (no restart). It stays a no-op until
	// Enable() is called — at startup here if configured, and on the toggle.
	bleProxy := &ble.Proxy{}
	if feature.BLE && cfg.Channel != nil && cfg.Channel.BLEEnabled {
		bleProxy.Enable()
	}
	return wechatClient, bleProxy
}

func newNotifyingHandler(wh *handler.WebHandler, wechatClient *weixin.Client, bleProxy *ble.Proxy) *handler.NotifyingHandler {
	nh := handler.NewNotifyingHandler(wh, 10*time.Second)
	nh.SetApprovalNotifier(func(toolName, toolArgs string) {
		if wechatClient.State() == channel.StateEnabled {
			if err := wechatClient.SendText(channel.ApprovalMessage(toolName, toolArgs, "Please check the web interface")); err != nil {
				config.Logger().Printf("[wechat] failed to send approval notification: %v", err)
			}
		}
	})
	nh.SetDoneNotifier(func(summary string, err error) {
		if wechatClient.State() == channel.StateEnabled {
			if sendErr := wechatClient.SendText(channel.DoneMessage(summary, err)); sendErr != nil {
				config.Logger().Printf("[wechat] failed to send done notification: %v", sendErr)
			}
		}
	})
	nh.AddNotifier(channel.NewChannelNotifier(wechatClient))
	if feature.BLE {
		nh.AddNotifier(bleProxy)
	}
	return nh
}

func webProviderRuntimeConfigLoader(pwd string, remote bool) providerRuntimeConfigLoader {
	if remote {
		return envProviderRuntimeConfigLoader()
	}
	return projectProviderRuntimeConfigLoader(pwd)
}

func runWebServer(parent context.Context, port int, host string, openBrowser bool, authToken string) error {
	// Check if we need setup (no providers configured).
	needsSetup := config.NeedsSetup()
	cfg, err := loadWebServerConfig(needsSetup)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pwd := util.GetWorkDir()
	platform := util.GetSystemInfo()

	skillLoader := skills.NewLoaderWithDisabled(cfg.DisabledSkills)
	skillLoader.ScanProjectSkills(pwd)

	flowLoader := flow.NewLoader()
	flowLoader.LoadProject(pwd)

	var providerName, modelName string
	if !needsSetup {
		providerName, modelName = cfg.GetProviderModel()
		providers := cfg.GetProviders()
		providerCfg := providers[providerName]
		_, isCloudProvider := cloud.ParseCloudProviderRef(providerName)
		if providerCfg == nil && !isCloudProvider {
			return fmt.Errorf("provider %q not found in config", providerName)
		}
	}

	registry := internalmodel.NewModelRegistryWithConfig(cfg)

	// MCP tools are loaded asynchronously after the web server starts listening.
	// A slow remote MCP server must not block /api/health and make desktop launch
	// look hung. mcpToolsPtr is swapped atomically by reloadMCPTools so a new task
	// (built concurrently by buildWebTask) always reads a consistent catalog
	// without a data race on hot-reload. The catalog also carries the config
	// epoch that its provider-managed transport was connected with.
	var mcpToolsPtr atomic.Pointer[providerSearchMCPCatalog]
	reloadMCPTools := func(servers map[string]*config.MCPServer) ([]tools.MCPStatus, error) {
		runtimeCfg, err := config.LoadConfig()
		if err != nil {
			// Publish a catalog with the credential-bearing provider preset
			// removed, then return success so the Web server rebuilds every live
			// task. Returning the load error here would leave old task agents
			// executable with a policy that can no longer be verified.
			var current []tool.BaseTool
			if loaded := mcpToolsPtr.Load(); loaded != nil {
				current = append(current, loaded.Tools...)
			}
			generic, _, identifyErr := splitProviderSearchMCPTools(ctx, current)
			mcpToolsPtr.Store(newProviderSearchMCPCatalog(nil, generic))
			config.Logger().Printf("[mcp] fail-closed provider MCP config reload: %v", err)
			if identifyErr != nil {
				config.Logger().Printf("[mcp] fail-closed provider tool filter: %v", identifyErr)
			}
			return nil, nil
		}
		config.ApplyProjectOverlay(runtimeCfg, pwd)
		runtimeCfg.MCPServers = servers
		nt, statuses := tools.LoadMCPTools(ctx, providertools.EffectiveMCPServers(runtimeCfg))
		mcpToolsPtr.Store(newProviderSearchMCPCatalog(runtimeCfg, nt))
		return statuses, nil
	}

	startupMode := resolveStartupMode(cfg, false)

	// Publish the developer toggles (Settings → Developer) before the first
	// logger / tracer is built. Both take effect on this startup; runtime
	// toggling is not supported and the UI marks them as "restart required".
	config.SetLoggingEnabled(config.LoggingEnabled(cfg))

	// Langfuse tracer (shared across tasks). Respects Settings → Developer →
	// Enable Langfuse tracing; an absent block keeps the historical behavior.
	var langfuseTracer *telemetry.LangfuseTracer
	if config.TracingEnabled(cfg) && cfg.Telemetry != nil && cfg.Telemetry.Langfuse != nil {
		langfuseTracer = telemetry.NewLangfuseTracer(cfg.Telemetry.Langfuse)
	}

	wechatClient, bleProxy := initChannelNotifiers(cfg)

	// makeNotifyingHandler wraps a fresh per-task WebHandler with the shared push
	// notifiers (WeChat + BLE) so a backgrounded task can still surface
	// approval/done/working notifications without stealing UI focus.
	makeNotifyingHandler := func(wh *handler.WebHandler) *handler.NotifyingHandler {
		return newNotifyingHandler(wh, wechatClient, bleProxy)
	}

	// newChatModel resolves a provider/model into a live chat model + context
	// limit. Shared because it has no per-task state — each task gets its own
	// model instance from it.
	newChatModel := func(prov, mod string) (model.ToolCallingChatModel, int, error) {
		currentCfg, err := config.LoadConfig()
		if err != nil {
			return nil, 0, fmt.Errorf("config error: %w", err)
		}
		if _, isCloud := cloud.ParseCloudProviderRef(prov); isCloud {
			resolveCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			catalogModel, proxyBase, deviceToken, err := cloud.ResolveCloudModel(
				resolveCtx, currentCfg, prov, mod,
			)
			if err != nil {
				return nil, 0, fmt.Errorf("resolve Cloud model %s/%s: %w", prov, mod, err)
			}
			vision := catalogModel.Capabilities.Image
			effort := config.ResolveEffort(prov, mod, "")
			proxyConfig := &config.ProviderConfig{
				APIKey: deviceToken, Vision: &vision, ReasoningEffort: effort,
			}
			cm, err := internalmodel.NewChatModelFromProvider(
				ctx, catalogModel.Kind, catalogModel.UpstreamModelID, proxyBase, proxyConfig,
			)
			if err != nil {
				return nil, 0, fmt.Errorf("create Cloud model %s/%s: %w", prov, mod, err)
			}
			return cm, catalogModel.ContextWindow, nil
		}
		provCfg := currentCfg.GetProviders()[prov]
		if provCfg == nil {
			return nil, 0, fmt.Errorf("provider %q not configured", prov)
		}
		bURL := provCfg.BaseURL
		if bURL == "" {
			bURL = registry.GetProviderAPI(prov)
		}
		// Apply a per-model reasoning-effort override (set from the chat picker)
		// over the provider-level default before constructing the model.
		pcEffort := config.ResolveEffort(prov, mod, provCfg.ReasoningEffort)
		effortCfg := *provCfg
		effortCfg.ReasoningEffort = pcEffort
		cm, err := internalmodel.NewChatModelFromProvider(ctx, prov, mod, bURL, &effortCfg)
		if err != nil {
			return nil, 0, fmt.Errorf("create model %s/%s: %w", prov, mod, err)
		}
		ctxLimit := internalmodel.ResolveContextLimit(registry, currentCfg, prov, mod)
		return cm, ctxLimit, nil
	}

	// Browser-use manager (extension bridge + managed Chrome), process-wide and
	// shared with every per-task Env so the settings UI and the agent's browser_*
	// tools operate the same Chrome. Created regardless of needsSetup so the
	// settings page works before providers are configured.
	browserMgr := browser.NewManager(browser.FromConfig(cfg.Browser))

	// Computer-use manager (native desktop app control), process-wide like the
	// browser one so the settings UI and the agent's computer_* tools share one
	// backend and one view of what is granted. Off unless config enables it.
	computerMgr := newComputerManager(cfg, "")
	if computerMgr != nil {
		defer func() { _ = computerMgr.Close() }()
	}

	// One process-wide metadata registry is shared by all local Web/Desktop task
	// engines. Its source of truth is still the session JSONL loader.
	artifactService := artifact.NewService(session.LoadArtifactRecords, time.Now)

	// Automation store (definitions + scheduler state). Skipped in setup mode.
	// Created before buildWebTask so every per-task Env shares this one live
	// store — the automation_create tool must write through it (not a throwaway)
	// so created automations are visible to the REST API and scheduler.
	var autoStore *automation.Store
	if !needsSetup {
		var aerr error
		if autoStore, aerr = automation.NewStore(); aerr != nil {
			config.Logger().Printf("[automation] store unavailable: %v", aerr)
			autoStore = nil
		}
	}

	// buildWebTask is the per-task engine factory. It produces a fully ISOLATED
	// set of run state — its own env, background manager, recorder, token tracker,
	// approval state, plan store, and event handler — so concurrent tasks never
	// share mutable execution state. exec != nil binds the task to a remote SSH
	// target instead of a local pwd. taskID != "" resumes an existing session.
	// interactiveTools are the tool names that require a live human to answer —
	// they cannot run unattended, so automation runs (scheduled, and manual runs
	// that may be headless) exclude them. An agent in an automation run that calls
	// ask_user would otherwise block on the WS channel forever (no client resolves
	// it) and stall the run until the liveness ceiling cancels it.
	buildWebTask := func(
		taskID, taskPwd, modeStr string,
		exec tools.RemoteExecutor,
		excludeInteractive bool,
		workspaceKind session.WorkspaceKind,
	) (*web.EngineConfig, error) {
		scratch := exec == nil && session.NormalizeWorkspaceKind(workspaceKind) == session.WorkspaceScratch
		// Per-task config snapshot, so a live task is insulated from mid-run
		// edits (the shared copy in internal/web is guarded by cfgMu). In setup
		// mode there is no valid config on disk yet — LoadConfig always fails —
		// so fall back to the minimal startup config. Setup mode exists to serve
		// the settings UI so the user can finish setup; hard-failing here aborts
		// the bootstrap engine before the server ever listens, making first-run
		// setup unreachable.
		taskCfg, err := config.LoadConfig()
		if err != nil {
			if !needsSetup {
				return nil, fmt.Errorf("load task config: %w", err)
			}
			taskCfg = cfg
		}
		startMode := startupMode
		if modeStr != "" {
			startMode = mode.Parse(modeStr)
		}
		if exec == nil && !scratch { // project config overlay (project-bound local tasks only)
			// Seed the shared managed deny-read policy from the user-owned
			// global config before the project overlay (deny rules are never
			// project-mergeable; refreshes only strengthen the live policy).
			tools.InitManagedDenyRead(taskCfg)
			config.ApplyProjectOverlay(taskCfg, taskPwd)
		} else {
			// Remote and scratch tasks skip project config. Environment variables
			// are user-owned process state and still apply to both. Deny rules
			// still seed from the global config — a remote workspace must not
			// carry a weaker rule set than local.
			tools.InitManagedDenyRead(taskCfg)
			config.ApplyEnvOverlay(taskCfg)
		}
		// Fresh execution environment for this task only.
		tenv := tools.NewEnv(taskPwd, platform)
		tenv.AutomationStore = autoStore
		tenv.Browser = browserMgr
		tenv.Computer = computerMgr
		promptPlatform := platform
		envLabel := "local"
		projectKey := taskPwd
		var taskEnvInfo *util.EnvInfo
		// Per-task skill loader: project skills are scanned into THIS task's loader
		// so concurrent tasks in different projects don't bleed each other's project
		// skills into one shared accumulator. (The process-wide skillLoader stays
		// for the path-agnostic slash/list/toggle management UI.)
		taskLoader := skills.NewLoaderWithDisabled(taskCfg.DisabledSkills)
		if exec != nil {
			tenv.SetRemote(exec, taskPwd)
			promptPlatform = exec.Platform()
			envLabel = fmt.Sprintf("%s (pwd: %s)", exec.Label(), taskPwd)
			projectKey = exec.ProjectLabel(taskPwd)
		} else {
			if !scratch {
				taskLoader.ScanProjectSkills(taskPwd)
			}
			taskEnvInfo = util.CollectEnvInfo(taskPwd)
		}

		// Per-task flow loader (builtin + user + this task's project workflows),
		// shared with the workflow_run tool so slash triggers and inline runs
		// resolve the same set. Project workflows only apply to a local exec.
		taskFlowLoader := flow.NewLoader()
		if exec == nil && !scratch {
			taskFlowLoader.LoadProject(taskPwd)
		}

		tbg := tools.NewBackgroundManager(tenv)
		trec, _ := session.NewRecorder(projectKey, providerName, modelName)
		if trec != nil {
			trec.SetWorkspaceKind(workspaceKind)
		}
		if taskID != "" && trec != nil {
			trec.SetUUID(taskID)
		}
		if trec != nil {
			tenv.SessionIDFn = trec.UUID
		}
		imageLedger, imageLedgerErr := newImageUsageLedger(trec)
		if imageLedgerErr != nil {
			config.Logger().Printf("[image] initialize web usage ledger: %v", imageLedgerErr)
		}
		providerSearchLedger, providerSearchLedgerErr := newProviderSearchUsageLedger(trec)
		if providerSearchLedgerErr != nil {
			config.Logger().Printf("[provider-search] initialize web usage ledger: %v", providerSearchLedgerErr)
		}
		// LLM session titles ride the small model (checked at fire time).
		// Resumed tasks (existing session file) never re-trigger titling.
		attachTitleRefiner(ctx, trec)
		ttok := &internalmodel.TokenUsage{}
		tplan := tools.NewPlanStore()
		tappr := runner.NewApprovalStateWithMode(taskPwd, startMode)
		twh := handler.NewWebHandler()
		tnotify := makeNotifyingHandler(twh)
		tappr.SetHandler(tnotify)
		// Provide the config/platform needed to lazily build the LLM reviewer when
		// this task enters Auto mode. The engine installs the transcript provider.
		tappr.SetReviewerConfig(taskCfg, platform)
		// Site-permission lookup for browser tools: an origin marked "allow" for a
		// class (navigate/interact) is auto-approved. Reads the live config each
		// call so settings changes take effect without rebuilding the task.
		tappr.SetBrowserPermFunc(func(origin, class string) bool {
			currentCfg, loadErr := config.LoadConfig()
			return loadErr == nil && browserSitePreapproved(currentCfg, origin, class)
		})
		// browser_act's args carry no URL, so its per-site permission check needs
		// the active tab's origin from THIS task's session.
		tappr.SetBrowserOriginFunc(tenv.CurrentBrowserOrigin)

		// Same shape for computer use: origin ↔ bundle id, and computer_act's
		// args carry no app identity, so the frontmost app must come from THIS
		// task's session.
		tappr.SetComputerPermFunc(func(bundleID, class string) bool {
			return computerMgr != nil && computerMgr.Preapproved(bundleID, class)
		})
		tappr.SetComputerAppFunc(tenv.CurrentComputerApp)

		// Wire THIS task's todo/goal stores to THIS task's recorder + handler, so
		// todos persist on resume and goal changes reach the task's UI and session
		// file. (Each engine is built by this factory, including the bootstrap, so
		// this replaces the old single bootstrap-only wiring in NewServer.)
		if tenv.TodoStore != nil {
			tenv.TodoStore.OnUpdate = func(items []tools.TodoItem) {
				if trec == nil || !trec.HasRecording() {
					return
				}
				snap := make([]session.TodoSnapshotItem, len(items))
				for i, it := range items {
					snap[i] = session.TodoSnapshotItem{ID: it.ID, Title: it.Title, Status: string(it.Status)}
				}
				trec.RecordTodoSnapshot(snap)
			}
		}
		if tenv.GoalStore != nil {
			tenv.GoalStore.OnUpdate = func(g *tools.Goal) {
				if trec != nil && trec.HasRecording() {
					tools.GoalRecorderHook(trec)(g)
				}
				twh.Emit("goal_update", g)
			}
		}

		// Background memory distillation per task session (gates inside).
		// Local sessions only: for remote (SSH/Docker) tasks taskPwd is a path
		// on the remote host — the memory store and session index are keyed to
		// the local machine, so a remote path would just create a junk scope
		// and never match any sessions.
		if exec == nil && !scratch {
			mempipeline.MaybeStartBackground(taskCfg, taskPwd)
		}

		// Re-render prompts on every agent build. Memory settings and curated
		// summaries can change while the web server is running; rebuilding an agent
		// must therefore refresh both schema and prompt, not reuse a startup string.
		renderPrompts := func() (string, string) {
			skillDescs := taskLoader.Descriptions()
			if exec != nil {
				return prompts.GetSystemPrompt(promptPlatform, taskPwd, envLabel, nil, skillDescs),
					prompts.GetPlanSystemPrompt(promptPlatform, taskPwd, envLabel, nil)
			}
			return prompts.GetSystemPrompt(platform, taskPwd, "local", taskEnvInfo, skillDescs),
				prompts.GetPlanSystemPrompt(platform, taskPwd, "local", taskEnvInfo)
		}
		providerRuntimeLoader := webProviderRuntimeConfigLoader(taskPwd, exec != nil || scratch)

		// Snapshot and wrap process-wide raw MCP endpoints for this task. The
		// ledger is created once above and reused across every model/mode/config
		// rebuild, while each wrapper captures the latest verified config epoch.
		currentTaskMCPTools := func(
			agentCfg *config.Config,
			planMode bool,
			headless bool,
		) ([]tool.BaseTool, error) {
			activeProvider, activeModel := agentCfg.GetProviderModel()
			return configuredProviderMCPTools(
				ctx, agentCfg, trec, providerSearchLedger, mcpToolsPtr.Load(),
				planMode, webTaskBillableAllowed(
					session.SessionToolWebSearch, exec != nil, excludeInteractive || headless,
				),
				activeChatProviderRuntimeConfigLoader(
					providerRuntimeLoader, activeProvider, activeModel,
				),
			)
		}

		toolSearchCounts := func(plan agent.ToolPlan) web.ToolSearchCounts {
			mcpDeferred := 0
			for _, descriptor := range plan.Deferred {
				if descriptor.Source == "mcp" || strings.HasPrefix(descriptor.Source, "mcp:") {
					mcpDeferred++
				}
			}
			return web.ToolSearchCounts{
				DirectCount:      len(plan.Direct),
				DeferredCount:    len(plan.Deferred),
				MCPDeferredCount: mcpDeferred,
			}
		}

		buildAllTools := func(
			cm model.ToolCallingChatModel,
			agentCfg *config.Config,
			planMode bool,
			headless bool,
		) []tool.BaseTool {
			// One factory serves subagent + workflow model overrides (incl.
			// the "small" alias); fallback is this task's current model.
			factory := internalmodel.NewModelFactory(agentCfg, cm)
			factory.SetExternalModelResolver(func(resolveCtx context.Context, provider, modelID string) (*internalmodel.ExternalModel, error) {
				if _, isCloud := cloud.ParseCloudProviderRef(provider); !isCloud {
					return nil, nil
				}
				catalogModel, proxyBase, deviceToken, err := cloud.ResolveCloudModel(
					resolveCtx, agentCfg, provider, modelID,
				)
				if err != nil {
					return nil, fmt.Errorf("resolve Cloud model %s/%s: %w", provider, modelID, err)
				}
				vision := catalogModel.Capabilities.Image
				return &internalmodel.ExternalModel{
					Provider: catalogModel.Kind,
					Model:    catalogModel.UpstreamModelID,
					BaseURL:  proxyBase,
					Config: &config.ProviderConfig{
						APIKey: deviceToken, Vision: &vision,
						ReasoningEffort: config.ResolveEffort(provider, modelID, ""),
					},
				}, nil
			})
			agentRoles := config.LoadAgentRoles(taskPwd)
			all := []tool.BaseTool{
				tenv.NewReadTool(), tenv.NewEditTool(), tenv.NewWriteTool(),
				tenv.NewExecuteTool(tbg), tenv.NewGrepTool(),
				tenv.NewTodoWriteTool(), tenv.NewTodoReadTool(),
				tenv.NewGoalSetTool(), tenv.NewGoalGetTool(), tenv.NewGoalUpdateTool(),
				tenv.NewAutomationCreateTool(),
				tenv.NewAutomationListTool(),
				tenv.NewAutomationDeleteTool(),
				tenv.NewSwitchEnvTool(),
				tenv.NewCheckBackgroundTool(tbg),
				tenv.NewSubagentTool(&tools.SubagentDeps{
					ChatModel:    cm,
					ModelFactory: factory,
					Recorder:     trec,
					Notifier: func(name, agentType string, done bool, result string, err error) {
						twh.OnSubagentEvent(name, agentType, done, result, err)
					},
					ProgressFn: func(agentName, event, toolName, detail string) {
						twh.OnSubagentProgress(agentName, event, toolName, detail)
					},
					AgentRoles: agentRoles,
				}),
				tenv.NewWorkflowRunTool(&tools.WorkflowToolDeps{
					ModelFactory: factory,
					Recorder:     trec,
					Loader:       taskFlowLoader,
					AgentRoles:   agentRoles,
				}),
				tools.NewAskUserTool(&tools.AskUserDeps{
					BatchRequestFn: twh.RequestAskUser,
				}),
				skills.NewLoadSkillTool(taskLoader),
			}
			if imageGenerationEnabled(
				agentCfg, planMode, webTaskBillableAllowed(
					session.SessionToolImageGeneration, exec != nil, excludeInteractive,
				),
			) {
				if imageTool, imageErr := configuredGenerateImageTool(
					agentCfg, artifactService, trec, imageLedger,
					providerRuntimeLoader, tnotify,
					func(record artifact.Record) { twh.Emit("artifact_upserted", record) },
				); imageErr == nil {
					all = append(all, imageTool)
				} else if agentCfg.ImageModel != "" {
					config.Logger().Printf("[image] web generate_image unavailable: %v", imageErr)
				}
			}
			if config.MemoryEnabled(agentCfg) && !scratch {
				all = append(all, tenv.NewMemoryNoteTool(&tools.MemoryNoteDeps{
					SessionIDFn: func() string {
						if trec != nil {
							return trec.UUID()
						}
						return ""
					},
				}))
			}
			all = append(all, tenv.NewBrowserTools()...)
			all = append(all, tenv.NewComputerTools()...)
			if exec == nil {
				all = append(all, tenv.NewShowArtifactTool(&tools.ShowArtifactDeps{
					SessionID:    trec.UUID,
					Recorder:     trec,
					Service:      artifactService,
					Emit:         twh.Emit,
					ForceNoFocus: excludeInteractive || headless,
				}))
			}
			// Automation runs are unattended — drop interactive tools that would
			// otherwise block on a human who isn't there (see dropInteractiveTools).
			if excludeInteractive || headless {
				all = dropInteractiveTools(all)
			}
			return all
		}

		buildPlanTools := func() []tool.BaseTool {
			plan := []tool.BaseTool{
				tenv.NewReadTool(),
				tenv.NewPlanExecuteTool(),
				tenv.NewGrepTool(),
				tenv.NewTodoWriteTool(), tenv.NewTodoReadTool(),
				tenv.NewGoalSetTool(), tenv.NewGoalGetTool(), tenv.NewGoalUpdateTool(),
				tenv.NewAutomationListTool(),
				tools.NewAskUserTool(&tools.AskUserDeps{
					BatchRequestFn: twh.RequestAskUser,
				}),
			}
			// Plan mode gets the read-only browser subset (look, don't change).
			plan = append(plan, tenv.NewBrowserPlanTools()...)
			plan = append(plan, tenv.NewComputerPlanTools()...)
			if exec == nil {
				plan = append(plan, tenv.NewShowArtifactTool(&tools.ShowArtifactDeps{
					SessionID:    trec.UUID,
					Recorder:     trec,
					Service:      artifactService,
					Emit:         twh.Emit,
					ForceNoFocus: excludeInteractive,
				}))
			}
			return plan
		}

		// Per-task compaction paths — transcript + reduction must be task-scoped or
		// concurrent summarization across tasks would corrupt a shared file.
		taskUUID := "task"
		if trec != nil {
			taskUUID = trec.UUID()
		}
		transcriptPath := filepath.Join(config.ConfigDir(), "transcripts", taskUUID+".txt")
		reductionRoot := filepath.Join(config.ConfigDir(), "reduction", taskUUID)
		_ = os.MkdirAll(filepath.Dir(transcriptPath), 0o755)
		_ = os.MkdirAll(reductionRoot, 0o755)

		makeAgent := func(
			cm model.ToolCallingChatModel,
			ctxLimit int,
			planMode bool,
			headless bool,
			roleName string,
			activeProvider string,
			activeModel string,
		) (*adk.ChatModelAgent, error) {
			agentCfg, loadErr := config.LoadConfig()
			if loadErr != nil {
				return nil, fmt.Errorf("reload agent config: %w", loadErr)
			}
			// Hot-reload: re-merge managed deny-read rules from the fresh
			// config (union — additions apply live, removals do not).
			tools.InitManagedDenyRead(agentCfg)
			if exec == nil && !scratch {
				config.ApplyProjectOverlay(agentCfg, taskPwd)
			} else {
				config.ApplyEnvOverlay(agentCfg)
			}
			// Model switches and custom-agent role overrides build the candidate
			// before the persisted global selection changes. Project the exact
			// model used by this candidate so provider-owned tools follow its chat
			// provider instead of stale config state.
			projectActiveChatModel(agentCfg, activeProvider, activeModel)
			var middlewares []adk.ChatModelAgentMiddleware
			if langfuseTracer != nil {
				middlewares = append(middlewares, langfuseTracer.AgentMiddleware())
			}

			var handlers []adk.ChatModelAgentMiddleware

			compactThreshold := agentCfg.CompactionThreshold()

			summMw, err := summarization.New(ctx, &summarization.Config{
				Model: cm,
				Trigger: &summarization.TriggerCondition{
					ContextTokens: int(float64(ctxLimit) * compactThreshold),
				},
				TranscriptFilePath: transcriptPath,
			})
			if err == nil {
				handlers = append(handlers, summMw)
			}

			reductionMw, err := reduction.New(ctx, agent.BuildReductionConfig(
				reductionRoot,
				ctxLimit,
				compactThreshold,
				internalmodel.NewCalibratedCounter(ttok).Count,
			))
			if err != nil {
				config.Logger().Printf("[web] reduction middleware init error: %v", err)
			} else {
				handlers = append(handlers, reductionMw)
			}
			// Aggregate cap on one turn's NEW tool results: reduction only caps
			// each result individually (50k), so N parallel calls could still
			// flood a single request. Registered after reduction so per-result
			// truncation runs first.
			handlers = append(handlers, agent.NewTurnToolResultBudgetMiddleware(0))

			// Env-drift/AGENTS.md refresh only applies to local tasks: for a
			// remote executor taskPwd lives on the remote host, so collecting
			// local state for it would be meaningless. Empty Pwd = feature off.
			reminderPwd, reminderSnapshot := "", ""
			if exec == nil {
				reminderPwd = taskPwd
				reminderSnapshot = prompts.SerializeEnvInfo(platform, taskPwd, "local", taskEnvInfo)
			}
			reminderMw := agent.NewReminderMiddleware(agent.ReminderConfig{
				TodoStore:    tenv.TodoStore,
				GoalStore:    tenv.GoalStore,
				PlanStore:    tplan,
				EnvLabel:     "local",
				IsRemote:     tenv.IsRemote(),
				ContextLimit: ctxLimit,
				FileTracker:  tenv.FileTracker,
				Env:          tenv,
				Pwd:          reminderPwd,
				Platform:     platform,
				EnvSnapshot:  reminderSnapshot,
			}, ttok)
			handlers = append(handlers, reminderMw)

			systemPrompt, planPrompt := renderPrompts()
			prompt := systemPrompt
			var toolList []tool.BaseTool
			selectedRole, roleErr := optionalCustomAgentRole(taskPwd, roleName)
			if roleErr != nil {
				return nil, fmt.Errorf("custom agent %q is no longer available", roleName)
			}
			if planMode {
				prompt = planPrompt
				toolList = buildPlanTools()
			} else {
				toolList = buildAllTools(cm, agentCfg, false, headless)
			}
			prompt = withCustomAgentPrompt(prompt, roleName, selectedRole)
			approvalFn := tappr.RequestApproval
			if headless {
				headlessApproval := runner.NewApprovalStateWithMode(taskPwd, mode.FullAccess)
				headlessApproval.SetHandler(tnotify)
				approvalFn = headlessApproval.RequestApproval
			}

			// Snapshot MCP exactly once so the candidate catalog and runtime plan
			// cannot observe different reload generations while an agent is built.
			currentMCPTools, mcpErr := currentTaskMCPTools(agentCfg, planMode, headless)
			if mcpErr != nil {
				config.Logger().Printf("[provider-search] web task %s MCP catalog filtered: %v", trec.UUID(), mcpErr)
			}

			allowMCP := !planMode
			if !config.ToolSearchEnabled(agentCfg) {
				// Preserve the eager/static path. Plan mode has never exposed MCP
				// tools, while normal mode appends the captured MCP generation.
				if allowMCP {
					toolList = append(toolList, currentMCPTools...)
				}
				return agent.NewAgent(ctx, cm, toolList, prompt, approvalFn, middlewares, handlers)
			}

			toolMode := agent.ToolModeNormal
			if planMode {
				toolMode = agent.ToolModePlan
			}
			toolPlan, err := buildCommandToolPlan(
				ctx,
				toolList,
				currentMCPTools,
				agent.ToolTransportWeb,
				toolMode,
			)
			if err != nil {
				return nil, fmt.Errorf("build web tool plan: %w", err)
			}
			return agent.NewAgentWithToolPlan(
				ctx,
				cm,
				toolPlan,
				prompt,
				approvalFn,
				middlewares,
				handlers,
			)
		}

		// Per-task chat-model cache so a model/mode switch rebuilds only this task.
		// cmMu serializes the cache against breakdownFn (a GET handler) reading it.
		var cmMu sync.Mutex
		var currentCM model.ToolCallingChatModel
		var currentCtxLimit int
		currentPlanMode := startMode.IsPlan()
		currentRole := ""
		currentProvider, currentModel := providerName, modelName

		// ToolSearch counts are derived on demand from the latest persisted policy,
		// MCP catalog and task mode. Candidate agents can be discarded by revision
		// checks during concurrent model/settings rebuilds; publishing counts while
		// building such a candidate would make Settings describe an agent that was
		// never installed.
		toolSearchStats := func() web.ToolSearchCounts {
			cmMu.Lock()
			cm, planMode, roleName := currentCM, currentPlanMode, currentRole
			activeProvider, activeModel := currentProvider, currentModel
			cmMu.Unlock()
			if cm == nil {
				return web.ToolSearchCounts{}
			}
			agentCfg, loadErr := config.LoadConfig()
			if loadErr != nil {
				return web.ToolSearchCounts{}
			}
			if exec == nil && !scratch {
				config.ApplyProjectOverlay(agentCfg, taskPwd)
			} else {
				config.ApplyEnvOverlay(agentCfg)
			}
			projectActiveChatModel(agentCfg, activeProvider, activeModel)
			toolList := buildAllTools(cm, agentCfg, planMode, false)
			toolMode := agent.ToolModeNormal
			if planMode {
				toolList = buildPlanTools()
				toolMode = agent.ToolModePlan
			} else if _, ok := config.LoadAgentRoles(taskPwd)[roleName]; roleName != "" && !ok {
				return web.ToolSearchCounts{}
			}
			currentMCPTools, mcpErr := currentTaskMCPTools(agentCfg, planMode, false)
			if mcpErr != nil {
				return web.ToolSearchCounts{}
			}
			plan, planErr := buildCommandToolPlan(
				ctx,
				toolList,
				currentMCPTools,
				agent.ToolTransportWeb,
				toolMode,
			)
			if planErr != nil {
				return web.ToolSearchCounts{}
			}
			return toolSearchCounts(plan)
		}

		createAgent := func(prov, mod string) (*adk.ChatModelAgent, error) {
			cm, ctxLimit, err := newChatModel(prov, mod)
			if err != nil {
				return nil, err
			}
			cmMu.Lock()
			plan, roleName := currentPlanMode, currentRole
			cmMu.Unlock()
			ag, err := makeAgent(cm, ctxLimit, plan, false, roleName, prov, mod)
			if err != nil {
				return nil, err // don't poison the cache with a model whose agent failed to build
			}
			cmMu.Lock()
			currentCM = cm
			currentCtxLimit = ctxLimit
			currentProvider = prov
			currentModel = mod
			cmMu.Unlock()
			return ag, nil
		}

		rebuildForMode := func(planMode bool) (*adk.ChatModelAgent, error) {
			cmMu.Lock()
			previousPlanMode := currentPlanMode
			currentPlanMode = planMode
			cm, ctxLimit, roleName := currentCM, currentCtxLimit, currentRole
			activeProvider, activeModel := currentProvider, currentModel
			cmMu.Unlock()
			if cm == nil {
				ag, createErr := createAgent(providerName, modelName)
				if createErr != nil {
					cmMu.Lock()
					currentPlanMode = previousPlanMode
					cmMu.Unlock()
				}
				return ag, createErr
			}
			ag, makeErr := makeAgent(cm, ctxLimit, planMode, false, roleName, activeProvider, activeModel)
			if makeErr != nil {
				cmMu.Lock()
				currentPlanMode = previousPlanMode
				cmMu.Unlock()
			}
			return ag, makeErr
		}

		rebuildForAutomation := func() (*adk.ChatModelAgent, error) {
			cmMu.Lock()
			cm, ctxLimit, roleName := currentCM, currentCtxLimit, currentRole
			activeProvider, activeModel := currentProvider, currentModel
			cmMu.Unlock()
			if cm == nil {
				var modelErr error
				cm, ctxLimit, modelErr = newChatModel(activeProvider, activeModel)
				if modelErr != nil {
					return nil, modelErr
				}
			}
			return makeAgent(cm, ctxLimit, false, true, roleName, activeProvider, activeModel)
		}

		rebuildForRole := func(
			roleName, baseProvider, baseModel string,
		) (*web.AgentRoleBuild, error) {
			_, targetProvider, targetModel, resolveErr := resolveWebCustomAgentSelection(
				taskPwd, roleName, baseProvider, baseModel,
			)
			if resolveErr != nil {
				return nil, resolveErr
			}
			cmMu.Lock()
			cm, ctxLimit, planMode := currentCM, currentCtxLimit, currentPlanMode
			cmMu.Unlock()
			if cm == nil || targetProvider != baseProvider || targetModel != baseModel {
				var modelErr error
				cm, ctxLimit, modelErr = newChatModel(targetProvider, targetModel)
				if modelErr != nil {
					return nil, modelErr
				}
			}
			ag, makeErr := makeAgent(cm, ctxLimit, planMode, false, roleName, targetProvider, targetModel)
			if makeErr != nil {
				return nil, makeErr
			}
			cmMu.Lock()
			currentRole = roleName
			currentCM = cm
			currentCtxLimit = ctxLimit
			currentProvider = targetProvider
			currentModel = targetModel
			cmMu.Unlock()
			return &web.AgentRoleBuild{
				Agent: ag, Provider: targetProvider, Model: targetModel,
			}, nil
		}

		breakdownFn := func() usage.ContextBreakdown {
			var b usage.ContextBreakdown
			skillDesc := taskLoader.Descriptions()
			b.SkillsTokens = usage.Estimate(skillDesc)
			systemPrompt, _ := renderPrompts()
			cmMu.Lock()
			roleName := currentRole
			cm := currentCM
			planMode := currentPlanMode
			activeProvider, activeModel := currentProvider, currentModel
			cmMu.Unlock()
			systemPrompt = withLoadedCustomAgentPrompt(systemPrompt, taskPwd, roleName)
			b.SystemPromptTokens = usage.Estimate(systemPrompt) - b.SkillsTokens
			if b.SystemPromptTokens < 0 {
				b.SystemPromptTokens = 0
			}
			currentCfg, currentCfgErr := config.LoadConfig()
			if currentCfgErr == nil {
				if exec == nil && !scratch {
					config.ApplyProjectOverlay(currentCfg, taskPwd)
				} else {
					config.ApplyEnvOverlay(currentCfg)
				}
				projectActiveChatModel(currentCfg, activeProvider, activeModel)
			}
			currentMCPTools, _ := currentTaskMCPTools(currentCfg, planMode, false)
			for _, t := range currentMCPTools {
				b.MCPToolsTokens += estimateToolTokens(ctx, t)
			}
			if cm != nil {
				if currentCfgErr != nil {
					return b
				}
				total := 0
				toolList := buildAllTools(cm, currentCfg, planMode, false)
				if planMode {
					toolList = buildPlanTools()
				}
				for _, at := range toolList {
					total += estimateToolTokens(ctx, at)
				}
				b.SystemToolsTokens = total
			}
			return b
		}

		var ag *adk.ChatModelAgent
		if !needsSetup {
			var err error
			ag, err = createAgent(providerName, modelName)
			if err != nil {
				// Model availability must not gate the Desktop control plane. In
				// particular, managed accounts that require reauthentication need
				// the settings UI served by this same process in order to recover.
				// Keep the selected provider/model and let the Engine lazily retry
				// agent creation before the next send; it never falls back to a
				// different model silently.
				config.Logger().Printf(
					"[web] selected model %s/%s unavailable while building task engine; control plane remains available: %v",
					providerName, modelName, err,
				)
			}
		}

		return &web.EngineConfig{
			TaskID:               taskID,
			Pwd:                  taskPwd,
			WorkspaceKind:        session.NormalizeWorkspaceKind(workspaceKind),
			Mode:                 startMode.String(),
			ProviderName:         providerName,
			ModelName:            modelName,
			Agent:                ag,
			Env:                  tenv,
			TodoStore:            tenv.TodoStore,
			Recorder:             trec,
			TokenUsage:           ttok,
			ApprovalState:        tappr,
			Handler:              twh,
			EventHandler:         tnotify,
			BreakdownFn:          breakdownFn,
			ToolSearchStats:      toolSearchStats,
			CreateAgent:          createAgent,
			RebuildForMode:       rebuildForMode,
			RebuildForAutomation: rebuildForAutomation,
			RebuildForRole:       rebuildForRole,
			FlowLoader:           taskFlowLoader,
			// Recorders the engine creates later (lazy create / session switch
			// in chat.go) get the same title hook as trec above.
			RecorderInit: func(r *session.Recorder) {
				attachTitleRefiner(ctx, r)
			},
		}, nil
	}

	return startWebServer(webServerRuntime{
		ctx: ctx, port: port, host: host, openBrowser: openBrowser, authToken: authToken,
		cfg: cfg, pwd: pwd, startupMode: startupMode.String(),
		providerName: providerName, modelName: modelName, registry: registry,
		skillLoader: skillLoader, flowLoader: flowLoader, reloadMCP: reloadMCPTools,
		wechatClient: wechatClient, bleProxy: bleProxy, tracer: langfuseTracer,
		needsSetup: needsSetup, automations: autoStore, browserManager: browserMgr,
		computerManager: computerMgr, artifactService: artifactService, buildTask: buildWebTask,
	})
}

func loadWebServerConfig(needsSetup bool) (*config.Config, error) {
	if needsSetup {
		return &config.Config{MaxIterations: 1000}, nil
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}
	// Apply env overlay to the server-level config so JCODE_MODEL,
	// JCODE_DEFAULT_MODE etc. take effect for provider resolution and startup
	// mode. Project overlay is applied per-task in buildWebTask.
	config.ApplyEnvOverlay(cfg)
	return cfg, nil
}

type webTaskBuilder func(
	taskID, taskPwd, modeStr string,
	exec tools.RemoteExecutor,
	excludeInteractive bool,
	workspaceKind session.WorkspaceKind,
) (*web.EngineConfig, error)

type webServerRuntime struct {
	ctx             context.Context
	port            int
	host            string
	openBrowser     bool
	authToken       string
	cfg             *config.Config
	pwd             string
	startupMode     string
	providerName    string
	modelName       string
	registry        *internalmodel.ModelRegistry
	skillLoader     *skills.Loader
	flowLoader      *flow.Loader
	reloadMCP       func(map[string]*config.MCPServer) ([]tools.MCPStatus, error)
	wechatClient    *weixin.Client
	bleProxy        *ble.Proxy
	tracer          *telemetry.LangfuseTracer
	needsSetup      bool
	automations     *automation.Store
	browserManager  *browser.Manager
	computerManager *computer.Manager
	artifactService *artifact.Service
	buildTask       webTaskBuilder
}

func automationWorkspaceKind(path string) session.WorkspaceKind {
	if managedworkspace.ValidateScratchPath(path) == nil {
		return session.WorkspaceScratch
	}
	return session.WorkspaceProject
}

// startWebServer owns the transport lifecycle after runWebServer has assembled
// the process-wide dependencies and per-task engine factory.
func startWebServer(runtime webServerRuntime) error {
	webToken, requireAuth, err := resolveWebToken(runtime.host, runtime.authToken)
	if err != nil {
		return err
	}
	if requireAuth {
		fmt.Printf("\n🔐 Web access token (required when reaching %s):\n   %s\n", runtime.host, webToken)
		fmt.Printf("   Open http://%s:%d/ and paste this token to sign in.\n\n", runtime.host, runtime.port)
		config.Logger().Printf("[web] token auth enabled for non-loopback bind %q", runtime.host)
	}

	cloudSup := newCloudSupervisor(runtime.cfg, runtime.port, webToken)
	bootEC, err := runtime.buildTask(
		"", runtime.pwd, runtime.startupMode, nil, false, session.WorkspaceProject,
	)
	if err != nil {
		return err
	}
	bootNotifying, _ := bootEC.EventHandler.(*handler.NotifyingHandler)

	srv := web.NewServer(&web.ServerConfig{
		Port:                 runtime.port,
		Host:                 runtime.host,
		OpenBrowser:          runtime.openBrowser,
		Pwd:                  runtime.pwd,
		Version:              Version,
		Agent:                bootEC.Agent,
		CreateAgent:          bootEC.CreateAgent,
		RebuildForMode:       bootEC.RebuildForMode,
		RebuildForAutomation: bootEC.RebuildForAutomation,
		RebuildForRole:       bootEC.RebuildForRole,
		NewEngine: func(taskID, taskPwd, modeStr string) (*web.EngineConfig, error) {
			return runtime.buildTask(taskID, taskPwd, modeStr, nil, false, session.WorkspaceProject)
		},
		NewScratchEngine: func(taskID, taskPwd, modeStr string) (*web.EngineConfig, error) {
			return runtime.buildTask(taskID, taskPwd, modeStr, nil, false, session.WorkspaceScratch)
		},
		NewRemoteEngine: func(
			taskID string, exec tools.RemoteExecutor, remotePwd, modeStr string,
		) (*web.EngineConfig, error) {
			return runtime.buildTask(taskID, remotePwd, modeStr, exec, false, session.WorkspaceProject)
		},
		NewAutomationEngine: func(taskID, taskPwd, modeStr string) (*web.EngineConfig, error) {
			return runtime.buildTask(taskID, taskPwd, modeStr, nil, true, automationWorkspaceKind(taskPwd))
		},
		WorkspaceKind:      session.WorkspaceProject,
		InitialMode:        runtime.startupMode,
		TodoStore:          bootEC.TodoStore,
		Recorder:           bootEC.Recorder,
		Tracer:             runtime.tracer,
		Env:                bootEC.Env,
		ProviderName:       runtime.providerName,
		ModelName:          runtime.modelName,
		Config:             runtime.cfg,
		Registry:           runtime.registry,
		ApprovalState:      bootEC.ApprovalState,
		SkillLoader:        runtime.skillLoader,
		FlowLoader:         runtime.flowLoader,
		ReloadMCP:          runtime.reloadMCP,
		WechatClient:       runtime.wechatClient,
		WebHandler:         bootEC.Handler,
		EventHandler:       bootEC.EventHandler,
		NeedsSetup:         runtime.needsSetup,
		TokenUsage:         bootEC.TokenUsage,
		ContextBreakdownFn: bootEC.BreakdownFn,
		ToolSearchStats:    bootEC.ToolSearchStats,
		Automations:        runtime.automations,
		AuthToken:          webToken,
		RequireAuth:        requireAuth,
		BrowserManager:     runtime.browserManager,
		ComputerManager:    runtime.computerManager,
		MemoryStart: func(runCtx context.Context, project string) (<-chan error, error) {
			currentCfg, loadErr := config.LoadConfig()
			if loadErr != nil {
				return nil, fmt.Errorf("load memory config: %w", loadErr)
			}
			return mempipeline.Start(runCtx, currentCfg, project, mempipeline.Options{
				IncludeRecent: true, IgnoreCooldown: true,
				Log: func(format string, args ...any) {
					config.Logger().Printf("[memory] "+format, args...)
				},
			})
		},
		BLEController:   runtime.bleProxy,
		CloudSupervisor: cloudSup,
		ArtifactService: runtime.artifactService,
	})

	if runtime.automations != nil {
		sched := automation.NewScheduler(runtime.automations, srv.AutomationRunner())
		go sched.Run(runtime.ctx)
	}
	if len(providertools.EffectiveMCPServers(runtime.cfg)) > 0 {
		srv.ReloadMCPInBackground()
	}

	runtime.wechatClient.SetOnMessage(func(from, text string) {
		if runtime.wechatClient.State() != channel.StateEnabled {
			return
		}
		config.Logger().Printf("[wechat] inbound message from %s: %s", from, text)
		accepted, submitErr := srv.SubmitMessage(text, "wechat")
		if submitErr != nil {
			if sendErr := runtime.wechatClient.SendText(channel.DoneMessage("", submitErr)); sendErr != nil {
				config.Logger().Printf("[wechat] failed to send submission error: %v", sendErr)
			}
		} else if !accepted {
			if sendErr := runtime.wechatClient.SendText(channel.BusyMessage()); sendErr != nil {
				config.Logger().Printf("[wechat] failed to send busy response: %v", sendErr)
			}
		}
	})
	defer func() {
		if bootNotifying != nil {
			bootNotifying.CloseNotifiers()
		}
		if runtime.wechatClient.State() == channel.StateEnabled {
			go func() { _ = runtime.wechatClient.SendText(channel.GoodbyeMessage(time.Now())) }()
			time.Sleep(500 * time.Millisecond)
			_ = runtime.wechatClient.Disable()
		}
	}()

	srv.SetupNativeMessaging()
	cloudSup.Start(runtime.ctx)
	if err := srv.Start(runtime.ctx); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	srv.CloseAllEngines()
	_ = runtime.browserManager.Close()
	if runtime.tracer != nil {
		runtime.tracer.Flush()
	}
	return nil
}
