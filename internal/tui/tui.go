package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/cnjack/jcode/internal/config"
	"github.com/cnjack/jcode/internal/mode"
	"github.com/cnjack/jcode/internal/tools"
)

// --- Model ---

type Mode int

const (
	ModeAgent Mode = iota
)

const (
	sidebarWidth       = 36
	minWidthForSidebar = 90
)

type Model struct {
	mode      Mode
	agentDone bool

	lines       []contentLine
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

	// ─── Status line state ───
	// pendingToolTitle/Subtitle mirror the display info of the most recent
	// tool call for the status detail row ("└ Shell: git push origin main").
	pendingToolTitle    string
	pendingToolSubtitle string
	// runningTools counts announced tool calls that have not reported a
	// result yet; >1 means a concurrent batch is in flight.
	runningTools int
	// runPausedTotal accumulates time spent in approval dialogs during the
	// current run; runPauseStart is non-zero while a dialog is open. The
	// status-line stopwatch subtracts both so waiting on the user is free.
	runPausedTotal time.Duration
	runPauseStart  time.Time

	// ─── Tool-call line tracking ───
	// toolLines maps a toolCallID to the timeline line holding its status
	// icon so a result flips the exact line, even when a concurrent batch
	// returns out of order. m.lines is append-only during a run, so an index
	// is a stable handle; flips re-validate the icon before replacing and
	// fall back to the legacy last-running-icon scan when stale (cleared
	// history, legacy replays without IDs).
	toolLines map[string]toolLineRef
	// batchLines maps a BatchID to its header-line stacking state until every
	// member of the batch has completed.
	batchLines map[string]*batchLineState

	// ─── Activity-group tracking (structured tool lines) ───
	// groupMembers maps a toolCallID to its activity-group member so a result
	// mutates structured state in place; the string-backfill maps above stay
	// only as the fallback for legacy no-ID paths.
	groupMembers map[string]groupMemberRef
	// groupBatches maps a BatchID to its activity group so batch members that
	// arrive after an interruption (approval dialog) still join their group.
	groupBatches map[string]*activityGroupData

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

	sshSavePrompt         bool
	sshSaveAddr           string
	sshSavePath           string
	sshHostKeyPrompt      bool
	sshHostKeyFingerprint string

	history      []string
	historyIndex int

	sessionPicker  list.Model
	pickingSession bool

	// Channel panel state
	channelMenu    list.Model
	showingChannel bool
	channelStates  map[string]string // channelID → state ("none", "disabled", "enabled")

	showingHelp bool // keyboard shortcuts help panel
	helpScroll  int  // scroll offset for help panel

	// ─── Transcript overlay state ───
	// showingTranscript is true while the full-screen transcript pager is
	// open (ctrl+t; ctrl+o during team sessions). transcriptVP holds the
	// snapshot content taken at open time.
	showingTranscript bool
	transcriptVP      viewport.Model

	version string // version string displayed in bottom bar

	agentsMdFound bool

	pwd string

	activeProvider string
	activeModel    string
	textareaLines  int

	pasteStore *PasteStore

	todoStore *tools.TodoStore
	goalStore *tools.GoalStore
	browser   *BrowserController
	computer  *ComputerController

	totalTokens       int64
	modelContextLimit int

	promptStartTime time.Time // when user submitted the current prompt

	pendingPrompts []string

	approvalPending         bool
	approvalToolName        string
	approvalToolArgs        string
	approvalRespChan        chan ToolApprovalResponse
	approvalIsExternal      bool   // Whether this is an external path access
	approvalIsBillable      bool   // Whether this is a bounded provider request
	approvalWorkerName      string // Non-empty for teammate approval
	approvalWorkerColor     string // Teammate color
	approvalAllowApproveAll bool
	approvalOptions         []ToolApprovalOption
	approvalMode            ApprovalMode
	approvalSelected        int                      // 0=Approve, 1=ApproveAll, 2=Reject
	approvalQueue           []ToolApprovalRequestMsg // queued requests when dialog is already active

	envLabel    string
	agentMode   AgentMode
	sessionMode mode.SessionMode // unified selector mode (source of truth for the pill)
	bgRunning   int              // count of running background tasks

	// lastAssistantRawText stores the raw (unrendered) text of the last
	// assistant response, used by Ctrl+Y and /copy to copy to clipboard
	// without picking up structural elements like dividers.
	lastAssistantRawText string

	// ─── Copy picker state (/copy) ───
	// pickingCopy is true while the copy-target picker overlay is open;
	// copyPicker is the list and copyTargets the snapshot it indexes into
	// (taken at open time so streaming cannot drift a selection).
	pickingCopy bool
	copyPicker  list.Model
	copyTargets []copyTarget

	// Plan review state
	planReviewActive   bool
	planReviewTitle    string
	planRejectInput    bool // true when prompting for rejection feedback
	planReviewSelected int  // 0=Approve, 1=Reject, 2=Dismiss

	// Ask user state
	askUserActive   bool
	askUserQuestion string
	askUserOptions  []string
	askUserSelected int // currently highlighted option index

	// Skill slash commands from skill loader
	skillSlashCommands []SkillSlashInfo
	// Workflow slash commands from flow loader (e.g. /repo-audit)
	flowSlashCommands []FlowSlashInfo

	// Subagent progress tracking: one slot per subagent (ordered by start)
	// so parallel subagents don't overwrite each other's live progress.
	subagentSlots []*subagentSlot
	subagentRev   int // bumped on every slot mutation; keys the box cache

	// Exit / cancel confirmation
	exitPending     bool      // true when quit dialog is showing
	exitWarningTime time.Time // when the warning was shown
	exitSelected    int       // 0=Yes, 1=No (default No for safety)

	// Cancel-agent confirmation
	cancelPending  bool // true when cancel-agent dialog is showing
	cancelSelected int  // 0=Cancel, 1=Wait

	// OnApprovalModeChange is called before the approval dialog's "Approve All"
	// response is released. The command layer prepares the target agent, commits
	// the mode journal, and publishes backend state; an error keeps the dialog
	// pending and the selector unchanged.
	OnApprovalModeChange func(enabled bool) error

	// Command autocomplete suggestions
	cmdSuggestionActive bool
	cmdSuggestionIndex  int
	cmdSuggestions      []commandSuggestion

	// Mouse text selection state

	// Team state
	teamState      TeamViewState
	teammateTokens map[string]int64 // per-teammate token usage for status bar
	// teamLeaderLines stores the leader's viewport content when switching to teammate view
	teamLeaderLines []contentLine
	teamLeaderText  string

	// ─── Sidebar state ───
	showSidebar         bool // whether sidebar is currently visible
	sidebarScrollOffset int  // scroll offset for todo list in sidebar
	sidebarComp         *SidebarComponent

	// ─── Manage models state ───
	managingModels     bool
	manageModelsPicker list.Model

	// ─── Theme picker state ───
	pickingTheme       bool
	themePicker        list.Model
	themeBeforePreview string // theme name to restore if the picker is cancelled
	themePersisted     bool   // true when a theme was explicitly chosen/loaded
	//   (suppresses terminal-background auto-detection)

	// ─── Render performance cache ───
	contentDirty      bool   // true when lines/currentText/thinking changed since last render
	renderedContent   string // cached output of renderContent()
	renderedLineWidth int    // the contentWidth() used for the cached render

	// sidebarCache caches the rendered sidebar string between frames.
	sidebarCache      string
	sidebarCacheDirty bool // true when sidebar-affecting state changed
	sidebarCacheH     int  // viewport height when cache was built

	// renderPending tracks whether a batched stream render is scheduled.
	renderPending bool

	// footerCache caches the rendered input area (mode pills + textarea + status bar).
	// It's invalidated when textarea content, mode, bg count, or width changes.
	footerCache  string
	footerCacheW int // textarea width when cache was built
	footerCacheH int // height in lines

	// subagentBoxCache caches the rendered subagent progress box.
	subagentBoxCache      string
	subagentBoxCacheRev   int // m.subagentRev when cached
	subagentBoxCacheWidth int // content width when cached

	renderPerf tuiRenderPerf
}

