package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type GrepInput struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Include         string `json:"include,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
}

func (e *Env) NewGrepTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "grep",
		Desc: `Searches for a pattern in files. Returns matching lines with file path and line number.
Uses ripgrep (rg) if available for best performance, otherwise falls back to grep.
By default: skips binary files, respects .gitignore, excludes .git/node_modules/vendor directories.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {
				Type:     schema.String,
				Desc:     "The search pattern (supports regex).",
				Required: true,
			},
			"path": {
				Type:     schema.String,
				Desc:     "The file or directory path to search in. Use absolute paths.",
				Required: true,
			},
			"include": {
				Type:     schema.String,
				Desc:     "Glob pattern to filter files (e.g. '*.go', '*.py').",
				Required: false,
			},
			"case_insensitive": {
				Type:     schema.Boolean,
				Desc:     "If true, perform case-insensitive matching.",
				Required: false,
			},
			"max_results": {
				Type:     schema.Integer,
				Desc:     "Maximum number of matching lines to return. Default 50, max 200.",
				Required: false,
			},
		}),
	}

	return &grepTool{env: e, info: info}
}

type grepTool struct {
	env  *Env
	info *schema.ToolInfo
}

func (g *grepTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return g.info, nil
}

func (g *grepTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input GrepInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse input: %w", err)
	}

	if input.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if input.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	maxResults := 50
	if input.MaxResults > 0 {
		maxResults = input.MaxResults
		if maxResults > 200 {
			maxResults = 200
		}
	}

	// On remote (SSH), build the command string and run via Executor.
	if g.env.IsRemote() {
		return g.runRemote(ctx, input, maxResults)
	}

	// On local, use exec.Command directly for better control.
	if rgPath, err := exec.LookPath("rg"); err == nil {
		return g.runLocalCmd(ctx, rgPath, g.buildRgArgs(input, maxResults))
	}
	return g.runLocalCmd(ctx, "grep", g.buildGrepArgs(input))
}

func (g *grepTool) buildRgArgs(input GrepInput, maxResults int) []string {
	args := []string{
		"--no-heading", "--line-number", "--color=never",
		"--max-count", fmt.Sprintf("%d", maxResults+1),
	}
	if input.CaseInsensitive {
		args = append(args, "--ignore-case")
	}
	if input.Include != "" {
		args = append(args, "--glob", input.Include)
	}
	args = append(args, input.Pattern, input.Path)
	return args
}

func (g *grepTool) buildGrepArgs(input GrepInput) []string {
	args := []string{
		"-rnI", "--color=never",
		"--exclude-dir=.git", "--exclude-dir=node_modules",
		"--exclude-dir=vendor", "--exclude-dir=__pycache__", "--exclude-dir=.venv",
	}
	if input.CaseInsensitive {
		args = append(args, "-i")
	}
	if input.Include != "" {
		args = append(args, "--include="+input.Include)
	}
	args = append(args, input.Pattern, input.Path)
	return args
}

func (g *grepTool) runLocalCmd(ctx context.Context, bin string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "No matches found.", nil
		}
		if stderr.Len() > 0 {
			return "", fmt.Errorf("search error: %s", strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("search failed: %w", err)
	}

	return formatGrepOutput(stdout.String(), 50)
}

// runRemote builds a command string and runs it over SSH via the Executor.
func (g *grepTool) runRemote(ctx context.Context, input GrepInput, maxResults int) (string, error) {
	// Try rg first, fall back to grep
	cmd := g.buildRemoteCmd(input, maxResults)
	stdout, stderr, err := g.env.Exec.Exec(ctx, cmd, "", 30*time.Second)

	if err != nil {
		// Exit code 1 = no matches (both rg and grep)
		if strings.Contains(err.Error(), "exit status 1") || strings.Contains(err.Error(), "status 1") {
			return "No matches found.", nil
		}
		if stderr != "" {
			return "", fmt.Errorf("search error: %s", strings.TrimSpace(stderr))
		}
		return "", fmt.Errorf("search failed: %w", err)
	}

	return formatGrepOutput(stdout, maxResults)
}

func (g *grepTool) buildRemoteCmd(input GrepInput, maxResults int) string {
	// Try rg first, fall back to grep
	var parts []string

	// rg command
	rgParts := []string{"rg", "--no-heading", "--line-number", "--color=never",
		"--max-count", fmt.Sprintf("%d", maxResults+1)}
	if input.CaseInsensitive {
		rgParts = append(rgParts, "--ignore-case")
	}
	if input.Include != "" {
		rgParts = append(rgParts, "--glob", ShellQuote(input.Include))
	}
	rgParts = append(rgParts, ShellQuote(input.Pattern), ShellQuote(input.Path))

	// grep fallback
	grepParts := []string{"grep", "-rnI", "--color=never",
		"--exclude-dir=.git", "--exclude-dir=node_modules",
		"--exclude-dir=vendor", "--exclude-dir=__pycache__"}
	if input.CaseInsensitive {
		grepParts = append(grepParts, "-i")
	}
	if input.Include != "" {
		grepParts = append(grepParts, "--include="+ShellQuote(input.Include))
	}
	grepParts = append(grepParts, ShellQuote(input.Pattern), ShellQuote(input.Path))

	// which rg && rg ... || grep ...
	parts = append(parts, "which rg >/dev/null 2>&1 &&")
	parts = append(parts, strings.Join(rgParts, " "))
	parts = append(parts, "||")
	parts = append(parts, strings.Join(grepParts, " "))

	return strings.Join(parts, " ")
}

func formatGrepOutput(output string, maxResults int) (string, error) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	truncated := len(lines) > maxResults
	if truncated {
		lines = lines[:maxResults]
	}

	var result strings.Builder
	for _, line := range lines {
		result.WriteString(line)
		result.WriteString("\n")
	}

	if truncated {
		result.WriteString(fmt.Sprintf("\n(results truncated at %d matches)\n", maxResults))
	} else {
		result.WriteString(fmt.Sprintf("\n(%d matches found)\n", len(lines)))
	}

	return result.String(), nil
}
