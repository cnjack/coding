package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcode/internal/automation"
	"github.com/cnjack/jcode/internal/browser"
	"github.com/cnjack/jcode/internal/computer"
	appconfig "github.com/cnjack/jcode/internal/config"
	"github.com/cnjack/jcode/internal/procutil"
	"golang.org/x/crypto/ssh"
)

// MaxSubagentDepth is the maximum allowed nesting depth for subagents.
const MaxSubagentDepth = 3

// Env holds the execution context (local or remote) and is shared by all tools.
type Env struct {
	Exec        Executor
	pwd         string
	platform    string
	TodoStore   *TodoStore
	GoalStore   *GoalStore
	FileTracker *FileTracker
	OnEnvChange func(envLabel string, isLocal bool, err error)
	Depth       int // subagent nesting depth, 0 for top-level

	// AutomationStore is the process-wide automation store shared with the web
	// server and its scheduler. The automation_create tool MUST write through it
	// (not a throwaway automation.NewStore()) so a created automation is visible
	// to the server's in-memory cache, its REST API, and the scheduler. nil falls
	// back to opening a fresh store (CLI/ACP contexts with no live server).
	AutomationStore *automation.Store
	// SessionIDFn returns the owning top-level conversation id for tools that
	// create session-bound resources. nil/empty falls back to isolated behavior.
	SessionIDFn func() string

	// Browser is the process-wide browser-use manager shared with the web server
	// (its extension bridge and /api/browser routes) so the agent's browser_*
	// tools and the settings UI operate the same Chrome. nil disables the tools.
	Browser *browser.Manager

	// browserSession is the lazily-opened per-task browser session (one per Env),
	// closed when the task ends. Guarded by browserMu.
	browserMu      sync.Mutex
	browserSession *browser.Session

	// Computer is the process-wide computer-use manager, shared with the web
	// server (/api/computer routes and the settings UI) so the agent's computer_*
	// tools and the settings page operate the same backend and app grants.
	// nil disables the tools. See internal-doc/computer-use-design.md.
	Computer *computer.Manager

	// computerSession is the lazily-opened per-task computer session (one per
	// Env). It holds the session app allowlist, so it must not be shared across
	// tasks: an app the user approved for one task is not approved for the next.
	computerMu      sync.Mutex
	computerSession *computer.Session

	// origExec and origPwd remember the initial executor state so that
	// ResetToLocal can restore the correct local executor after SSH.
	origExec Executor
	origPwd  string

	// DenyRead is the shared managed deny-read policy (see readpolicy.go).
	// Every Env — including clones for subagents and fresh Envs for teammates
	// — points at the SAME process-wide policy object, so denials apply
	// identically everywhere and a child agent can never hold a weaker rule
	// set (higher read permission) than its parent. Nil behaves as an empty
	// (allow-all) policy; NewEnv always sets it.
	DenyRead *DenyReadPolicy
}

// NewEnv creates a local Env.
//
// The FileTracker (no StorageManager: conflict tracking without backups)
// powers read-before-edit enforcement and external-change detection in the
// edit/write/read tools and the reminder middleware. CloneForSubagent shares
// it, so subagent reads count for the whole session.
func NewEnv(pwd, platform string) *Env {
	exec := NewLocalExecutor(platform)
	return &Env{
		Exec:        exec,
		pwd:         pwd,
		platform:    platform,
		TodoStore:   NewTodoStore(),
		GoalStore:   NewGoalStore(),
		FileTracker: NewFileTracker(nil),
		origExec:    exec,
		origPwd:     pwd,
		// Shared managed deny-read policy: same object for the whole process,
		// so runtime strengthen is visible to every Env immediately.
		DenyRead: ManagedDenyRead(),
	}
}

// SetRemote switches this Env to use a remote executor (SSH or Docker).
func (e *Env) SetRemote(executor RemoteExecutor, remotePwd string) {
	e.Exec = executor
	e.pwd = remotePwd
	e.platform = executor.Platform()
}

// SetSSH switches this Env to use a remote SSH executor. Thin wrapper kept for
// existing callers; SetRemote is the general form.
func (e *Env) SetSSH(executor *SSHExecutor, remotePwd string) {
	e.SetRemote(executor, remotePwd)
}

// CloseRemote closes the executor if it is remote (SSH/Docker), releasing the
// SSH connection or the Docker container hold (ref-count). No-op when local.
func (e *Env) CloseRemote() error {
	if re, ok := e.Exec.(RemoteExecutor); ok {
		return re.Close()
	}
	return nil
}