func NewModel(hasPrompt bool, pwd string, todoStore *tools.TodoStore) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	md, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle(currentTheme.GlamourStyle()),
		glamour.WithWordWrap(96), // default, recreated on first WindowSizeMsg
	)

	initialMode := ModeAgent
	thinking := false
	var initialLines []contentLine
	if hasPrompt {
		thinking = true
	} else {
		initialLines = []contentLine{
			textLine(lipgloss.NewStyle().Foreground(colorMuted).Render("Welcome to JCODE. How can I help you today?")),
			textLine(""),
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
	ml.SetFilteringEnabled(true)

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

	// Channel menu list
	channelDel := list.NewDefaultDelegate()
	channelDel.SetSpacing(0)
	chl := list.New([]list.Item{}, channelDel, 0, 0)
	chl.Title = "Channels"
	chl.SetShowHelp(false)

	m := Model{
		mode:              initialMode,
		spinner:           s,
		thinking:          thinking,
		mdRenderer:        md,
		textarea:          newTextarea(),
		textareaLines:     1,
		currentText:       &strings.Builder{},
		sidebarComp:       NewSidebarComponent(),
		dirList:           l,
		modelPicker:       ml,
		settingMenu:       sl,
		sshAliasPicker:    sal,
		sessionPicker:     sesl,
		channelMenu:       chl,
		channelStates:     make(map[string]string),
		toolLines:         make(map[string]toolLineRef),
		batchLines:        make(map[string]*batchLineState),
		groupMembers:      make(map[string]groupMemberRef),
		groupBatches:      make(map[string]*activityGroupData),
		pwd:               pwd,
		history:           loadHistory(),
		todoStore:         todoStore,
		pasteStore:        NewPasteStore(),
		lines:             initialLines,
		envLabel:          "Local",
		approvalMode:      ModeManual, // Default to manual approval mode
		contentDirty:      true,
		sidebarCacheDirty: true,
		renderPerf:        newTUIRenderPerf(),
	}
	m.historyIndex = len(m.history)
	m.sessionMode = mode.Approval // Default unified selector mode

	if cfg, err := config.LoadConfig(); err == nil {
		m.activeProvider, m.activeModel = cfg.GetProviderModel()
		if cfg.AutoApprove { //nolint:staticcheck // intentional fallback to the deprecated field when DefaultMode is unset
			m.approvalMode = ModeAuto
		}
	}

	m.logRenderPerf("start")

	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		textarea.Blink,
		// Ask the terminal for its background color so we can auto-pick a light
		// or dark default theme when the user hasn't chosen one explicitly.
		tea.RequestBackgroundColor,
		// Force BubbleTea to use GraphemeWidth mode and enable Unicode Core
		// mode (2027) on the terminal. This synchronizes the renderer's width
		// calculation with the terminal, fixing emoji border alignment.
		func() tea.Msg {
			return tea.ModeReportMsg{
				Mode:  ansi.ModeUnicodeCore,
				Value: ansi.ModeReset,
			}
		},
	)
}

