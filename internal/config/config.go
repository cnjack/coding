package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	configDir  = ".jcode"
	configFile = "config.json"
)

type ProviderConfig struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url,omitempty"`
	// Auth binds this provider to a locally managed login account. The binding
	// is deliberately non-secret and may be synced with the rest of the provider
	// configuration; access/refresh credentials live in the provider-auth secret
	// store and are resolved immediately before each request. nil preserves the
	// legacy API-key behavior.
	Auth *ProviderAuthBinding `json:"auth,omitempty"`
	// Protocol identifies the provider's chat/request protocol when it differs
	// from the registry default (for example "responses"). Capability-specific
	// protocols belong on their endpoint block instead.
	Protocol string `json:"protocol,omitempty"`
	// Name is an optional display name for custom providers not in the registry.
	Name string `json:"name,omitempty"`
	// Headers are extra HTTP headers injected into every request to this
	// provider's endpoint (e.g. a gateway's "X-Api-Key" or "X-Org-Id"). Values
	// may be secrets — they are masked by the API and never logged.
	Headers map[string]string `json:"headers,omitempty"`
	// Vision, when non-nil, overrides registry detection of image-input support
	// for this provider. nil ⇒ defer to registry metadata (default: allow images).
	Vision *bool `json:"vision,omitempty"`
	// Thinking, when non-nil, explicitly toggles extended reasoning for this
	// provider. It is sent as the OpenAI-compatible chat_template_kwargs
	// {"enable_thinking": <bool>} extension (e.g. qwen3 gateways). nil ⇒ omit.
	Thinking *bool `json:"thinking,omitempty"`
	// ReasoningEffort controls thinking depth via the OpenAI-compatible
	// "reasoning_effort" parameter. One of "", "low", "medium", "high".
	// Empty ⇒ omit the parameter.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// Deprecated: model lists are now sourced from the models.dev registry.
	// Preserved for backward compatibility with existing config files.
	Models []string `json:"models,omitempty"`
	// CustomModels defines additional models for this provider.
	// These are merged into the registry and treated identically to built-in models.
	CustomModels []CustomModelConfig `json:"custom_models,omitempty"`
	// ProviderTools stores policy for provider-bound capabilities such as Web
	// Search. Image generation is selected independently through ImageModel;
	// legacy image_generation entries are read only for optional call limits.
	// Runtime/adapter selection must never be inferred from these keys.
	ProviderTools map[string]ProviderToolPolicy `json:"provider_tools,omitempty"`
	// ImageEndpoint is an explicit, capability-specific endpoint for custom
	// image-generation models. Chat model discovery never populates this block.
	ImageEndpoint *ImageEndpointConfig `json:"image_endpoint,omitempty"`
}

// ProviderAuthBinding identifies one managed login method and, optionally, a
// concrete account. An empty AccountID follows that method's default usable
// account. Method is validated by the provider-management boundary and the
// runtime resolver; unknown values fail closed.
type ProviderAuthBinding struct {
	Method    string `json:"method"`
	AccountID string `json:"account_id,omitempty"`
}

// HasConfiguredChatModels reports whether this provider has an explicit chat
// model list in either the current or legacy config shape.
func (p *ProviderConfig) HasConfiguredChatModels() bool {
	return p != nil && (len(p.CustomModels) > 0 || len(p.Models) > 0)
}

// ProviderToolPolicy controls a provider-bound capability. Such capabilities
// are disabled by default: the zero value is intentionally fail-closed.
type ProviderToolPolicy struct {
	Enabled            bool `json:"enabled,omitempty"`
	MaxCallsPerTurn    int  `json:"max_calls_per_turn,omitempty"`
	MaxCallsPerSession int  `json:"max_calls_per_session,omitempty"`
}

// ImageEndpointConfig describes an image-generation API independently of the
// provider's chat endpoint. Protocol is currently "openai_images" for custom
// OpenAI-compatible Images APIs; unknown protocols remain catalog-visible but
// unresolved so future versions can add adapters without changing the schema.
type ImageEndpointConfig struct {
	Protocol   string             `json:"protocol,omitempty"`
	BaseURL    string             `json:"base_url,omitempty"`
	Models     []ImageModelConfig `json:"models,omitempty"`
	AssetHosts []string           `json:"asset_hosts,omitempty"`
}

// ImageModelConfig is one selectable model served by an ImageEndpointConfig.
type ImageModelConfig struct {
	ID    string   `json:"id"`
	Name  string   `json:"name,omitempty"`
	Sizes []string `json:"sizes,omitempty"`
}

// MediaConfig controls retention for JCode-managed generated media.
type MediaConfig struct {
	RetentionDays int   `json:"retention_days,omitempty"`
	MaxTotalBytes int64 `json:"max_total_bytes,omitempty"`
}

// CustomModelConfig defines a model that can be added via config.
type CustomModelConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	ToolCall  bool   `json:"tool_call,omitempty"`
	Reasoning bool   `json:"reasoning,omitempty"`
	Context   int    `json:"context,omitempty"`
	// Attachment marks the model as accepting image inputs. When false (the
	// default) the model inherits the provider-level Vision override (if set) or
	// the registry default (allow images).
	Attachment bool `json:"attachment,omitempty"`
	// EffortTiers are the selectable reasoning-effort levels for a reasoning
	// model, e.g. ["minimal","low","medium","high","max"]. When Reasoning is true
	// and this is non-empty, it overrides the default standard effort options;
	// when empty the standard set (minimal/low/medium/high) is used.
	EffortTiers []string `json:"effort_tiers,omitempty"`
	// Managed marks account-scoped models discovered from a managed provider.
	// These entries make an explicitly enabled live model available after a
	// restart, but remain read-only in the custom-model editor.
	Managed bool `json:"managed,omitempty"`
	// Protocol records a managed model's provider-declared wire format. It is
	// ignored for API-key custom models and never accepted from the browser form.
	Protocol string `json:"protocol,omitempty"`
	Vendor   string `json:"vendor,omitempty"`
}

// SSHAlias represents a saved SSH connection alias
type SSHAlias struct {
	Name string `json:"name"`
	Addr string `json:"addr"`           // user@host
	Path string `json:"path,omitempty"` // remote working directory
}

// DockerAlias represents a saved Docker container alias
type DockerAlias struct {
	Name      string `json:"name"`
	Container string `json:"container"`      // container name or id
	Path      string `json:"path,omitempty"` // working directory inside the container
}

