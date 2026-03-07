package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type WriteInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func (e *Env) NewWriteTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "write",
		Desc: "Write content to a file, creating it if it doesn't exist or overwriting if it does. Works on both local and remote (SSH) machines.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_path": {
				Type:     schema.String,
				Desc:     "The absolute path to the file to write.",
				Required: true,
			},
			"content": {
				Type:     schema.String,
				Desc:     "The full content to write to the file.",
				Required: true,
			},
		}),
	}

	return &writeTool{env: e, info: info}
}

type writeTool struct {
	env  *Env
	info *schema.ToolInfo
}

func (w *writeTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return w.info, nil
}

func (w *writeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input WriteInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse input: %w", err)
	}

	if input.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	fi, _ := w.env.Exec.Stat(ctx, input.FilePath)
	isNew := fi == nil || !fi.Exists

	if err := w.env.Exec.WriteFile(ctx, input.FilePath, []byte(input.Content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", input.FilePath, err)
	}

	lines := strings.Count(input.Content, "\n") + 1
	action := "Created"
	if !isNew {
		action = "Wrote"
	}

	return fmt.Sprintf("%s %s (%d lines, %d bytes)", action, input.FilePath, lines, len(input.Content)), nil
}
