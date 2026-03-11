package tui

import (
	"github.com/cnjack/coding/internal/config"
)

const maxToolOutputLen = 500

var promptCh = make(chan string, 1)

var pendingPromptCh = make(chan string, 16)

var sshCh = make(chan interface{}, 1)

func GetPromptChannel() <-chan string {
	return promptCh
}

func GetPendingPromptChannel() <-chan string {
	return pendingPromptCh
}

func GetSSHChannel() <-chan interface{} {
	return sshCh
}

var configCh = make(chan *config.Config, 1)

// GetConfigChannel returns the channel that receives configuration changes.
func GetConfigChannel() <-chan *config.Config {
	return configCh
}

// addModelCh is used to notify main goroutine to launch add-model setup wizard.
var addModelCh = make(chan struct{}, 1)

// GetAddModelChannel returns the channel that receives add-model requests.
func GetAddModelChannel() <-chan struct{} {
	return addModelCh
}

// resumeCh is used to pass a selected session UUID from TUI to the main goroutine.
var resumeCh = make(chan string, 1)

// GetResumeChannel returns the channel that receives session resume requests.
func GetResumeChannel() <-chan string {
	return resumeCh
}

// approvalCh is used to pass tool approval requests from main goroutine to TUI.
var approvalCh = make(chan ToolApprovalRequestMsg, 1)

// GetApprovalChannel returns the channel that receives tool approval requests.
func GetApprovalChannel() chan ToolApprovalRequestMsg {
	return approvalCh
}

// autoApproveCh is used to notify main goroutine when auto-approve state changes.
var autoApproveCh = make(chan bool, 1)

// GetAutoApproveChannel returns the channel that receives auto-approve mode changes.
func GetAutoApproveChannel() <-chan bool {
	return autoApproveCh
}

// --- Messages ---

type AgentTextMsg struct{ Text string }
type ToolCallMsg struct{ Name, Args string }
type ToolResultMsg struct {
	Name, Output string
	Err          error
}
type AgentDoneMsg struct{ Err error }
type PromptSubmitMsg struct{ Prompt string }
type UserPromptMsg struct{ Prompt string }

// TodoUpdateMsg signals that the todo store has been updated.
type TodoUpdateMsg struct{}

// AddModelMsg signals that the user wants to add a new model via setup wizard
type AddModelMsg struct{}

// ResumeRequestMsg is sent when the user requests to resume a session by UUID.
type ResumeRequestMsg struct{ UUID string }

// SessionEntry is a display-ready record from a replayed session.
type SessionEntry struct {
	Type    string
	Content string
	Name    string
	Args    string
	Output  string
	Error   string
}

// SessionResumedMsg is sent by the main goroutine to replay a session in the TUI.
type SessionResumedMsg struct {
	UUID    string
	Entries []SessionEntry
}

// AgentsMdMsg is sent by the main goroutine to notify TUI that agents.md was loaded.
type AgentsMdMsg struct {
	Found bool
	Path  string
}

// TokenUpdateMsg is sent periodically to update token usage display
type TokenUpdateMsg struct {
	PromptTokens      int64
	CompletionTokens  int64
	TotalTokens       int64
	ModelContextLimit int // 0 if unknown
}

// ToolApprovalRequestMsg is sent when a tool needs user approval
type ToolApprovalRequestMsg struct {
	Name string
	Args string
	Resp chan ToolApprovalResponse
}

// ToolApprovalResponse is the user's response to a tool approval request
type ToolApprovalResponse struct {
	Approved bool
}

// SSHConnectMsg is sent when user initially requests connection
type SSHConnectMsg struct {
	Addr string // user@host
	Path string // remote working dir (optional)
}

// SSHListDirReqMsg is sent when TUI needs to list a directory on the remote machine
type SSHListDirReqMsg struct {
	Path string
}

// SSHDirResultsMsg is sent from main to TUI with directory contents
type SSHDirResultsMsg struct {
	Path  string
	Items []string
	Err   error
}

// SSHStatusMsg carries the result of an SSH connection attempt.
type SSHStatusMsg struct {
	Success bool
	Label   string // e.g. "root@myserver:22"
	Err     error
}

// SSHCancelMsg is sent when user cancels the SSH dir picker via Esc.
type SSHCancelMsg struct{}

// ConfigUpdatedMsg is sent when the provider/model configuration is updated via setup wizard
type ConfigUpdatedMsg struct {
	Provider string
	Model    string
	Message  string
}

type MCPStatusItem struct {
	Name      string
	ToolCount int
	Running   bool
	ErrMsg    string
}

type MCPStatusMsg struct {
	Statuses []MCPStatusItem
}
