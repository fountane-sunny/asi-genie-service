package llm

import (
	"asi-genie-poc/internal/logx"
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

const retryAttempts = 3

var retryBase = time.Second

type retryClient struct {
	primary, fallback    LLMClient
	model, fallbackModel string
	attempt              time.Duration
	provider             string
}

func (c *retryClient) try(ctx context.Context, cl LLMClient, model string, attempt int, systemPrompt string, messages []Message, tools []Tool) (Response, error) {
	actx, cancel := ctx, context.CancelFunc(func() {})
	if c.attempt > 0 {
		actx, cancel = context.WithTimeout(ctx, c.attempt)
	}
	defer cancel()
	start := time.Now()
	resp, err := cl.SendMessage(actx, systemPrompt, messages, tools)
	logCall(c.provider, model, attempt, systemPrompt, messages, tools, resp, err, time.Since(start))
	return resp, err
}

func logCall(provider, model string, attempt int, system string, messages []Message, tools []Tool, resp Response, err error, d time.Duration) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	st := "OK"
	if err != nil {
		st = "ERROR"
	}
	fields := []logx.Field{
		logx.F("provider", provider),
		logx.F("model", model),
		logx.F("attempt", fmt.Sprintf("%d/%d", attempt, retryAttempts)),
		logx.F("tools", logx.Trunc(strings.Join(names, ", "), 300)),
	}
	if err != nil {
		fields = append(fields, logx.F("error", err))
	}
	fields = append(fields,
		logx.B("system prompt", logx.Trunc(system, 800)),
		logx.B("messages", messages),
	)
	if err == nil {
		if resp.Text != "" {
			fields = append(fields, logx.B("response text", resp.Text))
		}
		for _, tc := range resp.ToolCalls {
			f := logx.B("tool call "+tc.Name, tc.Arguments)
			f.Force = true // intent and slots stay visible when bodies are off
			fields = append(fields, f)
		}
		fields = append(fields, logx.F("tokens", fmt.Sprintf("in=%d out=%d", resp.Usage.InputTokens, resp.Usage.OutputTokens)))
	}
	logx.Block("LLM", provider+" "+model, st, d, err == nil, fields...)
}

func (c *retryClient) SendMessage(ctx context.Context, systemPrompt string, messages []Message, tools []Tool) (Response, error) {
	resp, err := withRetry(ctx, c.model, func(n int) (Response, error) {
		return c.try(ctx, c.primary, c.model, n, systemPrompt, messages, tools)
	})
	if err == nil || c.fallback == nil || !retryable(err) || ctx.Err() != nil {
		return resp, err
	}
	log.Printf("llm: %s failed after %d attempts (%v); trying fallback model %s", c.model, retryAttempts, err, c.fallbackModel)
	fresp, ferr := withRetry(ctx, c.fallbackModel, func(n int) (Response, error) {
		return c.try(ctx, c.fallback, c.fallbackModel, n, systemPrompt, messages, tools)
	})
	if ferr != nil {
		return fresp, fmt.Errorf("%w (fallback %s: %v)", err, c.fallbackModel, ferr)
	}
	return fresp, nil
}

func withRetry(ctx context.Context, model string, call func(attempt int) (Response, error)) (Response, error) {
	var resp Response
	var err error
	for i := 0; i < retryAttempts; i++ {
		resp, err = call(i + 1)
		if err == nil || !retryable(err) || i == retryAttempts-1 || ctx.Err() != nil {
			return resp, err
		}
		wait := retryBase << i
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= wait {
			return resp, err
		}
		log.Printf("llm: %s transient error (attempt %d/%d), retrying in %s: %v", model, i+1, retryAttempts, wait, err)
		select {
		case <-ctx.Done():
			return resp, err
		case <-time.After(wait):
		}
	}
	return resp, err
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	for _, s := range []string{"429", "500", "502", "503", "504", "529", "unavailable", "resource_exhausted", "overloaded", "rate limit", "high demand", "deadline_exceeded", "deadline exceeded", "timeout"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
