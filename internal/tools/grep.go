package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// GrepInput defines the input for the grep tool.
type GrepInput struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Include         string `json:"include,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`

	OutputMode    string `json:"output_mode,omitempty"`    // "files_with_matches" (default), "content", "count"
	BeforeContext int    `json:"before_context,omitempty"` // -B lines
	AfterContext  int    `json:"after_context,omitempty"`  // -A lines
	Context       int    `json:"context,omitempty"`        // -C lines (overrides before/after)
	Offset        int    `json:"offset,omitempty"`         // skip N results for pagination
	Multiline     bool   `json:"multiline,omitempty"`      // rg --multiline
	FileType      string `json:"file_type,omitempty"`      // rg --type
}

// grepDefaultMax is the default max_results.
const grepDefaultMax = 250

// grepAbsoluteMax is the hard upper limit for max_results.
const grepAbsoluteMax = 1000

// grepTimeout is the max time a local grep command may run.
const grepTimeout = 20 * time.Second

// grepHardCapLines bounds how many output lines runLocalRg buffers in
// files_with_matches/count modes — a pure memory guard, normally never hit.
// Content mode uses the tighter offset+max_results+1 window instead.
const grepHardCapLines = 50000

// grepScannerMaxLine bounds a single output line read from rg. Match lines
// are already truncated by --max-columns=500; this is defensive.
const grepScannerMaxLine = 1024 * 1024

// grepVCSExclusions are directories excluded from search.
var grepVCSExclusions = []string{
	".git", "node_modules", "vendor", "__pycache__", ".venv",
	".svn", ".hg", ".bzr", ".jj", ".sl",
}

