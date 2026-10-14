package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/genai"
)

type geminiClient struct {
	cfg Config
	c   *genai.Client
}

func newGemini(cfg Config, key string) (*geminiClient, error) {
	c, err := genai.NewClient(context.Background(), &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, fmt.Errorf("llm/gemini: %w", err)
	}
	return &geminiClient{cfg: cfg, c: c}, nil
}

func (g *geminiClient) SendMessage(ctx context.Context, systemPrompt string, messages []Message, tools []Tool) (Response, error) {
	temp := float32(g.cfg.Temperature)
	gc := &genai.GenerateContentConfig{Temperature: &temp, MaxOutputTokens: int32(g.cfg.MaxTokens)}
	if systemPrompt != "" {
		gc.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: systemPrompt}}}
	}
	if len(tools) > 0 {
		decls := make([]*genai.FunctionDeclaration, 0, len(tools))
		for _, t := range tools {
			decls = append(decls, &genai.FunctionDeclaration{Name: t.Name, Description: t.Description, ParametersJsonSchema: t.Parameters})
		}
		gc.Tools = []*genai.Tool{{FunctionDeclarations: decls}}
	}
	var contents []*genai.Content
	for _, m := range messages {
		switch m.Role {
		case RoleTool:
			var resp map[string]any
			if json.Unmarshal([]byte(m.Content), &resp) != nil || resp == nil {
				resp = map[string]any{"output": m.Content}
			}
			contents = append(contents, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
				FunctionResponse: &genai.FunctionResponse{ID: m.ToolCallID, Name: m.ToolName, Response: resp},
			}}})
		case RoleAssistant:
			c := &genai.Content{Role: genai.RoleModel}
			if m.Content != "" {
				c.Parts = append(c.Parts, &genai.Part{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal(rawArgs(tc.Arguments), &args)
				c.Parts = append(c.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: tc.ID, Name: tc.Name, Args: args}})
			}
			contents = append(contents, c)
		default:
			contents = append(contents, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{Text: m.Content}}})
		}
	}
	resp, err := g.c.Models.GenerateContent(ctx, g.cfg.Model, contents, gc)
	if err != nil {
		return Response{}, fmt.Errorf("llm/gemini: %w", err)
	}
	out := Response{}
	if resp.UsageMetadata != nil {
		out.Usage = Usage{InputTokens: int(resp.UsageMetadata.PromptTokenCount), OutputTokens: int(resp.UsageMetadata.CandidatesTokenCount)}
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return out, nil
	}
	cand := resp.Candidates[0]
	out.StopReason = string(cand.FinishReason)
	for i, p := range cand.Content.Parts {
		if p.Text != "" {
			out.Text += p.Text
		}
		if p.FunctionCall != nil {
			b, _ := json.Marshal(p.FunctionCall.Args)
			id := p.FunctionCall.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: id, Name: p.FunctionCall.Name, Arguments: rawArgs(b)})
		}
	}
	return out, nil
}
