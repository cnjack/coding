package command

import (
	"testing"

	"github.com/cnjack/jcode/internal/config"
)

func TestTrustCmd_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.TrustProjectEnv, "")
	dir := t.TempDir()

	// Untrusted before any action.
	if config.ProjectInstructionsAllowed(dir).Allowed {
		t.Fatal("project must start untrusted")
	}

	// `jcode trust <dir>` persists trust.
	trustCmd := NewTrustCmd()
	trustCmd.SetArgs([]string{dir})
	if err := trustCmd.Execute(); err != nil {
		t.Fatalf("trust command failed: %v", err)
	}
	if !config.ProjectInstructionsAllowed(dir).Allowed {
		t.Fatal("project must be trusted after `jcode trust`")
	}

	// `jcode untrust <dir>` revokes it.
	untrustCmd := NewUntrustCmd()
	untrustCmd.SetArgs([]string{dir})
	if err := untrustCmd.Execute(); err != nil {
		t.Fatalf("untrust command failed: %v", err)
	}
	if config.ProjectInstructionsAllowed(dir).Allowed {
		t.Fatal("project must be untrusted after `jcode untrust`")
	}
}

func TestTrustCmd_StatusDoesNotMutate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.TrustProjectEnv, "")
	dir := t.TempDir()

	cmd := NewTrustCmd()
	cmd.SetArgs([]string{"--status", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status command failed: %v", err)
	}
	if config.ProjectInstructionsAllowed(dir).Allowed {
		t.Fatal("--status must not grant trust")
	}
}
