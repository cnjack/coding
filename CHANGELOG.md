# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security
- **Untrusted projects no longer supply project-level `AGENTS.md` instructions.** New/unknown projects (including fresh clones) are untrusted by default: the walk-up `AGENTS.md` chain and `AGENTS.local.md` are excluded from the system prompt, while the user-owned global `~/.jcode/AGENTS.md` keeps loading. Trust is an explicit user decision persisted outside the repository (`jcode trust` / `jcode untrust`, stored in `~/.jcode/project_trust.json`) or the `JCODE_AGENTS_TRUST_PROJECT=1` opt-in — repository content can never self-authorize. Behavior is identical across TUI, Web, Desktop, and ACP, and skipped instructions are audit-logged.
- **Managed deny-read rules (`deny_read`).** A new user-managed global config list blocks `read`/`grep`/`glob`/`execute` (and `edit`/`write`, so denied content cannot leak through diffs) from matched paths, with exact/dir-prefix/`filepath.Match`-glob semantics and symlink resolution. Rules are never mergeable from project config, are shared by every Env (subagents, teammates, workflows, remote SSH/Docker sessions inherit the same policy object and cannot gain higher read permission), and — once a session is running — stay enforced across approval-mode changes, Full Access, config hot-reloads, and resume; additions apply immediately, removals require a restart. Denied calls return a stable `path_denied_by_policy` error and are recorded under `[security]` in the debug log.

### Added
- **Unified Provider account sign-in.** Settings and first-run setup can now authenticate OpenAI through ChatGPT/Codex, xAI through Grok, and GitHub Copilot through one device-code account flow, while preserving API-key providers. Providers bind to a default or explicit local account and expose connected, reauthentication, and multi-account management states.
- GitHub Copilot requests keep one stable session interaction while classifying tool continuations and delegated agents as agent-initiated, avoiding accidental extra premium interactions.
- Managed ChatGPT/Codex, xAI, and GitHub Copilot Providers now browse the selected account's live model catalog. Enabled account-scoped models survive restart, and Copilot routes OpenAI models through Responses while retaining Chat Completions for other advertised vendors.
- **Provider-backed image generation.** Configure a global Image Model independently from the chat model, then use `generate_image` from normal-mode TUI, Web, Desktop, or ACP sessions. The first release supports OpenAI-compatible Images endpoints, BigModel CogView, and Alibaba Token Plan Wan 2.7 models.
- Grok account sign-in now exposes the official `grok-imagine-image` and `grok-imagine-image-quality` models through the Image Model role with dispatch-time OAuth credentials; xAI video entries are kept out of unsupported chat/image surfaces.
- **Generated images as managed Artifacts.** Results are verified, stored outside the workspace under the session, persisted for replay, and shown as lifecycle-aware image cards in Web/Desktop. TUI reports the local path and metadata; ACP degrades to metadata, resource links, or bounded inline images according to negotiated capabilities.
- **Provider capability routing.** Settings now distinguishes chat, image generation, vision input, and provider-bound tools using the exact provider profile, endpoint, protocol, and model. It includes an Image Model picker, provider capability status, a BigModel Search MCP preset, and provider Web Search policy.

### Changed
- Grok Imagine generation now uses xAI-native `aspect_ratio` and `resolution` controls (`1k`/`2k`) instead of forwarding OpenAI-style `size`; older common JCode sizes are normalized into the equivalent native controls before approval and dispatch.
- **Ask User is now a bottom interaction dock.** Pending questions replace the composer and are presented one at a time with paging, recommended and multi-select options, custom answers, skip, submission progress, and retryable errors. Once answered, a compact receipt remains in the conversation timeline.
- Pending Ask User calls no longer merge into activity groups, and both pending and resolved question surfaces align with the conversation gutter.
- Fresh blank sessions hide task/session chrome until conversation work exists; loading and persisted sessions keep their controls.

### Fixed
- Grok device sign-in now accepts xAI's official `accounts.x.ai/oauth2/device` verification page while retaining strict HTTPS, host, port, and user-info checks.
- Provider Settings now shows the last successful account-scoped model catalog immediately when reopened, revalidates it in the background, and preserves it through transient refresh failures without allowing an older account request to overwrite newer results.
- Managed Grok Image Models now pass the image-tool availability check without requiring an API key, so selecting a supported Grok Imagine model exposes `generate_image` to active normal-mode agents.
- Provider configuration writes are serialized as reload → mutate → atomic save, reject stale snapshots, preserve secrets, and rebuild provider tools after keys, endpoints, or models change.
- Session replay now restores provider operations, managed Artifacts, tool lifecycle, session modes, and per-session tool overrides without trusting dropped WebSocket events.

### Security
- Managed Provider credentials are resolved immediately before dispatch, never returned by the Web API, and kept out of `config.json`. OAuth-backed providers pin their upstream endpoint, wire protocol, and protected headers; refreshes are singleflight, account writes are locked and atomic, device flows are bounded/cancellable, and invalid or reauthentication-required bindings fail closed.
- Externally billable calls bind approval to an immutable provider/model/argument intent and idempotency key. Ask for approval and Auto require a fresh per-call decision; Full access is the only session-level preauthorization. Per-turn and per-session limits are reserved atomically and dispatch is durably journaled before the provider call.
- Image downloads require HTTPS and enforce trusted-host, redirect, timeout, MIME, size, dimension, and pixel limits. Private and link-local destinations are rejected; generated files use owner-only directories/files and atomic persistence.
- Security-sensitive session journals fail closed on malformed or invalid transitions, and logs/session metadata exclude credentials, complete prompts, signed URLs, provider response bodies, and image base64.

## [0.12.2] - 2026-08-05

### Fixed
- Preserved the real structured assistant/tool transcript in live multi-turn history and continuation laps, without flattening tool calls into text or recording internal continuation prompts as user messages. Follow-up turns and post-restart replay now receive equivalent context.
- Interrupted, cancelled, and stream-error turns now pair every announced tool call with a deterministic result, including parallel same-name calls. Already-rendered assistant text is retained, persistence failures stop the turn, and a partial failed plan is no longer treated as approval-ready output.

## [0.12.1] - 2026-08-02

