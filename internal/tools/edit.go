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

const MaxEditFileSize = 10 * 1024 * 1024 // 10MB

// EditOp represents a single edit operation in multi-edit mode.
type EditOp struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type EditInput struct {
	FilePath   string   `json:"file_path"`
	OldString  string   `json:"old_string"`
	NewString  string   `json:"new_string"`
	ReplaceAll bool     `json:"replace_all,omitempty"`
	StartLine  int      `json:"start_line,omitempty"`
	EndLine    int      `json:"end_line,omitempty"`
	Edits      []EditOp `json:"edits,omitempty"`
}

func (e *Env) NewEditTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "edit",
		Desc: `Performs exact string replacements in files. Can also create new files.
- To EDIT a file: provide file_path, old_string, and new_string. old_string must match exactly.
- To CREATE a file: provide file_path with new_string and leave old_string empty. The file must not already exist.
- For MULTI-EDIT: provide file_path and edits array. Each edit has old_string, new_string, and an optional per-edit replace_all. Edits are applied sequentially, each operating on the result of the previous one; each old_string must match exactly once unless its replace_all is true, and must not match text inserted by an earlier edit's new_string.
- Use start_line/end_line to narrow the search scope when old_string is ambiguous.
- Whitespace (including trailing spaces and line endings) must match exactly.
- edits and old_string are mutually exclusive.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_path": {
				Type:     schema.String,
				Desc:     "The absolute path to the file to modify or create (preferred). Relative paths are resolved against the working directory.",
				Required: true,
			},
			"old_string": {
				Type:     schema.String,
				Desc:     "The text to replace. Must match exactly. Leave empty to create a new file. Mutually exclusive with edits.",
				Required: false,
			},
			"new_string": {
				Type:     schema.String,
				Desc:     "The replacement text, or the full file content when creating.",
				Required: false,
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
			"edits": {
				Type:     schema.Array,
				Desc:     "Array of edit operations, applied sequentially. Mutually exclusive with old_string.",
				Required: false,
				ElemInfo: &schema.ParameterInfo{
					Type: schema.Object,
					SubParams: map[string]*schema.ParameterInfo{
						"old_string": {
							Type:     schema.String,
							Desc:     "Exact text to replace.",
							Required: true,
						},
						"new_string": {
							Type:     schema.String,
							Desc:     "Replacement text.",
							Required: true,
						},
						"replace_all": {
							Type: schema.Boolean,
							Desc: "Replace all occurrences of old_string in this edit. Default false.",
						},
					},
				},
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
	input.FilePath = e.env.ResolvePath(input.FilePath)

	// Managed deny-read policy: an edit of a denied file would read its old
	// content for matching and echo it back through diffs/errors — deny the
	// whole operation.
	if err := e.env.checkDenyRead("edit", input.FilePath); err != nil {
		return "", err
	}

	// Validate mutual exclusivity.
	if len(input.Edits) > 0 && input.OldString != "" {
		return "", fmt.Errorf("edits and old_string are mutually exclusive; use one or the other")
	}

	// Binary detection by extension.
	if detectBinaryByExtension(input.FilePath) {
		return "", fmt.Errorf("cannot edit binary file %s (detected by extension)", input.FilePath)
	}

	// Multi-edit mode.
	if len(input.Edits) > 0 {
		return e.applyMultiEdits(ctx, input)
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

	fi, err := e.env.Exec.Stat(ctx, input.FilePath)
	if err != nil {
		return "", fmt.Errorf("failed to inspect file %s before creating: %w", input.FilePath, err)
	}
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

	// Register the new file in the tracker so a follow-up edit does not
	// trip the read-before-edit guard.
	e.updateTrackerAfterWrite(input.FilePath, []byte(input.NewString))

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

	// File size check.
	if len(content) > MaxEditFileSize {
		return "", fmt.Errorf("file %s is too large (%d bytes, max %d). Use start_line/end_line to edit a specific range",
			input.FilePath, len(content), MaxEditFileSize)
	}

	// Binary content detection.
	if detectBinaryByContent(content) {
		return "", fmt.Errorf("cannot edit binary file %s (binary content detected)", input.FilePath)
	}

	contentStr := string(content)

	// Conflict detection.
	if err := e.checkConflict(input.FilePath); err != nil {
		return err.Error(), nil
	}

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

	// Backup before writing.
	backupPath := e.createBackup(input.FilePath, content)

	// Write back
	if err := e.env.Exec.WriteFile(ctx, input.FilePath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", input.FilePath, err)
	}

	// Update FileTracker after write.
	e.updateTrackerAfterWrite(input.FilePath, []byte(newContent))

	replacedCount := 1
	if input.ReplaceAll {
		replacedCount = count
	}

	// Generate unified diff.
	diff := generateUnifiedDiff(contentStr, newContent, filepath.Base(input.FilePath))

	var result strings.Builder
	fmt.Fprintf(&result, "Successfully replaced %d occurrence(s) in %s", replacedCount, input.FilePath)
	if backupPath != "" {
		fmt.Fprintf(&result, "\nBackup: %s", backupPath)
	}
	if diff != "" {
		result.WriteString("\n\n```diff\n")
		result.WriteString(diff)
		result.WriteString("```")
	}

	return result.String(), nil
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

	// Backup before writing.
	backupPath := e.createBackup(input.FilePath, []byte(contentStr))

	if err := e.env.Exec.WriteFile(ctx, input.FilePath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", input.FilePath, err)
	}

	// Update FileTracker after write.
	e.updateTrackerAfterWrite(input.FilePath, []byte(newContent))

	replacedCount := 1
	if input.ReplaceAll {
		replacedCount = count
	}

	diff := generateUnifiedDiff(contentStr, newContent, filepath.Base(input.FilePath))

	var result strings.Builder
	fmt.Fprintf(&result, "Successfully replaced %d occurrence(s) in %s (lines %d-%d)",
		replacedCount, input.FilePath, startLine, endLine)
	if backupPath != "" {
		fmt.Fprintf(&result, "\nBackup: %s", backupPath)
	}
	if diff != "" {
		result.WriteString("\n\n```diff\n")
		result.WriteString(diff)
		result.WriteString("```")
	}

	return result.String(), nil
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

