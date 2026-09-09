package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	internalmodel "github.com/cnjack/jcode/internal/model"
	"github.com/cnjack/jcode/internal/prompts"
	"github.com/cnjack/jcode/internal/tools"
	utils "github.com/cnjack/jcode/internal/util"
)

// newTestReminder constructs a reminderMiddleware with the given config.
func newTestReminder(t *testing.T, cfg ReminderConfig, tu *internalmodel.TokenUsage) *reminderMiddleware {
	t.Helper()
	mw := NewReminderMiddleware(cfg, tu)
	rm, ok := mw.(*reminderMiddleware)
	if !ok {
		t.Fatalf("NewReminderMiddleware returned %T, want *reminderMiddleware", mw)
	}
	return rm
}

// runReminder invokes BeforeModelRewriteState once on a fresh state and
// returns the concatenated system messages the middleware appended.
func runReminder(t *testing.T, rm *reminderMiddleware) string {
	t.Helper()
	state := &adk.ChatModelAgentState{}
	_, state, err := rm.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	var sb strings.Builder
	for _, msg := range state.Messages {
		if msg.Role == schema.System {
			sb.WriteString(msg.Content)
		}
	}
	return sb.String()
}

// collectReminderText runs BeforeModelRewriteState on a fresh state and returns
// the concatenated system messages the middleware appended.
func collectReminderText(t *testing.T, tu *internalmodel.TokenUsage, contextLimit int) string {
	t.Helper()
	return runReminder(t, newTestReminder(t, ReminderConfig{ContextLimit: contextLimit}, tu))
}

// The occupancy reminders (token_warning/token_critical) must reflect the LAST
// API call's total tokens (current context size), not the session's cumulative
// prompt-token spend: the agent re-sends the whole window every tool loop, so
// the cumulative ledger grows without bound and crosses any threshold after a
// few calls even when the live context is small.
func TestReminderOccupancyUsesLastCallTotal(t *testing.T) {
	tu := &internalmodel.TokenUsage{}
	// Three calls at ~50k context each: cumulative prompt = 147k (147% of
	// limit), but the live context is only 50k (50% — below the 60% warning).
	for i := 0; i < 3; i++ {
		tu.Add(internalmodel.AddParams{Prompt: 49000, Completion: 1000, Total: 50000})
	}

	text := collectReminderText(t, tu, 100000)
	if strings.Contains(text, "Context is") {
		t.Fatalf("occupancy reminder fired from cumulative usage; live context is 50%%:\n%s", text)
	}

	// A genuinely large last call must still trigger the critical reminder.
	tu.Add(internalmodel.AddParams{Prompt: 89000, Completion: 1000, Total: 90000})
	text = collectReminderText(t, tu, 100000)
	if !strings.Contains(text, "Context is 90% full") {
		t.Fatalf("token_critical missing for 90%% live context:\n%s", text)
	}
}

// After a compaction shrinks the live context, ResetContext clears the last-call
// snapshot (but deliberately keeps the cumulative ledger). The occupancy
// reminders must go quiet instead of being stuck >100% forever.
func TestReminderOccupancyResetsAfterCompaction(t *testing.T) {
	tu := &internalmodel.TokenUsage{}
	for i := 0; i < 5; i++ {
		tu.Add(internalmodel.AddParams{Prompt: 95000, Completion: 1000, Total: 96000})
	}
	tu.ResetContext()

	text := collectReminderText(t, tu, 100000)
	if strings.Contains(text, "Context is") {
		t.Fatalf("occupancy reminder still firing after ResetContext:\n%s", text)
	}
}

// A file modified outside the session after the agent read it must be reported
// once (with a re-read instruction), and not repeated on subsequent rounds.
func TestReminderInjectsExternalFileChange(t *testing.T) {
	ft := tools.NewFileTracker(nil)
	path := filepath.Join(t.TempDir(), "watched.go")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	ft.TrackRead(path, []byte("v1"), info.ModTime())

	rm := newTestReminder(t, ReminderConfig{FileTracker: ft}, nil)

	// External rewrite with a forced mtime bump.
	if err := os.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	text := runReminder(t, rm)
	if !strings.Contains(text, path) || !strings.Contains(text, "re-read") {
		t.Fatalf("expected external-change reminder with path and re-read instruction, got: %q", text)
	}
	// Not re-injected on the next round.
	if again := runReminder(t, rm); strings.Contains(again, path) {
		t.Fatalf("external change reported twice: %q", again)
	}
}

