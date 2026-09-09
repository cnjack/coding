package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func trustTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(TrustProjectEnv, "")
}

func TestProjectTrust_DefaultUntrusted(t *testing.T) {
	trustTestHome(t)
	dir := t.TempDir()

	decision := ProjectInstructionsAllowed(dir)
	if decision.Allowed {
		t.Fatal("unknown project must not be trusted by default")
	}
	if decision.Reason != "untrusted" {
		t.Fatalf("reason = %q, want %q", decision.Reason, "untrusted")
	}
}

func TestProjectTrust_EnvOverride(t *testing.T) {
	trustTestHome(t)
	dir := t.TempDir()
	t.Setenv(TrustProjectEnv, "1")

	decision := ProjectInstructionsAllowed(dir)
	if !decision.Allowed {
		t.Fatal("JCODE_AGENTS_TRUST_PROJECT=1 must trust the project")
	}
	if decision.Reason != "env" {
		t.Fatalf("reason = %q, want %q", decision.Reason, "env")
	}
}

func TestProjectTrust_ExplicitTrustRoundTrip(t *testing.T) {
	trustTestHome(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := TrustProjectRoot(root); err != nil {
		t.Fatalf("TrustProjectRoot: %v", err)
	}
	decision := ProjectInstructionsAllowed(root)
	if !decision.Allowed || decision.Reason != "store" {
		t.Fatalf("after trust: allowed=%v reason=%q, want allowed=true reason=store", decision.Allowed, decision.Reason)
	}
	if decision.Root != root {
		t.Fatalf("decision.Root = %q, want %q", decision.Root, root)
	}

	// Trust persists across loads (separate process simulation).
	store := LoadProjectTrust()
	if len(store.Trusted) != 1 || store.Trusted[0].Path != root {
		t.Fatalf("trust store = %+v, want one entry for %s", store.Trusted, root)
	}

	// Untrust revokes immediately.
	if err := UntrustProjectRoot(root); err != nil {
		t.Fatalf("UntrustProjectRoot: %v", err)
	}
	if decision := ProjectInstructionsAllowed(root); decision.Allowed {
		t.Fatal("revoked project must not stay trusted")
	}
	// Store no longer contains the entry.
	store = LoadProjectTrust()
	if len(store.Trusted) != 0 {
		t.Fatalf("trust store after revoke = %+v, want empty", store.Trusted)
	}
}

func TestProjectTrust_TrustIsKeyedOnGitRoot(t *testing.T) {
	trustTestHome(t)
	root := t.TempDir()
	initGitRepoForTrust(t, root)
	sub := filepath.Join(root, "pkg", "inner")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Trust declared from a subdirectory applies to the whole repo root...
	if err := TrustProjectRoot(sub); err != nil {
		t.Fatalf("TrustProjectRoot(sub): %v", err)
	}
	if decision := ProjectInstructionsAllowed(sub); !decision.Allowed {
		t.Fatal("trusting a subdirectory must trust the git root (and thus the subdir)")
	}
	// ...and a sibling subdirectory is covered too.
	sibling := filepath.Join(root, "pkg", "other")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if decision := ProjectInstructionsAllowed(sibling); !decision.Allowed {
		t.Fatal("sibling under the same trusted git root must be trusted")
	}

	// An unrelated directory is NOT covered.
	if decision := ProjectInstructionsAllowed(t.TempDir()); decision.Allowed {
		t.Fatal("unrelated directory must not inherit trust")
	}
}

func TestProjectTrust_MalformedStoreFailsClosed(t *testing.T) {
	trustTestHome(t)
	dir := t.TempDir()
	if err := TrustProjectRoot(dir); err != nil {
		t.Fatal(err)
	}
	// Corrupt the store: trust must fail closed, not grant.
	if err := os.WriteFile(ProjectTrustStorePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if decision := ProjectInstructionsAllowed(dir); decision.Allowed {
		t.Fatal("malformed trust store must fail closed (untrusted)")
	}
}

func TestProjectTrust_StoreFileIsOwnerOnly(t *testing.T) {
	trustTestHome(t)
	dir := t.TempDir()
	if err := TrustProjectRoot(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ProjectTrustStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("trust store mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestProjectTrust_RemotePathCollisionDoesNotInheritTrust verifies trust never
// crosses executors: a stored path that does not exist on this machine (a
// remote session's path colliding with a trust record) grants nothing.
func TestProjectTrust_RemotePathCollisionDoesNotInheritTrust(t *testing.T) {
	trustTestHome(t)
	local := t.TempDir()
	if err := TrustProjectRoot(local); err != nil {
		t.Fatal(err)
	}

	// Simulate a remote session whose remote path string equals the locally
	// trusted path, but the path does not exist locally under that name: use
	// a store entry for a path that does not exist on this machine.
	store := LoadProjectTrust()
	store.Trusted = append(store.Trusted, TrustedProject{Path: "/remote/home/deploy/app"})
	if err := os.WriteFile(ProjectTrustStorePath(), mustMarshalTrust(t, store), 0o600); err != nil {
		t.Fatal(err)
	}
	if decision := ProjectInstructionsAllowed("/remote/home/deploy/app"); decision.Allowed {
		t.Fatal("a trusted path that does not exist locally (remote collision) must not grant trust")
	}

	// The genuine local record still works (it exists).
	if decision := ProjectInstructionsAllowed(local); !decision.Allowed {
		t.Fatal("existing locally-trusted project must remain trusted")
	}
}

func mustMarshalTrust(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// initGitRepoForTrust initializes a real git repo (GitRoot must resolve).
func initGitRepoForTrust(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "init", "-q")
	// Scrub GIT_* vars so the init targets dir, not an inherited repo.
	var clean []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_DIR=") &&
			!strings.HasPrefix(kv, "GIT_WORK_TREE=") &&
			!strings.HasPrefix(kv, "GIT_INDEX_FILE=") &&
			!strings.HasPrefix(kv, "GIT_COMMON_DIR=") &&
			!strings.HasPrefix(kv, "GIT_PREFIX=") {
			clean = append(clean, kv)
		}
	}
	cmd.Env = clean
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}
