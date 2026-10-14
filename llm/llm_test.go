package llm

import (
	"strings"
	"testing"
)

func TestMissingKey(t *testing.T) {
	for p, env := range keyEnv {
		t.Setenv(env, "")
		err := ValidateEnv(p)
		if err == nil || !strings.Contains(err.Error(), env+" is not set (provider="+p+")") {
			t.Fatalf("%s: unexpected err %v", p, err)
		}
		if _, err := New(Config{Provider: p}); err == nil {
			t.Fatalf("%s: New should fail without key", p)
		}
	}
}

func TestUnknownProvider(t *testing.T) {
	if _, err := New(Config{Provider: "foo"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestFactory(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("ANTHROPIC_API_KEY", "test")
	t.Setenv("GEMINI_API_KEY", "")
	if c, err := New(Config{Provider: "openai", Model: "test-model"}); err != nil || c == nil {
		t.Fatalf("openai: %v", err)
	}
	if c, err := New(Config{Provider: "Anthropic", Model: "claude-test"}); err != nil || c == nil {
		t.Fatalf("anthropic: %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "gemini")
	t.Setenv("LLM_MODEL", "m")
	t.Setenv("LLM_TEMPERATURE", "0.5")
	t.Setenv("LLM_MAX_TOKENS", "100")
	c, err := ConfigFromEnv()
	if err != nil || c.Provider != "gemini" || c.Model != "m" || c.Temperature != 0.5 || c.MaxTokens != 100 {
		t.Fatalf("got %+v %v", c, err)
	}
}
