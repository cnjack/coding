# AGENTS.md — JCode Project Development Guide

Go coding agent — [Eino](https://github.com/cloudwego/eino) + BubbleTea v2 TUI + React web UI + Tauri 2 desktop shell.

- **Module:** `github.com/cnjack/jcode` | **Entry:** `cmd/jcode/` | **Config dir:** `~/.jcode/`

---

## Quick Start

```bash
make build          # generate → build-web → go build
make install        # generate → build-web → go install
make run            # go run ./cmd/jcode/
make lint           # golangci-lint + React typecheck (lint-go / lint-web)
make doctor         # system check
make desktop-dev    # Tauri desktop app in dev mode (rebuilds the Go sidecar first)
make desktop-build  # distributable desktop bundle (.app/.dmg/.msi)
make build-ble      # optional jcode-ble helper (BLE is a separate binary by design)
```

- `make build-web` requires `pnpm`. Frontend builds to `internal/web/dist/`.
- `make generate` runs `go generate` for `internal/model/...` (fetches models.dev data → `registry_generated.go`) **and** `internal/theme/...` (palette → web CSS/TS tokens). **Never edit generated files by hand.**
- Build injects `Version`, `BuildTime`, `GitCommit` via ldflags into `internal/command`.

---

## Architecture Overview

```
cmd/jcode/           # Entry: cobra root (interactive) + subcommands; native-messaging host detour
cmd/jcode-ble/       # BLE helper binary (spawned by the main binary; keeps CoreBluetooth out of jcode)
internal/
  command/           # Subcommand implementations + interactive session orchestration + tool wiring
  agent/             # ChatModelAgent factory + middleware chain
  runner/            # Agent run loop + event bus + approval tiering
  handler/           # AgentEventHandler interface (TUI / ACP / Web implementations)
  tools/             # All built-in tools + Executor/Env abstraction
  model/             # OpenAI-compatible chat model + model registry (build-time generated)
  config/            # JSON config loader + logger
  prompts/           # System/plan prompts + AGENTS.md injection + env info
  session/           # JSONL session recording/replay
  skills/            # Skill loader (builtin → user → project override chain)
  team/              # Multi-agent team coordination
  mode/              # Unified session mode (Ask / Plan / Autopilot) shared by all transports
  automation/        # Scheduled/manual agent runs (each run = a tagged session)
  browser/           # Browser use: managed Chrome (CDP) + extension WS bridge + native-messaging host
  remote/            # SSH / Docker connection helpers (UI-agnostic; /api/remote/*)
  channel/           # External messaging channels (e.g. WeChat push)
  theme/             # Single source of truth for built-in themes (generates web CSS/TS)
  usage/             # Token-usage recording + aggregation (stats views)
  feature/           # Compile-time feature flags via build tags (e.g. desktop, jcode_headless, ble)
  telemetry/         # Optional Langfuse tracing
  tui/               # BubbleTea v2 TUI components
  web/               # HTTP server (REST + WS + PTY) + embedded React dist
web/                 # React 18 + Vite + RTK product UI (embedded in the binary / Tauri)
packages/            # publish source for the jcode-ui npm packages (still in root pnpm workspace)
  jcode-ui/          #   styled React chat components → npm: jcode-ui
  jcode-ui-core/     #   types, ChatRuntime, headless primitives → npm: jcode-ui-core
site/                # React + Vite marketing/docs site → www.j-code.net (docs markdown in site/docs/)
desktop/             # Tauri 2 desktop shell; the Go binary runs as a sidecar
extension/           # jcode Browser Bridge Chrome extension (MV3) for the browser-use extension backend
internal-doc/        # Internal design docs (NOT published; site/docs is the published documentation)
script/              # Build-time code generation + install.sh
agent-eval/          # Agent evaluation harness + showcase generation
```

### Frontend (React)

The product UI is React 18 (`web/`). **Internal** consumers — `web/` and `packages/jcode-ui` — link `jcode-ui` / `jcode-ui-core` via the `workspace:*` protocol so the monorepo co-evolves and `make build-web` builds from local source (not the published tarballs). **External** consumers — `site/`, `examples/*` — use registry version ranges.

- `make build-web` typechecks/builds package `dist/` then builds the React app → `internal/web/dist/` (production embed).
- `make lint-web` typechecks the React app + both packages.
- Go `//go:embed dist/*` and Tauri `frontendDist` both point at `internal/web/dist/`.
- Internal deps use `workspace:*` (`web/package.json` → `jcode-ui`/`jcode-ui-core`; `packages/jcode-ui` → `jcode-ui-core`). External consumers (`site/`, `examples/*`) declare registry ranges, e.g. `"jcode-ui": "^0.4.1"`, `"jcode-ui-core": "^0.4.0"`.
- Inside `packages/jcode-ui`, depend on core via `workspace:*` — **never** `file:../jcode-ui-core` (a `file:` link baked into the published `jcode-ui` tarball broke `jcode-ui@0.1.0` on npm).

See `packages/jcode-ui/README.md` and `site/docs/chat-ui/`. Published: [jcode-ui](https://www.npmjs.com/package/jcode-ui) (styled) + [jcode-ui-core](https://www.npmjs.com/package/jcode-ui-core) (headless). The runtime abstraction (`ChatRuntime` + `createExternalStoreRuntime`) is the seam that lets the components render from any Redux-shaped store.

#### Publishing jcode-ui to npm

Order matters: **publish `jcode-ui-core` first, then `jcode-ui`**.

```bash
# 1) bump versions in packages/*/package.json as needed
# 2) build
cd packages/jcode-ui-core && pnpm build
cd ../jcode-ui && pnpm build

# 3) publish (2FA/OTP may be required)
cd packages/jcode-ui-core && npm publish --access public --otp=XXXXXX
cd ../jcode-ui && npm publish --access public --otp=XXXXXX
```

After a successful publish, bump the **external** consumer deps (`site/`, `examples/*`) to the new range and run `pnpm install` (and `cd site && pnpm install` for the site workspace). Also refresh `minimumReleaseAgeExclude` entries in every `pnpm-workspace.yaml` (root, `site/`, `examples/*`) so the newly published versions are not blocked by release-age checks. `web/` needs no bump — it links local source via `workspace:*` and picks up `packages/` edits on the next `make build-web`.

Checklist before publish:

1. `jcode-ui` → `jcode-ui-core` uses `workspace:*` locally (never `file:`); only external consumers (`site/`, `examples/*`) carry registry ranges
2. Both packages have fresh `dist/` (`pnpm build`)
3. Smoke: `npm install jcode-ui@<ver>` in a temp dir imports both packages and pulls core transitively
4. After publish: update `minimumReleaseAgeExclude` in all `pnpm-workspace.yaml` files to the new `jcode-ui` / `jcode-ui-core` versions (drop stale entries)

### Key Design Decisions

- **Three transports, one interface:** TUI, ACP (JSON-RPC), and Web all implement `AgentEventHandler`. New transports only need to implement this interface.
- **Middleware chain in `agent/`:** Ordered outermost→innermost: langfuse → budget → compaction → recovery → approval. **Approval is always innermost** — never add middleware after it.
- **Tools are methods on `*Env`:** Each tool is created via `env.NewXxxTool()`, receives the shared `Env` for file I/O and command execution. This enables transparent local/SSH switching.
- **Eino framework:** We use [cloudwego/eino](https://github.com/cloudwego/eino) `adk.ChatModelAgent` — not raw LLM calls. Follow Eino's `tool.InvokableTool` + `schema.ToolInfo` patterns.

---

## Conventions (MUST follow)

### Logging & Output
- **All diagnostics go to `config.Logger()`** (writes to `~/.jcode/debug.log`). Never use `fmt.Print`, `log.Print`, or write to stdout/stderr directly — the TUI owns stdout.
- Tool execution errors are returned as plain strings (the agent reads them). Do NOT `panic` or `log.Fatal` in tool code.
- Exclude the script/ directory from the linter — it contains code generation scripts that may not follow all conventions.

### Error Handling
- Tools return `(string, error)`. Return descriptive error strings that help the agent self-correct. Include file paths, line numbers, or command output in error messages.
- Use `fmt.Errorf("tool_name: %w", err)` for wrapped errors in non-tool code.

### File Paths
- All file paths in tools must be resolved via `env.ResolvePath(path)`. This handles relative→absolute conversion and logs warnings for paths escaping the working directory.
- Store and pass absolute paths internally. Only accept relative paths at the tool input boundary.

### Tool Development Pattern
1. Define `XxxInput` struct with `json` tags
2. Create `func (e *Env) NewXxxTool() tool.InvokableTool` on `*Env`
3. Build `schema.ToolInfo` with `schema.NewParamsOneOfByParams(...)` — use `schema.String`, `schema.Integer`, `schema.Boolean`, `schema.Array`
4. Register it in **all three transports**: `buildAllTools()` / `buildPlanTools()` in `internal/command/interactive.go`, and the inline `all`/`plan` tool lists in `acp.go` and `web.go`
5. If the tool is read-only, also add it to the plan-mode list
6. **Approval policy:** Read-only tools skip approval. Mutating tools require approval unless `AutoApprove` is set. Match existing patterns in `internal/runner/approval.go` (browser tools additionally tier by action/origin there).

### Approval Policy
- Read-only tools (read, grep, glob, todoread, etc.): auto-approved
- Mutating tools (edit, write, execute): require user approval
- Execute exceptions: background commands and safe prefixes (`ls`, `cat`, `echo`, `which`, `git status`, `git log`, etc.) are auto-approved
- `switch_env`: always requires approval

### Code Style
- Follow standard Go conventions. The linter config (`.golangci.yml`) enforces: `errcheck`, `govet`, `staticcheck`, `unused`, `revive`, `gocritic`, `funlen` (max 800 lines/600 statements).
- Use `context.Context` as the first parameter. Thread cancellation properly.
- Prefer returning errors over panicking. Only `panic` for truly unrecoverable programmer errors.
- Interfaces live in the package that consumes them (e.g., `AgentEventHandler` in `handler/`, `Executor` in `tools/`).

### Concurrency
- `AgentEventHandler` implementations must be goroutine-safe — the runner may call methods from multiple goroutines simultaneously.
- Use `sync.RWMutex` for shared state in tools (see `TodoStore`, `BackgroundManager`).
- Channel-based coordination in `cmd/jcode/main.go` — the main event loop uses `for/select` over typed channels.

---

## How To: Common Tasks

### Add a New Tool
1. Create `internal/tools/xxx.go` with input struct + `NewXxxTool()` method on `*Env`
2. Add to `buildAllTools()`/`buildPlanTools()` in `interactive.go` and the inline tool lists in `acp.go` and `web.go`
3. If read-only and useful in plan mode, also add to the plan-mode lists
4. Set appropriate approval requirements following the approval policy above
5. If the tool needs external dependencies (like `BackgroundManager`), pass them as parameters to `NewXxxTool()`

### Add a New Middleware
1. Implement `adk.ChatModelAgentMiddleware` in `internal/agent/`
2. Add a functional option `WithXxx()` following the `WithCompaction`, `WithBudget`, `WithRecovery` pattern
3. Wire it up in `internal/command/interactive.go` where the agent is constructed
4. **Insert before approval** in the middleware chain — approval must remain innermost

### Add a New Handler (Transport)
1. Implement `handler.AgentEventHandler` interface in `internal/handler/`
2. Create a new subcommand in `internal/command/` if needed
3. The interface is the only contract — keep handler logic independent of agent internals

### Add a Builtin Skill
1. Create `internal/skills/builtin/{name}/SKILL.md` with frontmatter (`name`, `description`, optional `slash`)
2. It will be automatically embedded via `//go:embed builtin` and loaded by `skills.Loader`
3. Skill override chain (later wins): builtin → `~/.agents/skills/` → `~/.jcode/skills/` → `.jcode/skills/`

### Modify Config Schema
1. Add fields to the appropriate struct in `internal/config/config.go`
2. Use `json:"field_name,omitempty"` tags for optional fields
3. Config is a flat JSON file at `~/.jcode/config.json` — keep it backward-compatible

### Modify Model Registry
- `internal/model/registry_generated.go` is **auto-generated** — edit `script/generate_models.go` instead
- Run `make generate` to regenerate

### Write Documentation
- **Published docs** live in `site/docs/**/*.md` (rendered at https://www.j-code.net/docs by the React site in `site/`). Pages need front-matter `title` + `nav_order` (+ `parent` for children) to appear in the nav.
- **Internal design docs** (PRDs, design notes, research) go in `internal-doc/` — never in `site/docs/`.
- Doc screenshots live in `site/docs/asset/` (for GitHub rendering) and `site/public/docs-asset/` (served by the site) — keep both copies in sync.

---

## Things to Avoid

- **Don't write to stdout/stderr.** The TUI controls the terminal. Use `config.Logger()` for debug output.
- **Don't add middleware after approval.** The approval middleware must be the innermost handler in the chain.
- **Don't manually edit `registry_generated.go`.** It's overwritten by `make generate`.
- **Don't use `os.Exit()` in library code.** Only `cmd/jcode/main.go` should exit.
- **Don't store mutable state in tool closures.** Use `*Env` or pass state explicitly. Tools may be re-created across mode transitions (normal ↔ plan).
- **Don't skip `env.ResolvePath()`.** Raw path concatenation can escape the working directory without warning.
- **Don't import `internal/tui` from non-TUI packages.** The handler interface is the decoupling boundary.
- **Don't depend on `jcode-ui` / `jcode-ui-core` via `file:`.** Internal links (`web/`, `packages/jcode-ui`→core) use `workspace:*`; external consumers (`site/`, `examples/*`) use registry version ranges.
- **Don't let tests read the real HOME.** The pre-push hook runs the full suite; tests must `t.Setenv("HOME", t.TempDir())` whenever code under test resolves `config.ConfigDir()` (a real `~/.jcode/AGENTS.md` breaks `internal/prompts`).

---

## Security: project trust & managed deny-read

- **Project `AGENTS.md` is gated on trust.** `internal/prompts.MemoryLoader.Load` loads the global `~/.jcode/AGENTS.md` always, but the project walk-up chain + `AGENTS.local.md` only for trusted projects (`internal/config/trust.go`: `~/.jcode/project_trust.json`, `jcode trust`/`untrust`, or `JCODE_AGENTS_TRUST_PROJECT=1`). Untrusted is the default — a fresh clone must not inject system-prompt content. Tests that assert project AGENTS.md content must opt in (`t.Setenv("JCODE_AGENTS_TRUST_PROJECT", "1")` + isolated HOME).
- **Managed deny-read rules are user-owned policy.** Global config `deny_read` (`internal/tools/readpolicy.go`) is enforced inside tool execution (below approval — no mode can bypass it), never merged from project config, and shared by every `*Env` via one process-wide policy object (subagents/teammates/workflows/remote switch inherit; runtime updates are strengthen-only). New file-access tools should call `env.checkDenyRead(tool, path)` (or `checkDenyReadCommand` for shell) right after path resolution and return its stable `path_denied_by_policy` error.

---

## Cloud relay (`internal/cloud/`)

jcode can log into jcloud (device code) and be remote-controlled via an
outbound-only relay; design contract lives in the cloud repo
(`docs/17-jcode-device-relay.md`). Durable rules:

- **Layout:** `credentials.go` (~/.jcode/cloud.json, 0600), `client.go`
  (transport, 32MB response cap — attachment commands are large),
  `connector.go` + `supervisor.go` (lifecycle, runtime auto_connect toggle),
  `events.go` (WS event pump: durable vs ephemeral classification +
  `agent_message` synthesis), `sessions.go` (index + capabilities mirror),
  `crypto.go` (CEK, AES-256-GCM envelopes, P-256 ECIES, BIP39),
  `pairing_inbox.go` (pairing approvals; QR offers auto-approve).
- **E2EE is the default.** Session content, metadata, command bodies/results,
  and other content-bearing relay payloads are sealed once a CEK exists; gray
  plaintext content is only for pre-CEK or `cloud.e2ee:false`. Plaintext is
  limited to routing metadata required by the orchestrator: ids, seq, kind,
  timestamps (including `last_activity_at`), and online state. Never add a
  content-bearing uplink path that bypasses `sealUplink`/`openDownlink`.
- **The connector is a client of the local control plane** (`/api/*` on
  loopback) — it must never change engine behavior or block `jcode web`;
  failures log and back off (≤60s).
- **Contract changes are cross-repo:** bump the cloud orchestrator first
  (strict decode rejects unknown fields), keep `docs/17` in sync, and extend
  the matching e2e journey in the cloud repo (`e2e/j7`–`j11`).
- **Cross-implementation crypto vectors** live outside the repos at
  `jcode-cloud-relay/shared/test-vectors.json`; `TestSharedVectorsFile` skips
  cleanly when the sibling dir is absent.

---

## Testing

- Run tests: `go test ./...`
- Test files follow `xxx_test.go` convention in the same package
- For tool tests, use `NewEnv()` with a temp directory to isolate file operations
- Lint is mandatory: `make lint` must pass before any PR

---

## Frontend (web/) — React (production)

- **Stack:** React 18 + TypeScript + Vite + Redux Toolkit + npm `jcode-ui` / `jcode-ui-core`
- **Build:** `make build-web` (package typecheck/build + `cd web && npx vite build`)
- **Output:** builds to `internal/web/dist/`, embedded in the Go binary via `//go:embed`
- **Lint:** `make lint-web` (tsc for web + packages)
- Changes to the product UI (`web/`) require rebuilding via `make build-web` for the Go binary to pick them up
- Changes to the chat library (`packages/jcode-ui*`) are picked up by `web/` immediately via `workspace:*` (rebuild with `make build-web`); only `site/`/`examples/*` need **npm publish + version bump + `pnpm install`**
- **Don't confuse `web/` with `site/`:** `web/` is the product UI embedded in the binary and reused by the desktop app; `site/` is the public website + docs at www.j-code.net and is deployed separately (`cd site && pnpm build`).

### Icons & Styling

- **Icons:** use `@heroicons/react/24/outline` exclusively. Import each icon by name. Do **not** hand-write inline `<svg>` icons.
- **Icon sizing:** use Tailwind `h-N w-N` classes (e.g. `className="h-3.5 w-3.5"`).
- **Colors:** every color must come from a CSS custom property (jcode-ui tokens / `tokens.generated.css`). Never hardcode hex/rgb/`#fff`/`white` in components.
- **Themes:** edit `internal/theme/palette.go` and run `make generate` — never edit `tokens.generated.css` or `themes.generated.ts` by hand.
- **Reusable chat UI:** import from the `jcode-ui` package (`workspace:*` in `web/`, registry range in `site/`/`examples/*`); implement/fix library code under `packages/jcode-ui` and publish for external consumers.
- **Product composer:** the desktop input experience (`ChatInput` + `WorkspacePicker` / `BranchPicker` / `GoalBanner`) lives in `packages/jcode-ui/src/product/` and is consumed as `jcode-ui/product`. The components are Redux/fetch/Tauri/i18next-free — hosts inject a `ProductComposerHost` (state + actions + strings + icons); the web adapter is `web/src/app/composerHost.ts`. Package tests: `cd packages/jcode-ui && pnpm test` (vitest).