// cancelAgent shows a confirmation dialog to cancel the running agent.
// The actual cancellation happens when the user confirms.
func (m *Model) requestCancelAgent() {
	if !m.thinking || m.agentDone {
		return
	}
	m.cancelPending = true
	m.cancelSelected = 1 // default to "Wait" (non-destructive, matches Quit dialog's safe "No")
	m.refreshViewport()
}

// confirmCancelAgent executes the agent cancellation after user confirms.
func (m *Model) confirmCancelAgent() {
	m.cancelPending = false
	select {
	case cancelAgentCh <- struct{}{}:
	default:
	}
}

func (m Model) inputActive() bool {
	return (m.mode == ModeAgent || m.sshStep > 0 || m.sshSavePrompt || m.sshHostKeyPrompt) && !m.pickingModel && !m.managingModels && !m.pickingTheme && !m.pickingCopy && !m.showingSetting && !m.showingHelp && !m.showingTranscript && !m.pickingSSHAlias && !m.pickingSession && !m.approvalPending && !m.planReviewActive && !m.askUserActive
}

// ModelOption configures a Model before the BubbleTea program starts.
type ModelOption func(*Model)

// WithApprovalModeChange sets the durable commit callback invoked when the user
// promotes to auto-approve via the approval dialog's "Approve All".
func WithApprovalModeChange(fn func(bool) error) ModelOption {
	return func(m *Model) {
		m.OnApprovalModeChange = fn
	}
}

// WithStartupMode seeds the initial selector mode so the mode pill reflects the
// resolved startup mode (config DefaultMode / legacy AutoApprove / --unsafe).
func WithStartupMode(sm mode.SessionMode) ModelOption {
	return func(m *Model) {
		m.applySelectorMode(sm)
	}
}

// selectorMode returns the unified selector mode stored in the TUI model.
func (m Model) selectorMode() mode.SessionMode {
	return m.sessionMode
}

