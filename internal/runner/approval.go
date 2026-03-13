package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cnjack/coding/internal/tui"
)

// ApprovalState manages whether tool calls require interactive user approval.
type ApprovalState struct {
	p               *tea.Program
	approvedSession bool
}

// SetProgram stores the TUI program used to send approval-request messages.
func (s *ApprovalState) SetProgram(p *tea.Program) {
	s.p = p
}

// SetSessionApproval marks all remaining tool calls in this session as
// pre-approved (or revokes that approval).
func (s *ApprovalState) SetSessionApproval(enabled bool) {
	s.approvedSession = enabled
}

// RequestApproval is the agent.ApprovalFunc implementation.
// It returns true immediately for read-only or obviously safe commands.
// For everything else it sends a TUI prompt and waits for the user's answer.
func (s *ApprovalState) RequestApproval(ctx context.Context, toolName, toolArgs string) (bool, error) {
	if s.approvedSession {
		return true, nil
	}

	noApprovalNeeded := map[string]bool{
		"read":      true,
		"glob":      true,
		"grep":      true,
		"todowrite": true,
		"todoread":  true,
		"question":  true,
		"webfetch":  true,
	}
	if noApprovalNeeded[toolName] {
		return true, nil
	}

	if toolName == "execute" {
		var input struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(toolArgs), &input); err == nil {
			cmd := strings.TrimSpace(input.Command)
			safePrefix := []string{"ls", "pwd", "env", "ls ", "cat ", "pwd ", "echo ", "which ", "git status", "git log"}
			for _, p := range safePrefix {
				if cmd == p || strings.HasPrefix(cmd, p) {
					return true, nil
				}
			}
		}
	}

	if s.p == nil {
		return false, fmt.Errorf("TUI program not initialized")
	}

	respCh := make(chan tui.ToolApprovalResponse, 1)
	s.p.Send(tui.ToolApprovalRequestMsg{
		Name: toolName,
		Args: toolArgs,
		Resp: respCh,
	})

	select {
	case resp := <-respCh:
		if resp.Approved {
			s.approvedSession = true
		}
		return resp.Approved, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