### Added
- **Session-scoped Artifacts for Web and Desktop.** The `show_artifact` tool explicitly registers completed workspace deliverables, opens them in a dedicated right-side panel, persists them across refresh/resume and automation runs, and previews common text, Markdown, code, HTML, image, PDF, and tabular formats.
- Desktop can open an Artifact in its default application or reveal it in the system file manager. Logged-in users can explicitly create and revoke end-to-end encrypted Cloud share links; local Artifacts never upload automatically.

### Changed
- Improved public-site navigation and Chat UI page loading with route preloads and deferred content.

### Security
- Artifact APIs use opaque IDs, revalidate the path on every read, do not scan the whole workspace, and do not expose absolute paths to Browser Web. HTML previews are sandboxed, and each Cloud share uses an independent secret that never reaches the server.

## [0.11.4] - 2026-07-31

### Added
- **Desktop task titlebar.** The native shell now shows task title, workspace, branch, and model, with task rename/pin/archive, per-session Cloud sync, panel controls, and an Open-in menu.
- Desktop discovers installed editors, terminals, and file managers on macOS, Windows, and Linux and opens the validated workspace through opaque application IDs.
- Browser Settings now shows Browser Bridge connection state and links directly to the Chrome Web Store when the extension is missing.

## [0.11.3] - 2026-07-30

### Added
- **Markdown custom agents.** Define user or project roles in `~/.jcode/agents/*.agent.md` or `<project>/.jcode/agents/*.agent.md`, select them for top-level Web/Desktop/CLI sessions, and reuse them for subagents, workflows, and teams. The selected role is recorded and restored on resume.
- **Layered project configuration.** JCode walks from the git root to the working directory, merging `.jcode/config.json` and `AGENTS.md`, and discovers compatible `mcp.json`/`.mcp.json` files. Settings identifies project- and agents-scoped MCP servers and skills.
- Environment overrides such as `JCODE_MODEL`, `JCODE_SMALL_MODEL`, `JCODE_THEME`, `JCODE_LANGUAGE`, `JCODE_DEFAULT_MODE`, and `JCODE_CONFIG` now have highest precedence.

### Changed
- Redesigned the Remote Access setup page and improved composer/agent-picker behavior on narrow viewports. Changing an agent in a blank session no longer replaces the welcome screen with a notice.

### Fixed
- Cloud legacy sends now create the session before sending content, and Cloud model sync preserves reasoning-effort metadata.
- Saved SSH aliases follow the active theme, and Langfuse traces capture both input and output.

