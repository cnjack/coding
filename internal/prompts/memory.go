package prompts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cnjack/jcode/internal/config"
)

// MemoryConfig controls how AGENTS.md files are loaded and merged.
type MemoryConfig struct {
	MaxTotalChars int
	MaxIncDepth   int
}

// MemoryLoader loads and merges multi-level AGENTS.md files with @include support.
type MemoryLoader struct {
	cfg MemoryConfig
}

var includeRe = regexp.MustCompile(`(?m)^@include\s+(.+)$`)

// NewMemoryLoader creates a MemoryLoader with the given config.
func NewMemoryLoader(cfg MemoryConfig) *MemoryLoader {
	if cfg.MaxTotalChars <= 0 {
		cfg.MaxTotalChars = 40000
	}
	if cfg.MaxIncDepth <= 0 {
		cfg.MaxIncDepth = 5
	}
	return &MemoryLoader{cfg: cfg}
}

// Load loads and merges multi-level AGENTS.md files:
//  1. ~/.jcode/AGENTS.md (global — user-owned, always loaded)
//  2. Walk-up from git root → pwd: each directory's AGENTS.md (root first,
//     pwd last). This lets monorepo roots define shared instructions while
//     sub-packages add their own. When not in a git repo, only pwd is checked.
//  3. {pwd}/AGENTS.local.md (local, expected gitignored)
//
// SECURITY: layers 2 and 3 are project content and load only when the project
// is trusted (config.ProjectInstructionsAllowed). An untrusted clone — the
// default for new/unknown projects — must not inject instructions into the
// system prompt. Trust comes from an explicit user decision recorded in
// ~/.jcode/project_trust.json (see `jcode trust`) or the
// JCODE_AGENTS_TRUST_PROJECT=1 opt-in; repository content can never
// self-authorize. Global instructions stay unaffected.
//
// Each file's @include directives are resolved recursively.
// The merged result is truncated to MaxTotalChars.
func (m *MemoryLoader) Load(pwd string) (string, error) {
	var sections []string

	// 1. Global AGENTS.md (user-owned; not project content)
	globalPath := filepath.Join(config.ConfigDir(), "AGENTS.md")
	if content, err := m.loadFile(globalPath); err == nil && content != "" {
		sections = append(sections, "<!-- global agents.md -->\n"+content)
	}

	// 2 + 3. Project AGENTS.md layers, gated on project trust.
	decision := config.ProjectInstructionsAllowed(pwd)
	if !decision.Allowed {
		m.logSkippedProjectInstructions(pwd, decision)
		return mergeSections(sections, m.cfg.MaxTotalChars)
	}

	// 2. Walk-up project AGENTS.md (git root → pwd, case-insensitive lookup)
	gitRoot := config.GitRoot(pwd)
	dirs := config.ConfigWalkDirs(gitRoot, pwd)
	for _, dir := range dirs {
		if projectPath := HasAgentsMd(dir); projectPath != "" {
			if content, err := m.loadFile(projectPath); err == nil && content != "" {
				sections = append(sections, "<!-- project agents.md -->\n"+content)
			}
		}
	}

	// 3. Local AGENTS.local.md
	localPath := filepath.Join(pwd, "AGENTS.local.md")
	if content, err := m.loadFile(localPath); err == nil && content != "" {
		sections = append(sections, "<!-- local agents.md -->\n"+content)
	}

	return mergeSections(sections, m.cfg.MaxTotalChars)
}

// logSkippedProjectInstructions records (audit log) that project instructions
// existed but were excluded because the project is untrusted. The log fires
// only when the working directory actually contains an AGENTS.md, so quiet
// projects stay quiet.
func (m *MemoryLoader) logSkippedProjectInstructions(pwd string, decision config.ProjectTrustDecision) {
	if pwd == "" {
		return
	}
	if HasAgentsMd(pwd) == "" {
		return
	}
	root := decision.Root
	if root == "" {
		root = pwd
	}
	config.Logger().Printf(
		"[security] project AGENTS.md skipped for untrusted project %s (root %s); run `jcode trust %s` to load it, or set JCODE_AGENTS_TRUST_PROJECT=1",
		pwd, root, root,
	)
}

// mergeSections joins the collected sections and enforces the total character
// limit. Extracted so the trusted and untrusted paths share one truncation
// contract.
func mergeSections(sections []string, maxTotalChars int) (string, error) {
	if len(sections) == 0 {
		return "", nil
	}

	merged := strings.Join(sections, "\n\n---\n\n")

	// Enforce total character limit.
	if len(merged) > maxTotalChars {
		merged = merged[:maxTotalChars] + "\n... (agents.md content truncated)"
	}

	return merged, nil
}

// loadFile reads a file and resolves @include directives.
func (m *MemoryLoader) loadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	visited := map[string]bool{path: true}
	resolved := m.resolveIncludes(content, filepath.Dir(path), visited, 0)
	return strings.TrimSpace(resolved), nil
}

// resolveIncludes recursively resolves @include directives in content.
// basePath is the directory of the file containing the directives.
// visited tracks already-included files to prevent circular references.
// depth is the current recursion depth.
func (m *MemoryLoader) resolveIncludes(content, basePath string, visited map[string]bool, depth int) string {
	if depth >= m.cfg.MaxIncDepth {
		return content
	}

	return includeRe.ReplaceAllStringFunc(content, func(match string) string {
		subs := includeRe.FindStringSubmatch(match)
		if len(subs) < 2 {
			return match
		}
		relPath := strings.TrimSpace(subs[1])
		absPath := relPath
		if !filepath.IsAbs(relPath) {
			absPath = filepath.Join(basePath, relPath)
		}
		absPath = filepath.Clean(absPath)

		// Security: prevent path traversal — included files must stay within
		// the base directory tree.
		rel, err := filepath.Rel(basePath, absPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			config.Logger().Printf("[memory] include path escapes base dir: %s (base: %s)", absPath, basePath)
			return "<!-- include blocked (path traversal): " + relPath + " -->"
		}

		if visited[absPath] {
			config.Logger().Printf("[memory] circular include detected: %s", absPath)
			return "<!-- circular include: " + relPath + " -->"
		}

		data, err := os.ReadFile(absPath)
		if err != nil {
			config.Logger().Printf("[memory] include file not found: %s", absPath)
			return "<!-- include not found: " + relPath + " -->"
		}

		visited[absPath] = true
		included := m.resolveIncludes(string(data), filepath.Dir(absPath), visited, depth+1)
		return strings.TrimSpace(included)
	})
}
