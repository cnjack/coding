package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ReadInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func (e *Env) NewReadTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "read",
		Desc: "Reads a file. Works on both local and remote (SSH) machines.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_path": {
				Type:     schema.String,
				Desc:     "The absolute path to the file to read.",
				Required: true,
			},
			"offset": {
				Type:     schema.Integer,
				Desc:     "The line number to start reading from (0-indexed).",
				Required: false,
			},
			"limit": {
				Type:     schema.Integer,
				Desc:     "The number of lines to read.",
				Required: false,
			},
		}),
	}

	return &readTool{env: e, info: info}
}

type readTool struct {
	env  *Env
	info *schema.ToolInfo
}

func (r *readTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return r.info, nil
}

func (r *readTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input ReadInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse input: %w", err)
	}

	if input.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	content, err := r.env.Exec.ReadFile(ctx, input.FilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", input.FilePath, err)
	}

	if input.Offset == 0 && input.Limit == 0 {
		return string(content), nil
	}

	lines := strings.Split(string(content), "\n")
	start := input.Offset
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}

	end := len(lines)
	if input.Limit > 0 && start+input.Limit < end {
		end = start + input.Limit
	}

	var result strings.Builder
	for i := start; i < end; i++ {
		result.WriteString(fmt.Sprintf("%6d\t%s\n", i+1, lines[i]))
	}

	return result.String(), nil
}
