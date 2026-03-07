package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type EditInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
}

func (e *Env) NewEditTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "edit",
		Desc: `Performs exact string replacements in files. Can also create new files.
- To EDIT a file: provide file_path, old_string, and new_string. old_string must match exactly.
- To CREATE a file: provide file_path with new_string and leave old_string empty. The file must not already exist.
- Use start_line/end_line to narrow the search scope when old_string is ambiguous.
- Whitespace (including trailing spaces and line endings) must match exactly.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_path": {
				Type:     schema.String,
				Desc:     "The absolute path to the file to modify or create.",
				Required: true,
			},
			"old_string": {
				Type:     schema.String,
				Desc:     "The text to replace. Must match exactly. Leave empty to create a new file.",
				Required: false,
			},
			"new_string": {
				Type:     schema.String,
				Desc:     "The replacement text, or the full file content when creating.",
				Required: true,
			},
			"replace_all": {
				Type:     schema.Boolean,
				Desc:     "If true, replace all occurrences of old_string. Default false.",
				Required: false,
			},
			"start_line": {
				Type:     schema.Integer,
				Desc:     "Optional 1-based start line to narrow the search scope for old_string.",
				Required: false,
			},
			"end_line": {
				Type:     schema.Integer,
				Desc:     "Optional 1-based end line to narrow the search scope for old_string.",
				Required: false,
			},
		}),
	}

	return &editTool{
		env:  e,
		info: info,
	}
}

type editTool struct {
	env  *Env
	info *schema.ToolInfo
}

func (e *editTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return e.info, nil
}

func (e *editTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input EditInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse input: %w", err)
	}

	if input.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	// === CREATE mode: old_string is empty ===
	if input.OldString == "" {
		return e.createFile(ctx, input)
	}

	return e.editFile(ctx, input)
}

func (e *editTool) createFile(ctx context.Context, input EditInput) (string, error) {
	if input.NewString == "" {
		return "", fmt.Errorf("new_string is required when creating a file")
	}

	fi, _ := e.env.Exec.Stat(ctx, input.FilePath)
	if fi != nil && fi.Exists {
		return "", fmt.Errorf("file %s already exists. Use old_string to edit existing files, or delete the file first", input.FilePath)
	}

	dir := filepath.Dir(input.FilePath)
	if err := e.env.Exec.MkdirAll(ctx, dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if err := e.env.Exec.WriteFile(ctx, input.FilePath, []byte(input.NewString), 0644); err != nil {
		return "", fmt.Errorf("failed to create file %s: %w", input.FilePath, err)
	}

	lines := strings.Count(input.NewString, "\n") + 1
	return fmt.Sprintf("Created file %s (%d lines)", input.FilePath, lines), nil
}

func (e *editTool) editFile(ctx context.Context, input EditInput) (string, error) {
	if input.NewString == input.OldString {
		return "", fmt.Errorf("new_string must be different from old_string")
	}

	content, err := e.env.Exec.ReadFile(ctx, input.FilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", input.FilePath, err)
	}

	contentStr := string(content)

	// If start_line/end_line specified, narrow the scope
	if input.StartLine > 0 || input.EndLine > 0 {
		return e.editWithLineRange(ctx, input, contentStr)
	}

	// Count occurrences
	count := strings.Count(contentStr, input.OldString)
	if count == 0 {
		return e.handleNoMatch(input, contentStr)
	}

	// If not replace_all and multiple occurrences, error
	if !input.ReplaceAll && count > 1 {
		return "", fmt.Errorf("old_string appears %d times in file. Use replace_all=true to replace all, or use start_line/end_line to narrow the scope, or provide a more unique string", count)
	}

	// Perform replacement
	var newContent string
	if input.ReplaceAll {
		newContent = strings.ReplaceAll(contentStr, input.OldString, input.NewString)
	} else {
		newContent = strings.Replace(contentStr, input.OldString, input.NewString, 1)
	}

	// Write back
	if err := e.env.Exec.WriteFile(ctx, input.FilePath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", input.FilePath, err)
	}

	replacedCount := 1
	if input.ReplaceAll {
		replacedCount = count
	}

	// Generate a diff snippet
	diffSnippet := generateDiffSnippet(input.OldString, input.NewString)

	return fmt.Sprintf("Successfully replaced %d occurrence(s) in %s\n\n%s", replacedCount, input.FilePath, diffSnippet), nil
}

func (e *editTool) editWithLineRange(ctx context.Context, input EditInput, contentStr string) (string, error) {
	lines := strings.Split(contentStr, "\n")
	totalLines := len(lines)

	startLine := input.StartLine
	endLine := input.EndLine

	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 || endLine > totalLines {
		endLine = totalLines
	}
	if startLine > endLine {
		return "", fmt.Errorf("start_line (%d) must be <= end_line (%d)", startLine, endLine)
	}

	// Extract the relevant section (1-based to 0-based)
	sectionLines := lines[startLine-1 : endLine]
	section := strings.Join(sectionLines, "\n")

	count := strings.Count(section, input.OldString)
	if count == 0 {
		// Show the section content for debugging
		return "", fmt.Errorf("old_string not found between lines %d-%d. Content in that range:\n%s", startLine, endLine, truncateString(section, 500))
	}

	if !input.ReplaceAll && count > 1 {
		return "", fmt.Errorf("old_string appears %d times between lines %d-%d. Use replace_all=true or narrow the line range further", count, startLine, endLine)
	}

	// Perform replacement within the section
	var newSection string
	if input.ReplaceAll {
		newSection = strings.ReplaceAll(section, input.OldString, input.NewString)
	} else {
		newSection = strings.Replace(section, input.OldString, input.NewString, 1)
	}

	// Reconstruct full content
	before := ""
	if startLine > 1 {
		before = strings.Join(lines[:startLine-1], "\n") + "\n"
	}
	after := ""
	if endLine < totalLines {
		after = "\n" + strings.Join(lines[endLine:], "\n")
	}

	newContent := before + newSection + after

	if err := e.env.Exec.WriteFile(ctx, input.FilePath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", input.FilePath, err)
	}

	replacedCount := 1
	if input.ReplaceAll {
		replacedCount = count
	}

	diffSnippet := generateDiffSnippet(input.OldString, input.NewString)
	return fmt.Sprintf("Successfully replaced %d occurrence(s) in %s (lines %d-%d)\n\n%s",
		replacedCount, input.FilePath, startLine, endLine, diffSnippet), nil
}

func (e *editTool) handleNoMatch(input EditInput, contentStr string) (string, error) {
	// Try to find a close match by normalizing whitespace
	normalizedOld := normalizeWhitespace(input.OldString)
	lines := strings.Split(contentStr, "\n")

	// Search for the best matching line
	bestMatch := ""
	bestLine := 0
	bestScore := 0

	oldLines := strings.Split(input.OldString, "\n")
	firstOldLine := strings.TrimSpace(oldLines[0])

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		score := longestCommonSubstring(trimmed, firstOldLine)
		if score > bestScore && score > len(firstOldLine)/3 {
			bestScore = score
			bestLine = i + 1
			bestMatch = line
		}
	}

	// Also try normalized whitespace match on the full content
	normalizedContent := normalizeWhitespace(contentStr)
	if strings.Contains(normalizedContent, normalizedOld) {
		return "", fmt.Errorf("old_string not found exactly, but a whitespace-normalized match exists. Check for trailing spaces, tabs vs spaces, or line ending differences. Hint: use the read tool to view the exact file content first")
	}

	if bestMatch != "" {
		return "", fmt.Errorf("old_string not found in file. Most similar line (line %d):\n  %s\nHint: use the read tool to view the exact file content around line %d", bestLine, bestMatch, bestLine)
	}

	return "", fmt.Errorf("old_string not found in file %s. Use the read tool to verify the file content first", input.FilePath)
}

// generateDiffSnippet creates a simple diff-like output
func generateDiffSnippet(oldStr, newStr string) string {
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	var diff strings.Builder
	diff.WriteString("```diff\n")

	for _, line := range oldLines {
		diff.WriteString("- " + line + "\n")
	}
	for _, line := range newLines {
		diff.WriteString("+ " + line + "\n")
	}

	diff.WriteString("```")

	result := diff.String()
	if len(result) > 1000 {
		return truncateString(result, 1000)
	}
	return result
}

// normalizeWhitespace collapses all whitespace to single spaces and trims
func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// longestCommonSubstring returns the length of the longest common substring
func longestCommonSubstring(a, b string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	// Limit to avoid excessive computation on very long strings
	if len(a) > 200 {
		a = a[:200]
	}
	if len(b) > 200 {
		b = b[:200]
	}

	maxLen := 0
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1] + 1
				if curr[j] > maxLen {
					maxLen = curr[j]
				}
			} else {
				curr[j] = 0
			}
		}
		prev, curr = curr, prev
		for k := range curr {
			curr[k] = 0
		}
	}

	return maxLen
}

// truncateString truncates a string to maxLen characters and appends "..."
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... (truncated)"
}