// ResetToLocal restores this Env to use the original local executor.
func (e *Env) ResetToLocal(pwd, platform string) {
	if e.origExec != nil {
		e.Exec = e.origExec
		e.pwd = e.origPwd
		e.platform = e.origExec.Platform()
	} else {
		e.Exec = NewLocalExecutor(platform)
		e.pwd = pwd
		e.platform = platform
	}
}

// Pwd returns the current working directory.
func (e *Env) Pwd() string { return e.pwd }

// ResolvePath resolves a file path relative to the working directory.
// Absolute paths are cleaned and returned as-is.
// Relative paths are joined with Pwd and cleaned.
// Logs a warning if the resolved relative path escapes the working directory.
func (e *Env) ResolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	cleaned := filepath.Clean(filepath.Join(e.pwd, path))
	pwd := filepath.Clean(e.pwd)
	if cleaned != pwd && !strings.HasPrefix(cleaned, pwd+string(filepath.Separator)) {
		appconfig.Logger().Printf("warning: resolved path %s escapes working directory %s", cleaned, pwd)
	}
	return cleaned
}

// CloneForSubagent creates a copy of this Env with the same executor and pwd
// but an isolated TodoStore, suitable for use by a subagent. GoalStore is
// intentionally left nil: the session goal belongs to the top-level agent and
// subagents are not registered with the goal tools.
func (e *Env) CloneForSubagent() *Env {
	return &Env{
		Exec:        e.Exec,
		pwd:         e.pwd,
		platform:    e.platform,
		TodoStore:   NewTodoStore(),
		FileTracker: e.FileTracker,
		Depth:       e.Depth + 1,
		Browser:     e.Browser,
		// Deny rules propagate unchanged: a subagent shares the parent's
		// policy object and must never inherit a higher read permission.
		DenyRead: e.DenyRead,
		// The Manager is shared, but the subagent's session (and therefore its
		// app allowlist) starts empty: a grant the user gave the parent for one
		// app is not a grant to every subagent it spawns.
		Computer: e.Computer,
	}
}

// BrowserSession returns this task's browser session, opening one on first use.
// It requires a configured, enabled Browser manager.
func (e *Env) BrowserSession(ctx context.Context) (*browser.Session, error) {
	if e.Browser == nil {
		return nil, fmt.Errorf("browser use is not available in this context")
	}
	e.browserMu.Lock()
	defer e.browserMu.Unlock()
	// Tool schemas are rebuilt after a settings change, but an older agent turn
	// may still hold a browser tool. Re-check the live manager before returning a
	// cached session so disabling the capability also revokes execution.
	if !e.Browser.Enabled() {
		return nil, fmt.Errorf("browser use is disabled (enable it in settings)")
	}
	if e.browserSession != nil {
		return e.browserSession, nil
	}
	sess, err := e.Browser.OpenSession(ctx)
	if err != nil {
		return nil, err
	}
	e.browserSession = sess
	return sess, nil
}

// CurrentBrowserOrigin returns the origin (scheme://host) of this task's active
// browser tab, or "" when no session is open yet. The approval layer uses it to
// scope per-site permissions for browser actions whose args carry no URL (e.g.
// clicks and fills), which otherwise could never match a site rule.
func (e *Env) CurrentBrowserOrigin() string {
	e.browserMu.Lock()
	sess := e.browserSession
	e.browserMu.Unlock()
	if sess == nil {
		return ""
	}
	return sess.CurrentOrigin()
}

// ComputerSession returns this task's computer session, opening one on first
// use. It requires a configured, enabled Computer manager.
func (e *Env) ComputerSession(ctx context.Context) (*computer.Session, error) {
	if e.Computer == nil {
		return nil, fmt.Errorf("computer use is not available in this context")
	}
	e.computerMu.Lock()
	defer e.computerMu.Unlock()
	if e.computerSession != nil {
		return e.computerSession, nil
	}
	sess, err := e.Computer.OpenSession(ctx)
	if err != nil {
		return nil, err
	}
	e.computerSession = sess
	return sess, nil
}

// CurrentComputerApp returns the bundle id of the frontmost app, or "" when no
// session is open. The approval layer uses it to scope per-app permissions for
// computer_act, whose args carry no app identity — a click is just a click. This
// is the exact counterpart of CurrentBrowserOrigin, and exists for the same
// reason.
func (e *Env) CurrentComputerApp() string {
	e.computerMu.Lock()
	sess := e.computerSession
	e.computerMu.Unlock()
	if sess == nil {
		return ""
	}
	// Bounded: an unanswered TCC prompt presents as a multi-minute hang, and the
	// approval path must not be the thing that wedges.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return sess.FrontmostBundle(ctx)
}

