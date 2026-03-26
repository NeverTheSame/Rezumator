package rezumator

import (
	"context"
	"os"
	"strings"
	"time"
)

// effectiveLLMCallTimeout is the maximum time for one LLM HTTP round-trip (OpenRouter client + Azure ctx).
// Default 30s. Large resume/cover outputs often need more time to stream; set LLM_CALL_TIMEOUT (e.g. 90s, 2m).
func effectiveLLMCallTimeout() time.Duration {
	d := strings.TrimSpace(os.Getenv("LLM_CALL_TIMEOUT"))
	if d == "" {
		return 30 * time.Second
	}
	parsed, err := time.ParseDuration(d)
	if err != nil || parsed < 5*time.Second {
		return 30 * time.Second
	}
	if parsed > 15*time.Minute {
		return 15 * time.Minute
	}
	return parsed
}

// llmClient is implemented by OpenRouterClient and AzureOpenAIClient.
type llmClient interface {
	GenerateContent(ctx context.Context, systemPrompt, userPrompt, input string) (string, error)
}
