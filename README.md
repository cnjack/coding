# Coding Agent

> An AI coding agent that works where your code lives, including remote servers over SSH.

```
 ◆ Let me read the file first.

   ⚙ Tool  read   path=server.go
      ✓ Done: 312 lines read

 ◆ Found it — the goroutine in handleConnection() is never joined.
   I'll patch it now.

   ⚙ Tool  edit   path=server.go

   ╭─────────────────────────────────────────────────────╮
   │  - go handle(conn)                                  │
   │  + wg.Add(1)                                        │
   │  + go func() { defer wg.Done(); handle(conn) }()   │
   ╰─────────────────────────────────────────────────────╯
      ✓ Edit applied

──────────────────────────────────────────────────────────
 > _
──────────────────────────────────────────────────────────
  Model: openai / gpt-4o    Approve: Ask │ Tokens: 2104 / 128000 (2%)
```

---

## What it does

Describe a task with human language. The agent reads your files, writes changes, runs commands, and shows you exactly what it did — step by step.

- **Read, edit & write files** — surgical string-level edits with inline diffs
- **Run shell commands** — output shown in a bordered box, last N lines surfaced
- **Search codebases** — regex grep with ripgrep/grep fallback
- **Track tasks** — a live todo list the agent updates as it works through multi-step jobs
- **Resume sessions** — every conversation is recorded; pick up exactly where you left off

---

## SSH — work on any machine

Type `/ssh user@host` and every tool — file reads, edits, shell commands — runs transparently on the remote host. No separate setup, no changing your workflow.

```
 You › /ssh deploy@10.0.1.5:/var/www/app

   ✓ SSH  Connected · linux/amd64

 You › why is nginx restarting?

 ◆ Let me check the container logs.

   ⚙ Tool  execute  [deploy@10.0.1.5]  docker logs app-nginx-1 --tail 20

   ╭─────────────────────────────────────────────────────╮
   │  nginx: [emerg] bind() to 0.0.0.0:80 failed        │
   │  (98: Address already in use)                       │
   ╰─────────────────────────────────────────────────────╯

 ◆ Port 80 is already taken. Let me find what's holding it.
```

Save connections as named aliases and jump between hosts with a keypress:

```
  ┌─────── /ssh ────────────────────────────────────┐
  │  > 🔗 prod        deploy@10.0.1.5:/var/www/app  │
  │    🔗 staging      ci@10.0.1.8:/srv/staging      │
  │    ➕ Connect New SSH                             │
  └─────────────────────────────────────────────────┘
```

---

## More

- **Any OpenAI-compatible provider** — switch models mid-session with `/model`
- **MCP servers** — connect HTTP / SSE / stdio MCP servers; their tools merge with the built-ins
- **Approval mode** — default is *Ask* (confirm each tool call); toggle to *Auto* for unattended runs
- **Session resume** — `/resume` brings back the full conversation history for any past project session

```
  ┌──────────────── Resume Session ─────────────────┐
  │  > 2026-03-12  gpt-4o      fix nginx crash       │
  │    2026-03-11  gpt-4o      refactor auth module  │
  │    2026-03-10  claude-3.5  add pagination logic  │
  └─────────────────────────────────────────────────┘
```

---

Config is created on first launch at `~/.jcoding/config.json`. Run `coding --doctor` to verify connectivity.

---

MIT
