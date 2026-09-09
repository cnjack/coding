package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const MaxReadFileSize = 10 * 1024 * 1024 // 10MB
const defaultReadLimit = 2000

// maxReadResultBytes caps the total formatted output of a single read call
// (~50K tokens at ~4 bytes/token). It is a tool-level backstop so one read
// cannot blow the context window even when no reduction middleware is
// attached (subagents) or the middleware fails open.
const maxReadResultBytes = 200 * 1024

// maxReadLineBytes caps a single output line so one minified/overlong line
// cannot consume the whole output budget.
const maxReadLineBytes = 2000

type ReadInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func (e *Env) NewReadTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "read",
		Desc: `Reads a file with line numbers. Works on both local and remote (SSH) machines.
If the path is a directory, it returns the directory structure instead.
Output format: line numbers followed by │ and content. Default limit is 2000 lines.
Lines longer than 2000 bytes are cut with an inline "[line truncated: N more bytes]" marker.
Total output is capped at 200KB; when hit, a message tells you which offset to use to continue reading.
Binary files (images, executables, etc.) are detected and rejected.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_path": {
				Type:     schema.String,
				Desc:     "The absolute path to the file to read (preferred). Relative paths are resolved against the working directory.",
				Required: true,
			},
			"offset": {
				Type:     schema.Integer,
				Desc:     "The line number to start reading from (0-indexed).",
				Required: false,
			},
			"limit": {
				Type:     schema.Integer,
				Desc:     "The number of lines to read. Default 2000.",
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
		return "", toolErrf("invalid_args", hintInvalidJSON, "failed to parse input: %w", err)
	}

	if input.FilePath == "" {
		return "", toolErrf("missing_param", missingParamHint("file_path"), "file_path is required")
	}
	input.FilePath = r.env.ResolvePath(input.FilePath)

	// Managed deny-read policy: enforced before any stat/read so denied paths
	// neither reveal existence nor contents. Holds in every approval mode.
	if err := r.env.checkDenyRead("read", input.FilePath); err != nil {
		return "", err
	}

	// Binary detection by extension (before reading content).
	if detectBinaryByExtension(input.FilePath) {
		return "", fmt.Errorf("cannot read binary file %s (detected by extension %s)",
			input.FilePath, strings.ToLower(getExt(input.FilePath)))
	}

	stat, err := r.env.Exec.Stat(ctx, input.FilePath)
	if err != nil {
		return "", toolErrf("read_failed", readFailHint(err), "failed to stat file %s: %w", input.FilePath, err)
	}
	if !stat.Exists {
		return "", toolErrf("file_not_found", hintFileNotFound, "file %s does not exist", input.FilePath)
	}

	if stat.IsDir {
		out, serr, err := ExecReadOnly(
			ctx, r.env.Exec, fmt.Sprintf("ls -la %s", ShellQuote(input.FilePath)), "", 10*time.Second,
		)
		if err != nil {
			return "", fmt.Errorf("failed to list directory %s: %w\n%s", input.FilePath, err, serr)
		}
		return fmt.Sprintf("Path %s is a directory. Here is its structure:\n%s", input.FilePath, out), nil
	}

	content, err := r.env.Exec.ReadFile(ctx, input.FilePath)
	if err != nil {
		return "", toolErrf("read_failed", readFailHint(err), "failed to read file %s: %w", input.FilePath, err)
	}

	// File size check.
	if len(content) > MaxReadFileSize {
		return "", fmt.Errorf("file %s is too large (%d bytes, max %d). Use offset and limit to read a specific range",
			input.FilePath, len(content), MaxReadFileSize)
	}

	// Content-based binary detection.
	if detectBinaryByContent(content) {
		return "", fmt.Errorf("cannot read binary file %s (binary content detected)", input.FilePath)
	}

	// Track the read in FileTracker if available (local only: the tracker
	// stats the local filesystem, which cannot see remote files).
	if r.env.FileTracker != nil && !r.env.IsRemote() {
		modTime := time.Now()
		if info, err := os.Stat(input.FilePath); err == nil {
			modTime = info.ModTime()
		}
		r.env.FileTracker.TrackRead(input.FilePath, content, modTime)
	}

	lines := strings.Split(string(content), "\n")
	totalLines := len(lines)

	start := input.Offset
	if start < 0 {
		start = 0
	}
	if start > totalLines {
		start = totalLines
	}

	// Apply default limit if not specified.
	limit := input.Limit
	if limit <= 0 {
		limit = defaultReadLimit
	}

	end := totalLines
	if start+limit < end {
		end = start + limit
	}

	// Format with line numbers.
	var result strings.Builder
	budgetHit := false
	lastEmitted := end - 1
	for i := start; i < end; i++ {
		text, truncated := truncateLine(lines[i], maxReadLineBytes)
		if truncated {
			fmt.Fprintf(&result, "%4d │ %s… [line truncated: %d more bytes]\n", i+1, text, len(lines[i])-len(text))
		} else {
			fmt.Fprintf(&result, "%4d │ %s\n", i+1, text)
		}
		if result.Len() >= maxReadResultBytes {
			lastEmitted = i
			budgetHit = true
			break
		}
	}

	// Truncation message.
	switch {
	case budgetHit && lastEmitted+1 < totalLines:
		// Total output budget hit before the requested range was exhausted.
		// The offset parameter is 0-indexed, so offset=lastEmitted+1 points
		// at the first line that was not emitted.
		fmt.Fprintf(&result, "\n... (output truncated at %d bytes: showed lines %d-%d of %d. Use offset=%d to continue)\n",
			maxReadResultBytes, start+1, lastEmitted+1, totalLines, lastEmitted+1)
	case end < totalLines:
		fmt.Fprintf(&result, "\n... (%d more lines, total %d. Use offset=%d to continue)\n",
			totalLines-end, totalLines, end)
	}

	return result.String(), nil
}

// truncateLine cuts line down to at most maxBytes bytes, backing up to the
// nearest UTF-8 rune boundary so the cut never splits a multi-byte rune.
// The second return value reports whether truncation happened.
func truncateLine(line string, maxBytes int) (string, bool) {
	if len(line) <= maxBytes {
		return line, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut], true
}

// getExt returns the file extension including the dot.
func getExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' || path[i] == '\\' {
			break
		}
	}
	return ""
}
