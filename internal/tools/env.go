package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Env holds the execution context (local or remote) and is shared by all tools.
type Env struct {
	Exec      Executor
	pwd       string
	platform  string
	TodoStore *TodoStore
}

// NewEnv creates a local Env.
func NewEnv(pwd, platform string) *Env {
	return &Env{
		Exec:      NewLocalExecutor(platform),
		pwd:       pwd,
		platform:  platform,
		TodoStore: NewTodoStore(),
	}
}

// SetSSH switches this Env to use a remote SSH executor.
func (e *Env) SetSSH(executor *SSHExecutor, remotePwd string) {
	e.Exec = executor
	e.pwd = remotePwd
	e.platform = executor.Platform()
}

// ResetToLocal restores this Env to use the local executor.
func (e *Env) ResetToLocal(pwd, platform string) {
	e.Exec = NewLocalExecutor(platform)
	e.pwd = pwd
	e.platform = platform
}

// Pwd returns the current working directory.
func (e *Env) Pwd() string { return e.pwd }

// IsRemote returns true if operating over SSH.
func (e *Env) IsRemote() bool {
	_, ok := e.Exec.(*SSHExecutor)
	return ok
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
	return os.WriteFile(path, data, perm)
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

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), fmt.Errorf("command timed out")
	}
	return stdout.String(), stderr.String(), err
}

func (l *LocalExecutor) Platform() string { return l.platform }
func (l *LocalExecutor) Label() string    { return "local" }

// ---------------------------------------------------------------------------
// SSHExecutor — runs everything on a remote machine over SSH
// ---------------------------------------------------------------------------

type SSHExecutor struct {
	client   *ssh.Client
	host     string
	user     string
	platform string
}

// NewSSHExecutor connects to a remote host and returns an executor.
// It tries the SSH agent first, then common key paths.
func NewSSHExecutor(addr, user string, authMethods []ssh.AuthMethod) (*SSHExecutor, error) {
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	// Ensure addr includes port
	if !strings.Contains(addr, ":") {
		addr = addr + ":22"
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s@%s: %w", user, addr, err)
	}

	// Detect remote platform
	platform := "linux/amd64"
	if out, _, err := sshExecSimple(client, "uname -sm"); err == nil {
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
			platform = os + "/" + arch
		}
	}

	return &SSHExecutor{
		client:   client,
		host:     addr,
		user:     user,
		platform: platform,
	}, nil
}

func (s *SSHExecutor) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

func (s *SSHExecutor) ReadFile(ctx context.Context, path string) ([]byte, error) {
	out, serr, err := s.run(ctx, fmt.Sprintf("cat %s", ShellQuote(path)), "", 30*time.Second)
	if err != nil {
		detail := strings.TrimSpace(serr)
		if detail != "" {
			return nil, fmt.Errorf("%s", detail)
		}
		return nil, err
	}
	return []byte(out), nil
}

func (s *SSHExecutor) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	// Create parent dirs, then write via stdin
	mkdirCmd := fmt.Sprintf("mkdir -p %s", ShellQuote(filepath.Dir(path)))
	if _, _, err := s.run(ctx, mkdirCmd, "", 10*time.Second); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}

	// Use cat with heredoc-style write. Encode data as base64 to avoid shell escaping issues.
	encoded := base64Encode(data)
	writeCmd := fmt.Sprintf("echo %s | base64 -d > %s && chmod %o %s",
		ShellQuote(encoded), ShellQuote(path), perm, ShellQuote(path))
	if _, serr, err := s.run(ctx, writeCmd, "", 30*time.Second); err != nil {
		return fmt.Errorf("write failed: %s %w", serr, err)
	}
	return nil
}

func (s *SSHExecutor) MkdirAll(ctx context.Context, path string, _ os.FileMode) error {
	_, serr, err := s.run(ctx, fmt.Sprintf("mkdir -p %s", ShellQuote(path)), "", 10*time.Second)
	if err != nil {
		return fmt.Errorf("mkdir -p failed: %s %w", serr, err)
	}
	return nil
}

func (s *SSHExecutor) Stat(ctx context.Context, path string) (*FileInfo, error) {
	// Use test command for existence and type checks
	out, _, err := s.run(ctx, fmt.Sprintf(
		`if [ -e %s ]; then if [ -d %s ]; then echo "dir"; else echo "file"; fi; else echo "none"; fi`,
		ShellQuote(path), ShellQuote(path),
	), "", 5*time.Second)
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
	fullCmd := command
	if workDir != "" {
		fullCmd = fmt.Sprintf("cd %s && %s", ShellQuote(workDir), command)
	}
	return s.run(ctx, fullCmd, "", timeout)
}

func (s *SSHExecutor) Platform() string { return s.platform }

func (s *SSHExecutor) Label() string {
	return fmt.Sprintf("%s@%s", s.user, s.host)
}

// run executes a command over SSH.
func (s *SSHExecutor) run(_ context.Context, command, _ string, timeout time.Duration) (string, string, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Run with timeout via goroutine
	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case err := <-done:
		return stdout.String(), stderr.String(), err
	case <-time.After(timeout):
		session.Signal(ssh.SIGTERM)
		return stdout.String(), stderr.String(), fmt.Errorf("command timed out after %v", timeout)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func sshExecSimple(client *ssh.Client, command string) (string, string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", "", err
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	err = session.Run(command)
	return stdout.String(), stderr.String(), err
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
