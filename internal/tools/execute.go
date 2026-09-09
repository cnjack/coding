package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/cnjack/jcode/internal/config"
)

// StreamChunk represents a chunk of command output.
//
// Phase E (live execute output deltas over WS) is intentionally deferred:
// emitting partial chunks requires runner/handler transport changes that would
// risk half-landed events. Types are kept here so a future delta path can
// reuse the same dual-channel streams shape without breaking callers.
type StreamChunk struct {
	Data      string
	Timestamp time.Time
	IsStderr  bool
}

// ToolProgressMsg conveys partial progress for long-running commands.
type ToolProgressMsg struct {
	ToolName      string
	PartialOutput string
	ElapsedSec    int
}

const (
	defaultTimeoutMs = 120000
	maxTimeoutMs     = 600000
	bgHintThreshold  = 15 * time.Second
)

type ExecuteInput struct {
	Command     string `json:"command"`
	Timeout     int    `json:"timeout,omitempty"`     // milliseconds
	Background  bool   `json:"background,omitempty"`  // run in background
	Description string `json:"description,omitempty"` // short human-readable description
}

func (e *Env) NewExecuteTool(bm *BackgroundManager) tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "execute",
		Desc: "Executes a bash command. By default runs synchronously and returns output. " +
			"Set background=true to run in the background (returns immediately with a task ID). " +
			"Use background mode for long-running commands (npm install, go test, docker build, etc.) " +
			"so you can keep working. Check results later with check_background. " +
			"Never use sleep, polling loops, or background shell commands to implement a user's delayed, future-time, or recurring request; use automation_create instead.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"command": {
				Type:     schema.String,
				Desc:     "The command to execute.",
				Required: true,
			},
			"timeout": {
				Type:     schema.Integer,
				Desc:     "Optional timeout in milliseconds (max 600000ms / 10 minutes). Default is 120000ms (2 minutes). Ignored when background=true.",
				Required: false,
			},
			"background": {
				Type:     schema.Boolean,
				Desc:     "If true, run the command in the background and return immediately with a task ID. Default is false.",
				Required: false,
			},
			"description": {
				Type: schema.String,
				Desc: "Clear, concise description of what this command does in 5-10 words. Examples:\n" +
					"Input: ls\nOutput: Lists files in current directory\n" +
					"Input: git status\nOutput: Shows working tree status\n" +
					"Input: npm install\nOutput: Installs package dependencies\n" +
					"Input: mkdir foo\nOutput: Creates directory 'foo'",
				Required: false,
			},
		}),
	}

	return &executeTool{env: e, bm: bm, info: info}
}

// NewPlanExecuteTool returns the Plan-mode execute endpoint. Its schema omits
// background execution and the endpoint itself enforces the shared strict
// read-only shell policy, so hand-written or stale-schema calls cannot bypass
// the mode boundary.
func (e *Env) NewPlanExecuteTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "execute",
		Desc: "Runs a strictly read-only foreground shell command for repository inspection. " +
			"Only the small documented allowlist is accepted; shell syntax, background execution, writes, and helper execution are rejected.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"command": {
				Type:     schema.String,
				Desc:     "A strictly read-only foreground command (for example: ls, cat, git status, git log, git diff, or git show).",
				Required: true,
			},
			"timeout": {
				Type:     schema.Integer,
				Desc:     "Optional timeout in milliseconds (max 600000ms / 10 minutes). Default is 120000ms (2 minutes).",
				Required: false,
			},
			"description": {
				Type:     schema.String,
				Desc:     "Short description of the read-only inspection.",
				Required: false,
			},
		}),
	}
	return &executeTool{env: e, info: info, planOnly: true}
}

type executeTool struct {
	env      *Env
	bm       *BackgroundManager
	info     *schema.ToolInfo
	planOnly bool
}

func (et *executeTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return et.info, nil
}

func (et *executeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input ExecuteInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", toolErrf("invalid_args", hintInvalidJSON, "failed to parse input: %w", err)
	}

	if input.Command == "" {
		return "", toolErrf("missing_param", missingParamHint("command"), "command is required")
	}

	// Managed deny-read policy: refuse commands that reference a denied path
	// (lexical check, see DenyReadPolicy.CheckCommand). Runs before the plan
	// gate and before background dispatch so neither mode nor backgrounding
	// can smuggle a read of a denied path.
	if err := et.env.checkDenyReadCommand("execute", input.Command); err != nil {
		return "", err
	}

	if et.planOnly {
		if input.Background {
			return "", toolErrf(
				"plan_read_only",
				"Run the command in the foreground or leave Plan mode before starting background work.",
				"background execution is not allowed in Plan mode",
			)
		}
		if !IsReadOnlyShellCommand(input.Command) {
			return "", toolErrf(
				"plan_read_only",
				"Use read/grep or a simple allowlisted inspection command; leave Plan mode for mutations or arbitrary shell execution.",
				"command is not allowed by the Plan read-only shell policy",
			)
		}
	}

	// Sleep detection: block dangerous sleep commands.
	if blocked, reason := detectSleep(input.Command); blocked {
		return reason, nil
	}

	// Classify the command for logging/UI hints.
	category := classifyCommand(input.Command)
	config.Logger().Printf("[execute] category=%s command=%s", category, input.Command)

	// Background mode: delegate to BackgroundManager and return immediately.
	if input.Background && et.bm != nil {
		taskID := et.bm.Run(ctx, input.Command)
		return fmt.Sprintf("Background task %s started: %s\nUse check_background to check status.", taskID, input.Command), nil
	}

	timeout := defaultTimeoutMs
	if input.Timeout > 0 {
		timeout = input.Timeout
		if timeout > maxTimeoutMs {
			timeout = maxTimeoutMs
		}
	}

	config.Logger().Printf("[execute] running (timeout=%dms): %s", timeout, input.Command)
	start := time.Now()
	stdout, stderr, err := et.env.Exec.Exec(ctx, input.Command, et.env.pwd, time.Duration(timeout)*time.Millisecond)
	elapsed := time.Since(start)
	config.Logger().Printf("[execute] finished in %v, err=%v", elapsed, err)

	// Dual-channel: ModelOutput keeps the historical labeled string for the
	// LLM; Streams/Meta are reconstructed by the web handler via
	// ParseExecModelOutput for structured UI rendering.
	res := BuildExecResult(stdout, stderr, err, elapsed, input.Command)
	if IsFatal(err) {
		// Preserve the structured RemoteTransportError through approval middleware.
		// In particular, an outcome-unknown SSH command must never be presented to
		// the model as an ordinary failure it may blindly retry.
		return res.ModelOutput, err
	}
	return res.ModelOutput, nil
}
