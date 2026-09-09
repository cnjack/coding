package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type GlobInput struct {
	Pattern  string `json:"pattern"`
	Path     string `json:"path,omitempty"`
	MaxDepth int    `json:"max_depth,omitempty"`
	Limit    int    `json:"limit,omitempty"` // default 100, max 500
}

const globDefaultLimit = 100
const globMaxLimit = 500

// globExclusions are directories excluded from glob search.
var globExclusions = []string{
	".git", "node_modules", "vendor", "__pycache__", ".venv",
	".svn", ".hg", ".bzr", ".jj", ".sl",
}

func (e *Env) NewGlobTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "glob",
		Desc: "Search for files by gitignore-style glob pattern (requires ripgrep). " +
			"'*' matches within one path segment, '**' matches across directories, and patterns containing '/' are anchored to the search directory. " +
			"Returns file paths relative to the search directory, most recently modified first. " +
			"Respects .gitignore, includes hidden files, and excludes VCS and dependency directories (.git, node_modules, vendor, etc.).",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {
				Type:     schema.String,
				Desc:     "Glob pattern matched against paths relative to the search directory (e.g. '*.go', '**/*.test.ts', 'src/**/*.go', 'Makefile').",
				Required: true,
			},
			"path": {
				Type:     schema.String,
				Desc:     "Directory to search in. Defaults to current working directory.",
				Required: false,
			},
			"max_depth": {
				Type:     schema.Integer,
				Desc:     "Maximum directory depth to search.",
				Required: false,
			},
			"limit": {
				Type:     schema.Integer,
				Desc:     "Maximum number of files to return. Default 100, max 500.",
				Required: false,
			},
		}),
	}

	return &globTool{env: e, info: info}
}

type globTool struct {
	env  *Env
	info *schema.ToolInfo
}

func (g *globTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return g.info, nil
}

func (g *globTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input GlobInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse input: %w", err)
	}

	if input.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if strings.HasPrefix(input.Pattern, "!") {
		return "", fmt.Errorf("invalid pattern %q: a leading '!' would be interpreted as an exclusion glob; use a plain pattern", input.Pattern)
	}

	searchPath := input.Path
	if searchPath == "" {
		searchPath = g.env.Pwd()
	} else {
		// Anchor relative paths to the tool workspace, not the process cwd:
		// the command cd's into searchPath, so an unresolved relative path
		// would glob wherever the jcode process happens to run.
		searchPath = g.env.ResolvePath(searchPath)
	}

	// Managed deny-read policy: denied trees cannot be enumerated either —
	// file names under a denied directory are themselves sensitive.
	if err := g.env.checkDenyRead("glob", searchPath); err != nil {
		return "", err
	}

	limit := globDefaultLimit
	if input.Limit > 0 {
		limit = input.Limit
		if limit > globMaxLimit {
			limit = globMaxLimit
		}
	}

	cmd := buildRgGlobCmd(input.Pattern, searchPath, input.MaxDepth, limit)

	start := time.Now()
	var stdout, stderr string
	var err error

	if g.env.IsRemote() {
		stdout, stderr, err = ExecReadOnly(ctx, g.env.Exec, cmd, "", 30*time.Second)
	} else {
		// Require ripgrep — same hard dependency as the grep tool.
		if _, lookErr := exec.LookPath("rg"); lookErr != nil {
			return "", fmt.Errorf("ripgrep (rg) is required but not found in PATH. Install it: https://github.com/BurntSushi/ripgrep#installation")
		}
		stdout, stderr, err = execLocal(ctx, cmd)
	}
	elapsed := time.Since(start)

	if err != nil {
		if IsFatal(err) {
			return "", err
		}
		// The pipeline exit code is head's (0); a non-zero code means the cd
		// failed (bad search path) or the shell itself failed.
		if stdout == "" {
			if stderr != "" {
				return "", fmt.Errorf("glob error: %s: %w", strings.TrimSpace(stderr), err)
			}
			return "No files found.", nil
		}
		// partial results — continue with what we have
	}

	// rg failures (e.g. invalid glob syntax) are masked by head's exit code 0:
	// surface them when they produced no output at all.
	if strings.TrimSpace(stdout) == "" && strings.TrimSpace(stderr) != "" {
		return "", fmt.Errorf("glob error: %s", strings.TrimSpace(stderr))
	}

	return formatGlobOutput(stdout, limit, elapsed)
}

// buildRgGlobCmd constructs a shell command that lists files matching the
// glob via ripgrep (rg --files). It cd's into searchPath first because rg
// roots --glob matching at its working directory, not at a path argument —
// this makes slash-anchored patterns like 'src/**/*.go' match relative to
// the search directory, and the output paths come back relative to it too.
// --sortr=modified sorts newest-first so the head truncation deterministically
// keeps the most recently modified files (same ordering as the grep tool's
// files_with_matches mode).
func buildRgGlobCmd(pattern, searchPath string, maxDepth, limit int) string {
	parts := []string{"cd", ShellQuote(searchPath), "&&", "rg", "--files", "--hidden", "--sortr=modified"}

	// Max depth (1 = direct children, same semantics as find -maxdepth)
	if maxDepth > 0 {
		parts = append(parts, "--max-depth", fmt.Sprintf("%d", maxDepth))
	}

	// Exclude VCS and dependency directories
	for _, dir := range globExclusions {
		parts = append(parts, "--glob", ShellQuote("!"+dir))
	}

	parts = append(parts, "--glob", ShellQuote(pattern))

	// Limit results (take limit+1 to detect truncation)
	parts = append(parts, "|", "head", "-n", fmt.Sprintf("%d", limit+1))

	return strings.Join(parts, " ")
}

// globExecTimeout bounds a local glob shell invocation.
const globExecTimeout = 30 * time.Second

// execLocal runs a shell command locally and returns stdout, stderr, error.
func execLocal(ctx context.Context, cmd string) (string, string, error) {
	execCtx, cancel := context.WithTimeout(ctx, globExecTimeout)
	defer cancel()

	c := exec.CommandContext(execCtx, "sh", "-c", cmd)
	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	return stdout.String(), stderr.String(), err
}

func formatGlobOutput(output string, limit int, elapsed time.Duration) (string, error) {
	if strings.TrimSpace(output) == "" {
		return "No files found.", nil
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	truncated := len(lines) > limit
	if truncated {
		lines = lines[:limit]
	}

	var result strings.Builder
	for _, line := range lines {
		result.WriteString(line)
		result.WriteString("\n")
	}

	if truncated {
		fmt.Fprintf(&result, "\n(%d files shown, more available — increase limit. %.0fms)\n", limit, float64(elapsed.Milliseconds()))
	} else {
		fmt.Fprintf(&result, "\n(%d files found. %.0fms)\n", len(lines), float64(elapsed.Milliseconds()))
	}

	return result.String(), nil
}
