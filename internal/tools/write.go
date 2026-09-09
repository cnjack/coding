package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const MaxWriteFileSize = 10 * 1024 * 1024 // 10MB

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
				Desc:     "The absolute path to the file to write (preferred). Relative paths are resolved against the working directory.",
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
		return "", toolErrf("invalid_args", hintInvalidJSON, "failed to parse input: %w", err)
	}

	if input.FilePath == "" {
		return "", toolErrf("missing_param", missingParamHint("file_path"), "file_path is required")
	}
	input.FilePath = w.env.ResolvePath(input.FilePath)

	// Managed deny-read policy: denied paths are off-limits to writes too —
	// writing (and the read-back/conflict detection below) would leak or
	// corrupt protected content.
	if err := w.env.checkDenyRead("write", input.FilePath); err != nil {
		return "", err
	}

	// File size limit.
	if len(input.Content) > MaxWriteFileSize {
		return "", fmt.Errorf("content too large (%d bytes, max %d)", len(input.Content), MaxWriteFileSize)
	}

	fi, err := w.env.Exec.Stat(ctx, input.FilePath)
	if err != nil {
		return "", fmt.Errorf("failed to inspect file %s before writing: %w", input.FilePath, err)
	}
	isNew := fi == nil || !fi.Exists

	var oldContent string
	var backupPath string

	if !isNew {
		// Conflict detection. Remote (SSH/Docker) sessions skip it: the
		// tracker stats the local filesystem and would misreport remote
		// files as gone or unread.
		if w.env.FileTracker != nil && !w.env.IsRemote() {
			cr, err := w.env.FileTracker.CheckConflict(input.FilePath)
			if err == nil {
				switch cr.Status {
				case ConflictModified:
					return fmt.Sprintf("conflict: file %s was modified externally since last read. Please re-read the file before writing", input.FilePath), nil
				case ConflictFileGone:
					// File was deleted externally; treat as new.
					isNew = true
				case ConflictNeverRead:
					return fmt.Sprintf("file %s already exists but has not been read yet. Use the read tool to read it before overwriting", input.FilePath), nil
				}
			}
		}

		// Read existing content for backup and diff.
		existing, err := w.env.Exec.ReadFile(ctx, input.FilePath)
		if err != nil {
			return "", fmt.Errorf("failed to read existing file %s before writing: %w", input.FilePath, err)
		}
		oldContent = string(existing)
		// Backup before overwriting.
		if w.env.FileTracker != nil {
			bp, bErr := w.env.FileTracker.CreateBackup(input.FilePath, existing)
			if bErr == nil {
				backupPath = bp
			}
		}
	}

	if err := w.env.Exec.WriteFile(ctx, input.FilePath, []byte(input.Content), 0644); err != nil {
		return "", toolErrf("write_failed", hintWriteFailed, "failed to write file %s: %w", input.FilePath, err)
	}

	// Update FileTracker after write (local only: os.Stat cannot see remote files).
	if w.env.FileTracker != nil && !w.env.IsRemote() {
		if info, err := os.Stat(input.FilePath); err == nil {
			w.env.FileTracker.UpdateAfterWrite(input.FilePath, []byte(input.Content), info.ModTime())
		}
	}

	lines := strings.Count(input.Content, "\n") + 1
	action := "Created"
	if !isNew {
		action = "Wrote"
	}

	var result strings.Builder
	fmt.Fprintf(&result, "%s %s (%d lines, %d bytes)", action, input.FilePath, lines, len(input.Content))
	if backupPath != "" {
		fmt.Fprintf(&result, "\nBackup: %s", backupPath)
	}

	// Unified diff for overwrites.
	if !isNew && oldContent != "" && oldContent != input.Content {
		diff := generateUnifiedDiff(oldContent, input.Content, filepath.Base(input.FilePath))
		if diff != "" {
			result.WriteString("\n\n```diff\n")
			result.WriteString(diff)
			result.WriteString("```")
		}
	}

	return result.String(), nil
}
