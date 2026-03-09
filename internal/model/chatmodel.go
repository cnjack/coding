package model

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	openai "github.com/sashabaranov/go-openai"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TokenUsage tracks token consumption across all API calls
type TokenUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// TokenTracker is a global token usage tracker
var TokenTracker = &TokenUsage{}

// Add adds token usage to the tracker
func (t *TokenUsage) Add(prompt, completion, total int) {
	atomic.AddInt64(&t.PromptTokens, int64(prompt))
	atomic.AddInt64(&t.CompletionTokens, int64(completion))
	atomic.AddInt64(&t.TotalTokens, int64(total))
}

// Get returns the current token usage
func (t *TokenUsage) Get() (prompt, completion, total int64) {
	return atomic.LoadInt64(&t.PromptTokens),
		atomic.LoadInt64(&t.CompletionTokens),
		atomic.LoadInt64(&t.TotalTokens)
}

// Reset resets the token tracker
func (t *TokenUsage) Reset() {
	atomic.StoreInt64(&t.PromptTokens, 0)
	atomic.StoreInt64(&t.CompletionTokens, 0)
	atomic.StoreInt64(&t.TotalTokens, 0)
}

// ModelInfo contains information about a model
type ModelInfo struct {
	ID           string
	ContextLimit int // Maximum context window size, 0 if unknown
}

type ChatModelConfig struct {
	Model   string
	APIKey  string
	BaseURL string
}

type chatModel struct {
	client *openai.Client
	model  string
	tools  []openai.Tool
}

func NewChatModel(_ context.Context, cfg *ChatModelConfig) (einomodel.ToolCallingChatModel, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("APIKey is required")
	}
	config := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		config.BaseURL = cfg.BaseURL
	}
	return &chatModel{
		client: openai.NewClientWithConfig(config),
		model:  cfg.Model,
	}, nil
}

func (m *chatModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	oaiTools := make([]openai.Tool, 0, len(tools))
	for _, ti := range tools {
		if ti == nil {
			continue
		}
		params, err := ti.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return nil, fmt.Errorf("failed to convert params for tool %s: %w", ti.Name, err)
		}
		oaiTools = append(oaiTools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        ti.Name,
				Description: ti.Desc,
				Parameters:  params,
			},
		})
	}
	return &chatModel{client: m.client, model: m.model, tools: oaiTools}, nil
}

func (m *chatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	req := m.buildRequest(input, false)
	resp, err := m.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	// Track token usage
	if resp.Usage.TotalTokens > 0 {
		TokenTracker.Add(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty response from model")
	}
	return toEinoMessage(resp.Choices[0].Message), nil
}

func (m *chatModel) Stream(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	req := m.buildRequest(input, true)
	// Enable stream options to get usage information
	req.StreamOptions = &openai.StreamOptions{
		IncludeUsage: true,
	}
	stream, err := m.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return nil, err
	}

	sr, sw := schema.Pipe[*schema.Message](16)
	go func() {
		defer sw.Close()
		defer stream.Close()
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				sw.Send(nil, err)
				break
			}
			// Track token usage from stream response
			if resp.Usage != nil && resp.Usage.TotalTokens > 0 {
				TokenTracker.Add(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
			}
			if len(resp.Choices) == 0 {
				continue
			}
			delta := resp.Choices[0].Delta
			msg := &schema.Message{
				Role:    schema.Assistant,
				Content: delta.Content,
			}
			if len(delta.ToolCalls) > 0 {
				msg.ToolCalls = toEinoToolCalls(delta.ToolCalls)
			}
			sw.Send(msg, nil)
		}
	}()

	return sr, nil
}

func (m *chatModel) buildRequest(input []*schema.Message, stream bool) openai.ChatCompletionRequest {
	msgs := make([]openai.ChatCompletionMessage, 0, len(input))
	for _, msg := range input {
		msgs = append(msgs, toOpenAIMessage(msg))
	}
	req := openai.ChatCompletionRequest{
		Model:    m.model,
		Messages: msgs,
		Stream:   stream,
	}
	if len(m.tools) > 0 {
		req.Tools = m.tools
	}
	return req
}

func toOpenAIMessage(msg *schema.Message) openai.ChatCompletionMessage {
	m := openai.ChatCompletionMessage{
		Role:       string(msg.Role),
		Content:    msg.Content,
		Name:       msg.Name,
		ToolCallID: msg.ToolCallID,
	}
	if len(msg.ToolCalls) > 0 {
		m.ToolCalls = toOpenAIToolCalls(msg.ToolCalls)
	}
	return m
}

