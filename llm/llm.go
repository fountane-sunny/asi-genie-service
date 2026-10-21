// API keys are read only from environment variables.
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

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	ToolName   string     `json:"toolName,omitempty"` // Gemini tool results require the name
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

type Response struct {
	Text       string     `json:"text"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	StopReason string     `json:"stopReason"`
	Usage      Usage      `json:"usage"`
}

type LLMClient interface {
	SendMessage(ctx context.Context, systemPrompt string, messages []Message, tools []Tool) (Response, error)
}

// The API key is not stored here; it is read from the provider env var.
type Config struct {
	Provider string
	Model    string
	// Empty means no fallback. Tried only after retries on Model fail.
	FallbackModel string
	// Each retry gets a fresh deadline inside the caller's context.
	AttemptTimeout time.Duration
	Temperature    float64
	MaxTokens      int
}

// No default provider or model; both come from the environment.
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

const ProviderNone = "none"

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
