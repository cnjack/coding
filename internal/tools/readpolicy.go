package tools

import (
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	appconfig "github.com/cnjack/jcode/internal/config"
)

// ---------------------------------------------------------------------------
// Managed deny-read policy
//
// A managed deny-read rule blocks agent tool access (read/grep/glob/execute,
// plus edit/write so denied content cannot leak through diffs or be modified)
// to the paths it matches. The rules are USER-MANAGED policy:
//
//   - They load from the global config (~/.jcode/config.json, "deny_read")
//     and are never merged from project config (config.mergeProjectFields
//     denylist) — repository content cannot add, relax, or remove them.
//   - Once loaded into a process, the live policy is strengthen-only: later
//     permission changes (approval mode, full access, config hot reload,
//     resume, remote switch) cannot drop a rule that was in force when the
//     session started. Removing a rule takes effect on the next start.
//   - Every Env — local, SSH, Docker, subagent, teammate, workflow — shares
//     ONE policy object (the process-wide singleton), so the effective
//     permission snapshot is identical everywhere and a subagent can never
//     inherit a higher read permission than its parent.
//
// Denials are enforced inside tool execution, below the approval middleware:
// no approval mode, "Approve All" promotion, or reviewer allow can bypass
// them, and full access is not an escape hatch.
// ---------------------------------------------------------------------------

// DenyReadPolicy is the shared, mutex-guarded set of managed deny-read rules.
// The zero value is an empty (allow-all) policy.
type DenyReadPolicy struct {
	mu    sync.RWMutex
	rules []denyReadRule
}

// denyReadRule is one normalized rule. pattern is kept exactly as configured
// (cleaned for non-globs); isGlob records whether it contains filepath.Match
// metacharacters.
type denyReadRule struct {
	pattern string
	reason  string
	isGlob  bool
}

// hasGlobMeta reports whether s contains filepath.Match metacharacters.
func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// NewDenyReadPolicy builds a policy from config rules. Invalid rules (empty or
// relative paths) are skipped with an audit-log line: deny rules are absolute
// by contract, and a relative pattern would be ambiguous across executors.
func NewDenyReadPolicy(rules []appconfig.DenyReadRule) *DenyReadPolicy {
	p := &DenyReadPolicy{}
	p.MergeRules(rules)
	return p
}

// MergeRules strengthens the policy with additional rules (union). Existing
// rules are never removed or replaced — the managed policy is monotonic for
// the lifetime of the process.
func (p *DenyReadPolicy) MergeRules(rules []appconfig.DenyReadRule) {
	if p == nil || len(rules) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range rules {
		pattern := strings.TrimSpace(r.Path)
		if pattern == "" {
			continue
		}
		if !filepath.IsAbs(pattern) {
			appconfig.Logger().Printf("[security] deny-read rule skipped (must be absolute): %q", r.Path)
			continue
		}
		isGlob := hasGlobMeta(pattern)
		if !isGlob {
			pattern = filepath.Clean(pattern)
		}
		// De-duplicate identical patterns (first reason wins).
		dup := false
		for _, existing := range p.rules {
			if existing.pattern == pattern {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		p.rules = append(p.rules, denyReadRule{pattern: pattern, reason: r.Reason, isGlob: isGlob})
	}
}

// Empty reports whether the policy has no rules (nothing is denied).
func (p *DenyReadPolicy) Empty() bool {
	if p == nil {
		return true
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.rules) == 0
}

// Rules returns a copy of the current rules (for status display).
func (p *DenyReadPolicy) Rules() []appconfig.DenyReadRule {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]appconfig.DenyReadRule, 0, len(p.rules))
	for _, r := range p.rules {
		out = append(out, appconfig.DenyReadRule{Path: r.pattern, Reason: r.reason})
	}
	return out
}

// DenyReadViolation describes one denial. It doubles as the stable error:
// callers can errors.As it to branch on code "path_denied_by_policy".
type DenyReadViolation struct {
	// Tool is the tool that was blocked (read/grep/glob/execute/edit/write).
	Tool string
	// Path is the offending path (for execute: the matched path token).
	Path string
	// Rule is the rule pattern that matched.
	Rule string
	// Reason is the configured reason (may be empty).
	Reason string
}

const DenyReadErrorCode = "path_denied_by_policy"

func (v *DenyReadViolation) Error() string {
	msg := "access to " + v.Path + " is denied by managed deny-read policy (rule: " + v.Rule + ")"
	if v.Reason != "" {
		msg += ": " + v.Reason
	}
	return msg
}

// checkPathLocked matches one cleaned path against the rules. The caller must
// hold at least a read lock.
func (p *DenyReadPolicy) checkPathLocked(path string) *denyReadRule {
	if path == "" {
		return nil
	}
	for i := range p.rules {
		r := &p.rules[i]
		if r.isGlob {
			if ok, err := filepath.Match(r.pattern, path); err == nil && ok {
				return r
			}
			continue
		}
		if path == r.pattern || strings.HasPrefix(path, r.pattern+string(filepath.Separator)) {
			return r
		}
	}
	return nil
}