### Security
- Project-defined MCP servers require `JCODE_MCP_TRUST_PROJECT=1`. Dangerous environment overrides such as `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, `NODE_OPTIONS`, and `PYTHONPATH` are blocked, and project config cannot loosen Browser or Computer Use capability policy.

## [0.11.2] - 2026-07-23

### Added
- Cloud account preferences sync now carries the selected model, small model, language, theme, and default mode across devices while excluding credentials, local paths, aliases, and permission policy.
- Custom delegated agent roles can be defined at user/project scope with bounded profiles, instructions, and model defaults.
- Desktop provider configurations can sync through the Cloud with an Account Sync Key, device approval/revocation, encrypted provider-vault reconciliation, and a Cloud model catalog/proxy. Local providers continue to work directly when signed out or offline.

### Changed
- Simplified per-session Cloud sync controls.

### Fixed
- The model picker now honors persisted model visibility, and generated provider ordering no longer contains duplicates.

## [0.11.1] - 2026-07-22

### Added
- **jcloud device relay — remote-control jcode from anywhere.** A new `internal/cloud/` package implements an outbound-only WebSocket relay to jcloud: `jcode login` authenticates via device-code flow, the connector auto-connects on `jcode web` start, and remote clients (mobile/desktop) can browse workspaces, open sessions, send messages, and arm goals through the relay. All relay traffic is end-to-end encrypted (AES-256-GCM envelopes, P-256 ECIES key exchange, BIP39 recovery phrase) — the relay server never sees plaintext. Device identity is stored in the OS keyring (macOS Keychain / Windows Credential Manager / libsecret). New `jcode cloud status|disconnect|guide` CLI commands and `docs/cloud.md` user guide.
- **Cloud pairing with desktop approval.** Remote devices pair by requesting access; the desktop shows a pairing inbox (web UI badge + settings panel) where the user approves or rejects. QR-code pairing was removed in favor of the desktop approval flow. Per-session cloud sync is opt-in via a toggle in the chat header.
- **Cloud settings UI.** Settings is now a first-class full-page view (renamed from `SettingsDialog`) with a dedicated Cloud tab: login/logout, device list, pairing management, E2EE status, and sync preferences. A `CloudBadge` in the sidebar shows relay connection state at a glance.
- **Product composer extracted to `jcode-ui/product`.** `ChatInput`, `WorkspacePicker`, `BranchPicker`, `GoalBanner`, and `drafts` moved from `web/src/components` into `packages/jcode-ui/src/product/` as a host-agnostic composer library. Hosts inject a `ProductComposerHost` (state + actions + strings + icons); the web adapter is `web/src/app/composerHost.ts`. New `ProviderIcon` component and full i18n string tables (5 languages). Package gains vitest coverage.
- **Project-level last-activity timestamp.** The sidebar persists per-project last-used time so projects sort by recency across restarts.
- **`JCODE_NO_BROWSER=1`** env var disables browser auto-open during `jcode login` (for headless/SSH environments).

### Changed
- **One-shot session resume.** Conversation switching is now a single round trip (`GET /api/sessions/:id/resume`) instead of the previous multi-request dance, cutting perceived switch latency significantly.
- **Model and mode switches are idempotent.** Re-selecting the current model or mode no longer triggers redundant state resets or UI flicker.
- Relay capabilities report the actual E2EE state at register time; the cloud connector reports `full_access` rejection when the mode ceiling disallows it.

### Fixed
- **Composer popup stacking and viewport overflow.** Popups (workspace picker, branch picker, goal banner) now anchor correctly in compact mode, stay within the viewport, and stack with proper z-index ordering.
- **Deleting the open conversation** now lands on the welcome screen with a fresh session instead of showing a stale/deleted chat. Deleting a conversation while the agent is running is blocked to prevent state corruption.

## [0.10.1] - 2026-07-20

### Added
- **Native Computer Use.** A signed macOS helper lets the agent inspect and operate native applications through an accessibility-tree workflow, with onboarding and permission controls in Desktop Settings. Browser Use and Computer Use remain separate capabilities with their own approval tiers.
- **Deferred Tool Search.** Low-frequency tools can be discovered only when needed across TUI, Web, and ACP, keeping the initial model toolset smaller while preserving canonical MCP identities and approval state.
- Added Alibaba Token Plan providers for China and international endpoints, including `qwen3.8-max-preview`.
- Added a Web Developer settings tab for logging, tracing, and masked Langfuse configuration.
- Added per-conversation composer drafts, persisted Web/Desktop model selection, model-specific chat backdrops, a floating goal pill with `/goal`, and richer running-tool indicators.

### Changed
- Switching to a text-only model now strips unsupported image parts, reports actionable model errors, and preserves unsent drafts. Desktop opens external links in the system browser.
- Browser and Computer Use configuration changes rebuild the active toolset immediately instead of requiring a restart.

### Fixed
- Stopped or interrupted sessions now backfill missing tool results so they remain resumable and render a calm stopped state instead of a protocol error.
- Fixed per-session message queues, restoration of the last conversation, first-run setup boot, concurrent configuration access, and Browser Bridge WebSocket origin handling.
- Fixed desktop release packaging so the Computer Use helper is bundled, Developer ID signed, and included in notarization.

### Security
- Enforced Plan mode at the execution boundary and bounded subagent/team permissions even when tools are discovered dynamically.
- Preserved approval isolation through Tool Search, canonicalized MCP identities, revoked disabled Browser/Computer sessions, restricted `browser_eval` to developer mode, and hardened managed-Chrome operations.
- Configuration and credential files are now atomically written with owner-only permissions.

## [0.9.6] - 2026-07-17

### Added
- **Small model picker.** Settings → Providers now exposes the `small_model` role, grouped by enabled provider, and applies changes immediately to subagents, automations, and session titles.
- **Auto approval mode.** An optional LLM reviewer adjudicates actions that would otherwise interrupt the user: low-risk calls are allowed, dangerous calls denied, and uncertain calls escalated. Reviewer model, timeout, and audit settings are configurable.
- Added the Kimi For Coding provider and Kimi K3 with its one-million-token context window, vision input, and supported reasoning effort.

### Fixed
- Fixed separator-less small-model references, provider forms that silently disabled vision models, and provider-rebuild races after model changes.
- Corrected approval-mode transitions, failed reviewer-settings loads, and a concurrent reviewer-configuration race.

### Security
- The approval reviewer fails open to the user on timeouts, panics, malformed verdicts, or uncertainty, and rejects cloud-metadata SSRF targets deterministically.

## [0.9.5] - 2026-07-14

### Added
- **`small_model` role.** The `"small"` alias routes inexpensive subagent, workflow, team, and automation work to a dedicated model and generates best-effort same-language session titles across TUI, Web, and ACP.
- ACP now exposes the subagent tool and bridges nested progress to editor clients.

### Fixed
- Per-spawn model overrides now reach production TUI/Web agents, usage is attributed to the model that actually ran, title generation is session-safe, and `jcode doctor` probes both main and small models with a timeout.
- Polished the Web settings layout and corrected the Desktop tray template icon.

### Changed
- Removed the unused `fallback_model` and `compaction.summary_model` settings; old config files remain loadable.

## [0.9.4] - 2026-07-13

### Added
- **React product UI and reusable chat packages.** The Web/Desktop frontend moved from Vue to React 18 and Redux Toolkit, backed by the new `jcode-ui` and headless `jcode-ui-core` packages.
- The component library gained scoped design tokens, Composer 2, attachments, reasoning and sources, conversation branches, regenerate/feedback/retry actions, export, AG-UI runtime support, and optional canvas/voice integrations.
- **Structured tool activity.** Tool calls carry batch, duration, approval-wait, and denied metadata end to end. TUI and Web group adjacent activity while running and collapse it into readable summaries when complete.
- Added full-screen TUI transcript inspection and turn-level `Changed N files` summaries with expandable per-file changes in Web.

### Changed
- Tool results now separate model-facing text from structured presentation metadata, improving terminal, diff, file, search, and subagent rendering without bloating the model context.
- React became the sole product frontend, and product consumers moved to the published `jcode-ui` packages.

### Fixed
- Fixed React startup and feature parity, new-session visibility in the sidebar, duplicated streaming text, sidebar ordering, automation localization, and package publication metadata.

## [0.9.3] - 2026-07-06

### Added
- Saved workflows now appear as `/<name>` commands in TUI, Web, and ACP, scoped to the active task's project.

### Fixed
- Desktop-launched sidecars now inherit the user's login-shell environment so `rg`, `git`, Node, and profile-configured developer tools work the same as in a terminal. The shell probe is time-bounded and cleaned up on failure.
- Updated Browser Bridge to version 0.1.5.

## [0.9.2] - 2026-07-06

### Changed
- **Long-horizon tool hardening.** Execute, read, grep, glob, and background output are bounded with honest truncation and spill-to-file paths; process trees cancel reliably across local, SSH, and Docker environments.
- Compaction now uses current occupancy, reserves output headroom, shares one reduction policy across transports, calibrates estimates from provider usage, and persists the retained tail for resume.
- Environment, git, `AGENTS.md`, and externally changed files are refreshed during long runs so the agent receives actionable drift reminders.

### Fixed
- Fixed recursive globbing, aggregate grep limits, empty-compaction results, remote fatal-error propagation, subagent panic handling, and inherited `GIT_*` variables that could target the wrong repository from a linked-worktree hook.

### Security
- Edits require a prior read, writes are atomic, overlapping/ambiguous multi-edits are rejected, and externally changed files require a fresh read before mutation.

## [0.9.1] - 2026-07-05

### Added
- **Dynamic JavaScript workflows.** A deterministic goja engine provides `agent`, `parallel`, `pipeline`, `phase`, `workflow`, argument, budget, and structured-output primitives, with `jcode flow list|show|validate|run` and the `workflow_run` agent tool.
- Added built-in `repo-audit`, `pr-review`, and `roundtable` workflows plus standalone syntax validation that fails before spawning an agent or spending tokens.

### Fixed
- Browser CDP calls now prefer an already-delivered response over a simultaneous connection-close signal.

### Security
- Workflows enforce determinism guards, concurrency and run caps, wall-clock deadlines, and run-scoped cancellation.

## [0.8.1] - 2026-07-05

### Added
- **Browser Use.** Agents can navigate, inspect, interact with, and screenshot either a managed Chrome session or the user's browser through Browser Bridge, with per-origin permissions in Settings.
- **Learned memory.** `memory_note`, offline extraction/consolidation, git-backed change tracking, budgets, cooldowns, and `jcode memory`/`/memory` controls provide durable project-scoped knowledge across sessions.
- **Lifecycle hooks.** User-configured commands can observe or influence session start, prompt submission, tool use, tool failure, and stop events through JSON stdin/stdout contracts.
- Added an agent-evaluation showcase and expanded public documentation for SSH, subagents, tools, themes, hooks, Browser Use, and releases.

### Fixed
- Fixed Browser Bridge connection lifetime/status races, extension ID persistence, memory redaction/concurrency/UTF-8 handling, and site privacy/snippet overflow.

### Security
- Binding Web to a non-loopback host now requires bearer-token authentication. Auto-generated tokens are owner-only, WebSockets use a subprotocol token, and comparisons are constant-time.
- Browser actions use read, site-scoped interaction, and always-prompt high-risk tiers. Project hooks are disabled unless `JCODE_HOOKS_TRUST_PROJECT=1`, and hooks have process-tree timeouts and bounded output.

## [0.7.2] - 2026-06-28

### Added
- **Card-based provider and model management.** Setup can auto-select a default model; Settings can add/edit/test custom OpenAI-compatible providers, manage built-in and custom models, and configure vision, thinking, context, and per-model reasoning effort.
- Provider tests report classified auth/network/server failures, latency, and model count; active-model deletion is guarded and newly added models become selectable without restart.

### Changed
- Upgraded Eino to v0.9.9.

### Security
- Patched Web dependency security advisories and tightened custom-provider header/model validation.

## [0.7.1] - 2026-06-24

### Added
- **Scheduled and manual automations.** Create and run agent jobs from the scheduler, Web UI, CLI, or agent tool. Each run is recorded as a session, and interactive-only tools are excluded from unattended execution.
- Added a redesigned sidebar navigation, Channels page, shared page surface, and Automation Run detail view.

### Fixed
- Fixed automation overlay/sidebar layout, dropdown dismissal, full project paths, navigation polish, and a Windows build collision in the file-lock helper.

## [0.6.4] - 2026-06-23

### Added
- **Parallel Web tasks.** Multiple top-level tasks can run concurrently with isolated messages, status, and usage; users can switch task, project, or model without stopping another task.
- **Docker workspaces.** Bind a Web task and terminal to a container, including lifecycle/ref-count management, saved aliases, `switch_env`, and SSH-like execution behavior.
- Added sidebar filtering, sorting, grouping, provider icons, and safer branch switching.

### Fixed
- Protected git checkout from overwriting work, corrected task run/branch-picker lifecycle, and stopped new chats from receiving another task's events.
- Fixed Docker PTY ownership, container exec errors, lifecycle logs, and missing localization.

## [0.6.3] - 2026-06-22

### Added
- **Usage Statistics.** Added a global usage page, per-task context capacity, live token accounting, and cache-hit rate.

## [0.6.2] - 2026-06-22

### Added
- Internationalized the Web UI in English, Simplified Chinese, Traditional Chinese, Japanese, and Korean.

### Changed
- Migrated product icons to Heroicons and hardcoded colors to semantic theme tokens, with polish across the composer, model picker, sidebar, settings, and remote wizard.

### Fixed
- Fixed a Desktop crash on Finder launch by declaring Bluetooth usage strings and resolved native-shell, bridge, and Web UI audit findings.
- Fixed Tauri's pre-build command and made `build-web` generate required frontend/theme assets first.

### Security
- Updated DOMPurify to address npm security advisories.

## [0.6.1] - 2026-06-21

### Added
- **Task-centric Web workspace.** Introduced the enclosed product shell, full-page Settings, and a multi-project task tree.
- Added the native Tauri Desktop app, reusing the Web UI, with SSH remote workspaces and a multi-platform release pipeline.

### Fixed
- Fixed cross-project task deletion and made git context request-scoped so simultaneous projects do not leak state into one another.

## [0.5.2] - 2026-06-15

### Added
- **Seven built-in themes shared across TUI and Web.** The TUI gains `/theme` with live preview and persistence; Web gains an Appearance tab with System, dark, and light theme previews.
- Redesigned the task list and workbench panel menu.
- Added the interactive Web `ask_user` flow plus MCP OAuth and MCP Settings management.

### Changed
- TUI styles now rebuild from semantic theme tokens at runtime, and Markdown follows the active light/dark appearance.

### Fixed
- TUI **Esc** returns from a teammate to the leader, Cancel defaults to the safe **Wait** action, and **`?`** opens complete shortcut/command help.
- Theme persistence failures are now surfaced in diagnostics.

## [0.5.1] - 2026-06-13

### Added
- **1M-context model support.** New `model.ResolveContextLimit()` is the single source of truth for context-window resolution (config override → registry → `knownModels` fallback → config default → `DefaultContextLimitFallback`), replacing five copy-pasted resolution blocks across the interactive/ACP/web/runner call sites. Previously any 1M-context model the registry didn't know (or under-reported) silently fell back to a 200K window and triggered compaction at ~150K.
- New config knobs: `context_limits` (per provider/model or bare id), `default_context_limit`, and the formerly-dead `compaction.threshold` is now wired so summarization/compaction/reduction scale off a configurable fraction.
- Model registry updates that survive `go generate`: `additionalModels` merge-injection (GLM-5.2 on the four first-party Zhipu/Z.ai providers, `contextWindow=1000000`), `contextLimitOverrides` (corrects MiniMax-M3 to its advertised 1M window), and refreshed `recommendedModels` / offline `knownModels` tables to the 2026 1M flagships.
- `docs/model-research.md` caching the per-provider context-window survey, plus `context_limit_test.go` covering resolution order and the registry corrections.
- Approval dialog now splits **Allow** into "Allow once" / "Allow all" so approving a single command no longer changes the session mode.

### Changed
- Removed the dead SSE transport: the web frontend only ever used WebSocket, so `internal/web/sse.go` (`SSEBroker`/`SSEEvent`/`ServeSSE`), `web/src/composables/sse.ts`, and the `/api/events` endpoint were deleted. Every event is still broadcast over WebSocket (parity verified).

### Security
- **Hardened the approval gate (P0+P1).** Background commands no longer bypass approval (Ask/Plan previously auto-approved any `execute` with `background=true`, a model-controlled flag). `isSafeCommand` now rejects shell operators (`; & | < > \` $() ${} ()`) and matches whole command words, so a "safe" prefix can no longer smuggle a payload (e.g. `git status && rm -rf /`). Web "approve once" no longer silently promotes the whole session to Autopilot (`ResolveApproval` gained an `approveAll` flag carried through the API/WS payload). `ask_user` is auto-approved again (the allowlist held the dead name `question`). `RequestApproval` and the teammate approval path now share a single `decide()` so they can't drift.

