package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ExecuteInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // milliseconds
}

func (e *Env) NewExecuteTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "execute",
		Desc: "Executes a bash command and returns its output. Works on both local and remote (SSH) machines.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"command": {
				Type:     schema.String,
				Desc:     "The command to execute.",
				Required: true,
			},
			"timeout": {
				Type:     schema.Integer,
				Desc:     "Optional timeout in milliseconds (max 600000ms / 10 minutes). Default is 120000ms (2 minutes).",
				Required: false,
			},
		}),
	}

	return &executeTool{env: e, info: info}
}

type executeTool struct {
	env  *Env
	info *schema.ToolInfo
}

func (et *executeTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return et.info, nil
}

func (et *executeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input ExecuteInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse input: %w", err)
	}

	if input.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := 120000 // 2 min default
	if input.Timeout > 0 {
		timeout = input.Timeout
		if timeout > 600000 {
			timeout = 600000
		}
	}

	stdout, stderr, err := et.env.Exec.Exec(ctx, input.Command, et.env.pwd, time.Duration(timeout)*time.Millisecond)

	var result strings.Builder
	if stdout != "" {
		result.WriteString("STDOUT:\n")
		result.WriteString(stdout)
	}
	if stderr != "" {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString("STDERR:\n")
		result.WriteString(stderr)
	}

	if err != nil {
		return result.String(), fmt.Errorf("command failed: %w", err)
	}
	if result.Len() == 0 {
		return "Command executed successfully (no output)", nil
	}

	return result.String(), nil
}