// CloseComputer closes this task's computer session if one was opened. The
// session allowlist dies with it, which is the point: grants are per-task.
func (e *Env) CloseComputer() {
	e.computerMu.Lock()
	sess := e.computerSession
	e.computerSession = nil
	e.computerMu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

// CloseBrowser closes this task's browser session if one was opened.
func (e *Env) CloseBrowser() {
	e.browserMu.Lock()
	sess := e.browserSession
	e.browserSession = nil
	e.browserMu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

// CanNest returns whether this Env can spawn further subagents.
func (e *Env) CanNest() bool {
	return e.Depth < MaxSubagentDepth
}

// IsRemote returns true if operating over a remote executor (SSH or Docker),
// i.e. anything that is not the local executor.
func (e *Env) IsRemote() bool {
	_, ok := e.Exec.(*LocalExecutor)
	return !ok
}

// RemoteExecutor is an Executor backed by a remote target (SSH host or Docker
// container) that owns a connection/hold needing release and can produce a
// stable, scheme-qualified session key.
type RemoteExecutor interface {
	Executor
	// Probe verifies that the bound transport/target is still usable. Every
	// implementation must honor ctx and apply its own short upper bound.
	Probe(ctx context.Context) error
	Close() error
	ProjectLabel(pwd string) string
}

// RemoteLeaseCloner is implemented by transports that can safely share their
// underlying connection while giving each Engine independent Close ownership.
// Callers must never share the same RemoteExecutor pointer across engines.
type RemoteLeaseCloner interface {
	CloneLease() (RemoteExecutor, error)
}

// Executor abstracts file and command operations so tools can work
// transparently on both local and remote (SSH) machines.
type Executor interface {
	// ReadFile returns the contents of the file at path.
	ReadFile(ctx context.Context, path string) ([]byte, error)

	// WriteFile writes data to the file at path, creating parent dirs as needed.
	WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error

	// MkdirAll creates a directory tree.
	MkdirAll(ctx context.Context, path string, perm os.FileMode) error

	// Stat returns basic info about a file (exists, is dir, etc.).
	Stat(ctx context.Context, path string) (*FileInfo, error)

	// Exec runs a command and returns stdout, stderr, and any error.
	Exec(ctx context.Context, command string, workDir string, timeout time.Duration) (stdout, stderr string, err error)

	// Platform returns the OS/arch string of the target machine.
	Platform() string

	// Label returns a human-readable description (e.g. "local" or "user@host:/path").
	Label() string
}

// FileInfo is a minimal stat result.
type FileInfo struct {
	Exists bool
	IsDir  bool
}

// ---------------------------------------------------------------------------
// LocalExecutor — runs everything on the local machine
// ---------------------------------------------------------------------------

type LocalExecutor struct {
	platform string
}

func NewLocalExecutor(platform string) *LocalExecutor {
	return &LocalExecutor{platform: platform}
}

func (l *LocalExecutor) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (l *LocalExecutor) WriteFile(_ context.Context, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return atomicWriteFile(path, data, perm)
}

// atomicWriteFile writes data via a sibling temp file, fsync, and rename so a
// crash or kill mid-write can never leave a truncated target file.
//
// Semantics:
//   - Symlinks are written through: the link's target is replaced, the link
//     itself is preserved.
//   - An existing target keeps its file mode (exec bits etc.); perm applies
//     only to newly created files.
//   - On any temp-file failure (e.g. read-only directory with a writable
//     file) it falls back to a plain in-place write, preserving the previous
//     non-atomic behavior rather than failing.
//
// Caveats (accepted, single-user CLI): rename changes the inode, so the file
// owner becomes the current process user and extra hard links are detached.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	target := path
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
			target = resolved
		}
	}
	if fi, err := os.Stat(target); err == nil {
		perm = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return os.WriteFile(target, data, perm)
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmpName, target)
	}
	if err != nil {
		_ = os.Remove(tmpName)
		return os.WriteFile(target, data, perm)
	}
	return nil
}

func (l *LocalExecutor) MkdirAll(_ context.Context, path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (l *LocalExecutor) Stat(_ context.Context, path string) (*FileInfo, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return &FileInfo{Exists: false}, nil
	}
	if err != nil {
		return nil, err
	}
	return &FileInfo{Exists: true, IsDir: info.IsDir()}, nil
}