### Fixed
- Fixed pre-existing `vue-tsc` errors (`noUncheckedIndexedAccess` narrowing in `TerminalPanel.vue` / `ToolCallCard.vue` and the dispatch typing) so `npm run build` passes again.
- Addressed CodeRabbit review feedback: workpath race, WebSocket error logging, and test timeouts.

## [0.5.0] - 2026-06-13

### Added
- Unified **Ask / Plan / Autopilot** session-mode selector across the TUI, web, and ACP frontends (Copilot-style). A single `internal/mode.SessionMode` drives both the tool/prompt axis (Plan is read-only) and the approval axis (Autopilot auto-approves). New `default_mode` config option (`ask` / `plan` / `autopilot`); the legacy `auto_approve` is kept as a fallback (`true` → `autopilot`).
- Persistent **session goal** with auto-continuation across TUI/Web/ACP (codex-style): a goal the agent keeps working toward across turns until it verifiably completes, marks it blocked, or hits a 25-continuation safety cap. New `goal_set`/`goal_get`/`goal_update` tools and a shared `/goal` grammar; the continuation loop injects a continuation prompt while a goal is active (appending only the per-turn delta to avoid quadratic context growth). Goals are persisted via `goal_update` session entries and restored on every resume path. TUI exposes `/goal <objective>|status|clear` with a 🎯 indicator; web adds `GET/POST/DELETE /api/goal`, `goal_update` WS events, a `GoalBanner`, and a 🎯 input toggle; ACP advertises `/goal` via `available_commands`.

