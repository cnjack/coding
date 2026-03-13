package telemetry

import (
	"context"
	"sync"
	"time"

	langfuseacl "github.com/cloudwego/eino-ext/libs/acl/langfuse"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/cnjack/coding/internal/config"
)

type contextKey string

const traceIDKey contextKey = "langfuse_trace_id"

// LangfuseTracer wraps the Langfuse client and provides eino integration helpers.
type LangfuseTracer struct {
	client langfuseacl.Langfuse
}

// NewLangfuseTracer creates a LangfuseTracer from the config.
// Returns nil if required credentials are missing.
func NewLangfuseTracer(cfg *config.LangfuseConfig) *LangfuseTracer {
	if cfg == nil || cfg.SecretKey == "" || cfg.PublicKey == "" {
		config.Logger().Println("[langfuse] tracer disabled: missing credentials")
		return nil
	}
	host := cfg.Host
	if host == "" {
		host = "https://cloud.langfuse.com"
	}
	config.Logger().Printf("[langfuse] tracer initialized: host=%s publicKey=%s\n", host, cfg.PublicKey)
	return &LangfuseTracer{
		client: langfuseacl.NewLangfuse(host, cfg.PublicKey, cfg.SecretKey),
	}
}

// WithNewTrace creates a new Langfuse trace and returns a context carrying its ID.
func (t *LangfuseTracer) WithNewTrace(ctx context.Context, name string) context.Context {
	traceID, err := t.client.CreateTrace(&langfuseacl.TraceEventBody{
		BaseEventBody: langfuseacl.BaseEventBody{Name: name},
		TimeStamp:     time.Now(),
	})
	if err != nil {
		config.Logger().Printf("[langfuse] CreateTrace error: %v\n", err)
		return ctx
	}
	config.Logger().Printf("[langfuse] trace created: id=%s name=%s\n", traceID, name)
	return context.WithValue(ctx, traceIDKey, traceID)
}

// Flush ensures all buffered events are sent to Langfuse.
func (t *LangfuseTracer) Flush() {
	t.client.Flush()
}

// AgentMiddleware returns an adk.AgentMiddleware that records model generations
// and tool-call spans to Langfuse, keyed by the traceID stored in the context.
func (t *LangfuseTracer) AgentMiddleware() adk.AgentMiddleware {
	var mu sync.Mutex
	var pendingGenID string

	return adk.AgentMiddleware{
		BeforeChatModel: func(ctx context.Context, state *adk.ChatModelAgentState) error {
			traceID, _ := ctx.Value(traceIDKey).(string)
			if traceID == "" {
				return nil
			}
			genID, _ := t.client.CreateGeneration(&langfuseacl.GenerationEventBody{
				BaseObservationEventBody: langfuseacl.BaseObservationEventBody{
					BaseEventBody: langfuseacl.BaseEventBody{Name: "chat_model"},
					TraceID:       traceID,
					StartTime:     time.Now(),
				},
			})
			mu.Lock()
			pendingGenID = genID
			mu.Unlock()
			return nil
		},

		AfterChatModel: func(ctx context.Context, state *adk.ChatModelAgentState) error {
			mu.Lock()
			genID := pendingGenID
			pendingGenID = ""
			mu.Unlock()
			if genID == "" {
				return nil
			}
			// Find the last assistant message to record as output.
			var outMsg *schema.Message
			for i := len(state.Messages) - 1; i >= 0; i-- {
				if state.Messages[i].Role == schema.Assistant {
					outMsg = state.Messages[i]
					break
				}
			}
			_ = t.client.EndGeneration(&langfuseacl.GenerationEventBody{
				BaseObservationEventBody: langfuseacl.BaseObservationEventBody{
					BaseEventBody: langfuseacl.BaseEventBody{ID: genID},
				},
				OutMessage: outMsg,
				EndTime:    time.Now(),
			})
			return nil
		},

		WrapToolCall: compose.ToolMiddleware{
			Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
				return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
					traceID, _ := ctx.Value(traceIDKey).(string)
					start := time.Now()
					var spanID string
					if traceID != "" {
						spanID, _ = t.client.CreateSpan(&langfuseacl.SpanEventBody{
							BaseObservationEventBody: langfuseacl.BaseObservationEventBody{
								BaseEventBody: langfuseacl.BaseEventBody{Name: input.Name},
								TraceID:       traceID,
								Input:         input.Arguments,
								StartTime:     start,
							},
						})
					}
					out, err := next(ctx, input)
					if spanID != "" {
						output := ""
						if out != nil {
							output = out.Result
						}
						_ = t.client.EndSpan(&langfuseacl.SpanEventBody{
							BaseObservationEventBody: langfuseacl.BaseObservationEventBody{
								BaseEventBody: langfuseacl.BaseEventBody{ID: spanID},
								Output:        output,
							},
							EndTime: time.Now(),
						})
					}
					return out, err
				}
			},
		},
	}
}