// applySelectorMode sets the unified session mode and the two low-level TUI
// fields (tool axis + approval axis) to match it. Plan leaves the approval
// field untouched (read-only tools make it moot).
func (m *Model) applySelectorMode(sm mode.SessionMode) {
	m.sessionMode = sm
	switch sm {
	case mode.Plan:
		m.agentMode = ModePlanning
	case mode.Auto:
		m.agentMode = ModeNormal
		m.approvalMode = ModeManual
	case mode.FullAccess:
		m.agentMode = ModeNormal
		m.approvalMode = ModeAuto
	default: // Approval
		m.agentMode = ModeNormal
		m.approvalMode = ModeManual
	}
}

// promoteToFullAccess handles the approval dialog's "Approve all" as a durable
// mode transition. It updates local selector state only after the command layer
// confirms that the candidate Full access agent and journal commit succeeded.
func (m *Model) promoteToFullAccess() error {
	// The callback prepares the candidate agent and fsyncs the mode journal.
	// Keep this dialog and every queued approval untouched unless that durable
	// commit succeeds; otherwise the runner must not observe ModeAuto.
	if m.OnApprovalModeChange != nil {
		if err := m.OnApprovalModeChange(true); err != nil {
			return err
		}
	}
	m.approvalMode = ModeAuto
	m.agentMode = ModeNormal
	m.sessionMode = mode.FullAccess
	m.invalidateFooterCache()
	return nil
}

// WithVersion sets the version string displayed in the bottom hint bar.
func WithVersion(v string) ModelOption {
	return func(m *Model) {
		m.version = v
	}
}

// WithGoalStore wires the session goal store so the TUI can render the active
// goal indicator. The store is read directly when rendering.
func WithGoalStore(gs *tools.GoalStore) ModelOption {
	return func(m *Model) {
		m.goalStore = gs
	}
}

// BrowserStatus is a snapshot of the browser-use subsystem for `/browser`.
type BrowserStatus struct {
	Available       bool // false when browser use is not wired in this context
	Enabled         bool
	Backend         string // auto | managed | extension
	ChromeFound     bool
	ChromeInfo      string // version or path
	ExtensionOnline bool
	DevMode         bool
}

// BrowserController lets the TUI read browser status and toggle enablement
// without depending on the browser manager directly (which lives in the command
// layer). Nil when browser use is unavailable.
type BrowserController struct {
	Status     func() BrowserStatus
	SetEnabled func(bool) error
}

// ComputerStatus is a snapshot of the computer-use subsystem for `/computer`.
//
// Detail carries the human-readable reason the subsystem is not ready. Computer
// use has three independent gates (enabled / a working backend / macOS
// permission) and a status line that cannot say which one is shut leaves the
// user with nothing to act on.
type ComputerStatus struct {
	Supported       bool // false outside macOS
	Platform        string
	Available       bool // native helper is connected and permissions are ready
	Enabled         bool
	HelperInstalled bool
	HelperConnected bool
	HelperVersion   string
	Accessibility   string // granted | denied | unknown
	ScreenRecording string // granted | denied | unknown
	Blocker         string // unsupported | disabled | no_helper | permissions | ""
	Detail          string
}

// ComputerController lets the TUI read computer-use status and toggle
// enablement without depending on the computer manager directly (which lives in
// the command layer). Nil when computer use is unavailable.
type ComputerController struct {
	Status     func() ComputerStatus
	SetEnabled func(bool) error
	// RequestPermissions surfaces the macOS consent prompts for the helper's
	// TCC grants (/computer grant). Nil on platforms/transports that cannot ask.
	RequestPermissions func() error
}

// WithComputer wires the `/computer` command to the computer-use subsystem.
func WithComputer(cc *ComputerController) ModelOption {
	return func(m *Model) {
		m.computer = cc
	}
}

// WithBrowser wires the `/browser` command to the browser-use subsystem.
func WithBrowser(bc *BrowserController) ModelOption {
	return func(m *Model) {
		m.browser = bc
	}
}

// WithTheme applies the persisted color theme at startup. A non-empty name
// marks the theme as explicit, suppressing terminal-background auto-detection.
func WithTheme(name string) ModelOption {
	return func(m *Model) {
		if name == "" {
			return
		}
		ApplyTheme(name)
		m.themePersisted = true
		// Rebuild the markdown renderer so it matches the applied theme.
		m.recreateMDRenderer()
	}
}

func RunTUI(hasPrompt bool, pwd string, todoStore *tools.TodoStore, opts ...ModelOption) (*tea.Program, Model) {
	m := NewModel(hasPrompt, pwd, todoStore)
	for _, opt := range opts {
		opt(&m)
	}
	p := tea.NewProgram(&m)
	return p, m
}