### Changed
- TUI: the two separate mode pills (Agent/Plan + Ask/Auto) are now a single tri-state pill cycled with **Shift+Tab** (Ask → Plan → Autopilot). The old **Ctrl+P** (plan toggle) and **Ctrl+A** (approval toggle) shortcuts are removed; the approval dialog's "Approve All" now switches the session to Autopilot.
- Web: the mode dropdown offers Ask / Plan / Autopilot and the separate auto-approve toggle is folded into Autopilot. Web **Plan** mode now actually swaps to the read-only tool set (previously it only prefixed the prompt). `POST /api/mode` accepts `ask`/`plan`/`autopilot` (legacy `build` still accepted).
- ACP: sessions advertise three modes (`ask`/`plan`/`autopilot`); the legacy `agent` mode id is still accepted as an alias for `ask`. Selecting "Allow All" emits a `current_mode_update` to Autopilot.
- Upgraded dependencies to latest: bubbletea v2.0.7, eino v0.9.6, eino-ext langfuse v0.1.1, acp-go-sdk v0.13.5, mcp-go v0.54.1, golang.org/x/sys v0.45.0.

## [0.4.11] - 2026-06-03

### Changed
- Improved ACP tool-call presentation for friendlier editor display and follow-along support: tool names mapped to semantic `ToolKind`s, friendly per-call titles (e.g. `Read main.go (1-50)`), file locations with absolute paths and line numbers, status streaming (Pending → InProgress → Completed/Failed) including a status transition for auto-approved tools, diff content for edit/write calls, and failure detection from the output prefix.
- ACP `ListSessions` is now scoped by `cwd` and returns an `UpdatedAt` field on each `SessionInfo`.
- TUI: extracted `renderViewportContent()` to centralize trimmed rendering and preserve scroll-to-bottom position in the batched render path.

### Removed
- ACP client filesystem/terminal executor path (the v2 client capability extension is no longer supported); ACP sessions now use the local executor. Cleaned up `NewEnvWithExecutor`, `IsACP`, and `SetACP`.

### Fixed
- Delay the ACP slash-command broadcast so commands are advertised at the right time.

## [0.4.10] - 2026-06-03

### Added
- **Slash commands.** Skills are now exposed as `/`-prefixed commands in the TUI and web UI, with ChatInput autocomplete on the web. Upgraded `coder/acp-go-sdk` v0.12.0 → v0.13.4, migrated `UnstableCloseSession` → stable `CloseSession`, added `ResumeSession` and `Logout` (both promoted to stable), and advertise the Resume capability in `SessionCapabilities`.

### Changed
- `perf(tui)`: multi-layer render cache and stream debounce with precise cache invalidation on state changes, sharply reducing redraw cost during streaming.
- Refactored the monolithic `tui.go` into focused modules.
- Added a pre-commit hook and applied fmt/lint fixes across the tree.

### Fixed
- Invalidate the sidebar cache on viewport height change; sanitized the render fast path and debounce logic per review.

## [0.4.9] - 2026-05-23

### Added
- Enhanced tool display info for skill and team tools (load_skill, team_list, team_create, team_spawn, team_send_message, team_delete) in the web UI.
- TUI formatted output for load_skill, team_list, and team operations (team_send_message, team_create, team_spawn, team_delete) — compact, styled summaries instead of raw text.
- ACP tool kind mappings for team tools — team_list is read-only, team_create/spawn/send_message/delete are execute-type.
- Skill description attribute in loaded skill output for richer context in tool results.

### Changed
- Session title generation now only fires for truly new sessions — resuming a session with a client-provided UUID that has no file on disk still generates a title.
- System prompt recording is deferred until the first real message, preventing empty session files when jcode is opened and immediately closed.

