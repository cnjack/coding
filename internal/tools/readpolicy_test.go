package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/cnjack/jcode/internal/config"
)

func TestDenyReadPolicy_PathMatching(t *testing.T) {
	p := NewDenyReadPolicy([]appconfig.DenyReadRule{
		{Path: "/home/user/.ssh", Reason: "credentials"},
		{Path: "/etc/shadow"},
		{Path: "/srv/*/secrets.env", Reason: "env files"},
	})

	cases := []struct {
		path   string
		denied bool
	}{
		{"/home/user/.ssh", true},                    // the dir itself
		{"/home/user/.ssh/id_ed25519", true},         // file under it
		{"/home/user/.ssh/nested/deeper/key", true},  // deep under it
		{"/home/user/.ssh-not-denied/ok.txt", false}, // prefix-but-not-boundary
		{"/etc/shadow", true},                        // exact file
		{"/etc/shadow.lock", false},                  // not under a file rule
		{"/srv/alpha/secrets.env", true},             // glob match
		{"/srv/alpha/secrets.env.bak", false},        // glob must match fully
		{"/srv/a/b/secrets.env", false},              // '*' stays within one segment
		{"/etc/hostname", false},                     // untouched
		{"/home/user/project/src/main.go", false},    // ordinary work file
	}
	for _, c := range cases {
		v := p.CheckPath(c.path)
		if (v != nil) != c.denied {
			t.Errorf("CheckPath(%q) denied=%v, want %v", c.path, v != nil, c.denied)
		}
	}
}

func TestDenyReadPolicy_SymlinkIntoDeniedDir(t *testing.T) {
	deniedDir := t.TempDir()
	outside := t.TempDir()

	p := NewDenyReadPolicy([]appconfig.DenyReadRule{{Path: deniedDir}})

	// A symlink outside the denied tree pointing into it must not bypass.
	// The target must exist: EvalSymlinks cannot resolve a dangling link.
	target := filepath.Join(deniedDir, "secret.txt")
	writeFileForTest(t, target, "TOPSECRET\n")
	link := filepath.Join(outside, "sneaky")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if v := p.CheckPath(link); v == nil {
		t.Error("symlink into a denied directory must be denied")
	}
}

func TestDenyReadPolicy_CommandMatching(t *testing.T) {
	p := NewDenyReadPolicy([]appconfig.DenyReadRule{
		{Path: "/etc/shadow"},
		{Path: "/home/user/.aws", Reason: "credentials"},
		{Path: "/srv/*/secrets.env"},
	})

	denied := []string{
		"cat /etc/shadow",
		"sudo cat /etc/shadow > /tmp/out",
		"ls -la /home/user/.aws",
		"cp /home/user/.aws/credentials /tmp/x",
		"env $(cat /srv/prod/secrets.env) ./deploy.sh",
		"tar czf backup.tgz /home/user/.aws",
	}
	for _, cmd := range denied {
		if v := p.CheckCommand(cmd); v == nil {
			t.Errorf("CheckCommand(%q) must deny", cmd)
		}
	}

	allowed := []string{
		"cat /etc/hostname",
		"ls /home/user/project",
		"echo /etc/shadows-not-a-real-path",
		"go build ./...",
	}
	for _, cmd := range allowed {
		if v := p.CheckCommand(cmd); v != nil {
			t.Errorf("CheckCommand(%q) must allow (got rule %q)", cmd, v.Rule)
		}
	}
}

func TestDenyReadPolicy_MergeIsStrengthenOnly(t *testing.T) {
	p := NewDenyReadPolicy([]appconfig.DenyReadRule{{Path: "/etc/shadow"}})

	// A second "load" with an empty/relaxed rule set must not weaken the
	// live policy — this is the permission-update race guard.
	p.MergeRules(nil)
	if v := p.CheckPath("/etc/shadow"); v == nil {
		t.Fatal("relaxed merge must not drop the live rule")
	}

	// Merging again with the same rule dedupes.
	p.MergeRules([]appconfig.DenyReadRule{{Path: "/etc/shadow"}})
	if got := len(p.Rules()); got != 1 {
		t.Fatalf("rules = %d, want 1 (dedup)", got)
	}

	// New rules take effect immediately for existing holders.
	p.MergeRules([]appconfig.DenyReadRule{{Path: "/var/lib/secrets"}})
	if v := p.CheckPath("/var/lib/secrets/key"); v == nil {
		t.Error("runtime-added rule must apply immediately")
	}
}