func (l *LocalExecutor) Exec(ctx context.Context, command, workDir string, timeout time.Duration) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Prevent interactive programs (pagers, editors, prompts) from blocking
	// forever by closing stdin. This causes programs like `less` (invoked by
	// git diff, git log, etc.) to exit immediately instead of waiting for
	// keyboard input. Same approach as opencode's stdin:"ignore".
	cmd.Stdin = nil

	// Set environment variables to further disable interactive behaviors:
	// - GIT_TERMINAL_PROMPT=0: prevent git from prompting for credentials
	// - GIT_PAGER=cat: disable git's pager (less/more) entirely
	// - PAGER=cat: disable generic pager for other commands (man, etc.)
	// - GIT_EDITOR=true: prevent git from opening an editor (rebase -i, commit without -m)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"GIT_EDITOR=true",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the command in its own process group so a timeout/cancel tears down
	// the whole tree, not just bash — otherwise a grandchild (e.g. a `sleep`)
	// survives as an orphan and keeps the stdout pipe open. WaitDelay bounds
	// cmd.Wait when a deliberately backgrounded grandchild (`daemon &`) holds
	// the pipes after bash itself exited.
	procutil.SetupProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), fmt.Errorf("command timed out")
	}
	// ErrWaitDelay means the command itself exited successfully and only the
	// pipes were force-closed after WaitDelay (a backgrounded grandchild still
	// held them). `cmd &` is normal usage, not a failure — fold to success.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return stdout.String(), stderr.String(), err
}

func (l *LocalExecutor) Platform() string { return l.platform }
func (l *LocalExecutor) Label() string    { return "local" }

// ---------------------------------------------------------------------------
// SSHExecutor — runs everything on a remote machine over SSH
// ---------------------------------------------------------------------------

type SSHExecutor struct {
	transport   *sshTransport
	host        string
	user        string
	platform    string
	leaseID     uint64
	releaseOnce sync.Once
	releaseErr  error
}

// sshTransport owns one encrypted TCP transport and its keepalive loop. Every
// SSHExecutor is a lease; sessions/channels are safe to create concurrently on
// ssh.Client, while ref-counting prevents one Engine teardown from closing the
// connection underneath another Engine.
type sshTransport struct {
	client           *ssh.Client
	clientGeneration uint64
	user             string
	host             string
	dial             func(context.Context) (*ssh.Client, error)
	backoff          func(int) time.Duration
	lifetimeCtx      context.Context
	lifetimeCancel   context.CancelFunc
	keepaliveStop    chan struct{}

	mu               sync.Mutex
	refs             int
	nextLeaseID      uint64
	observers        map[uint64]RemoteConnectionStatusHandler
	closed           bool
	closeErr         error
	reconnecting     bool
	reconnectDone    chan struct{}
	reconnectErr     error
	reconnectCause   error
	reconnectCancel  context.CancelFunc
	reconnectWaiters int
}

const (
	sshDialTimeout             = 10 * time.Second
	sshProbeTimeout            = 5 * time.Second
	sshFailureProbeTimeout     = time.Second
	sshSessionOpenTimeout      = 5 * time.Second
	sshKeepaliveEvery          = 25 * time.Second
	sshReconnectAttemptTimeout = 5 * time.Second
	sshReconnectTotalTimeout   = 65 * time.Second
	sshReconnectMaxAttempts    = 8
	sshReconnectInitialBackoff = 250 * time.Millisecond
	sshReconnectMaxBackoff     = 4 * time.Second
	sshOperationMaxAttempts    = 3
	sshKeepaliveRetryEvery     = time.Minute
)

// NewSSHExecutor connects with JCode's strict ~/.jcode/known_hosts policy.
// It is retained for TUI/tool compatibility; web callers use
// NewSSHExecutorContext so request cancellation is propagated.
func NewSSHExecutor(addr, user string, authMethods []ssh.AuthMethod) (*SSHExecutor, error) {
	hostKeyCallback, err := NewSSHHostKeyCallback(SSHHostKeyPolicy{})
	if err != nil {
		return nil, err
	}
	return NewSSHExecutorContext(context.Background(), addr, user, authMethods, hostKeyCallback)
}