### Fixed
- Simplified function signatures and removed unused variables in ToolCallCard and TerminalPanel components.
- Removed unused import in Langfuse telemetry.

## [0.4.8] - 2026-05-22

### Added
- Custom model support in ModelRegistry — users can now add and use custom models alongside the built-in registry.
- Two new model providers added to the built-in registry.

### Fixed
- Deep-copy providers in `NewModelRegistry` to prevent shared state mutation across instances.
- Addressed PR review comments on model registry code.
- Removed unnecessary blank line in registry generation.

## [0.4.7] - 2026-05-21

### Added
- **Web Frontend Redesign** — complete visual overhaul based on the Pencil design system.
- Borderless input area with a right panel showing file tree and changes.
- Multi-tab terminal with resizable panels and search renderer.
- Themed bash output and dark mode fixes.
- Inline channel toggle in the input area with sidebar border.

### Changed
- Aligned TopBar, Sidebar, and ChatInput with the Pencil design spec.
- Removed toolbar separator lines, compacted textarea, and shortened file paths.
- Enlarged input card, removed sidebar lines, and added terminal close button.
- Removed max-width constraint on input card for widescreen displays.

### Fixed
- Updated terminal icon from laptop to lightning bolt for consistency.
- Restored header row matching Pencil design — no separator line, click to collapse.
- Hide title row when expanded and collapse via hover button.
- Unified search and diff expanded content, removed internal dividers.
- Removed duplicate inner headers from expanded tool call cards.

## [0.4.6] - 2026-05-14

### Added
- Enhanced context management with telemetry and approval improvements.
- Version display in TUI sidebar and logo rendering.

### Changed
- Disabled pager and interactive prompts in all executors for non-interactive use.

### Fixed
- Dependency updates: bumped marked (18.0.2), postcss (8.5.10), nokogiri (1.19.3).

## [0.4.5] - 2026-04-28

### Fixed
- Passthrough `reasoning_content` for DeepSeek thinking mode so reasoning tokens are preserved in the response.

## [0.4.4] - 2026-04-28

### Added
- Enhanced token usage tracking with cached tokens in Langfuse telemetry.
- Refactored TUI textarea line calculation for improved accuracy.

### Fixed
- Corrected version formatting in Makefile and version rendering in shortcut hints.
- Removed version formatting in server version display.
- Fixed web version to use the build version instead of placeholder.

## [0.4.3] - 2026-04-27

### Added
- Initial ACP transport integration work for editor-driven sessions.

### Fixed
- Improved ACP command execution timeout and cancellation error handling.

## [0.4.2] - 2026-04-27

### Added
- Message edit, retry, and copy actions in the web UI.

### Fixed
- Addressed CodeRabbit review feedback on message actions implementation.

## [0.4.1] - 2026-04-26

### Added
- Added multimodal image support for web and ACP transports when the selected model accepts image input.
- Added image persistence and multimodal message reconstruction in session history so restored sessions retain attached images.

## [0.3.10] - 2026-04-25

### Added
- Added automatic browser opening after the web server starts listening.
- Added a keyboard shortcuts help surface for the TUI.

### Changed
- Redesigned the model picker with provider grouping and visibility controls.
- Reflowed TUI content correctly when the terminal is resized.
- Rendered subagent output as Markdown in the TUI with expand/collapse support.

### Fixed
- Stored raw assistant text for Ctrl+Y copy actions instead of reconstructing it from rendered lines.

## [0.3.9] - 2026-04-24

### Added
- Added a keyboard shortcuts help panel with context-sensitive usage hints.

## [0.3.8] - 2026-04-24

### Changed
- Cleaned up sidebar component structure in the TUI.
- Improved ANSI width handling for sidebar and layout rendering.

## [0.3.7] - 2026-04-24

### Added
- Added JCode Buddy BLE documentation and configuration guidance.
- Added multi-line paste support with reference storage.

### Changed
- Cleaned up internal documentation around the new Buddy and paste workflows.

### Fixed
- Fixed web TODO panel rendering issues.
- Fixed frontend session split issues.

## [0.3.6] - 2026-04-24

### Changed
- Removed the redundant `build_frontend.sh` helper; frontend builds are now handled by the Makefile workflow.

### Fixed
- Reworked TUI main/sidebar composition to render the divider column independently with ANSI-aware truncation and width padding, keeping the vertical rule aligned under styled or wide-character content.
- Fixed teammate lifecycle management so spawned teammates are no longer cancelled when the leader agent's current run finishes.

## [0.3.5] - 2026-04-23

### Added
- **TUI Redesign**
  - New two-column TUI layout with a dedicated sidebar component and refreshed orange-accented styling.
  - Improved sidebar truncation, spacing, and status presentation.
- **Updated Product Visuals**
  - Added fresh documentation screenshots for the TUI, web interface, Zed ACP flow, and agent teams.
  - Added `DESIGN_PLAN.md` to capture the redesigned product direction.

### Changed
- Updated branding across docs, icons, and UI components to `[J]CODE`.
- Improved web session restoration to preserve current session content alongside session identifiers.
- Removed the copy notice prompt and tightened sidebar spacing in the redesigned TUI.
- Refreshed documentation to match the redesigned product surfaces.

### Removed
- Deleted an obsolete PR artifact file from the repository.

## [0.3.4] - 2026-04-22

### Added
- **Bidirectional BLE Control**
  - Added bidirectional BLE communication so JCODE BLE devices can send commands back to the agent runtime.

### Changed
- Made `/resume` behave consistently with the `--resume` CLI flag across entry points.

### Fixed
- Fixed ACP tool-call approval rejection when multiple tool calls are pending in the same turn.
- Applied ACP handler stability and review fixes across approval and session flows.

## [0.3.3] - 2026-04-21

### Added
- **Tool Display and Session UX Polish**
  - Refined tool call display, session resume flow, and UI polish across TUI and web.
  - Added stronger session ID validation and improved message history reconstruction on restore.

### Changed
- Execute tool output now strips the `STDERR` header unconditionally and supports optional descriptions.
- Improved tool call cards, approval surfaces, and settings/session UI consistency in web and TUI.