// MCPServer represents a configured MCP server connection
type MCPServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     []string          `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// TimeoutSeconds is the request timeout for HTTP/SSE transports. 0 → default (180s).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Disabled, when true, excludes this server from tool loading without deleting it.
	Disabled bool `json:"disabled,omitempty"`
	// OAuth configures OAuth 2.0 authorization for HTTP/SSE transports (MCP
	// authorization spec). When nil, only static Headers are used.
	OAuth *MCPOAuthConfig `json:"oauth,omitempty"`
	// Source tracks which config layer defined this server (global | project).
	// Not persisted to disk — populated at load time for UI display.
	Source string `json:"-"`
}

// MCPOAuthConfig holds OAuth 2.0 settings for an MCP server. Tokens are NOT
// stored here — they live in ~/.jcode/oauth/<server>.json. ClientID/ClientSecret
// are the manual fallback used when the authorization server does not support
// dynamic client registration (RFC 7591).
type MCPOAuthConfig struct {
	// Enabled turns on the OAuth bearer-token flow for this server.
	Enabled bool `json:"enabled,omitempty"`
	// ClientID is the OAuth client id. Empty → attempt dynamic registration.
	ClientID string `json:"client_id,omitempty"`
	// ClientSecret is set for confidential clients (manual fallback).
	ClientSecret string `json:"client_secret,omitempty"`
	// Scopes is the list of OAuth scopes to request.
	Scopes []string `json:"scopes,omitempty"`
	// AuthServerMetadataURL optionally overrides automatic metadata discovery.
	AuthServerMetadataURL string `json:"auth_server_metadata_url,omitempty"`
}

// LangfuseConfig holds Langfuse telemetry credentials.
type LangfuseConfig struct {
	Host      string `json:"LANGFUSE_BASE_URL,omitempty"`
	PublicKey string `json:"LANGFUSE_PUBLIC_KEY,omitempty"`
	SecretKey string `json:"LANGFUSE_SECRET_KEY,omitempty"`
}

// TelemetryConfig holds optional observability integrations.
type TelemetryConfig struct {
	Langfuse *LangfuseConfig `json:"langfuse,omitempty"`
}

// BudgetConfig controls token and cost budget limits.
type BudgetConfig struct {
	MaxTokensPerTurn  int64   `json:"max_tokens_per_turn,omitempty"`
	MaxCostPerSession float64 `json:"max_cost_per_session,omitempty"`
	WarningThreshold  float64 `json:"warning_threshold,omitempty"`
}

// ChannelConfig controls external messaging channel behavior.
type ChannelConfig struct {
	// WebEnabled enables WeChat channel in web mode (default false).
	WebEnabled bool `json:"web_enabled,omitempty"`
	// BLEEnabled enables BLE device notifications (default false).
	BLEEnabled bool `json:"ble_enabled,omitempty"`
}

// CompactionConfig controls automatic context compaction. Compaction always
// runs on the session's main model: summary quality directly bounds the
// agent's post-compaction performance, so it is deliberately not routed to
// SmallModel (a former "summary_model" key was parsed but never honored, and
// has been removed).
type CompactionConfig struct {
	Enabled    bool    `json:"enabled,omitempty"`
	Threshold  float64 `json:"threshold,omitempty"`
	KeepRecent int     `json:"keep_recent,omitempty"`
}

// PromptConfig controls prompt system behavior.
type PromptConfig struct {
	Compaction      *CompactionConfig `json:"compaction,omitempty"`
	MemoryMaxChars  int               `json:"memory_max_chars,omitempty"`
	MemoryMaxDepth  int               `json:"memory_max_depth,omitempty"`
	CacheEnabled    bool              `json:"cache_enabled,omitempty"`
	AsyncEnvTimeout string            `json:"async_env_timeout,omitempty"`
}

// SubagentConfig controls subagent behavior.
type SubagentConfig struct {
	MaxParallel  int `json:"max_parallel,omitempty"`
	MaxCompleted int `json:"max_completed,omitempty"`
	MaxDepth     int `json:"max_depth,omitempty"`
}

// AgentRoleConfig defines a named custom role for a top-level or delegated
// agent. Roles inherit the caller's mode, approval policy, sandbox, and tools.
type AgentRoleConfig struct {
	Description  string
	Instructions string
	Model        string
}

// MemoryConfig controls cross-session learned memory (the file-based store
// under ~/.jcode/memory). See internal-doc/agent-memory-design.md. All fields
// have defaults so zero config works; Enabled/Generate are pointers because
// their default is true.
type MemoryConfig struct {
	Enabled *bool `json:"enabled,omitempty"` // default true; false disables read+write
	// Generate gates only the offline distillation pipeline (M2+). When false,
	// reading/injection and memory_note inbox writes remain available.
	Generate *bool `json:"generate,omitempty"` // default true
	// Model for pipeline extraction, "provider/model". Empty → main Model.
	// Deliberately not routed through SmallModel: distilled memories persist
	// across sessions, so extraction quality matters more than the token cost.
	Model string `json:"model,omitempty"`
	// DailyTokenBudget caps pipeline token spend per day (BYOM guard).
	DailyTokenBudget int `json:"daily_token_budget,omitempty"` // default 300000
	CooldownHours    int `json:"cooldown_hours,omitempty"`     // default 6
	MaxAgeDays       int `json:"max_age_days,omitempty"`       // default 30
	MaxUnusedDays    int `json:"max_unused_days,omitempty"`    // default 45
	Phase2TopN       int `json:"phase2_top_n,omitempty"`       // default 40
	// SummaryInjectTokens caps the memory summary injected into the system prompt.
	SummaryInjectTokens int `json:"summary_inject_tokens,omitempty"` // default 1200
}

// memoryMu guards publication of Config.Memory. Settings handlers replace the
// complete block rather than mutating it in place, and readers take a detached
// copy so background work cannot retain a pointer into the live configuration.
// It is package-level because Config is copied by value in a few places.
var memoryMu sync.RWMutex

func cloneBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}

func cloneMemoryConfig(mc *MemoryConfig) *MemoryConfig {
	if mc == nil {
		return nil
	}
	copy := *mc
	copy.Enabled = cloneBool(mc.Enabled)
	copy.Generate = cloneBool(mc.Generate)
	return &copy
}

// MemorySettings returns a detached snapshot of the memory settings. A zero
// value represents an absent block and is interpreted by the helpers below
// using the documented defaults.
func (c *Config) MemorySettings() MemoryConfig {
	memoryMu.RLock()
	defer memoryMu.RUnlock()
	if c == nil || c.Memory == nil {
		return MemoryConfig{}
	}
	return *cloneMemoryConfig(c.Memory)
}

// MemoryConfigSnapshot returns a detached copy of the stored block while
// preserving the distinction between an absent block and an explicit empty
// block. Settings handlers use it for exact rollback when persistence fails.
func (c *Config) MemoryConfigSnapshot() *MemoryConfig {
	memoryMu.RLock()
	defer memoryMu.RUnlock()
	if c == nil {
		return nil
	}
	return cloneMemoryConfig(c.Memory)
}

// SetMemory atomically publishes a detached copy of mc. Callers may safely
// reuse or mutate mc after this method returns. Passing nil removes the block.
func (c *Config) SetMemory(mc *MemoryConfig) {
	if c == nil {
		return
	}
	memoryMu.Lock()
	defer memoryMu.Unlock()
	c.Memory = cloneMemoryConfig(mc)
}

// MemoryPipelineSnapshot returns a Config copy whose Memory block is detached
// from the live settings. A pipeline run may retain this snapshot for its whole
// lifetime without racing a Settings update or observing a half-updated block.
func (c *Config) MemoryPipelineSnapshot() *Config {
	if c == nil {
		return nil
	}
	memoryMu.RLock()
	defer memoryMu.RUnlock()
	copy := *c
	copy.Memory = cloneMemoryConfig(c.Memory)
	return &copy
}

// MemoryEnabled reports whether the memory system is on (default true).
func MemoryEnabled(c *Config) bool {
	mc := c.MemorySettings()
	if mc.Enabled == nil {
		return true
	}
	return *mc.Enabled
}

// MemoryGenerate reports whether the distillation pipeline may run (default true).
func MemoryGenerate(c *Config) bool {
	if !MemoryEnabled(c) {
		return false
	}
	return MemoryGenerateSetting(c)
}

// MemoryGenerateSetting reports the stored auto-distillation preference
// independently of the master Memory switch. Settings UIs must use this value
// so temporarily disabling Memory does not erase the user's Dream preference.
func MemoryGenerateSetting(c *Config) bool {
	mc := c.MemorySettings()
	if mc.Generate == nil {
		return true
	}
	return *mc.Generate
}

// MemorySummaryInjectTokens returns the summary injection cap (default 1200).
func MemorySummaryInjectTokens(c *Config) int {
	if n := c.MemorySettings().SummaryInjectTokens; n > 0 {
		return n
	}
	return 1200
}

// MemoryDailyTokenBudget returns the pipeline daily token budget (default 300k).
func MemoryDailyTokenBudget(c *Config) int {
	if n := c.MemorySettings().DailyTokenBudget; n > 0 {
		return n
	}
	return 300000
}

// MemoryCooldownHours returns the pipeline cooldown (default 6).
func MemoryCooldownHours(c *Config) int {
	if n := c.MemorySettings().CooldownHours; n > 0 {
		return n
	}
	return 6
}

// MemoryMaxAgeDays returns the extraction window (default 30).
func MemoryMaxAgeDays(c *Config) int {
	if n := c.MemorySettings().MaxAgeDays; n > 0 {
		return n
	}
	return 30
}

// MemoryMaxUnusedDays returns the unused-expiry window (default 45).
func MemoryMaxUnusedDays(c *Config) int {
	if n := c.MemorySettings().MaxUnusedDays; n > 0 {
		return n
	}
	return 45
}

// MemoryPhase2TopN returns the consolidation input cap (default 40).
func MemoryPhase2TopN(c *Config) int {
	if n := c.MemorySettings().Phase2TopN; n > 0 {
		return n
	}
	return 40
}

// Config represents the application configuration
type Config struct {
	// diskRevision is the SHA-256 digest of the exact file bytes loaded from
	// disk. It is intentionally not serialized. SaveConfig compares it under the
	// cross-process lock so an unrelated stale full-snapshot write fails closed
	// instead of reviving credentials or policies changed by another process.
	diskRevision string

	// Provider settings: map of provider name → config (api_key, base_url)
	Providers map[string]*ProviderConfig `json:"providers"`
	// Deprecated: use Providers instead. Kept for backward compatibility.
	Models map[string]*ProviderConfig `json:"models,omitempty"`

	// Active model in "provider/model" format (e.g. "openai/gpt-4o")
	Model string `json:"model"`
	// SmallModel is an optional lightweight model in "provider/model" format.
	// It backs the "small" model alias (subagent/flow model params) and LLM
	// session-title generation. Unset → those paths use the main model /
	// truncated titles; behavior is unchanged.
	SmallModel string `json:"small_model,omitempty"`
	// ImageModel is the independent image-generation role in "provider/model"
	// format. Empty means image generation is unavailable to the agent.
	ImageModel string `json:"image_model,omitempty"`
	// Media holds retention limits for JCode-managed generated media.
	Media *MediaConfig `json:"media,omitempty"`

	// ContextLimits overrides the resolved context window (in tokens) for a model.
	// Keys may be "provider/model" (preferred) or a bare model id. Use this to teach
	// jcode the window of a brand-new or custom model the registry doesn't know yet.
	ContextLimits map[string]int `json:"context_limits,omitempty"`
	// DefaultContextLimit is the fallback context window (in tokens) assumed when a
	// model's limit is unknown from the registry and built-in tables. Defaults to 200000.
	DefaultContextLimit int `json:"default_context_limit,omitempty"`

	// Deprecated: use Model field with "provider/model" format instead.
	Provider string `json:"provider,omitempty"`

	MaxIterations int                   `json:"max_iterations,omitempty"`
	SSHAliases    []SSHAlias            `json:"ssh_aliases,omitempty"`
	DockerAliases []DockerAlias         `json:"docker_aliases,omitempty"`
	MCPServers    map[string]*MCPServer `json:"mcp_servers,omitempty"`
	Telemetry     *TelemetryConfig      `json:"telemetry,omitempty"`
	Budget        *BudgetConfig         `json:"budget,omitempty"`
	Compaction    *CompactionConfig     `json:"compaction,omitempty"`
	Prompt        *PromptConfig         `json:"prompt,omitempty"`
	Subagent      *SubagentConfig       `json:"subagent,omitempty"`
	Team          *TeamConfig           `json:"team,omitempty"`
	Memory        *MemoryConfig         `json:"memory,omitempty"`

	// DenyRead holds managed deny-read rules: path patterns whose contents the
	// agent may not read via read/grep/glob (nor reach through execute, edit,
	// or write). Rules are USER-MANAGED policy: they load from the global
	// config only and are NEVER merged from project config (see
	// mergeProjectFields) — a repository must not be able to add, relax, or
	// remove them. Once loaded into a session, the rules stay enforced across
	// approval-mode changes, full access, config hot reloads, resume, and
	// remote (SSH/Docker) switches; they propagate to subagents and teammates
	// unchanged (see internal/tools/readpolicy.go).
	DenyRead []DenyReadRule `json:"deny_read,omitempty"`

	// AutoApprove sets the default approval mode to auto on startup.
	//
	// Deprecated: superseded by DefaultMode; still honored as a fallback when
	// DefaultMode is empty (true → "full_access").
	AutoApprove bool `json:"auto_approve,omitempty"`

	// DefaultMode is the unified session mode to start in: "approval" (default),
	// "plan", or "full_access". When empty, AutoApprove is used as a fallback.
	DefaultMode string `json:"default_mode,omitempty"`

	// Theme is the built-in color theme name for the terminal UI (e.g.
	// "jcode-dark", "nord-dark", "github-light"). Empty auto-selects a default
	// from the detected terminal background. See internal/theme for the catalog.
	Theme string `json:"theme,omitempty"`

	// Language is the portable UI locale. The version/timestamp fields are CAS
	// metadata for the encrypted account settings document.
	Language                 string    `json:"language,omitempty"`
	AccountSettingsVersion   int64     `json:"account_settings_version,omitempty"`
	AccountSettingsUpdatedAt time.Time `json:"account_settings_updated_at,omitempty"`

	// Channel controls external messaging channel behavior.
	Channel *ChannelConfig `json:"channel,omitempty"`

	// DisabledProviders lists provider IDs to exclude from registry
	DisabledProviders []string `json:"disabled_providers,omitempty"`

	// DisabledSkills lists skill names to exclude from the agent (slash commands,
	// system-prompt descriptions, and the load_skill tool).
	DisabledSkills []string `json:"disabled_skills,omitempty"`

	// ToolSearch controls progressive tool disclosure. It is opt-in so existing
	// configurations keep exposing the complete static tool set.
	ToolSearch *ToolSearchConfig `json:"tool_search,omitempty"`

	// Browser controls the browser-use capability (CDP-driven page control).
	Browser *BrowserConfig `json:"browser,omitempty"`

	// Computer controls the computer-use capability (native desktop app control).
	Computer *ComputerConfig `json:"computer,omitempty"`

	// ApprovalReview holds tuning knobs for the LLM approval reviewer used in
	// Auto session mode. It does not contain an on/off switch — the reviewer is
	// active whenever the session is in Auto mode.
	ApprovalReview *ApprovalReviewConfig `json:"approval_review,omitempty"`

	// Developer holds developer-only toggles surfaced under the Settings →
	// Developer tab: debug logging and Langfuse tracing. Pointer fields keep an
	// explicitly-disabled value distinguishable from an absent one. Both take
	// effect on the next process start (like the BLE toggle).
	Developer *DeveloperConfig `json:"developer,omitempty"`

	// Cloud controls the jcloud device-relay connection (see
	// cloud/docs/17-jcode-device-relay.md). It is written by `jcode login` /
	// `jcode logout`; credentials themselves live in ~/.jcode/cloud.json.
	Cloud *CloudConfig `json:"cloud,omitempty"`
}

// CloudConfig controls the jcloud device-relay connection. AutoConnect is a
// pointer so an explicitly-disabled value survives config round-trips while an
// absent value keeps the platform default (connect automatically).
type CloudConfig struct {
	// Enabled is true once this device completed `jcode login`.
	Enabled bool `json:"enabled,omitempty"`
	// URL is the orchestrator base URL (https required; localhost http is
	// allowed for development).
	URL string `json:"url,omitempty"`
	// AutoConnect gates starting the relay connector on `jcode web` startup.
	// nil ⇒ true.
	AutoConnect *bool `json:"auto_connect,omitempty"`
	// E2EE gates the M5 end-to-end encryption layer: nil/true ⇒ the connector
	// lazily generates the account CEK and seals everything uplink; false ⇒ it
	// stays on the plaintext grey path (CipherDisabled) for the gradual
	// roll-out. Pointer so an explicit false survives config round-trips.
	E2EE *bool `json:"e2ee,omitempty"`
	// SyncDefault (M19) is the global default for NEW sessions: when true, the
	// web layer stamps each newly created session as cloud-synced (an explicit
	// entry in ~/.jcode/cloud-sessions.json). Existing sessions are never
	// touched retroactively. Absent/false ⇒ new sessions do not sync.
	SyncDefault bool `json:"sync_default,omitempty"`
	// ConfigSync enables ASK-encrypted synchronization of local Desktop provider
	// configurations and portable preferences. It is opt-in: nil/false keeps
	// every provider strictly local even while conversation relay is connected.
	ConfigSync *bool `json:"config_sync,omitempty"`
}

// cloudMu guards publication of Config.Cloud. As with Memory/ToolSearch, the
// block is swapped atomically and snapshots never expose the live pointer. The
// mutex is package-level because Config is copied by value in a few places.
var cloudMu sync.RWMutex

func cloneCloudConfig(cc *CloudConfig) *CloudConfig {
	if cc == nil {
		return nil
	}
	copy := *cc
	copy.AutoConnect = cloneBool(cc.AutoConnect)
	copy.E2EE = cloneBool(cc.E2EE)
	copy.ConfigSync = cloneBool(cc.ConfigSync)
	return &copy
}

// CloudSettings returns a detached snapshot of the cloud settings. A zero
// value represents an absent block (logged out / never configured).
func (c *Config) CloudSettings() CloudConfig {
	cloudMu.RLock()
	defer cloudMu.RUnlock()
	if c == nil || c.Cloud == nil {
		return CloudConfig{}
	}
	return *cloneCloudConfig(c.Cloud)
}

// SetCloud atomically publishes a detached copy of cc. Callers may safely
// reuse or mutate cc after this method returns. Passing nil removes the block.
func (c *Config) SetCloud(cc *CloudConfig) {
	if c == nil {
		return
	}
	cloudMu.Lock()
	defer cloudMu.Unlock()
	c.Cloud = cloneCloudConfig(cc)
}

// CloudAutoConnect reports whether the relay connector should start
// automatically (default true for absent blocks and nil values).
func CloudAutoConnect(c *Config) bool {
	cc := c.CloudSettings()
	if cc.AutoConnect == nil {
		return true
	}
	return *cc.AutoConnect
}

// CloudE2EE reports whether the device relay seals uplink payloads with the
// account CEK (M5 E2E encryption). Default true for absent blocks and nil
// values; an explicit false keeps the connector on the plaintext grey path.
func CloudE2EE(c *Config) bool {
	cc := c.CloudSettings()
	if cc.E2EE == nil {
		return true
	}
	return *cc.E2EE
}

// CloudSyncDefault reports whether NEW sessions are stamped as cloud-synced
// at creation time (M19). Default false for absent blocks: nothing syncs
// unless the user opts a session in. Existing sessions are unaffected either
// way — the value is consulted only at session-creation stamping.
func CloudSyncDefault(c *Config) bool {
	return c.CloudSettings().SyncDefault
}

// CloudConfigSync reports whether ASK-encrypted Desktop configuration sync is
// explicitly enabled. Unlike relay E2EE, this is opt-in because it uploads
// encrypted provider credentials to the configured Cloud account.
func CloudConfigSync(c *Config) bool {
	cc := c.CloudSettings()
	return cc.ConfigSync != nil && *cc.ConfigSync
}

// ApprovalReviewConfig holds tuning knobs for jcode's LLM approval reviewer.
// The reviewer is active only in Auto session mode; these settings control its
// model, policy, timeout, investigation behavior, prompt-cache reuse, and audit
// log location. See internal-doc/approval-review-design.md.
type ApprovalReviewConfig struct {
	// Model is the "provider/model" (or "small" alias) the reviewer runs on.
	// Empty resolves to small_model, then to the main model — so the reviewer
	// always has a working model even if small_model is unset.
	Model string `json:"model,omitempty"`
	// Policy is extra workspace-specific policy text appended to the built-in
	// risk policy (e.g. trusted internal hosts, stricter deny rules).
	Policy string `json:"policy,omitempty"`
	// TimeoutSeconds bounds a single review. 0 uses the built-in default.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Investigate lets the reviewer run read-only tools (read/grep/glob) to
	// gather evidence before deciding (V2). Off by default (single-shot).
	Investigate bool `json:"investigate,omitempty"`
	// ReuseSession keeps a cached reviewer conversation so the large policy
	// prefix is served from the provider's prompt cache across reviews (V3).
	ReuseSession bool `json:"reuse_session,omitempty"`
	// AuditPath overrides the verdict log location. Empty →
	// <config dir>/approval-review.jsonl.
	AuditPath string `json:"audit_path,omitempty"`
}

// approvalReviewMu guards the Config.ApprovalReview pointer against concurrent
// publish/read. The web settings handler swaps the block in on the live shared
// Config from an HTTP goroutine, while a task goroutine reads it to build its
// reviewer on entering Auto mode; those two hold no lock in common, so the
// pointer needs its own. It is package-level rather than a Config field because
// Config is copied by value in a few places and an embedded mutex would trip
// go vet's copylocks check.
var approvalReviewMu sync.RWMutex

// ApprovalReviewSettings returns a snapshot of the reviewer tuning knobs, or
// zero values when unset. The copy means callers never hold a pointer into the
// live config, so a later SetApprovalReview cannot mutate what they read.
func (c *Config) ApprovalReviewSettings() ApprovalReviewConfig {
	approvalReviewMu.RLock()
	defer approvalReviewMu.RUnlock()
	if c == nil || c.ApprovalReview == nil {
		return ApprovalReviewConfig{}
	}
	return *c.ApprovalReview
}

// SetApprovalReview publishes a new reviewer tuning block onto the live config
// so reviewers built after this call pick it up. rc must not be mutated after
// being handed over.
func (c *Config) SetApprovalReview(rc *ApprovalReviewConfig) {
	approvalReviewMu.Lock()
	defer approvalReviewMu.Unlock()
	c.ApprovalReview = rc
}

// ToolSearchConfig controls progressive tool disclosure. Enabled is a pointer
// so config round-trips preserve an explicitly disabled value while an absent
// value remains distinguishable for future compatibility.
type ToolSearchConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// toolSearchMu guards publication of Config.ToolSearch. As with Memory, the
// block is swapped atomically and snapshots never expose the live pointer.
var toolSearchMu sync.RWMutex

func cloneToolSearchConfig(tc *ToolSearchConfig) *ToolSearchConfig {
	if tc == nil {
		return nil
	}
	copy := *tc
	copy.Enabled = cloneBool(tc.Enabled)
	return &copy
}

// ToolSearchSettings returns a detached settings snapshot. Its zero value is
// the disabled-by-default configuration.
func (c *Config) ToolSearchSettings() ToolSearchConfig {
	toolSearchMu.RLock()
	defer toolSearchMu.RUnlock()
	if c == nil || c.ToolSearch == nil {
		return ToolSearchConfig{}
	}
	return *cloneToolSearchConfig(c.ToolSearch)
}

// ToolSearchConfigSnapshot returns a detached copy of the stored block while
// preserving nil. This lets a failed Settings save restore the exact prior
// representation rather than replacing an absent block with an empty one.
func (c *Config) ToolSearchConfigSnapshot() *ToolSearchConfig {
	toolSearchMu.RLock()
	defer toolSearchMu.RUnlock()
	if c == nil {
		return nil
	}
	return cloneToolSearchConfig(c.ToolSearch)
}

// SetToolSearch atomically publishes a detached copy of tc. Passing nil removes
// the block and restores the disabled default.
func (c *Config) SetToolSearch(tc *ToolSearchConfig) {
	if c == nil {
		return
	}
	toolSearchMu.Lock()
	defer toolSearchMu.Unlock()
	c.ToolSearch = cloneToolSearchConfig(tc)
}

// ToolSearchEnabled reports whether progressive tool disclosure is enabled.
// It is disabled by default, including for absent config blocks and nil values.
func ToolSearchEnabled(c *Config) bool {
	tc := c.ToolSearchSettings()
	return tc.Enabled != nil && *tc.Enabled
}

// DeveloperConfig holds developer-only toggles (debug logging, tracing).
// Pointer fields preserve an explicitly-disabled value through config
// round-trips while a nil value remains the platform default (enabled).
type DeveloperConfig struct {
	EnableLogging *bool `json:"enable_logging,omitempty"`
	EnableTracing *bool `json:"enable_tracing,omitempty"`
}

// developerMu guards publication of Config.Developer. As with ToolSearch, the
// block is swapped atomically and snapshots never expose the live pointer. The
// mutex is package-level because Config is copied by value in a few places and
// an embedded mutex would trip go vet's copylocks check.
var developerMu sync.RWMutex

func cloneDeveloperConfig(dc *DeveloperConfig) *DeveloperConfig {
	if dc == nil {
		return nil
	}
	copy := *dc
	copy.EnableLogging = cloneBool(dc.EnableLogging)
	copy.EnableTracing = cloneBool(dc.EnableTracing)
	return &copy
}

// DeveloperSettings returns a detached snapshot. Its zero value is the
// platform default (both toggles enabled).
func (c *Config) DeveloperSettings() DeveloperConfig {
	developerMu.RLock()
	defer developerMu.RUnlock()
	if c == nil || c.Developer == nil {
		return DeveloperConfig{}
	}
	return *cloneDeveloperConfig(c.Developer)
}

// DeveloperConfigSnapshot returns a detached copy of the stored block while
// preserving nil. This lets a failed save restore the exact prior
// representation rather than replacing an absent block with an empty one.
func (c *Config) DeveloperConfigSnapshot() *DeveloperConfig {
	developerMu.RLock()
	defer developerMu.RUnlock()
	if c == nil {
		return nil
	}
	return cloneDeveloperConfig(c.Developer)
}

// SetDeveloper atomically publishes a detached copy of dc. Passing nil removes
// the block and restores the platform defaults.
func (c *Config) SetDeveloper(dc *DeveloperConfig) {
	if c == nil {
		return
	}
	developerMu.Lock()
	defer developerMu.Unlock()
	c.Developer = cloneDeveloperConfig(dc)
}

// LoggingEnabled reports whether debug logging to ~/.jcode/debug.log is on.
// The platform default is enabled (absent block or nil pointer → true) to
// preserve the historical always-on behavior.
func LoggingEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	dc := c.DeveloperSettings()
	if dc.EnableLogging == nil {
		return true
	}
	return *dc.EnableLogging
}

// TracingEnabled reports whether Langfuse tracing may be started. The platform
// default is enabled (absent block or nil pointer → true); callers still need
// to check for configured Langfuse credentials before constructing a tracer.
func TracingEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	dc := c.DeveloperSettings()
	if dc.EnableTracing == nil {
		return true
	}
	return *dc.EnableTracing
}

// langfuseMu guards Config.Telemetry.Langfuse against concurrent publish/read.
// The web settings handler swaps the block in from an HTTP goroutine while the
// startup path in internal/command/{web,interactive,acp}.go reads it once to
// build a tracer; package-level mutex mirrors developerMu / toolSearchMu
// because Config is copied by value in a few places.
var langfuseMu sync.RWMutex

// LangfuseSettings returns a detached snapshot of the Langfuse credentials, or
// a zero LangfuseConfig when unset. Callers never hold a pointer into the live
// config, so a later SetLangfuse cannot mutate what they read.
func (c *Config) LangfuseSettings() LangfuseConfig {
	langfuseMu.RLock()
	defer langfuseMu.RUnlock()
	if c == nil || c.Telemetry == nil || c.Telemetry.Langfuse == nil {
		return LangfuseConfig{}
	}
	return *c.Telemetry.Langfuse
}

// LangfuseSnapshot returns a detached pointer to the stored Langfuse block
// while preserving nil. Lets a failed save restore the exact prior block.
func (c *Config) LangfuseSnapshot() *LangfuseConfig {
	langfuseMu.RLock()
	defer langfuseMu.RUnlock()
	if c == nil {
		return nil
	}
	if c.Telemetry == nil || c.Telemetry.Langfuse == nil {
		return nil
	}
	copy := *c.Telemetry.Langfuse
	return &copy
}

// SetLangfuse atomically publishes lc. A non-nil value always replaces the
// block (including a zero-value block, which clears the credentials); passing
// nil removes the Langfuse block entirely.
func (c *Config) SetLangfuse(lc *LangfuseConfig) {
	if c == nil {
		return
	}
	langfuseMu.Lock()
	defer langfuseMu.Unlock()
	if lc == nil {
		if c.Telemetry != nil {
			c.Telemetry.Langfuse = nil
		}
		return
	}
	if c.Telemetry == nil {
		c.Telemetry = &TelemetryConfig{}
	}
	copy := *lc
	c.Telemetry.Langfuse = &copy
}

// LangfuseConfigured reports whether both required Langfuse credentials are
// non-empty (Host is optional — the tracer falls back to Langfuse Cloud when
// unset). Used by the Developer settings status endpoint.
func LangfuseConfigured(c *Config) bool {
	if c == nil {
		return false
	}
	lc := c.LangfuseSettings()
	return lc.PublicKey != "" && lc.SecretKey != ""
}

// BrowserConfig controls the browser-use capability. Browser use is opt-in:
// an absent block and Enabled=false both keep its tools out of the agent schema.
type BrowserConfig struct {
	Enabled    bool   `json:"enabled,omitempty"`
	Backend    string `json:"backend,omitempty"`     // auto | managed | extension (default auto)
	ChromePath string `json:"chrome_path,omitempty"` // empty → auto-discover
	Headless   bool   `json:"headless,omitempty"`    // managed backend
	Viewport   string `json:"viewport,omitempty"`    // e.g. "1280x720"
	// Approval holds per-class defaults: "navigate" and "interact" map to
	// "ask" (default) or "always_allow".
	Approval map[string]string `json:"approval,omitempty"`
	// SitePermissions overrides Approval defaults per origin.
	SitePermissions []BrowserSitePermission `json:"site_permissions,omitempty"`
	// DevMode unlocks browser_eval / raw CDP (high-risk). Off by default.
	DevMode bool `json:"dev_mode,omitempty"`
}

// BrowserSitePermission is a per-origin approval override.
type BrowserSitePermission struct {
	Origin   string `json:"origin"`
	Navigate string `json:"navigate,omitempty"` // ask | allow
	Interact string `json:"interact,omitempty"` // ask | allow
}

// ComputerConfig controls the computer-use capability (native desktop app
// control). See internal-doc/computer-use-design.md.
//
// Enabled defaults to false. Like browser use, computer use is opt-in; it also
// requires native OS permissions because it can reach anything on the machine.
type ComputerConfig struct {
	Enabled bool `json:"enabled,omitempty"`
	// Backend is retained only to migrate configurations written before computer
	// use became macOS-helper-only. Runtime backend selection must not consult it.
	//
	// Deprecated: safe legacy values (empty, auto, helper) are discarded. Any
	// other value fails closed via MigrateLegacyBackend so a configuration that
	// expected a fake screen can never start controlling the real desktop after an
	// upgrade.
	Backend string `json:"backend,omitempty"`
	// Approval holds per-class defaults: "launch" and "interact" map to
	// "ask" (default) or "always_allow".
	Approval map[string]string `json:"approval,omitempty"`
	// AppPermissions overrides Approval defaults, and optionally the tier, per app.
	AppPermissions []ComputerAppPermission `json:"app_permissions,omitempty"`
	// MaxActionsPerBatch bounds a computer_act batch (default 20).
	MaxActionsPerBatch int `json:"max_actions_per_batch,omitempty"`
	// Grant flags, orthogonal to the app allowlist. All off by default: an app
	// grant is not a clipboard grant.
	ClipboardRead   bool `json:"clipboard_read,omitempty"`
	ClipboardWrite  bool `json:"clipboard_write,omitempty"`
	SystemKeyCombos bool `json:"system_key_combos,omitempty"`
}

// MigrateLegacyBackend removes the obsolete computer backend selector.
//
// Empty, auto, and helper all meant the shipping native helper and therefore
// preserve the surrounding policy. fake, osa, and unknown values are unsafe to
// reinterpret: mapping an enabled fake configuration to the real helper would
// unexpectedly turn a test screen into real desktop control. Those values fail
// closed by disabling computer use and clearing every persisted preapproval and
// ambient grant. The rejected normalized value is returned for diagnostics.
func (c *ComputerConfig) MigrateLegacyBackend() (rejected string) {
	if c == nil {
		return ""
	}
	backend := strings.ToLower(strings.TrimSpace(c.Backend))
	c.Backend = ""
	switch backend {
	case "", "auto", "helper":
		return ""
	default:
		c.Enabled = false
		c.Approval = nil
		c.AppPermissions = nil
		c.ClipboardRead = false
		c.ClipboardWrite = false
		c.SystemKeyCombos = false
		return backend
	}
}

// ComputerAppPermission is a per-app approval override.
//
// Tier may only tighten the built-in tier for that app; a row that tries to
// loosen one is ignored (internal/computer.Manager.TierOverrides). Loosening is
// a deliberate act the settings UI gates behind a warning — a hand-edited config
// file is not that gate.
type ComputerAppPermission struct {
	BundleID string `json:"bundle_id"`
	Tier     string `json:"tier,omitempty"`     // read | click | full; "" = built-in default
	Launch   string `json:"launch,omitempty"`   // ask | allow
	Interact string `json:"interact,omitempty"` // ask | allow
}

// TeamConfig controls agent team behavior.
type TeamConfig struct {
	MaxTeammates  int `json:"max_teammates,omitempty"`   // max teammates per team (default 5)
	MailboxPollMs int `json:"mailbox_poll_ms,omitempty"` // mailbox poll interval in ms (default 500)
	MessageCap    int `json:"message_cap,omitempty"`     // max messages in UI per teammate (default 50)
}

// DenyReadRule is one managed deny-read rule. A rule denies tool access
// (read/grep/glob/execute/edit/write) to every path it matches:
//
//   - An absolute directory path denies the directory and everything under it
//     (e.g. "/home/user/.ssh").
//   - An absolute file path denies exactly that file (e.g. "/etc/shadow").
//   - A pattern containing glob metacharacters (* ? [) matches each path
//     segment via filepath.Match semantics (e.g. "/srv/*/secrets.env").
//
// Reason is free-form and appears in tool errors and audit records so the
// agent (and the user) can see why a path is blocked.
type DenyReadRule struct {
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}

// GetProviders returns the effective provider map, merging legacy Models field into Providers.
func (c *Config) GetProviders() map[string]*ProviderConfig {
	if len(c.Providers) > 0 {
		return c.Providers
	}
	return c.Models
}

// GetProviderModel returns the provider name and model name from the active
// Model field. If Model is in "provider/model" format, it splits them.
// Otherwise it falls back to the legacy Provider + Model fields.
func (c *Config) GetProviderModel() (provider, model string) {
	if parts := splitProviderModel(c.Model); len(parts) == 2 {
		return parts[0], parts[1]
	}
	// Legacy fallback
	return c.Provider, c.Model
}

// CompactionThreshold returns the configured fraction (0-1) of the context window
// at which automatic compaction/summarization triggers, or 0.75 when unset/invalid.
func (c *Config) CompactionThreshold() float64 {
	if c != nil && c.Compaction != nil && c.Compaction.Threshold > 0 && c.Compaction.Threshold <= 1 {
		return c.Compaction.Threshold
	}
	return 0.75
}

func splitProviderModel(s string) []string {
	idx := -1
	for i, ch := range s {
		if ch == '/' {
			idx = i
			break
		}
	}
	if idx <= 0 || idx >= len(s)-1 {
		return nil
	}
	return []string{s[:idx], s[idx+1:]}
}

// ConfigDir returns the full path to the config directory (~/.jcode).
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback: use /tmp so callers never get a literal "~" path
		// that Go's filesystem APIs cannot resolve.
		return filepath.Join(os.TempDir(), configDir)
	}
	return filepath.Join(home, configDir)
}

// configFilePath returns the full path to the config file. When JCODE_CONFIG is
// set it takes precedence, allowing CI/CD pipelines and containerized deployments
// to point at a mounted config without touching ~/.jcode/.
func configFilePath() (string, error) {
	if v := os.Getenv(EnvConfigFile); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, configDir, configFile), nil
}

// HistoryFilePath returns the full path to the history file
func HistoryFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, configDir, "history"), nil
}

// NeedsSetup returns true if the config file does not exist or is incomplete.
func NeedsSetup() bool {
	cfgPath, err := configFilePath()
	if err != nil {
		return true
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return true
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return true
	}
	return len(cfg.GetProviders()) == 0
}

// LoadConfig loads configuration from $HOME/.jcode/config.json.
func LoadConfig() (*Config, error) {
	return loadConfig(true)
}

func loadConfig(requireProviders bool) (*Config, error) {
	cfg := &Config{
		MaxIterations: 1000, // default
	}

	cfgPath, err := configFilePath()
	if err != nil {
		return nil, fmt.Errorf("config file path error: %w", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("config file not found at %s, please run setup first: %w", cfgPath, err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", cfgPath, err)
	}
	cfg.diskRevision = configContentRevision(data)

	if cfg.Computer != nil {
		if rejected := cfg.Computer.MigrateLegacyBackend(); rejected != "" {
			Logger().Printf("[config] disabled computer use while removing unsupported legacy backend %q; preapprovals and grants were cleared", rejected)
		}
	}

	// Migrate legacy "models" field to "providers"
	if len(cfg.Providers) == 0 && len(cfg.Models) > 0 {
		cfg.Providers = cfg.Models
	}

	// Migrate legacy provider IDs to models.dev canonical IDs
	cfg.migrateProviderIDs()

	// Validation
	if requireProviders && len(cfg.GetProviders()) == 0 {
		return nil, fmt.Errorf("no providers configured: set 'providers' in %s", cfgPath)
	}

	// Resolve legacy Provider field format
	if cfg.Provider != "" && !containsSlash(cfg.Model) {
		cfg.Model = cfg.Provider + "/" + cfg.Model
	}

	// Validate Model field is set. This is no longer a hard error: setup no
	// longer forces a model selection, and some agent-construction paths can
	// pick a default at runtime. We only warn so a stale/legacy config with
	// providers but no active model doesn't prevent the app from booting — the
	// caller resolves a concrete model (or surfaces a clearer error) when it
	// actually builds an agent.
	if cfg.Model == "" {
		Logger().Printf("[config] warning: no active model set in %s; it will be resolved on first use", cfgPath)
	}

	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 1000
	}

	// Tag MCP servers loaded from global config for UI provenance display.
	for _, srv := range cfg.MCPServers {
		if srv != nil {
			srv.Source = "global"
		}
	}

	return cfg, nil
}

// migrateProviderIDs converts legacy provider IDs to models.dev canonical IDs.
// This ensures backward compatibility with configs using old IDs like "bailian" or "bailian-plan".
func (c *Config) migrateProviderIDs() {
	// Map of legacy provider ID → models.dev canonical ID
	migrations := map[string]string{
		"bailian":      "alibaba-cn",
		"bailian-plan": "alibaba-coding-plan-cn",
	}

	// Migrate provider keys in Providers map
	if c.Providers != nil {
		for oldID, newID := range migrations {
			if provCfg, exists := c.Providers[oldID]; exists {
				c.Providers[newID] = provCfg
				delete(c.Providers, oldID)
				Logger().Printf("[config] Migrated provider ID: %s → %s", oldID, newID)
			}
		}
	}

	// Migrate Model field (active model in "provider/model" format)
	for oldID, newID := range migrations {
		if hasPrefix(c.Model, oldID+"/") {
			c.Model = newID + c.Model[len(oldID):]
			Logger().Printf("[config] Migrated active model: %s → %s", oldID, newID)
		}
	}

	// Migrate SmallModel field
	for oldID, newID := range migrations {
		if hasPrefix(c.SmallModel, oldID+"/") {
			c.SmallModel = newID + c.SmallModel[len(oldID):]
			Logger().Printf("[config] Migrated small_model: %s → %s", oldID, newID)
		}
	}

	// Migrate ImageModel independently from the active chat model.
	for oldID, newID := range migrations {
		if hasPrefix(c.ImageModel, oldID+"/") {
			c.ImageModel = newID + c.ImageModel[len(oldID):]
			Logger().Printf("[config] Migrated image_model: %s → %s", oldID, newID)
		}
	}

}

func hasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

func containsSlash(s string) bool {
	for _, ch := range s {
		if ch == '/' {
			return true
		}
	}
	return false
}

// SaveConfig writes the config to the active config file path (default
// $HOME/.jcode/config.json; overridden by JCODE_CONFIG when set).
func SaveConfig(cfg *Config) error {
	return withConfigWriteLock(func() error {
		if err := verifyConfigRevision(cfg); err != nil {
			return err
		}
		return saveConfig(cfg, os.Rename)
	})
}

// saveConfig writes through an owner-only temporary file in the destination
// directory, then atomically replaces config.json. Keeping the temporary file
// beside the destination makes the final rename stay on one filesystem. The
// rename function is a parameter so failure handling can be tested without
// relying on platform-specific permission behavior.
func saveConfig(cfg *Config, rename func(string, string) error) error {
	return saveConfigWithSync(cfg, rename, syncConfigDirectory)
}

func saveConfigWithSync(
	cfg *Config,
	rename func(string, string) error,
	syncDirectory func(string) error,
) error {
	cfgPath, err := configFilePath()
	if err != nil {
		return fmt.Errorf("config file path error: %w", err)
	}

	// Config may contain provider API keys, custom headers, MCP credentials,
	// and remote connection details. Keep both newly-created and legacy paths
	// owner-only before writing any serialized content.
	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("failed to secure config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "."+configFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary config file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	// CreateTemp currently creates 0600 files, but set the mode explicitly so
	// the security invariant does not depend on that implementation detail.
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("failed to secure temporary config file %s: %w", tmpPath, err)
	}
	if n, err := tmp.Write(data); err != nil {
		return fmt.Errorf("failed to write temporary config file %s: %w", tmpPath, err)
	} else if n != len(data) {
		return fmt.Errorf("failed to write temporary config file %s: %w", tmpPath, io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary config file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary config file %s: %w", tmpPath, err)
	}
	if err := rename(tmpPath, cfgPath); err != nil {
		return fmt.Errorf("failed to replace config file %s: %w", cfgPath, err)
	}
	// Rename moved the temporary file into place. Never let the deferred cleanup
	// target that path, including when the directory durability barrier fails.
	tmpPath = ""
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("failed to sync config directory %s: %w", dir, err)
	}
	if cfg != nil {
		cfg.diskRevision = configContentRevision(data)
	}

	return nil
}

func configContentRevision(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

// ConfigPath returns the expected path of the config file (for display purposes)
func ConfigPath() string {
	p, err := configFilePath()
	if err != nil {
		return filepath.Join("~", configDir, configFile)
	}
	return p
}

// SessionsDir returns the path to the sessions directory (~/.jcode/sessions).
func SessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, configDir, "sessions"), nil
}

// SessionsIndexPath returns the path to the sessions index file
// (~/.jcode/sessions/session.json).
func SessionsIndexPath() (string, error) {
	dir, err := SessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session.json"), nil
}

// UsageDir returns the path to the usage-statistics directory (~/.jcode/usage).
func UsageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, configDir, "usage"), nil
}

// UsageEventsPath returns the path to the append-only usage event log
// (~/.jcode/usage/events.jsonl), one JSON line per recorded agent turn.
func UsageEventsPath() (string, error) {
	dir, err := UsageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "events.jsonl"), nil
}
