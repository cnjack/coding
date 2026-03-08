package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// TodoStatus represents the state of a todo item.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	TodoCancelled  TodoStatus = "cancelled"
)

// TodoItem represents a single todo entry.
type TodoItem struct {
	ID     int        `json:"id"`
	Title  string     `json:"title"`
	Status TodoStatus `json:"status"`
}

// TodoStore is a concurrency-safe in-memory store for todo items.
type TodoStore struct {
	mu    sync.RWMutex
	items []TodoItem
}

// NewTodoStore creates an empty TodoStore.
func NewTodoStore() *TodoStore {
	return &TodoStore{}
}

// Update replaces the entire todo list (full-replacement semantics).
func (s *TodoStore) Update(items []TodoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make([]TodoItem, len(items))
	copy(s.items, items)
}

// Items returns a snapshot copy of the current todo items.
func (s *TodoStore) Items() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TodoItem, len(s.items))
	copy(out, s.items)
	return out
}

// HasItems returns true if there are any todo items.
func (s *TodoStore) HasItems() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items) > 0
}

// HasIncomplete returns true if any items are not completed/cancelled.
func (s *TodoStore) HasIncomplete() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.items {
		if item.Status != TodoCompleted && item.Status != TodoCancelled {
			return true
		}
	}
	return false
}

// Summary returns a human-readable summary string.
func (s *TodoStore) Summary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var pending, inProgress, completed, cancelled int
	for _, item := range s.items {
		switch item.Status {
		case TodoPending:
			pending++
		case TodoInProgress:
			inProgress++
		case TodoCompleted:
			completed++
		case TodoCancelled:
			cancelled++
		}
	}
	total := len(s.items)
	return fmt.Sprintf("%d todos (%d completed, %d in_progress, %d pending, %d cancelled)",
		total, completed, inProgress, pending, cancelled)
}

// IncompleteSummary returns a message listing the incomplete items, for use as an agent reminder.
func (s *TodoStore) IncompleteSummary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var lines []string
	for _, item := range s.items {
		if item.Status != TodoCompleted && item.Status != TodoCancelled {
			lines = append(lines, fmt.Sprintf("  - [%s] #%d: %s", item.Status, item.ID, item.Title))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	msg := "You still have incomplete todos:\n"
	for _, l := range lines {
		msg += l + "\n"
	}
	msg += "Please complete or cancel all remaining todos before finishing."
	return msg
}

// --- todowrite tool ---

type todoWriteInput struct {
	Todos []TodoItem `json:"todos"`
}

func (e *Env) NewTodoWriteTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "todowrite",
		Desc: `Use this tool to create and manage a structured task list for your current coding session. This helps you track progress, organize complex tasks, and demonstrate thoroughness to the user.

## When to Use This Tool
Use this tool proactively in these scenarios:
1. Complex multistep tasks - When a task requires 3 or more distinct steps or actions
2. Non-trivial and complex tasks - Tasks that require careful planning or multiple operations
3. User explicitly requests todo list - When the user directly asks you to use the todo list
4. User provides multiple tasks - When users provide a list of things to be done (numbered or comma-separated)
5. After receiving new instructions - Immediately capture user requirements as todos. Feel free to edit the todo list based on new information.
6. After completing a task - Mark it complete and add any new follow-up tasks
7. When you start working on a new task, mark the todo as in_progress. Ideally you should only have one todo as in_progress at a time. Complete existing tasks before starting new ones.

## When NOT to Use This Tool
1. There is only a single, straightforward task
2. The task is trivial and tracking it provides no organizational benefit
3. The task can be completed in less than 3 trivial steps
4. The task is purely conversational or informational

## Task States
- pending: Task not yet started
- in_progress: Currently working on (limit to ONE task at a time)
- completed: Task finished successfully
- cancelled: Task no longer needed

## Task Management Rules
- Send the FULL list of all todos each time (not just changed ones)
- Update task status in real-time as you work
- Mark tasks complete IMMEDIATELY after finishing (don't batch completions)
- Only have ONE task in_progress at any time
- Complete current tasks before starting new ones
- Cancel tasks that become irrelevant
- Create specific, actionable items
- Break complex tasks into smaller, manageable steps`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"todos": {
				Type:     schema.Array,
				Desc:     `The complete todo list. Each item: {"id": <int>, "title": "<desc>", "status": "pending|in_progress|completed|cancelled"}. Always send the FULL list.`,
				Required: true,
			},
		}),
	}
	return &todoWriteTool{env: e, info: info}
}

type todoWriteTool struct {
	env  *Env
	info *schema.ToolInfo
}

func (t *todoWriteTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *todoWriteTool) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var input todoWriteInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse todowrite input: %w", err)
	}

	// Validate
	seen := make(map[int]bool)
	inProgressCount := 0
	for _, item := range input.Todos {
		if item.Title == "" {
			return "", fmt.Errorf("todo item #%d has an empty title", item.ID)
		}
		if seen[item.ID] {
			return "", fmt.Errorf("duplicate todo id: %d", item.ID)
		}
		seen[item.ID] = true
		switch item.Status {
		case TodoPending, TodoInProgress, TodoCompleted, TodoCancelled:
		default:
			return "", fmt.Errorf("invalid status %q for todo #%d, must be pending/in_progress/completed/cancelled", item.Status, item.ID)
		}
		if item.Status == TodoInProgress {
			inProgressCount++
		}
	}
	if inProgressCount > 1 {
		return "", fmt.Errorf("at most 1 todo can be in_progress at a time, found %d", inProgressCount)
	}

	t.env.TodoStore.Update(input.Todos)

	result, _ := json.Marshal(input.Todos)
	return fmt.Sprintf("%s\n%s", t.env.TodoStore.Summary(), string(result)), nil
}

// --- todoread tool ---

func (e *Env) NewTodoReadTool() tool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "todoread",
		Desc: `Use this tool to read the current to-do list for the session. Use proactively and frequently:
- At the beginning of conversations to see what's pending
- Before starting new tasks to prioritize work
- When uncertain about what to do next
- After completing tasks to update your understanding of remaining work
- After every few messages to ensure you're on track

This tool takes no parameters.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
	return &todoReadTool{env: e, info: info}
}

type todoReadTool struct {
	env  *Env
	info *schema.ToolInfo
}

func (t *todoReadTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *todoReadTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	items := t.env.TodoStore.Items()
	if len(items) == 0 {
		return "No todos yet.", nil
	}
	result, _ := json.Marshal(items)
	return fmt.Sprintf("%s\n%s", t.env.TodoStore.Summary(), string(result)), nil
}