// The periodic env refresh must fire only on the configured cadence, diff
// against the preset startup snapshot (including a stale date), and go quiet
// once the snapshot has been advanced.
func TestReminderEnvRefreshCadence(t *testing.T) {
	pwd := t.TempDir()
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	baseline := prompts.SerializeEnvInfo("test-os", pwd, "local", &utils.EnvInfo{GitBranch: "a"})
	baseline = strings.Replace(baseline, "date="+today, "date="+yesterday, 1)

	var collects int
	rm := newTestReminder(t, ReminderConfig{
		Pwd:             pwd,
		Platform:        "test-os",
		EnvLabel:        "local",
		EnvSnapshot:     baseline,
		EnvRefreshEvery: 2,
		EnvCollector: func(string) *utils.EnvInfo {
			collects++
			return &utils.EnvInfo{GitBranch: "b"}
		},
	}, nil)

	// Round 1: below cadence — no refresh.
	if text := runReminder(t, rm); strings.Contains(text, "git_branch") {
		t.Fatalf("round 1 must not refresh env, got: %q", text)
	}
	// Round 2: cadence hit — branch change and stale date both reported.
	text := runReminder(t, rm)
	if !strings.Contains(text, "git_branch: a → b") {
		t.Fatalf("round 2 missing git_branch diff: %q", text)
	}
	if !strings.Contains(text, "date: "+yesterday+" → "+today) {
		t.Fatalf("round 2 missing date drift: %q", text)
	}
	// Rounds 3-4: snapshot advanced — the same diff is not repeated.
	_ = runReminder(t, rm)
	if text := runReminder(t, rm); strings.Contains(text, "git_branch") || strings.Contains(text, "date:") {
		t.Fatalf("round 4 re-injected an already-reported diff: %q", text)
	}
	if collects != 2 {
		t.Fatalf("collector must run only on cadence rounds (want 2), ran %d times", collects)
	}
}

// Without a Pwd the env refresh is disabled entirely: the collector must never
// run, regardless of cadence.
func TestReminderEnvRefreshDisabled(t *testing.T) {
	var collects int
	rm := newTestReminder(t, ReminderConfig{
		EnvRefreshEvery: 1,
		EnvCollector: func(string) *utils.EnvInfo {
			collects++
			return &utils.EnvInfo{}
		},
	}, nil)

	for i := 0; i < 3; i++ {
		_ = runReminder(t, rm)
	}
	if collects != 0 {
		t.Fatalf("collector must never run without Pwd, ran %d times", collects)
	}
}

