package runner

import (
	"context"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/cnjack/coding/internal/config"
	internalmodel "github.com/cnjack/coding/internal/model"
	"github.com/cnjack/coding/internal/session"
	"github.com/cnjack/coding/internal/telemetry"
	"github.com/cnjack/coding/internal/tools"
	"github.com/cnjack/coding/internal/tui"
)

// Run executes the agent for a single turn, wrapping the response with a
// Langfuse trace when a tracer is present, enforcing todo-completion guards,
// and sending token-usage updates to the TUI when done.
func Run(
	ctx context.Context,
	ag *adk.ChatModelAgent,
	messages []adk.Message,
	p *tea.Program,
	rec *session.Recorder,
	todoStore *tools.TodoStore,
	tracer *telemetry.LangfuseTracer,
) string {
	if tracer != nil {
		ctx = tracer.WithNewTrace(ctx, "coding_agent")
	}
	resp := runInner(ctx, ag, messages, p, rec, todoStore)

	// Completion guard: if the agent finished but there are still incomplete
	// todos, re-run with a reminder so nothing is left behind.
	const maxGuardRetries = 3
	for i := 0; i < maxGuardRetries; i++ {
		if todoStore == nil || !todoStore.HasIncomplete() {
			break
		}
		reminder := todoStore.IncompleteSummary()
		p.Send(tui.AgentTextMsg{Text: "\n⚠️ Incomplete todos detected, continuing...\n"})
		messages = append(messages, &schema.Message{Role: schema.Assistant, Content: resp})
		messages = append(messages, schema.UserMessage(reminder))
		extra := runInner(ctx, ag, messages, p, rec, todoStore)
		resp += extra
	}

	// Send token usage update before signalling done.
	promptTokens, completionTokens, totalTokens := internalmodel.GetTokenUsage()
	p.Send(tui.TokenUpdateMsg{
		PromptTokens:      promptTokens,
		CompletionTokens:  completionTokens,
		TotalTokens:       totalTokens,
		ModelContextLimit: modelContextLimit(),
	})

	p.Send(tui.AgentDoneMsg{})
	return resp
}

func runInner(
	ctx context.Context,
	ag *adk.ChatModelAgent,
	messages []adk.Message,
	p *tea.Program,
	rec *session.Recorder,
	todoStore *tools.TodoStore,
) string {
	input := &adk.AgentInput{
		Messages:        messages,
		EnableStreaming: true,
	}

	var assistantText strings.Builder

	iterator := ag.Run(ctx, input)
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			p.Send(tui.AgentDoneMsg{Err: event.Err})
			return assistantText.String()
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mo := event.Output.MessageOutput

		if mo.Role == schema.Tool {
			toolName := mo.ToolName
			if !mo.IsStreaming && mo.Message != nil {
				output := mo.Message.Content
				p.Send(tui.ToolResultMsg{Name: toolName, Output: output})
				if toolName == "todowrite" || toolName == "todoread" {
					p.Send(tui.TodoUpdateMsg{})
				}
				if rec != nil {
					rec.RecordToolResult(toolName, output, nil)
				}
			} else if mo.IsStreaming {
				var sb strings.Builder
				var toolErr error
				for {
					chunk, err := mo.MessageStream.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						toolErr = err
						p.Send(tui.ToolResultMsg{Name: toolName, Err: err})
						break
					}
					if chunk != nil {
						sb.WriteString(chunk.Content)
					}
				}
				if toolErr == nil {
					p.Send(tui.ToolResultMsg{Name: toolName, Output: sb.String()})
					if toolName == "todowrite" || toolName == "todoread" {
						p.Send(tui.TodoUpdateMsg{})
					}
					if rec != nil {
						rec.RecordToolResult(toolName, sb.String(), nil)
					}
				} else if rec != nil {
					rec.RecordToolResult(toolName, "", toolErr)
				}
			}
			continue
		}

		if mo.Role != schema.Assistant {
			continue
		}

		if mo.IsStreaming {
			for {
				chunk, err := mo.MessageStream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					break
				}
				if chunk == nil {
					continue
				}
				if len(chunk.ToolCalls) > 0 {
					for _, tc := range chunk.ToolCalls {
						if tc.Function.Name != "" {
							p.Send(tui.ToolCallMsg{Name: tc.Function.Name, Args: tc.Function.Arguments})
							if rec != nil {
								rec.RecordToolCall(tc.Function.Name, tc.Function.Arguments)
							}
						}
					}
				}
				if chunk.Content != "" {
					assistantText.WriteString(chunk.Content)
					p.Send(tui.AgentTextMsg{Text: chunk.Content})
				}
			}
		} else if mo.Message != nil {
			if len(mo.Message.ToolCalls) > 0 {
				for _, tc := range mo.Message.ToolCalls {
					p.Send(tui.ToolCallMsg{Name: tc.Function.Name, Args: tc.Function.Arguments})
					if rec != nil {
						rec.RecordToolCall(tc.Function.Name, tc.Function.Arguments)
					}
				}
			}
			if mo.Message.Content != "" {
				assistantText.WriteString(mo.Message.Content)
				p.Send(tui.AgentTextMsg{Text: mo.Message.Content})
			}
		}
	}

	if rec != nil && assistantText.Len() > 0 {
		rec.RecordAssistant(assistantText.String())
	}

	return assistantText.String()
}

func modelContextLimit() int {
	cfg, err := config.LoadConfig()
	if err != nil {
		return 0
	}
	return internalmodel.GetModelContextLimit(cfg.Model)
}