func TestDenyReadPolicy_InvalidRulesSkipped(t *testing.T) {
	p := NewDenyReadPolicy([]appconfig.DenyReadRule{
		{Path: "relative/path"},
		{Path: "  "},
		{Path: "/abs/ok"},
	})
	if got := len(p.Rules()); got != 1 {
		t.Fatalf("rules = %+v, want only the absolute one", p.Rules())
	}
}

// --- Tool-level enforcement ---

// denyTestEnv returns an Env with a fresh policy bound to one denied path
// under a temp workspace, so tests never depend on (or pollute) the
// process-wide managed policy.
func denyTestEnv(t *testing.T, deniedPath string) *Env {
	t.Helper()
	workspace := t.TempDir()
	env := NewEnv(workspace, "linux/amd64")
	env.DenyRead = NewDenyReadPolicy([]appconfig.DenyReadRule{
		{Path: deniedPath, Reason: "test policy"},
	})
	return env
}

func TestDenyReadTool_ReadBlocked(t *testing.T) {
	denied := t.TempDir()
	secret := filepath.Join(denied, "secret.txt")
	writeFileForTest(t, secret, "TOPSECRET\n")

	env := denyTestEnv(t, denied)
	run := env.NewReadTool()

	out, err := run.InvokableRun(context.Background(), mustJSON(t, map[string]any{"file_path": secret}))
	if err == nil {
		t.Fatalf("read of denied file must fail, got output %q", out)
	}
	if strings.Contains(out, "TOPSECRET") {
		t.Fatal("denied content must never be returned")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != DenyReadErrorCode {
		t.Fatalf("read denial must carry stable code %q, got %v", DenyReadErrorCode, err)
	}
	// Stable error text mentions the rule.
	if !strings.Contains(err.Error(), "managed deny-read policy") {
		t.Fatalf("stable denial text missing, got: %v", err)
	}
}

func TestDenyReadTool_GrepBlocked(t *testing.T) {
	denied := t.TempDir()
	writeFileForTest(t, filepath.Join(denied, "a.txt"), "needle\n")

	env := denyTestEnv(t, denied)
	run := env.NewGrepTool()

	_, err := run.InvokableRun(context.Background(), mustJSON(t, map[string]any{
		"pattern": "needle", "path": denied,
	}))
	if err == nil || !strings.Contains(err.Error(), "managed deny-read policy") {
		t.Fatalf("grep of denied tree must fail with stable error, got %v", err)
	}
}

func TestDenyReadTool_GlobBlocked(t *testing.T) {
	denied := t.TempDir()
	writeFileForTest(t, filepath.Join(denied, "a.txt"), "x")

	env := denyTestEnv(t, denied)
	run := env.NewGlobTool()

	_, err := run.InvokableRun(context.Background(), mustJSON(t, map[string]any{
		"pattern": "*", "path": denied,
	}))
	if err == nil || !strings.Contains(err.Error(), "managed deny-read policy") {
		t.Fatalf("glob of denied tree must fail with stable error, got %v", err)
	}
}

func TestDenyReadTool_ExecuteBlocked(t *testing.T) {
	denied := t.TempDir()
	secret := filepath.Join(denied, "secret.txt")
	writeFileForTest(t, secret, "TOPSECRET\n")

	env := denyTestEnv(t, denied)
	run := env.NewExecuteTool(nil)

	// `cat` is an auto-approved read-only command: the policy must still block
	// it — approval policy and deny policy are orthogonal layers.
	_, err := run.InvokableRun(context.Background(), mustJSON(t, map[string]any{
		"command": "cat " + secret,
	}))
	if err == nil || !strings.Contains(err.Error(), "managed deny-read policy") {
		t.Fatalf("execute referencing denied path must fail with stable error, got %v", err)
	}

	// Background variant is equally blocked.
	_, err = run.InvokableRun(context.Background(), mustJSON(t, map[string]any{
		"command": "cat " + secret, "background": true,
	}))
	if err == nil || !strings.Contains(err.Error(), "managed deny-read policy") {
		t.Fatalf("background execute referencing denied path must fail, got %v", err)
	}
}

func TestDenyReadTool_EditWriteBlocked(t *testing.T) {
	denied := t.TempDir()
	secret := filepath.Join(denied, "secret.txt")
	writeFileForTest(t, secret, "TOPSECRET\n")

	env := denyTestEnv(t, denied)

	editRun := env.NewEditTool()
	_, err := editRun.InvokableRun(context.Background(), mustJSON(t, map[string]any{
		"file_path": secret, "old_string": "TOPSECRET", "new_string": "leaked?",
	}))
	if err == nil || !strings.Contains(err.Error(), "managed deny-read policy") {
		t.Fatalf("edit of denied file must fail with stable error, got %v", err)
	}

	writeRun := env.NewWriteTool()
	_, err = writeRun.InvokableRun(context.Background(), mustJSON(t, map[string]any{
		"file_path": secret, "content": "overwritten",
	}))
	if err == nil || !strings.Contains(err.Error(), "managed deny-read policy") {
		t.Fatalf("write to denied file must fail with stable error, got %v", err)
	}
}

// TestDenyRead_SubagentInheritsNoHigherPermission proves a cloned subagent Env
// shares the parent's policy object: denials (and runtime additions) apply to
// the child identically.
func TestDenyRead_SubagentInheritsNoHigherPermission(t *testing.T) {
	workspace := t.TempDir()
	parent := NewEnv(workspace, "linux/amd64")
	parent.DenyRead = NewDenyReadPolicy([]appconfig.DenyReadRule{{Path: "/etc/shadow"}})

	child := parent.CloneForSubagent()
	if child.DenyRead != parent.DenyRead {
		t.Fatal("subagent must share the parent's deny-read policy object")
	}
	if v := child.DenyRead.CheckPath("/etc/shadow"); v == nil {
		t.Fatal("subagent must inherit the parent's denials")
	}

	// Runtime strengthen reaches the child (no stale snapshot).
	parent.DenyRead.MergeRules([]appconfig.DenyReadRule{{Path: "/var/lib/tls"}})
	if v := child.DenyRead.CheckPath("/var/lib/tls/server.key"); v == nil {
		t.Fatal("runtime-added deny rule must apply to already-cloned subagents")
	}
}

// TestDenyRead_SurvivesRemoteSwitch proves the policy is bound to the Env, not
// the executor: switching to a remote executor cannot drop denials.
func TestDenyRead_SurvivesRemoteSwitch(t *testing.T) {
	workspace := t.TempDir()
	env := NewEnv(workspace, "linux/amd64")
	policy := NewDenyReadPolicy([]appconfig.DenyReadRule{{Path: "/etc/shadow"}})
	env.DenyRead = policy

	// Simulate an environment switch (SSH/Docker): executor and pwd change,
	// the policy pointer must survive.
	env.SetRemote(&fakeRemoteExecutor{platform: "linux/amd64"}, "/remote/path")
	if env.DenyRead != policy {
		t.Fatal("deny-read policy must survive an environment switch")
	}
	if v := env.DenyRead.CheckPath("/etc/shadow"); v == nil {
		t.Fatal("denial must still hold after switching to a remote executor")
	}

	env.ResetToLocal(workspace, "linux/amd64")
	if env.DenyRead != policy || env.DenyRead.CheckPath("/etc/shadow") == nil {
		t.Fatal("denial must hold after returning to the local executor")
	}
}

// fakeRemoteExecutor is a minimal RemoteExecutor used to prove the deny policy
// is bound to the Env, not the executor.
type fakeRemoteExecutor struct{ platform string }

func (f *fakeRemoteExecutor) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (f *fakeRemoteExecutor) WriteFile(context.Context, string, []byte, os.FileMode) error {
	return nil
}
func (f *fakeRemoteExecutor) MkdirAll(context.Context, string, os.FileMode) error { return nil }
func (f *fakeRemoteExecutor) Stat(context.Context, string) (*FileInfo, error) {
	return &FileInfo{Exists: false}, nil
}
func (f *fakeRemoteExecutor) Exec(context.Context, string, string, time.Duration) (string, string, error) {
	return "", "", nil
}
func (f *fakeRemoteExecutor) Platform() string               { return f.platform }
func (f *fakeRemoteExecutor) Label() string                  { return "fake-remote" }
func (f *fakeRemoteExecutor) Probe(context.Context) error    { return nil }
func (f *fakeRemoteExecutor) Close() error                   { return nil }
func (f *fakeRemoteExecutor) ProjectLabel(pwd string) string { return "fake://remote" + pwd }

// --- helpers ---

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFileForTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