### Fixed
- Fixed duplicated session identifiers and resume bugs across web, ACP, and TUI.
- Fixed formatting in session ID validation logic.
- Fixed message history reconstruction when restoring existing sessions.

## [0.3.2] - 2026-04-20

### Added
- **TUI Cancel Agent Confirmation Dialog**
  - Confirmation dialog when cancelling a running agent to prevent accidental aborts
  - Visual feedback with styled Yes/No options in the TUI

- **TUI Interactive Slash Command Autocomplete**
  - Interactive suggestion list above the input when typing `/` commands
  - Keyboard navigation (Up/Down arrows) and filtering as you type
  - Tab or Enter to accept highlighted suggestion, Esc to dismiss

- **OnAgentStart Lifecycle Event**
  - New `OnAgentStart()` hook in `AgentEventHandler` for immediate working feedback
  - Called before any LLM call, providing instant visual indicator instead of waiting for first text chunk
  - Web frontend handles `agent_start` event for real-time running state

### Changed
- Simplified token usage tracking with `LastTotalTokens` field and `GetLastTotal()` accessor
- Working notification moved from `OnAgentText` to `OnAgentStart` for earlier feedback

### Fixed
- Resolved gocritic, staticcheck and ineffassign lint warnings
- Handle unchecked error returns in BLE code (errcheck)

## [0.3.1] - 2026-04-19

### Added
- **BLE IoT Device Notifications**
  - Auto-discovery and connection to JCODE-* BLE devices
  - Real-time agent status push to IoT devices (idle/working/attention/complete)
  - Nordic UART Service (NUS) protocol support for device commands
  - Lazy background connection with automatic reconnection on failure

- **Notifier System Refactoring**
  - `Notifier` interface with structured `NotifyEvent` (replaces raw strings)
  - `ChannelNotifier` wrapper to use any `Channel` as a `Notifier`
  - WeChat now integrated as notifier for automatic working/idle status pushes
  - Varied, time-aware rich messages for WeChat (4 variations per event type)
  - BLE-optimized short commands with ~10 char display values

### Changed
- `Notifier.Notify()` now takes structured `NotifyEvent` instead of string
- Event types: `EventIdle`, `EventWorking`, `EventApproval`, `EventDone`
- Each notifier formats events for its own display (BLE: device commands, WeChat: rich text)
- BLE messages: idle→"ready", working→"thinking", complete→"done"/"failed"
- WeChat messages now vary by time-of-day and rotate for natural feel
- Agent completion triggers "complete" then auto-returns to "idle" after 5 seconds

## [0.2.2] - 2026-04-18

### Added
- **Grep Tool Enhancements**
  - Hidden file support for grep searches
  - Updated default output mode for better usability

- **Doctor Command Enhancements**
  - Enhanced `jcode doctor` with improved system diagnostics

- **WeChat Idempotent Messaging**
  - Login reminder message for WeChat session activation
  - Idempotent message deduplication to prevent duplicate processing

### Changed
- Makefile now computes version from latest git tag automatically
- Message splitting adjusted to prevent orphaning tool-result messages during compaction
- Web sessions sorted by creation date for consistent ordering

### Fixed
- Web conversation rendering and session sort order
- Tool result handling improved for proper display in web UI

## [0.2.1] - 2026-04-17

### Added
- **Dark Mode / Theme System**
  - Light / Dark / System theme modes with CSS custom-property design tokens
  - `useTheme` composable for reactive theme management across all components
  - Pre-hydration script in `index.html` to prevent flash of unstyled content (FOUC)
  - Smooth cross-fade animations on theme transitions
  - All 15+ web components updated to use token-based theming instead of hardcoded colors

- **WeChat Channel Integration**
  - Bidirectional messaging: send prompts to jcode from WeChat, receive results back
  - Inbound WeChat messages displayed in web UI with green "WeChat" label
  - Toolbar toggle for quick enable/disable next to the Auto toggle
  - Settings dialog Channels tab with Connect/Disconnect flow and QR code
  - Approval and task-done notifications pushed to WeChat (10s delay for approvals)
  - Auto-enable on startup when `channel.web_enabled` is set and credentials exist
  - Busy reply when agent is running and new WeChat message arrives

- **Tool Call Enhancements**
  - `toolCallID` field for precise tool call/result matching
  - Tool call notifications with index ordering

### Changed
- Web UI design system migrated from hardcoded Tailwind classes to CSS custom properties (`tokens.css`)
- Execute tool (`InvokableRun`) now reports exit codes correctly instead of returning errors
- NotifyingHandler and SetOnMessage always registered in web mode (UI toggle works without config)
- DOMPurify bumped to 3.4.0

### Fixed
- Web UI showing user messages twice (duplicate from `user_message` event)
- WeChat inbound messages not reaching the agent in web mode
- Done/approval notifications not firing in web mode (handler wrapping issue)
- Approval notification race: resolved approvals no longer re-notified after 10s timer
- Exit code not reported when command fails in execute tool

## [0.1.1] - 2026-04-16

### Added
- **TUI Session Display Enhancements**
  - Full session reconstruction with complete tool call history on resume
  - Subagent session recording: inner tool calls and results now persisted to JSONL
  - Subagent entries displayed on session resume (start/done states)
  - Tool call ID linking between calls and results for full audit trails
  
- **Langfuse Integration Improvements**
  - Nested span support for subagent and teammate traces using `ParentObservationID`
  - Child trace context propagation through agent execution
  - Complete call chain visibility: LLM → Tool → Agent → Subagent → Teammate

- **Session Recording Enhancements**
  - `ToolCallID` field added to session entries for tool call/result pairing
  - Tool call parameters fully preserved in JSONL for complete conversation recovery

### Changed
- **TUI Parameter Display**
  - Streaming tool call arguments now accumulated across chunks before display
  - Full tool arguments shown in TUI (no truncation) for complete audit visibility
  - Subagent parameters displayed during streaming execution

- **Session Architecture**
  - Tool call history reconstruction from JSONL includes complete tool messages
  - Subagent inner tool calls and results recorded alongside parent session
  - Session resume preserves full context including nested tool interactions

### Fixed
- Streaming tool call arguments partial JSON fragments now properly accumulated
- Subagent tool call parameters missing from TUI display
- Subagent session content not persisted or displayed on resume
- Langfuse traces incomplete for subagent and teammate execution paths
- Tool parameters disappearing from conversation history during reduction

