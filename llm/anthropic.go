package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type anthropicClient struct {
	cfg Config
	c   anthropic.Client
}

func newAnthropic(cfg Config, key string) *anthropicClient {
	return &anthropicClient{cfg: cfg, c: anthropic.NewClient(option.WithAPIKey(key))}
}

func (a *anthropicClient) SendMessage(ctx context.Context, systemPrompt string, messages []Message, tools []Tool) (Response, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.cfg.Model),
		MaxTokens: int64(a.cfg.MaxTokens),
	}
	// Newer models such as claude-haiku-5-5 reject temperature.
	if !isTemperatureDeprecated(a.cfg.Model) {
		params.Temperature = anthropic.Float(a.cfg.Temperature)
	}
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{{Text: systemPrompt}}
	}
	for _, m := range messages {
		switch m.Role {
		case RoleTool:
			params.Messages = append(params.Messages, anthropic.NewUserMessage(anthropic.NewToolResultBlock(m.ToolCallID, m.Content, false)))
		case RoleAssistant:
			var blocks []anthropic.ContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				var in any
				_ = json.Unmarshal(rawArgs(tc.Arguments), &in)
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, in, tc.Name))
			}
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(blocks...))
		default:
			params.Messages = append(params.Messages, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		}
	}
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{Properties: t.Parameters["properties"]}
		if req, ok := t.Parameters["required"]; ok {
			schema.ExtraFields = map[string]any{"required": req}
		}
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schema,
		}})
	}
	msg, err := a.c.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("llm/anthropic: %w", err)
	}
	out := Response{
		StopReason: string(msg.StopReason),
		Usage:      Usage{InputTokens: int(msg.Usage.InputTokens), OutputTokens: int(msg.Usage.OutputTokens)},
	}
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			out.Text += b.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: rawArgs(b.Input)})
		}
	}
	return out, nil
}

func isTemperatureDeprecated(model string) bool {
	deprecatedModels := map[string]bool{
		"claude-haiku-5-5": true,
	}
	return deprecatedModels[model]
}
