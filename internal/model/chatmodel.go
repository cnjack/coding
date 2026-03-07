package model

import (
	"context"
	"fmt"
	"io"

	openai "github.com/sashabaranov/go-openai"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

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
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty response from model")
	}
	return toEinoMessage(resp.Choices[0].Message), nil
}

func (m *chatModel) Stream(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	req := m.buildRequest(input, true)
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