// AGENTS.md changed on disk mid-session: baseline round is silent, the change
// round injects the new content, and the following round does not repeat it.
func TestReminderAgentsMdReload(t *testing.T) {
	// Project AGENTS.md loads only for trusted projects — this test asserts
	// reload behavior of project instructions, so opt in explicitly.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JCODE_AGENTS_TRUST_PROJECT", "1")

	pwd := t.TempDir()
	agentsPath := filepath.Join(pwd, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("rule v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := newTestReminder(t, ReminderConfig{Pwd: pwd}, nil)

	// Round 1 establishes the baseline silently (content already in the
	// system prompt).
	if text := runReminder(t, rm); strings.Contains(text, "AGENTS.md") {
		t.Fatalf("baseline round must not inject, got: %q", text)
	}

	// External edit with a forced mtime advance.
	if err := os.WriteFile(agentsPath, []byte("rule v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(agentsPath, future, future); err != nil {
		t.Fatal(err)
	}

	text := runReminder(t, rm)
	if !strings.Contains(text, "rule v2") || !strings.Contains(text, "supersedes") {
		t.Fatalf("expected updated AGENTS.md content with supersedes wording, got: %q", text)
	}
	// Round 3: hash unchanged — no repeat.
	if again := runReminder(t, rm); strings.Contains(again, "rule v2") {
		t.Fatalf("AGENTS.md update injected twice: %q", again)
	}
}

// A touch (mtime change, same content) must not trigger a reload notice.
func TestReminderAgentsMdTouchOnly(t *testing.T) {
	pwd := t.TempDir()
	agentsPath := filepath.Join(pwd, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("stable rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := newTestReminder(t, ReminderConfig{Pwd: pwd}, nil)
	_ = runReminder(t, rm) // baseline

	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(agentsPath, future, future); err != nil {
		t.Fatal(err)
	}

	if text := runReminder(t, rm); strings.Contains(text, "AGENTS.md") {
		t.Fatalf("touch-only must not inject, got: %q", text)
	}
}

// Deleting AGENTS.md injects a removal notice once, then stays silent.
func TestReminderAgentsMdRemoved(t *testing.T) {
	pwd := t.TempDir()
	agentsPath := filepath.Join(pwd, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("doomed rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := newTestReminder(t, ReminderConfig{Pwd: pwd}, nil)
	_ = runReminder(t, rm) // baseline

	if err := os.Remove(agentsPath); err != nil {
		t.Fatal(err)
	}

	text := runReminder(t, rm)
	if !strings.Contains(text, "AGENTS.md was removed") {
		t.Fatalf("expected removal notice, got: %q", text)
	}
	if again := runReminder(t, rm); strings.Contains(again, "AGENTS.md") {
		t.Fatalf("removal notice injected twice: %q", again)
	}
}

// fakeRemoteExecutor is the minimal RemoteExecutor used to flip a live Env to
// remote in tests without a real SSH/Docker backend.
type fakeRemoteExecutor struct{}

func (fakeRemoteExecutor) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (fakeRemoteExecutor) WriteFile(context.Context, string, []byte, os.FileMode) error {
	return nil
}
func (fakeRemoteExecutor) MkdirAll(context.Context, string, os.FileMode) error { return nil }
func (fakeRemoteExecutor) Stat(context.Context, string) (*tools.FileInfo, error) {
	return &tools.FileInfo{}, nil
}
func (fakeRemoteExecutor) Exec(context.Context, string, string, time.Duration) (string, string, error) {
	return "", "", nil
}
func (fakeRemoteExecutor) Platform() string            { return "linux/amd64" }
func (fakeRemoteExecutor) Label() string               { return "fake-remote" }
func (fakeRemoteExecutor) Probe(context.Context) error { return nil }
func (fakeRemoteExecutor) Close() error                { return nil }
func (fakeRemoteExecutor) ProjectLabel(string) string  { return "fake" }

// switch_env mutates the shared *tools.Env in place without rebuilding the
// agent (ACP/web). While the live env is remote, the local-filesystem sweeps
// (env drift, AGENTS.md, external file changes) must pause — they would report
// wrong-host state — and resume after switching back to local.
func TestReminderLocalSweepsPauseWhileEnvRemote(t *testing.T) {
	pwd := t.TempDir()
	env := tools.NewEnv(pwd, "test-os")

	// A tracked file whose external change would normally be reported.
	watched := filepath.Join(pwd, "watched.go")
	if err := os.WriteFile(watched, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(watched)
	env.FileTracker.TrackRead(watched, []byte("v1"), info.ModTime())

	var collects int
	rm := newTestReminder(t, ReminderConfig{
		Pwd:             pwd,
		Platform:        "test-os",
		EnvSnapshot:     prompts.SerializeEnvInfo("test-os", pwd, "local", &utils.EnvInfo{GitBranch: "a"}),
		EnvRefreshEvery: 1,
		EnvCollector: func(string) *utils.EnvInfo {
			collects++
			return &utils.EnvInfo{GitBranch: "b"}
		},
		FileTracker: env.FileTracker,
		Env:         env,
	}, nil)

	// Switch to remote, then change the watched file externally.
	env.SetRemote(fakeRemoteExecutor{}, "/remote/pwd")
	if err := os.WriteFile(watched, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(watched, future, future); err != nil {
		t.Fatal(err)
	}

	text := runReminder(t, rm)
	if strings.Contains(text, watched) || strings.Contains(text, "git_branch") || strings.Contains(text, "AGENTS.md") {
		t.Fatalf("local sweeps ran while env was remote: %q", text)
	}
	if collects != 0 {
		t.Fatalf("env collector ran %d times while remote, want 0", collects)
	}

	// Back to local: the sweeps resume and the pending change is reported.
	env.ResetToLocal(pwd, "test-os")
	text = runReminder(t, rm)
	if !strings.Contains(text, watched) {
		t.Fatalf("external change not reported after returning to local: %q", text)
	}
	if collects == 0 {
		t.Fatalf("env collector did not resume after returning to local")
	}
}