// NewSSHExecutorContext connects to a remote host using the supplied strict
// host-key callback. TCP dial and SSH handshake share a ten-second upper bound.
func NewSSHExecutorContext(
	ctx context.Context,
	addr, user string,
	authMethods []ssh.AuthMethod,
	hostKeyCallback ssh.HostKeyCallback,
) (*SSHExecutor, error) {
	if hostKeyCallback == nil {
		return nil, fmt.Errorf("SSH host-key callback is required")
	}
	normalizedAddr, err := normalizeSSHAddress(addr)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         sshDialTimeout,
	}

	client, err := dialSSHClient(ctx, normalizedAddr, user, config, sshDialTimeout)
	if err != nil {
		return nil, err
	}
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.Background())
	dial := func(dialCtx context.Context) (*ssh.Client, error) {
		return dialSSHClient(dialCtx, normalizedAddr, user, config, sshReconnectAttemptTimeout)
	}

	executor := &SSHExecutor{
		transport: &sshTransport{
			client:           client,
			clientGeneration: 1,
			user:             user,
			host:             normalizedAddr,
			dial:             dial,
			backoff:          sshReconnectBackoff,
			lifetimeCtx:      lifetimeCtx,
			lifetimeCancel:   lifetimeCancel,
			keepaliveStop:    make(chan struct{}),
			refs:             1,
			nextLeaseID:      1,
			observers:        make(map[uint64]RemoteConnectionStatusHandler),
		},
		host:     normalizedAddr,
		user:     user,
		platform: "linux/amd64",
		leaseID:  1,
	}

	// Detect remote platform
	platformCtx, platformCancel := context.WithTimeout(ctx, sshProbeTimeout)
	if out, _, platformErr := executor.runWithRetry(
		platformCtx, "uname -sm", "", sshProbeTimeout, true,
	); platformErr == nil {
		parts := strings.Fields(strings.TrimSpace(out))
		if len(parts) == 2 {
			os := strings.ToLower(parts[0])
			arch := strings.ToLower(parts[1])
			switch arch {
			case "x86_64":
				arch = "amd64"
			case "aarch64":
				arch = "arm64"
			}
			executor.platform = os + "/" + arch
		}
	}
	platformCancel()
	if !executor.transport.isOpen() {
		return nil, fmt.Errorf("SSH transport %s@%s became unavailable during initialization", user, normalizedAddr)
	}

	go executor.transport.keepaliveLoop()
	return executor, nil
}

func dialSSHClient(
	ctx context.Context,
	addr, user string,
	config *ssh.ClientConfig,
	timeout time.Duration,
) (*ssh.Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	appconfig.Logger().Printf("[ssh] dial tcp %s@%s", user, addr)
	start := time.Now()
	netConn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		appconfig.Logger().Printf("[ssh] dial failed after %v: %v", time.Since(start), err)
		return nil, fmt.Errorf("ssh dial %s@%s: %w", user, addr, err)
	}
	deadline, _ := dialCtx.Deadline()
	if err := netConn.SetDeadline(deadline); err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("ssh handshake deadline %s@%s: %w", user, addr, err)
	}
	clientConn, channels, requests, err := ssh.NewClientConn(netConn, addr, config)
	if err != nil {
		_ = netConn.Close()
		appconfig.Logger().Printf("[ssh] handshake failed after %v: %v", time.Since(start), err)
		return nil, fmt.Errorf("ssh handshake %s@%s: %w", user, addr, err)
	}
	if err := netConn.SetDeadline(time.Time{}); err != nil {
		_ = clientConn.Close()
		return nil, fmt.Errorf("ssh clear handshake deadline %s@%s: %w", user, addr, err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	appconfig.Logger().Printf("[ssh] dial success %s@%s in %v", user, addr, time.Since(start))
	return client, nil
}

func (s *SSHExecutor) Close() error {
	s.releaseOnce.Do(func() {
		if s.transport != nil {
			s.releaseErr = s.transport.release(s.leaseID)
		}
	})
	return s.releaseErr
}

// CloneLease gives another Engine independent ownership of the same healthy
// SSH transport. The clone opens its own SSH channels but Close only decrements
// the shared ref-count.
func (s *SSHExecutor) CloneLease() (RemoteExecutor, error) {
	if s.transport == nil {
		return nil, fmt.Errorf("SSH transport %s@%s is closed", s.user, s.host)
	}
	leaseID, ok := s.transport.retain()
	if !ok {
		return nil, fmt.Errorf("SSH transport %s@%s is closed", s.user, s.host)
	}
	return &SSHExecutor{
		transport: s.transport,
		host:      s.host,
		user:      s.user,
		platform:  s.platform,
		leaseID:   leaseID,
	}, nil
}

// SetRemoteConnectionStatusHandler binds an observer to this executor lease.
// Replacing or closing one Engine's lease cannot remove another Engine's
// observer even though both share the same SSH transport.
func (s *SSHExecutor) SetRemoteConnectionStatusHandler(handler RemoteConnectionStatusHandler) {
	if s.transport != nil {
		s.transport.setObserver(s.leaseID, handler)
	}
}

// Probe performs a bounded transport round trip. OpenSSH servers commonly
// reply false to this request; a reply of either polarity proves the encrypted
// transport is alive. A timeout closes the client to unblock the pending SSH
// request and make subsequent activation reconnect instead of reusing a zombie.
func (s *SSHExecutor) Probe(ctx context.Context) error {
	if s.transport == nil {
		return fmt.Errorf("ssh probe: missing transport")
	}
	return s.transport.probe(ctx)
}

func (s *SSHExecutor) ReadFile(ctx context.Context, path string) ([]byte, error) {
	out, serr, err := s.runWithRetry(ctx, fmt.Sprintf("cat %s", ShellQuote(path)), "", 30*time.Second, true)
	if err != nil {
		detail := strings.TrimSpace(serr)
		if detail != "" {
			return nil, fmt.Errorf("%s: %w", detail, err)
		}
		return nil, err
	}
	return []byte(out), nil
}

func (s *SSHExecutor) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	// Create parent dirs, then write via stdin
	mkdirCmd := fmt.Sprintf("mkdir -p %s", ShellQuote(filepath.Dir(path)))
	if _, _, err := s.runWithRetry(ctx, mkdirCmd, "", 10*time.Second, false); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}

	// Use cat with heredoc-style write. Encode data as base64 to avoid shell escaping issues.
	encoded := base64Encode(data)
	writeCmd := sshAtomicWriteCmd(encoded, path, perm)
	if _, serr, err := s.runWithRetry(ctx, writeCmd, "", 30*time.Second, false); err != nil {
		return fmt.Errorf("write failed: %s %w", serr, err)
	}
	return nil
}