### Technical
- Runner event loop refactored for streaming tool call accumulation
- Subagent event loop enhanced with session recording and proper ToolCallID capture
- Session history reconstruction now handles tool call and tool result message types
- Middleware stack improved for nested tracing with proper span nesting

## [0.0.5] - 2026-04-16

### Added
- **Changelog and Tool Call Traceability**
  - Added repository and docs-site changelog pages.
  - Added `toolCallID` propagation and indexed tool notifications for clearer tool/result pairing.
- Added a PR description guidance template for contributors.

### Changed
- Improved error handling and logging around tool execution, handlers, and web session updates.
- Updated project documentation and AGENTS guidance.
- Bumped DOMPurify to 3.4.0.

### Fixed
- Execute tool now reports command exit codes cleanly even when the command itself fails.

## [0.0.4] - 2026-04-16

### Added
- **TUI and Session Recording Improvements**
  - Improved TUI display and session recording flow.
  - Added `Recorder.HasRecording()` and startup checks for existing recordings.
- **Langfuse Tracing Improvements**
  - Updated Langfuse integration with improved tracing capabilities for interactive runs, subagents, and teammates.

### Changed
- Refined runner, session history, and interactive startup plumbing for recorded sessions.
- Stopped tracking generated model registry output in version control.

### Fixed
- Updated runtime dependencies for improved compatibility and telemetry behavior.

## [0.0.3] - 2026-04-10

### Added
- `jcode update` command for self-updating binary
- Installation script for easy setup from GitHub

### Changed
- Improved build and release workflow
- Enhanced artifact handling in CI/CD pipeline

## [0.0.2] - 2026-04-09

### Fixed
- Artifact download path corrected to 'internal/' in release workflow

## [0.0.1] - 2026-04-08

### Added
- Initial release of jcode
- Core agent loop with file operations, shell execution, and regex search
- Todo tracking and interactive user prompting
- Plan Mode for structured task review before execution
- Agent Teams for parallel multi-agent coordination
- SSH support for remote code editing
- Multi-model support with OpenAI-compatible APIs
- Session recording and resume capability
- Web interface with React/Vue frontend
- Langfuse integration for tracing
- Per-agent token usage tracking
- WebSocket support for real-time communication

[Unreleased]: https://github.com/cnjack/jcode/compare/v0.12.2...HEAD
[0.12.2]: https://github.com/cnjack/jcode/compare/v0.12.1...v0.12.2
[0.12.1]: https://github.com/cnjack/jcode/compare/v0.11.4...v0.12.1
[0.11.4]: https://github.com/cnjack/jcode/compare/v0.11.3...v0.11.4
[0.11.3]: https://github.com/cnjack/jcode/compare/v0.11.2...v0.11.3
[0.11.2]: https://github.com/cnjack/jcode/compare/v0.11.1...v0.11.2
[0.11.1]: https://github.com/cnjack/jcode/compare/v0.10.1...v0.11.1
[0.10.1]: https://github.com/cnjack/jcode/compare/v0.9.6...v0.10.1
[0.9.6]: https://github.com/cnjack/jcode/compare/v0.9.5...v0.9.6
[0.9.5]: https://github.com/cnjack/jcode/compare/v0.9.4...v0.9.5
[0.9.4]: https://github.com/cnjack/jcode/compare/v0.9.3...v0.9.4
[0.9.3]: https://github.com/cnjack/jcode/compare/v0.9.2...v0.9.3
[0.9.2]: https://github.com/cnjack/jcode/compare/v0.9.1...v0.9.2
[0.9.1]: https://github.com/cnjack/jcode/compare/v0.8.1...v0.9.1
[0.8.1]: https://github.com/cnjack/jcode/compare/v0.7.2...v0.8.1
[0.7.2]: https://github.com/cnjack/jcode/compare/v0.7.1...v0.7.2
[0.7.1]: https://github.com/cnjack/jcode/compare/v0.6.4...v0.7.1
[0.6.4]: https://github.com/cnjack/jcode/compare/v0.6.3...v0.6.4
[0.6.3]: https://github.com/cnjack/jcode/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/cnjack/jcode/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/cnjack/jcode/compare/v0.5.2...v0.6.1
[0.5.2]: https://github.com/cnjack/jcode/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/cnjack/jcode/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/cnjack/jcode/compare/v0.4.11...v0.5.0
[0.4.11]: https://github.com/cnjack/jcode/compare/v0.4.10...v0.4.11
[0.4.10]: https://github.com/cnjack/jcode/compare/v0.4.9...v0.4.10
[0.4.9]: https://github.com/cnjack/jcode/compare/v0.4.8...v0.4.9
[0.4.8]: https://github.com/cnjack/jcode/compare/v0.4.7...v0.4.8
[0.4.7]: https://github.com/cnjack/jcode/compare/v0.4.6...v0.4.7
[0.4.6]: https://github.com/cnjack/jcode/compare/v0.4.5...v0.4.6
[0.4.5]: https://github.com/cnjack/jcode/compare/v0.4.4...v0.4.5
[0.4.4]: https://github.com/cnjack/jcode/compare/v0.4.3...v0.4.4
[0.4.3]: https://github.com/cnjack/jcode/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/cnjack/jcode/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/cnjack/jcode/compare/v0.3.10...v0.4.1
[0.3.10]: https://github.com/cnjack/jcode/compare/v0.3.9...v0.3.10
[0.3.9]: https://github.com/cnjack/jcode/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/cnjack/jcode/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/cnjack/jcode/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/cnjack/jcode/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/cnjack/jcode/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/cnjack/jcode/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/cnjack/jcode/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/cnjack/jcode/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/cnjack/jcode/compare/v0.2.2...v0.3.1
[0.2.2]: https://github.com/cnjack/jcode/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/cnjack/jcode/compare/v0.1.1...v0.2.1
[0.1.1]: https://github.com/cnjack/jcode/compare/v0.0.5...v0.1.1
[0.0.5]: https://github.com/cnjack/jcode/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/cnjack/jcode/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/cnjack/jcode/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/cnjack/jcode/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/cnjack/jcode/releases/tag/v0.0.1
