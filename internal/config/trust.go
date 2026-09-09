package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// projectTrustFile stores explicit per-project trust decisions outside any
// repository, so repository content can never grant itself trust.
const projectTrustFile = "project_trust.json"

// TrustProjectEnv is the explicit opt-in that treats every project as trusted
// for project-level AGENTS.md instructions. It follows the same pattern as
// JCODE_HOOKS_TRUST_PROJECT / JCODE_MCP_TRUST_PROJECT: an environment variable
// is user/process-owned state, never repository content.
const TrustProjectEnv = "JCODE_AGENTS_TRUST_PROJECT"

// ProjectTrust is the on-disk trust store (~/.jcode/project_trust.json).
type ProjectTrust struct {
	Version int              `json:"version"`
	Trusted []TrustedProject `json:"trusted"`
}

// TrustedProject records one explicitly trusted project root.
type TrustedProject struct {
	// Path is the project root (git root, or the directory itself when not a
	// git repository) with symlinks resolved, so a moved or re-linked checkout
	// does not silently inherit trust.
	Path      string    `json:"path"`
	TrustedAt time.Time `json:"trusted_at"`
}

// ProjectTrustStorePath returns the trust-store file path inside ConfigDir().
func ProjectTrustStorePath() string {
	return filepath.Join(ConfigDir(), projectTrustFile)
}

// LoadProjectTrust reads the trust store. Failures (missing file, malformed
// JSON, unreadable store) fail CLOSED: an unreadable trust record must not
// grant trust, so they resolve to an empty store. The failure is logged to the
// debug log for auditability.
func LoadProjectTrust() ProjectTrust {
	data, err := os.ReadFile(ProjectTrustStorePath())
	if err != nil {
		if !os.IsNotExist(err) {
			Logger().Printf("[trust] project trust store unreadable (%v); treating all projects as untrusted", err)
		}
		return ProjectTrust{Version: 1}
	}
	var store ProjectTrust
	if err := json.Unmarshal(data, &store); err != nil {
		Logger().Printf("[trust] project trust store malformed (%v); treating all projects as untrusted", err)
		return ProjectTrust{Version: 1}
	}
	if store.Version == 0 {
		store.Version = 1
	}
	return store
}

// saveProjectTrustStore writes the store atomically with owner-only
// permissions. The store lives under ConfigDir, which is created with 0700.
func saveProjectTrustStore(store ProjectTrust) error {
	if store.Version == 0 {
		store.Version = 1
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project trust store: %w", err)
	}
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir for project trust store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+projectTrustFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("create project trust temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure project trust temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write project trust temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close project trust temp file: %w", err)
	}
	if err := os.Rename(tmpPath, ProjectTrustStorePath()); err != nil {
		return fmt.Errorf("replace project trust store: %w", err)
	}
	return nil
}

// projectTrustRoot resolves the project root for a working directory: the git
// root when inside a work tree, otherwise the directory itself (symlinks
// resolved). This is the identity a trust record is keyed on.
func projectTrustRoot(pwd string) string {
	if pwd == "" {
		return ""
	}
	root := GitRoot(pwd)
	if root == "" {
		root = pwd
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

// IsProjectTrusted reports whether pwd belongs to a project the user has
// explicitly trusted (or JCODE_AGENTS_TRUST_PROJECT=1 is set).
func IsProjectTrusted(pwd string) bool {
	return ProjectInstructionsAllowed(pwd).Allowed
}

// ProjectTrustDecision explains one trust evaluation.
type ProjectTrustDecision struct {
	// Allowed reports whether project-level instructions may load.
	Allowed bool
	// Root is the project root the decision was made for.
	Root string
	// Reason is a stable machine-readable cause: "env", "store", or "untrusted".
	Reason string
}

// ProjectInstructionsAllowed decides whether project-level AGENTS.md content
// may be loaded for pwd. Only two paths grant trust, and neither can be
// influenced by repository content:
//
//  1. JCODE_AGENTS_TRUST_PROJECT=1 — explicit process-level opt-in (CI, power
//     users), mirroring the hooks/MCP trust gates.
//  2. An entry for the project root in ~/.jcode/project_trust.json, created by
//     `jcode trust <path>` after user confirmation.
//
// Everything else — including brand-new clones and unknown directories — is
// untrusted: project AGENTS.md files (walk-up chain and AGENTS.local.md) are
// excluded from the system prompt.
func ProjectInstructionsAllowed(pwd string) ProjectTrustDecision {
	if os.Getenv(TrustProjectEnv) == "1" {
		return ProjectTrustDecision{Allowed: true, Root: projectTrustRoot(pwd), Reason: "env"}
	}
	root := projectTrustRoot(pwd)
	if root == "" {
		return ProjectTrustDecision{Allowed: false, Reason: "untrusted"}
	}
	store := LoadProjectTrust()
	for _, t := range store.Trusted {
		if t.Path != root {
			continue
		}
		// The trusted root must exist on THIS machine. Trust records are
		// user-confirmed local checkouts; a remote (SSH/Docker) session whose
		// remote path merely collides with a stored path string must not
		// inherit that trust. (Trust never crosses executors.)
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		return ProjectTrustDecision{Allowed: true, Root: root, Reason: "store"}
	}
	return ProjectTrustDecision{Allowed: false, Root: root, Reason: "untrusted"}
}

// TrustProjectRoot records pwd's project root as trusted. The caller is
// responsible for the user-visible confirmation — this function only persists
// the decision. It is idempotent.
func TrustProjectRoot(pwd string) error {
	root := projectTrustRoot(pwd)
	if root == "" {
		return fmt.Errorf("cannot resolve project root for %q", pwd)
	}
	store := LoadProjectTrust()
	for _, t := range store.Trusted {
		if t.Path == root {
			return nil // already trusted
		}
	}
	store.Trusted = append(store.Trusted, TrustedProject{Path: root, TrustedAt: time.Now().UTC()})
	if err := saveProjectTrustStore(store); err != nil {
		return err
	}
	Logger().Printf("[trust] project trusted: %s", root)
	return nil
}

// UntrustProjectRoot removes pwd's project root from the trust store. Revocation
// takes effect on the next session/prompt build. It is idempotent: untrusting an
// unknown root is not an error.
func UntrustProjectRoot(pwd string) error {
	root := projectTrustRoot(pwd)
	if root == "" {
		return fmt.Errorf("cannot resolve project root for %q", pwd)
	}
	store := LoadProjectTrust()
	kept := store.Trusted[:0]
	for _, t := range store.Trusted {
		if t.Path != root {
			kept = append(kept, t)
		}
	}
	store.Trusted = kept
	if err := saveProjectTrustStore(store); err != nil {
		return err
	}
	Logger().Printf("[trust] project untrusted: %s", root)
	return nil
}