// sshAtomicWriteCmd builds the remote shell command that decodes base64 data
// into a sibling temp file, sets its mode, then atomically renames it over
// the target — so a dropped connection mid-write never truncates the target.
func sshAtomicWriteCmd(encoded, path string, perm os.FileMode) string {
	tmp := path + ".jcode-tmp"
	return fmt.Sprintf("echo %s | base64 -d > %s && chmod %o %s && mv -f %s %s",
		ShellQuote(encoded), ShellQuote(tmp), perm, ShellQuote(tmp), ShellQuote(tmp), ShellQuote(path))
}

func (s *SSHExecutor) MkdirAll(ctx context.Context, path string, _ os.FileMode) error {
	_, serr, err := s.runWithRetry(
		ctx, fmt.Sprintf("mkdir -p %s", ShellQuote(path)), "", 10*time.Second, false,
	)
	if err != nil {
		return fmt.Errorf("mkdir -p failed: %s %w", serr, err)
	}
	return nil
}

func (s *SSHExecutor) Stat(ctx context.Context, path string) (*FileInfo, error) {
	// Use test command for existence and type checks
	out, _, err := s.runWithRetry(ctx, fmt.Sprintf(
		`if [ -e %s ]; then if [ -d %s ]; then echo "dir"; else echo "file"; fi; else echo "none"; fi`,
		ShellQuote(path), ShellQuote(path),
	), "", 5*time.Second, true)
	if err != nil {
		return nil, err
	}
	result := strings.TrimSpace(out)
	switch result {
	case "dir":
		return &FileInfo{Exists: true, IsDir: true}, nil
	case "file":
		return &FileInfo{Exists: true, IsDir: false}, nil
	default:
		return &FileInfo{Exists: false}, nil
	}
}

func (s *SSHExecutor) Exec(ctx context.Context, command, workDir string, timeout time.Duration) (string, string, error) {
	return s.exec(ctx, command, workDir, timeout, false)
}

// ExecReadOnly explicitly allows replay after a post-dispatch transport loss.
// Only built-in read/grep/glob/discovery callers use this method; arbitrary
// execute tool input always goes through Exec and is never replayed.
func (s *SSHExecutor) ExecReadOnly(
	ctx context.Context,
	command, workDir string,
	timeout time.Duration,
) (string, string, error) {
	return s.exec(ctx, command, workDir, timeout, true)
}

func (s *SSHExecutor) exec(
	ctx context.Context,
	command, workDir string,
	timeout time.Duration,
	readOnly bool,
) (string, string, error) {
	// Prepend environment variables to disable pagers/editors/prompts on remote.
	envPrefix := "export GIT_TERMINAL_PROMPT=0 GIT_PAGER=cat PAGER=cat GIT_EDITOR=true; "
	fullCmd := envPrefix + command
	if workDir != "" {
		fullCmd = fmt.Sprintf("cd %s && %s", ShellQuote(workDir), envPrefix+command)
	}
	return s.runWithRetry(ctx, fullCmd, "", timeout, readOnly)
}

