// Package llm provides provider-agnostic LLM clients (OpenAI, Anthropic, Gemini)
// with tool/function calling. API keys are read ONLY from environment variables.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Roles used in Message.Role.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one turn of conversation history.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"toolCallId,omitempty"` // for Role=tool: the call this result answers
	ToolName   string     `json:"toolName,omitempty"`   // for Role=tool: needed by Gemini
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`  // for Role=assistant
}

// Tool describes a callable function; Parameters is a JSON schema object.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is a model request to invoke a tool.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Usage holds token counts.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Response is the model output.
type Response struct {
	Text       string     `json:"text"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	StopReason string     `json:"stopReason"`
	Usage      Usage      `json:"usage"`
}

// LLMClient is implemented by every provider.
type LLMClient interface {
	SendMessage(ctx context.Context, systemPrompt string, messages []Message, tools []Tool) (Response, error)
}

// Config selects provider and generation settings. The API key is intentionally
// NOT part of Config; it is read from the provider's env var.
type Config struct {
	Provider string
	Model    string
	// FallbackModel (same provider) is tried after retries on Model fail; "" = none.
	FallbackModel string
	// AttemptTimeout bounds each single provider call (LLM_ATTEMPT_TIMEOUT, default 12s);
	// every retry gets a fresh one, within the caller's overall context.
	AttemptTimeout time.Duration
	Temperature    float64
	MaxTokens      int
}

// Providers, their key env vars and per-provider model env vars.
// There is deliberately NO default provider or model: both come from env.
var (
	keyEnv = map[string]string{
		"openai":    "OPENAI_API_KEY",
		"anthropic": "ANTHROPIC_API_KEY",
		"gemini":    "GEMINI_API_KEY",
	}
	modelEnv = map[string]string{
		"openai":    "OPENAI_MODEL",
		"anthropic": "ANTHROPIC_MODEL",
		"gemini":    "GEMINI_MODEL",
	}
)

// ProviderNone disables the LLM (deterministic keyword routing only).
const ProviderNone = "none"

// ConfigFromEnv reads LLM_PROVIDER (required: openai|anthropic|gemini|none),
// the model from LLM_MODEL or the provider's own var (OPENAI_MODEL /
// ANTHROPIC_MODEL / GEMINI_MODEL; required unless provider=none),
// LLM_TEMPERATURE (default 0.2) and LLM_MAX_TOKENS (default 2048).
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Provider:       strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER"))),
		Model:          strings.TrimSpace(os.Getenv("LLM_MODEL")),
		FallbackModel:  strings.TrimSpace(os.Getenv("LLM_FALLBACK_MODEL")),
		AttemptTimeout: 12 * time.Second,
		Temperature:    0.2,
		MaxTokens:      2048,
	}
	if cfg.Provider == "" {
		return cfg, fmt.Errorf("llm: LLM_PROVIDER is required (openai|anthropic|gemini|none)")
	}
	if cfg.Provider != ProviderNone {
		if _, ok := keyEnv[cfg.Provider]; !ok {
			return cfg, fmt.Errorf("llm: unknown LLM_PROVIDER %q (want openai|anthropic|gemini|none)", cfg.Provider)
		}
		if cfg.Model == "" {
			cfg.Model = strings.TrimSpace(os.Getenv(modelEnv[cfg.Provider]))
		}
		if cfg.Model == "" {
			return cfg, fmt.Errorf("llm: LLM_MODEL (or %s) is required for provider %s", modelEnv[cfg.Provider], cfg.Provider)
		}
	}
	if v := os.Getenv("LLM_ATTEMPT_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return cfg, fmt.Errorf("llm: invalid LLM_ATTEMPT_TIMEOUT %q (e.g. 12s)", v)
		}
		cfg.AttemptTimeout = d
	}
	if v := os.Getenv("LLM_TEMPERATURE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return cfg, fmt.Errorf("llm: invalid LLM_TEMPERATURE %q: %w", v, err)
		}
		cfg.Temperature = f
	}
	if v := os.Getenv("LLM_MAX_TOKENS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("llm: invalid LLM_MAX_TOKENS %q", v)
		}
		cfg.MaxTokens = n
	}
	return cfg, nil
}

// ValidateEnv checks the provider is known and its API key env var is set.
func ValidateEnv(provider string) error {
	_, err := apiKey(provider)
	return err
}

func apiKey(provider string) (string, error) {
	env, ok := keyEnv[provider]
	if !ok {
		return "", fmt.Errorf("llm: unknown provider %q (want openai|anthropic|gemini)", provider)
	}
	k := strings.TrimSpace(os.Getenv(env))
	if k == "" {
		return "", fmt.Errorf("llm: %s is not set (provider=%s)", env, provider)
	}
	return k, nil
}

// New returns the client for cfg.Provider.
func New(cfg Config) (LLMClient, error) {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	key, err := apiKey(cfg.Provider)
	if err != nil {
		return nil, err
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm: model is required for provider %s (LLM_MODEL or %s)", cfg.Provider, modelEnv[cfg.Provider])
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 2048
	}
	primary, err := newProvider(cfg, key)
	if err != nil {
		return nil, err
	}
	rc := &retryClient{primary: primary, model: cfg.Model, attempt: cfg.AttemptTimeout, provider: cfg.Provider}
	if cfg.FallbackModel != "" && cfg.FallbackModel != cfg.Model {
		fcfg := cfg
		fcfg.Model = cfg.FallbackModel
		if rc.fallback, err = newProvider(fcfg, key); err != nil {
			return nil, err
		}
		rc.fallbackModel = cfg.FallbackModel
	}
	return rc, nil
}

func newProvider(cfg Config, key string) (LLMClient, error) {
	switch cfg.Provider {
	case "openai":
		return newOpenAI(cfg, key), nil
	case "anthropic":
		return newAnthropic(cfg, key), nil
	case "gemini":
		return newGemini(cfg, key)
	}
	return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
}

func rawArgs(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}