func (e *Env) NewGrepTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "grep",
		Desc: `Search tool built on ripgrep (rg). Requires rg to be installed.
Supports full regex syntax (e.g., "log.*Error", "function\s+\w+").
Pattern syntax: Uses ripgrep regex — literal braces need escaping (use "interface\{\}" to find "interface{}" in Go code).
By default: skips binary files, respects .gitignore, searches hidden files, excludes VCS/dependency directories.
Output modes: "files_with_matches" (default, filenames only sorted by mtime), "content" (matching lines with line numbers), "count" (file:count).
Supports context lines, pagination via offset, multiline matching, and file type filtering.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {
				Type:     schema.String,
				Desc:     "The search pattern (regex). Literal braces need escaping: use \\{ and \\}.",
				Required: true,
			},
			"path": {
				Type:     schema.String,
				Desc:     "The file or directory path to search in. Use absolute paths.",
				Required: true,
			},
			"include": {
				Type:     schema.String,
				Desc:     "Glob pattern to filter files (e.g. '*.go', '*.{ts,tsx}').",
				Required: false,
			},
			"case_insensitive": {
				Type:     schema.Boolean,
				Desc:     "If true, perform case-insensitive matching.",
				Required: false,
			},
			"max_results": {
				Type:     schema.Integer,
				Desc:     "Maximum number of results to return. Default 250, max 1000.",
				Required: false,
			},
			"output_mode": {
				Type:     schema.String,
				Desc:     `Output mode: "files_with_matches" (default, filenames sorted by mtime), "content" (matching lines), "count" (file:count format).`,
				Enum:     []string{"files_with_matches", "content", "count"},
				Required: false,
			},
			"before_context": {
				Type:     schema.Integer,
				Desc:     "Number of lines to show before each match. Only for content mode.",
				Required: false,
			},
			"after_context": {
				Type:     schema.Integer,
				Desc:     "Number of lines to show after each match. Only for content mode.",
				Required: false,
			},
			"context": {
				Type:     schema.Integer,
				Desc:     "Number of lines to show before AND after each match. Overrides before_context/after_context. Only for content mode.",
				Required: false,
			},
			"offset": {
				Type:     schema.Integer,
				Desc:     "Skip this many results for pagination.",
				Required: false,
			},
			"multiline": {
				Type:     schema.Boolean,
				Desc:     "Enable multiline matching where patterns can span lines.",
				Required: false,
			},
			"file_type": {
				Type:     schema.String,
				Desc:     "Filter by file type (e.g. 'go', 'py', 'js'). Uses rg --type. More efficient than include for standard file types.",
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

	// Managed deny-read policy: the search root must be readable. Checking
	// before dispatch means content under a denied tree is never streamed
	// back, in every approval mode and on both local and remote executors.
	if err := g.env.checkDenyRead("grep", g.env.ResolvePath(input.Path)); err != nil {
		return "", err
	}

	maxResults := grepDefaultMax
	if input.MaxResults > 0 {
		maxResults = input.MaxResults
		if maxResults > grepAbsoluteMax {
			maxResults = grepAbsoluteMax
		}
	}

	if input.OutputMode == "" {
		input.OutputMode = "files_with_matches"
	}
	// The schema declares an enum, but providers do not necessarily enforce
	// it — reject unknown values instead of silently running in content mode.
	switch input.OutputMode {
	case "files_with_matches", "content", "count":
	default:
		return "", fmt.Errorf("invalid output_mode %q: must be one of files_with_matches, content, count", input.OutputMode)
	}

	// On remote (SSH), build the command string and run via Executor.
	if g.env.IsRemote() {
		return g.runRemote(ctx, input, maxResults)
	}

	// Require ripgrep — no grep fallback to avoid BRE/ERE regex incompatibilities.
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		return "", fmt.Errorf("ripgrep (rg) is required but not found in PATH. Install it: https://github.com/BurntSushi/ripgrep#installation")
	}
	return g.runLocalRg(ctx, rgPath, input, maxResults)
}

func (g *grepTool) buildRgArgs(input GrepInput) []string {
	args := []string{
		"--no-heading", "--line-number", "--color=never",
		"--max-columns=500", "--max-columns-preview",
		"--hidden", // search hidden files (rg respects .gitignore anyway)
	}

	// VCS exclusions
	for _, dir := range grepVCSExclusions {
		args = append(args, "--glob", "!"+dir)
	}

	// Output mode. Content mode (the default) deliberately adds no rg-side
	// limit: --max-count is per-file, which silently drops matches beyond the
	// cap within a single file and makes them unreachable via offset. The
	// global offset+limit window is applied in Go (see runLocalRg's capLines).
	switch input.OutputMode {
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count")
	}

	// Context lines (only meaningful for content mode)
	if input.OutputMode == "content" || input.OutputMode == "" {
		if input.Context > 0 {
			args = append(args, fmt.Sprintf("--context=%d", input.Context))
		} else {
			if input.BeforeContext > 0 {
				args = append(args, fmt.Sprintf("--before-context=%d", input.BeforeContext))
			}
			if input.AfterContext > 0 {
				args = append(args, fmt.Sprintf("--after-context=%d", input.AfterContext))
			}
		}
	}

	if input.CaseInsensitive {
		args = append(args, "--ignore-case")
	}
	if input.Include != "" {
		args = append(args, "--glob", input.Include)
	}
	if input.Multiline {
		args = append(args, "--multiline", "--multiline-dotall")
	}
	if input.FileType != "" {
		args = append(args, "--type", input.FileType)
	}

	// Handle patterns starting with '-' to prevent rg interpreting them as flags
	if strings.HasPrefix(input.Pattern, "-") {
		args = append(args, "-e", input.Pattern)
	} else {
		args = append(args, input.Pattern)
	}
	args = append(args, input.Path)
	return args
}

// runLocalRg executes ripgrep locally with timeout and processes results.
// Output is consumed as a stream with an early stop: once enough lines for
// the requested page (content mode) or the hard memory cap (other modes) are
// collected, rg is killed instead of letting it flood memory with the full
// result set of a broad pattern over a large repo.
func (g *grepTool) runLocalRg(ctx context.Context, rgPath string, input GrepInput, maxResults int) (string, error) {
	args := g.buildRgArgs(input)

	ctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, rgPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}

	// capLines: how many raw output lines we need at most. Content mode reads
	// one line past the requested page to detect truncation (same slicing
	// contract as postProcessOutput); other modes only have the memory guard.
	capLines := grepHardCapLines
	if input.OutputMode == "content" || input.OutputMode == "" {
		capLines = input.Offset + maxResults + 1
	}

	var lines []string
	capped := false
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 64*1024), grepScannerMaxLine)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) >= capLines {
			capped = true
			break
		}
	}
	scanErr := scanner.Err()
	if capped || scanErr != nil {
		cancel() // stop rg early — we have all the lines we need
	}
	waitErr := cmd.Wait()

	if scanErr != nil && !capped {
		if len(lines) == 0 {
			return "", fmt.Errorf("search error: failed reading results: %v", scanErr)
		}
		capped = true // oversized line aborted the scan — treat as partial results
	}

	if waitErr != nil && !capped {
		// Exit code 1 = no matches
		if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "No matches found.", nil
		}
		// Timeout: report partial results or clear error
		if ctx.Err() == context.DeadlineExceeded {
			if len(lines) > 0 {
				// Drop last line which may be incomplete
				if len(lines) > 1 {
					lines = lines[:len(lines)-1]
				}
				result := g.postProcessOutput(lines, input, maxResults, false)
				return fmt.Sprintf("Search timed out after %s. Partial results:\n%s", grepTimeout, result), nil
			}
			return fmt.Sprintf("Search timed out after %s. Try a more specific path or pattern.", grepTimeout), nil
		}
		if stderr.Len() > 0 {
			return "", fmt.Errorf("search error: %s", strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("search failed: %w", waitErr)
	}

	if len(lines) == 0 {
		return "No matches found.", nil
	}
	return g.postProcessOutput(lines, input, maxResults, capped), nil
}

// postProcessOutput handles path relativization, mtime sorting (for files_with_matches),
// pagination, and result formatting. capped indicates the line stream was cut
// off early (rg killed after capLines), so totals derived from lines are lower
// bounds rather than exact counts.
func (g *grepTool) postProcessOutput(lines []string, input GrepInput, maxResults int, capped bool) string {
	pwd := g.env.Pwd()

	switch input.OutputMode {
	case "files_with_matches":
		return g.formatFilesWithMatches(lines, pwd, maxResults, input.Offset, capped)
	case "count":
		return g.formatCount(lines, pwd, maxResults, input.Offset, capped)
	default:
		return g.formatContent(lines, pwd, maxResults, input.Offset, capped)
	}
}

// formatFilesWithMatches sorts file paths by mtime (newest first) and converts to relative paths.
func (g *grepTool) formatFilesWithMatches(lines []string, pwd string, maxResults, offset int, capped bool) string {
	type fileEntry struct {
		path  string
		mtime int64
	}

	entries := make([]fileEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var mtime int64
		if info, err := os.Stat(line); err == nil {
			mtime = info.ModTime().UnixNano()
		}
		entries = append(entries, fileEntry{path: line, mtime: mtime})
	}

	// Sort by mtime descending (most recently modified first)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].mtime != entries[j].mtime {
			return entries[i].mtime > entries[j].mtime
		}
		return entries[i].path < entries[j].path // stable tiebreaker
	})

	// Apply offset
	totalFiles := len(entries)
	if offset > 0 {
		if offset >= len(entries) {
			return fmt.Sprintf("No more results (offset %d exceeds %d total files).", offset, totalFiles)
		}
		entries = entries[offset:]
	}

	// Apply limit
	truncated := len(entries) > maxResults
	if truncated {
		entries = entries[:maxResults]
	}

	var result strings.Builder
	for _, e := range entries {
		result.WriteString(toRelativePath(e.path, pwd))
		result.WriteString("\n")
	}

	switch {
	case truncated:
		fmt.Fprintf(&result, "\nFound %d files (showing %d, offset %d — use offset=%d for next page)\n",
			totalFiles, len(entries), offset, offset+maxResults)
	case offset > 0:
		fmt.Fprintf(&result, "\nFound %d files (showing %d, offset %d)\n",
			totalFiles, len(entries), offset)
	default:
		fmt.Fprintf(&result, "\nFound %d files\n", len(entries))
	}
	if capped {
		fmt.Fprintf(&result, "(file list truncated at %d entries — narrow the search)\n", grepHardCapLines)
	}

	return result.String()
}

// formatContent converts absolute paths in content lines to relative paths.
// capped means the underlying scan stopped early, so len(lines) is a lower
// bound — the footer must not present it as an exact total.
func (g *grepTool) formatContent(lines []string, pwd string, maxResults, offset int, capped bool) string {
	totalLines := len(lines)

	// Apply offset
	if offset > 0 {
		if offset >= len(lines) {
			return fmt.Sprintf("No more results (offset %d exceeds %d total matches).", offset, totalLines)
		}
		lines = lines[offset:]
	}

	// Apply limit
	truncated := len(lines) > maxResults
	if truncated {
		lines = lines[:maxResults]
	}

	var result strings.Builder
	for _, line := range lines {
		// Lines have format: /absolute/path:linenum:content — convert path to relative
		result.WriteString(relativizeLine(line, pwd))
		result.WriteString("\n")
	}

	switch {
	case capped:
		fmt.Fprintf(&result, "\n(showing %d results, offset %d — more results available, use offset=%d for next page)\n",
			len(lines), offset, offset+len(lines))
	case truncated:
		fmt.Fprintf(&result, "\n(showing %d results, %d total, offset %d — use offset=%d for next page)\n",
			len(lines), totalLines, offset, offset+maxResults)
	case offset > 0:
		fmt.Fprintf(&result, "\n(%d results, offset %d, %d total)\n", len(lines), offset, totalLines)
	default:
		fmt.Fprintf(&result, "\n(%d matches found)\n", len(lines))
	}

	return result.String()
}

// formatCount converts absolute paths in count lines to relative paths.
func (g *grepTool) formatCount(lines []string, pwd string, maxResults, offset int, capped bool) string {
	totalLines := len(lines)

	if offset > 0 {
		if offset >= len(lines) {
			return fmt.Sprintf("No more results (offset %d exceeds %d total entries).", offset, totalLines)
		}
		lines = lines[offset:]
	}

	truncated := len(lines) > maxResults
	if truncated {
		lines = lines[:maxResults]
	}

	var totalMatches int
	var result strings.Builder
	for _, line := range lines {
		rel := relativizeLine(line, pwd)
		result.WriteString(rel)
		result.WriteString("\n")
		// Parse count from "file:N" format
		if idx := strings.LastIndex(line, ":"); idx > 0 {
			var n int
			if _, err := fmt.Sscanf(line[idx+1:], "%d", &n); err == nil {
				totalMatches += n
			}
		}
	}

	if truncated {
		fmt.Fprintf(&result, "\nFound %d occurrences (showing %d files, offset %d — use offset=%d for next page)\n",
			totalMatches, len(lines), offset, offset+maxResults)
	} else {
		fmt.Fprintf(&result, "\nFound %d occurrences across %d files\n", totalMatches, len(lines))
	}
	if capped {
		fmt.Fprintf(&result, "(file list truncated at %d entries — narrow the search)\n", grepHardCapLines)
	}

	return result.String()
}

// toRelativePath converts an absolute path to a path relative to pwd.
// Returns the path unchanged if it cannot be made relative.
func toRelativePath(absPath, pwd string) string {
	if pwd == "" {
		return absPath
	}
	rel, err := filepath.Rel(pwd, absPath)
	if err != nil {
		return absPath
	}
	return rel
}

// relativizeLine converts the leading absolute path in a grep output line
// (e.g. "/abs/path/file.go:42:code") to a relative path.
func relativizeLine(line, pwd string) string {
	if pwd == "" || !strings.HasPrefix(line, "/") {
		return line
	}
	// Find first colon that separates path from the rest
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return toRelativePath(line, pwd)
	}
	filePath := line[:idx]
	rest := line[idx:]
	return toRelativePath(filePath, pwd) + rest
}

// runRemote builds a command string and runs it over SSH via the Executor.
func (g *grepTool) runRemote(ctx context.Context, input GrepInput, maxResults int) (string, error) {
	cmd := g.buildRemoteCmd(input, maxResults)
	stdout, stderr, err := ExecReadOnly(ctx, g.env.Exec, cmd, "", 30*time.Second)

	if err != nil {
		if IsFatal(err) {
			return "", err
		}
		// Exit code 1 = no matches (files_with_matches/count run rg bare;
		// content mode pipes through head, whose exit code masks rg's — that
		// case is handled by the empty-stdout check below).
		if strings.Contains(err.Error(), "exit status 1") || strings.Contains(err.Error(), "status 1") {
			return "No matches found.", nil
		}
		if stdout == "" {
			if stderr != "" {
				return "", fmt.Errorf("search error: %s: %w", strings.TrimSpace(stderr), err)
			}
			return "", fmt.Errorf("search failed: %w", err)
		}
		// partial results — continue with what we have
	}

	if stdout == "" {
		// With the head pipeline the exit status is head's 0 even when rg
		// found nothing (or failed): empty stdout with stderr output is a
		// real failure, otherwise it is the no-match signal.
		if strings.TrimSpace(stderr) != "" {
			return "", fmt.Errorf("search error: %s", strings.TrimSpace(stderr))
		}
		return "No matches found.", nil
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")

	// The head guard cut the stream at capLines: reaching it means more
	// results may exist and line-derived totals are lower bounds.
	capped := false
	if input.OutputMode == "content" {
		if capLines := input.Offset + maxResults + 1; len(lines) >= capLines {
			capped = true
		}
	}
	return g.postProcessOutput(lines, input, maxResults, capped), nil
}

func (g *grepTool) buildRemoteCmd(input GrepInput, maxResults int) string {
	// On remote, only use rg — fail clearly if not available.
	rgParts := []string{"rg", "--no-heading", "--line-number", "--color=never",
		"--max-columns=500", "--max-columns-preview", "--hidden"}

	for _, dir := range grepVCSExclusions {
		rgParts = append(rgParts, "--glob", ShellQuote("!"+dir))
	}

	// Content mode adds no rg-side limit here (--max-count is per-file and
	// drops matches); a head guard is appended to the pipeline below instead.
	switch input.OutputMode {
	case "files_with_matches":
		rgParts = append(rgParts, "--files-with-matches")
	case "count":
		rgParts = append(rgParts, "--count")
	}

	if input.OutputMode == "content" || input.OutputMode == "" {
		if input.Context > 0 {
			rgParts = append(rgParts, fmt.Sprintf("--context=%d", input.Context))
		} else {
			if input.BeforeContext > 0 {
				rgParts = append(rgParts, fmt.Sprintf("--before-context=%d", input.BeforeContext))
			}
			if input.AfterContext > 0 {
				rgParts = append(rgParts, fmt.Sprintf("--after-context=%d", input.AfterContext))
			}
		}
	}

	if input.CaseInsensitive {
		rgParts = append(rgParts, "--ignore-case")
	}
	if input.Include != "" {
		rgParts = append(rgParts, "--glob", ShellQuote(input.Include))
	}
	if input.Multiline {
		rgParts = append(rgParts, "--multiline", "--multiline-dotall")
	}
	if input.FileType != "" {
		rgParts = append(rgParts, "--type", ShellQuote(input.FileType))
	}

	// Handle patterns starting with '-'
	if strings.HasPrefix(input.Pattern, "-") {
		rgParts = append(rgParts, "-e", ShellQuote(input.Pattern))
	} else {
		rgParts = append(rgParts, ShellQuote(input.Pattern))
	}
	rgParts = append(rgParts, ShellQuote(input.Path))

	cmd := strings.Join(rgParts, " ")
	// Volume guard for content mode: cap the stream at one line past the
	// requested page (mirrors runLocalRg's capLines early stop).
	if input.OutputMode == "content" || input.OutputMode == "" {
		cmd += fmt.Sprintf(" | head -n %d", input.Offset+maxResults+1)
	}
	return cmd
}