// CheckPath reports whether path is denied. It checks the cleaned path and,
// best-effort, its symlink-resolved form — a symlink pointing into a denied
// directory must not bypass the rule. Resolution only happens for existing
// local paths; remote (SSH/Docker) paths are matched as-is.
func (p *DenyReadPolicy) CheckPath(path string) *DenyReadViolation {
	if p == nil || path == "" {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.rules) == 0 {
		return nil
	}
	candidates := normalizeDenyCandidates(path)
	for _, c := range candidates {
		if r := p.checkPathLocked(c); r != nil {
			return &DenyReadViolation{Path: path, Rule: r.pattern, Reason: r.reason}
		}
	}
	return nil
}

// normalizeDenyCandidates returns the path forms worth matching: the cleaned
// path and, when it exists locally, its symlink-resolved form. Relative paths
// are matched as-is (cleaned) — callers resolve against their workspace first.
func normalizeDenyCandidates(path string) []string {
	cleaned := filepath.Clean(path)
	candidates := []string{cleaned}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && resolved != cleaned {
		candidates = append(candidates, filepath.Clean(resolved))
	}
	return candidates
}

// absPathTokenRe extracts absolute-path-looking tokens from a shell command.
// Lexical analysis is best-effort by design: it does not try to understand
// the shell, it just refuses to run commands that reference a denied path
// token (including via redirections, arguments, or subcommands).
var absPathTokenRe = regexp.MustCompile(`(?:/[A-Za-z0-9_.\-]+)+`)

// CheckCommand reports whether a shell command references a denied path.
// Every absolute-path-looking token in the command is extracted and matched
// against the rules with the same semantics as CheckPath (boundary-aware, so
// a rule "/etc/shadow" does not match "/etc/shadows"). This is a lexical
// guard, not a sandbox — it closes the trivial `cat /etc/shadow` path while
// execute approval continues to govern everything else.
func (p *DenyReadPolicy) CheckCommand(command string) *DenyReadViolation {
	if p == nil || command == "" {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.rules) == 0 {
		return nil
	}
	for _, token := range absPathTokenRe.FindAllString(command, -1) {
		if r := p.checkPathLocked(filepath.Clean(token)); r != nil {
			return &DenyReadViolation{Path: token, Rule: r.pattern, Reason: r.reason}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Process-wide managed policy
// ---------------------------------------------------------------------------

var (
	managedDenyOnce   sync.Once
	managedDenyPolicy *DenyReadPolicy
)

// ManagedDenyRead returns the process-wide managed deny-read policy, shared by
// every Env. It is initialized on first use from the global config (tolerant
// of a missing config: no rules) so that Env creation paths which do not
// receive a loaded config — teammates, workflows, library callers — still
// enforce the user's deny rules.
func ManagedDenyRead() *DenyReadPolicy {
	managedDenyOnce.Do(func() {
		managedDenyPolicy = &DenyReadPolicy{}
		if cfg, err := appconfig.LoadConfig(); err == nil && cfg != nil {
			managedDenyPolicy.MergeRules(cfg.DenyRead)
		}
	})
	return managedDenyPolicy
}

// InitManagedDenyRead seeds the managed policy from an already-loaded config.
// Startup paths call it so the policy exactly matches the config the session
// was built from. It is idempotent and union-merges with whatever is already
// in force.
func InitManagedDenyRead(cfg *appconfig.Config) {
	if cfg == nil {
		return
	}
	ManagedDenyRead().MergeRules(cfg.DenyRead)
}

// AddManagedDenyReadRules strengthens the live managed policy at runtime
// (settings updates). There is deliberately no removal API: managed deny-read
// rules take priority over later relaxed settings, so dropping a rule
// requires editing the config and starting a new session.
func AddManagedDenyReadRules(rules []appconfig.DenyReadRule) {
	ManagedDenyRead().MergeRules(rules)
}

// ---------------------------------------------------------------------------
// Env integration
// ---------------------------------------------------------------------------

// checkDenyRead enforces the managed deny-read policy for a path-taking tool
// call. It returns a stable ToolError (code path_denied_by_policy) and writes
// an audit line to the debug log. nil error means the path is allowed.
func (e *Env) checkDenyRead(tool, path string) error {
	violation := e.DenyRead.CheckPath(path)
	if violation == nil {
		return nil
	}
	violation.Tool = tool
	appconfig.Logger().Printf(
		"[security] deny-read blocked %s: path=%s rule=%s", tool, path, violation.Rule,
	)
	return toolErrf(
		DenyReadErrorCode,
		"This path is blocked by a managed deny-read rule; choose a different path or ask the user to review the deny_read policy.",
		"%s", violation.Error(),
	)
}

// checkDenyReadCommand enforces the policy for shell execution. See
// DenyReadPolicy.CheckCommand for the lexical matching contract.
func (e *Env) checkDenyReadCommand(tool, command string) error {
	violation := e.DenyRead.CheckCommand(command)
	if violation == nil {
		return nil
	}
	violation.Tool = tool
	appconfig.Logger().Printf(
		"[security] deny-read blocked %s: command references denied path %s (rule %s)",
		tool, violation.Path, violation.Rule,
	)
	return toolErrf(
		DenyReadErrorCode,
		"The command references a path blocked by a managed deny-read rule; rewrite it without that path or ask the user to review the deny_read policy.",
		"%s", violation.Error(),
	)
}

// ensure DenyReadViolation satisfies error (used directly by callers/tests).
var _ error = (*DenyReadViolation)(nil)
