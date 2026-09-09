---
title: Commands & Shortcuts
nav_order: 8
---

# Commands & Shortcuts

## Command Line

### Subcommands

| Command | Description |
|---|---|
| `jcode` | Start the interactive TUI (default) |
| `jcode web` | Start the web interface |
| `jcode acp` | Start headless JSON-RPC server (for editor integration) |
| `jcode doctor` | Verify model and MCP connectivity |
| `jcode version` | Show version, build time, and commit |
| `jcode update` | Update jcode to the latest version |
| `jcode sessions` | List recorded sessions for this project |
| `jcode trust [path]` | Trust a project so its `AGENTS.md` instructions load (`--status` to inspect) |
| `jcode untrust [path]` | Revoke project trust (project `AGENTS.md` stops loading) |
| `jcode mcp add <name> <url>` | Add an MCP server |
| `jcode mcp list` | List configured MCP servers |
| `jcode mcp login <name>` | Authenticate an existing http/sse MCP server via OAuth |
| `jcode automation list` | List all automations |
| `jcode automation show <id>` | Show an automation's definition and last run state |
| `jcode automation templates` | List built-in automation templates |
| `jcode automation enable <id>` | Enable an automation |
| `jcode automation disable <id>` | Disable an automation |
| `jcode automation delete <id>` | Delete an automation |

{: .note }
> `jcode automation` is also available as `jcode automations` or `jcode auto`. Definition management works standalone; periodic firing is owned by a running `jcode web` process.

### Flags

| Flag | Short | Description |
|---|---|---|
| `--prompt <text>` | `-p` | One-shot mode: run a single prompt and exit |
| `--resume <UUID>` | | Resume a previous session |
| `--unsafe` | | Auto-approve all tool calls |

### Web Server Flags

| Flag | Default | Description |
|---|---|---|
| `--port` | 8080 | HTTP port for web interface |
| `--host` | 127.0.0.1 | Server bind address |
| `--open` | true | Open browser after server starts |

### MCP Add Flags

| Flag | Short | Description |
|---|---|---|
| `--type` | `-t` | Server type: `sse`, `http`, or `stdio` (auto-detected) |
| `--header` | | HTTP header in `Key: Value` format (repeatable) |
| `--env` | | Environment variable in `KEY=VALUE` format (repeatable) |
| `--scope` | `-s` | Config scope (`user`) |
| `--oauth` | | Authenticate via OAuth after adding (http/sse only) |
| `--client-id` | | OAuth client id (manual fallback when dynamic registration is unsupported) |
| `--client-secret` | | OAuth client secret (confidential clients) |
| `--scope-oauth` | | OAuth scope to request (repeatable) |

## Keyboard Shortcuts (TUI)

| Shortcut | Action |
|---|---|
| **Enter** | Submit prompt |
| **Shift+Enter** | Insert new line |
| **Shift+Tab** | Cycle session mode (Ask for approval → Plan → Full access) |
| **Ctrl+L** | Open model picker |
| **F1** / **?** | Open keyboard shortcuts help (`?` when the input is empty) |
| **Ctrl+C** | Exit (press once for confirmation, twice to force) |
| **Ctrl+T** | Toggle team coordinator panel |
| **Ctrl+Y** | Copy last assistant message to clipboard |
| **Shift+Up/Down** | Switch between teammate views |
| **Up/Down** | Navigate prompt history |
| **Esc** | Clear selection / exit teammate view / cancel dialog |
| **Right-click** | Paste from clipboard |

## Slash Commands

Type these in the TUI input area:

| Command | Description |
|---|---|
| `/model` | Switch model mid-session |
| `/theme` | Switch color theme (live preview, persisted to config) |
| `/setting` | Open settings menu |
| `/ssh` | Open SSH connection wizard |
| `/resume` | Resume a previous session |
| `/compact` | Compact conversation context |
| `/goal` | Set a persistent objective the agent works toward ([Goals](goal.html)) |
| `/memory` | Show project memory; `/memory sync` to distill, `/memory clear` to wipe ([Project Memory](overview/learned-memory.html)) |
| `/bg` | Show background tasks |
| `/channel` | Open the channel management panel (WeChat push/messaging) |
| `/mcp` | List MCP servers and status; `/mcp login <name>` to authenticate via OAuth |
| `/help` | Show keyboard shortcuts help |
| `/review-pr` | Run PR review skill |
| `/pr-comments` | Fetch PR comments |
| `/security-review` | Run security review |
| `/submit-pr` | Commit, push the branch, and open a GitHub pull request |
| `/<custom-skill>` | Any skill with a slash command |

## One-Shot Mode

For quick tasks without entering the interactive TUI:

```bash
jcode -p "Explain what main.go does"
jcode -p "Fix the TODO in auth.go"
```

The agent processes the request, displays output, and exits. The session is saved for later review.

## Headless Mode (ACP)

For editor and tool integration:

```bash
jcode acp
```

Starts a JSON-RPC server on stdio using the Agent Communication Protocol. Designed for programmatic access — no TUI.