func (s *SSHExecutor) Platform() string { return s.platform }

// User returns the SSH username.
func (s *SSHExecutor) User() string { return s.user }

// Host returns the dialed host (includes the port, e.g. "1.2.3.4:22").
func (s *SSHExecutor) Host() string { return s.host }

func (s *SSHExecutor) Label() string {
	return fmt.Sprintf("%s@%s", s.user, s.host)
}

// ProjectLabel returns a stable, host-qualified session key of the form
// ssh://user@host:port/remote/path.
func (s *SSHExecutor) ProjectLabel(pwd string) string {
	return fmt.Sprintf("ssh://%s@%s%s", s.user, s.host, normalizeAbs(pwd))
}

// isSSHConnDead reports whether err means the current SSH client generation is
// unusable (EOF or a closed network connection), as opposed to a per-command
// failure. The lease-owning transport may replace that generation by redialing.
func isSSHConnDead(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "use of closed network connection") ||
		strings.Contains(lower, "connection reset by peer") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "connection lost") ||
		strings.Contains(lower, "ssh: disconnect")
}

// runWithRetry separates transport repair from operation replay. Channel-open
// failures are known to be before dispatch and are safe to retry for every
// operation. Once session.Run has been called, only explicitly read-only
// operations may be replayed; all others return a Fatal outcome-unknown error.
func (s *SSHExecutor) runWithRetry(
	ctx context.Context,
	command, _ string,
	timeout time.Duration,
	readOnly bool,
) (string, string, error) {
	var lastTransportErr *RemoteTransportError
	for attempt := 1; attempt <= sshOperationMaxAttempts; attempt++ {
		client, generation, err := s.transport.connectedClient(ctx, lastTransportErr)
		if err != nil {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			code, retryable := classifySSHReconnectError(err)
			return "", "", Fatal(&RemoteTransportError{
				Kind: "ssh", Code: code, Phase: RemoteTransportBeforeDispatch,
				Retryable: retryable, Err: err, ReconnectErr: err,
			})
		}

		stdout, stderr, runErr := s.runOnce(ctx, client, generation, command, timeout)
		if runErr == nil {
			return stdout, stderr, nil
		}
		if ctx.Err() != nil {
			return stdout, stderr, ctx.Err()
		}
		var transportErr *RemoteTransportError
		if !errors.As(runErr, &transportErr) {
			return stdout, stderr, runErr
		}
		lastTransportErr = transportErr
		s.transport.invalidateClient(client, generation)
		if ctx.Err() != nil {
			return stdout, stderr, ctx.Err()
		}
		reconnectErr := s.transport.ensureConnected(ctx, transportErr)
		if ctx.Err() != nil {
			return stdout, stderr, ctx.Err()
		}
		if reconnectErr != nil {
			code, retryable := classifySSHReconnectError(reconnectErr)
			transportErr.Code = code
			transportErr.Retryable = retryable
			transportErr.ReconnectErr = reconnectErr
			return stdout, stderr, Fatal(transportErr)
		}
		if transportErr.Phase == RemoteTransportOutcomeUnknown && !readOnly {
			return stdout, stderr, Fatal(transportErr)
		}
		if attempt == sshOperationMaxAttempts {
			transportErr.Err = fmt.Errorf(
				"SSH operation transport retry limit (%d) reached: %w",
				sshOperationMaxAttempts, transportErr.Err,
			)
			return stdout, stderr, Fatal(transportErr)
		}
		appconfig.Logger().Printf(
			"[ssh] replaying %s operation on %s@%s after transport recovery (attempt %d/%d)",
			map[bool]string{true: "read-only", false: "before-dispatch"}[readOnly],
			s.user, s.host, attempt+1, sshOperationMaxAttempts,
		)
	}
	return "", "", Fatal(lastTransportErr)
}