func toEinoMessage(msg openai.ChatCompletionMessage) *schema.Message {
	m := &schema.Message{
		Role:       schema.RoleType(msg.Role),
		Content:    msg.Content,
		Name:       msg.Name,
		ToolCallID: msg.ToolCallID,
	}
	if len(msg.ToolCalls) > 0 {
		m.ToolCalls = toEinoToolCalls(msg.ToolCalls)
	}
	return m
}

func toOpenAIToolCalls(tcs []schema.ToolCall) []openai.ToolCall {
	ret := make([]openai.ToolCall, len(tcs))
	for i, tc := range tcs {
		ret[i] = openai.ToolCall{
			Index: tc.Index,
			ID:    tc.ID,
			Type:  openai.ToolTypeFunction,
			Function: openai.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return ret
}

func toEinoToolCalls(tcs []openai.ToolCall) []schema.ToolCall {
	ret := make([]schema.ToolCall, len(tcs))
	for i, tc := range tcs {
		ret[i] = schema.ToolCall{
			Index: tc.Index,
			ID:    tc.ID,
			Type:  string(tc.Type),
			Function: schema.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return ret
}

// knownModelContextLimits maps model names to their context window sizes.
// This is used as a fallback when the /models API doesn't return this info.
var knownModelContextLimits = map[string]int{
	// OpenAI models
	"gpt-4o":                 128000,
	"gpt-4o-mini":            128000,
	"gpt-4-turbo":            128000,
	"gpt-4-turbo-preview":    128000,
	"gpt-4-0125-preview":     128000,
	"gpt-4-1106-preview":     128000,
	"gpt-4":                  8192,
	"gpt-4-32k":              32768,
	"gpt-3.5-turbo":          16385,
	"gpt-3.5-turbo-16k":      16385,
	"o1":                     200000,
	"o1-preview":             128000,
	"o1-mini":                128000,
	// Claude models (for Anthropic-compatible APIs)
	"claude-3-5-sonnet-latest": 200000,
	"claude-3-5-sonnet-20241022": 200000,
	"claude-3-5-sonnet-20240620": 200000,
	"claude-3-5-haiku-latest":   200000,
	"claude-3-5-haiku-20241022": 200000,
	"claude-3-opus-20240229":    200000,
	"claude-3-sonnet-20240229":  200000,
	"claude-3-haiku-20240307":   200000,
	"claude-sonnet-4-20250514":  200000,
	"claude-opus-4-20250514":    200000,
	// DeepSeek models
	"deepseek-chat":      64000,
	"deepseek-coder":     16000,
	"deepseek-reasoner":  64000,
	// Other common models
	"llama-3.1-405b":  128000,
	"llama-3.1-70b":   128000,
	"llama-3.1-8b":    128000,
	"llama-3-70b":     8192,
	"llama-3-8b":      8192,
	"mixtral-8x7b":    32768,
	"mixtral-8x22b":   65536,
	"mistral-large":   128000,
	"gemini-1.5-pro":  1000000,
	"gemini-1.5-flash": 1000000,
}

// GetModelInfo retrieves model information. It first tries the /models API,
// then falls back to known model limits.
func (m *chatModel) GetModelInfo(ctx context.Context) ModelInfo {
	info := ModelInfo{ID: m.model}

	// Try to get model info from API (may not work for all providers)
	model, err := m.client.GetModel(ctx, m.model)
	if err == nil {
		info.ID = model.ID
	}

	// Look up known context limit
	if limit, ok := knownModelContextLimits[m.model]; ok {
		info.ContextLimit = limit
	} else {
		// Try partial match for model name patterns
		for pattern, limit := range knownModelContextLimits {
			if containsModelPattern(m.model, pattern) {
				info.ContextLimit = limit
				break
			}
		}
	}

	return info
}

// containsModelPattern checks if the model name matches a pattern (partial match)
func containsModelPattern(model, pattern string) bool {
	// Simple prefix/suffix matching
	return len(model) >= len(pattern) &&
		(model == pattern ||
			(len(model) > len(pattern) && (model[:len(pattern)] == pattern || model[len(model)-len(pattern):] == pattern)))
}

// GetTokenUsage returns the current token usage statistics
func GetTokenUsage() (prompt, completion, total int64) {
	return TokenTracker.Get()
}

// ResetTokenUsage resets the token usage tracker
func ResetTokenUsage() {
	TokenTracker.Reset()
}

// GetModelContextLimit returns the known context limit for a given model name.
// Returns 0 if the model is not in the known list.
func GetModelContextLimit(modelName string) int {
	if limit, ok := knownModelContextLimits[modelName]; ok {
		return limit
	}
	// Try partial match
	for pattern, limit := range knownModelContextLimits {
		if containsModelPattern(modelName, pattern) {
			return limit
		}
	}
	return 0
}