// checkConflict checks if the file was modified externally since last read,
// or was never read at all in this session.
// Returns an error (as a user-visible message) if there is a conflict, nil otherwise.
// Remote (SSH/Docker) sessions skip the check entirely: the tracker stats the
// local filesystem and would misreport remote files as gone or unread.
func (e *editTool) checkConflict(path string) error {
	if e.env.FileTracker == nil || e.env.IsRemote() {
		return nil
	}
	cr, err := e.env.FileTracker.CheckConflict(path)
	if err != nil {
		return nil // ignore check errors
	}
	switch cr.Status {
	case ConflictModified:
		return fmt.Errorf("conflict: file %s was modified externally since last read. Please re-read the file before editing", path)
	case ConflictFileGone:
		return fmt.Errorf("conflict: file %s no longer exists on disk. It may have been deleted externally", path)
	case ConflictNeverRead:
		return fmt.Errorf("file %s has not been read yet. Use the read tool to read it before editing", path)
	}
	return nil
}

// createBackup creates a backup of the file content via FileTracker.
// Returns the backup path, or empty string if FileTracker is nil or backup fails.
func (e *editTool) createBackup(path string, content []byte) string {
	if e.env.FileTracker == nil {
		return ""
	}
	bp, err := e.env.FileTracker.CreateBackup(path, content)
	if err != nil {
		return ""
	}
	return bp
}

// updateTrackerAfterWrite updates the FileTracker with new content after a
// write. Remote sessions skip it: the local os.Stat would not see the file.
func (e *editTool) updateTrackerAfterWrite(path string, content []byte) {
	if e.env.FileTracker == nil || e.env.IsRemote() {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	e.env.FileTracker.UpdateAfterWrite(path, content, info.ModTime())
}

// applyMultiEdits applies multiple edit operations sequentially to a file.
func (e *editTool) applyMultiEdits(ctx context.Context, input EditInput) (string, error) {
	content, err := e.env.Exec.ReadFile(ctx, input.FilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", input.FilePath, err)
	}

	// File size check.
	if len(content) > MaxEditFileSize {
		return "", fmt.Errorf("file %s is too large (%d bytes, max %d)", input.FilePath, len(content), MaxEditFileSize)
	}

	// Binary content detection.
	if detectBinaryByContent(content) {
		return "", fmt.Errorf("cannot edit binary file %s (binary content detected)", input.FilePath)
	}

	original := string(content)

	// Conflict detection.
	if err := e.checkConflict(input.FilePath); err != nil {
		return err.Error(), nil
	}

	modified := original
	for i, op := range input.Edits {
		if op.OldString == "" {
			return "", fmt.Errorf("edit #%d: old_string must not be empty (to create a file, use old_string=\"\" without the edits array)", i+1)
		}
		if op.OldString == op.NewString {
			return "", fmt.Errorf("edit #%d: old_string and new_string are identical", i+1)
		}
		// Overlap guard: an old_string that appears inside an earlier edit's
		// new_string would (also) match text this call just inserted — an
		// almost-certain mistake. Require a single merged edit instead.
		for j := 0; j < i; j++ {
			if input.Edits[j].NewString != "" && strings.Contains(input.Edits[j].NewString, op.OldString) {
				return "", fmt.Errorf("edit #%d: old_string overlaps with new_string of edit #%d; merge them into a single edit",
					i+1, j+1)
			}
		}
		count := strings.Count(modified, op.OldString)
		if count == 0 {
			return "", fmt.Errorf("edit #%d: old_string not found in file (%d of %d edits applied successfully before failure)",
				i+1, i, len(input.Edits))
		}
		if count > 1 && !op.ReplaceAll {
			return "", fmt.Errorf("edit #%d: old_string appears %d times; set replace_all on this edit or provide a more unique string",
				i+1, count)
		}
		if op.ReplaceAll {
			modified = strings.ReplaceAll(modified, op.OldString, op.NewString)
		} else {
			modified = strings.Replace(modified, op.OldString, op.NewString, 1)
		}
	}

	// Backup before writing.
	backupPath := e.createBackup(input.FilePath, content)

	// Write back.
	if err := e.env.Exec.WriteFile(ctx, input.FilePath, []byte(modified), 0644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", input.FilePath, err)
	}

	// Update FileTracker after write.
	e.updateTrackerAfterWrite(input.FilePath, []byte(modified))

	diff := generateUnifiedDiff(original, modified, filepath.Base(input.FilePath))

	var result strings.Builder
	fmt.Fprintf(&result, "Successfully applied %d edit(s) to %s", len(input.Edits), input.FilePath)
	if backupPath != "" {
		fmt.Fprintf(&result, "\nBackup: %s", backupPath)
	}
	if diff != "" {
		result.WriteString("\n\n```diff\n")
		result.WriteString(diff)
		result.WriteString("```")
	}

	return result.String(), nil
}