func (s *SSHExecutor) runOnce(
	ctx context.Context,
	client *ssh.Client,
	generation uint64,
	command string,
	timeout time.Duration,
) (string, string, error) {
	openCtx, openCancel := context.WithTimeout(ctx, sshSessionOpenTimeout)
	defer openCancel()
	type sessionResult struct {
		session *ssh.Session
		err     error
	}
	sessionReady := make(chan sessionResult, 1)
	go func() {
		session, err := client.NewSession()
		sessionReady <- sessionResult{session: session, err: err}
	}()

	var session *ssh.Session
	select {
	case result := <-sessionReady:
		if result.err != nil {
			if isSSHConnDead(result.err) {
				return "", "", newSSHTransportError(RemoteTransportBeforeDispatch, result.err)
			}
			var openErr *ssh.OpenChannelError
			if !errors.As(result.err, &openErr) {
				if probeErr := probeSSHClient(openCtx, client, sshFailureProbeTimeout); probeErr != nil {
					return "", "", newSSHTransportError(
						RemoteTransportBeforeDispatch,
						fmt.Errorf("%v; SSH transport probe failed: %w", result.err, probeErr),
					)
				}
			}
			return "", "", fmt.Errorf("ssh session: %w", result.err)
		}
		session = result.session
	case <-openCtx.Done():
		// Opening an SSH channel has no cancellation API. Closing the transport
		// is the only way to unblock the goroutine and avoids leaking it forever.
		s.transport.invalidateClient(client, generation)
		return "", "", newSSHTransportError(RemoteTransportBeforeDispatch, openCtx.Err())
	}
	if session == nil {
		return "", "", Fatal(fmt.Errorf("ssh session: empty session"))
	}
	openCancel()
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	commandCtx, commandCancel := context.WithTimeout(ctx, timeout)
	defer commandCancel()
	select {
	case err := <-done:
		if isSSHConnDead(err) {
			return stdout.String(), stderr.String(), newSSHTransportError(RemoteTransportOutcomeUnknown, err)
		}
		if err != nil {
			var exitErr *ssh.ExitError
			if !errors.As(err, &exitErr) {
				if probeErr := probeSSHClient(commandCtx, client, sshFailureProbeTimeout); probeErr != nil {
					return stdout.String(), stderr.String(), newSSHTransportError(
						RemoteTransportOutcomeUnknown,
						fmt.Errorf("%v; SSH transport probe failed: %w", err, probeErr),
					)
				}
			}
		}
		return stdout.String(), stderr.String(), err
	case <-commandCtx.Done():
		terminateDone := make(chan struct{})
		go func() {
			terminateSSHCommand(session, done)
			close(terminateDone)
		}()
		select {
		case <-terminateDone:
		case <-time.After(sshFailureProbeTimeout):
			_ = session.Close()
		}
		probeCtx, probeCancel := context.WithTimeout(context.Background(), sshFailureProbeTimeout)
		probeErr := probeSSHClient(probeCtx, client, sshFailureProbeTimeout)
		probeCancel()
		if probeErr != nil {
			return stdout.String(), stderr.String(), newSSHTransportError(
				RemoteTransportOutcomeUnknown,
				fmt.Errorf("command timed out; SSH transport probe failed: %w", probeErr),
			)
		}
		return stdout.String(), stderr.String(), fmt.Errorf("command timed out after %v", timeout)
	}
}

func newSSHTransportError(phase RemoteTransportPhase, err error) *RemoteTransportError {
	return &RemoteTransportError{
		Kind:      "ssh",
		Code:      "ssh_connection_failed",
		Phase:     phase,
		Retryable: true,
		Err:       err,
	}
}

// terminateSSHCommand asks the remote command to exit: SIGTERM, a short grace
// period, then SIGKILL. Best-effort only — without a PTY some sshd
// implementations ignore signal requests entirely, so remote orphans are still
// possible (a PTY/setsid-based teardown is a future enhancement). The caller's
// deferred session.Close still releases the channel either way.
func terminateSSHCommand(session *ssh.Session, done <-chan error) {
	_ = session.Signal(ssh.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
	}
	_ = session.Signal(ssh.SIGKILL)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func normalizeSSHAddress(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("SSH address is required")
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if host == "" || port == "" {
			return "", fmt.Errorf("invalid SSH address %q", addr)
		}
		return net.JoinHostPort(host, port), nil
	}
	if ip := net.ParseIP(strings.Trim(addr, "[]")); ip != nil {
		return net.JoinHostPort(ip.String(), "22"), nil
	}
	if strings.Contains(addr, ":") {
		return "", fmt.Errorf("invalid SSH address %q", addr)
	}
	return net.JoinHostPort(addr, "22"), nil
}

func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func base64Encode(data []byte) string {
	var buf bytes.Buffer
	encoder := base64.NewEncoder(base64.StdEncoding, &buf)
	_, _ = encoder.Write(data)
	_ = encoder.Close()
	return buf.String()
}
