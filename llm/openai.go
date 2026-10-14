package llm

import (
	"context"
	"fmt"

	openai "github.com/sashabaranov/go-openai"
)

type openAIClient struct {
	cfg Config
	c   *openai.Client
}

func newOpenAI(cfg Config, key string) *openAIClient {
	return &openAIClient{cfg: cfg, c: openai.NewClient(key)}
}

func (o *openAIClient) SendMessage(ctx context.Context, systemPrompt string, messages []Message, tools []Tool) (Response, error) {
	msgs := make([]openai.ChatCompletionMessage, 0, len(messages)+1)
	if systemPrompt != "" {
		msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: systemPrompt})
	}
	for _, m := range messages {
		cm := openai.ChatCompletionMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, openai.ToolCall{
				ID: tc.ID, Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: tc.Name, Arguments: string(rawArgs(tc.Arguments))},
			})
		}
		msgs = append(msgs, cm)
	}
	req := openai.ChatCompletionRequest{
		Model:       o.cfg.Model,
		Messages:    msgs,
		Temperature: float32(o.cfg.Temperature),
		MaxTokens:   o.cfg.MaxTokens,
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		}})
	}
	resp, err := o.c.CreateChatCompletion(ctx, req)
	if err != nil {
		return Response{}, fmt.Errorf("llm/openai: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Response{}, fmt.Errorf("llm/openai: empty response")
	}
	ch := resp.Choices[0]
	out := Response{
		Text: ch.Message.Content, StopReason: string(ch.FinishReason),
		Usage: Usage{InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens},
	}
	for _, tc := range ch.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: rawArgs([]byte(tc.Function.Arguments))})
	}
	return out, nil
}
