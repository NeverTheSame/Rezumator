package rezumator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	defaultOpenRouterModel   = "openrouter/cypher-alpha"
)

// OpenRouterClient calls OpenRouter's OpenAI-compatible Chat Completions API.
type OpenRouterClient struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewOpenRouterClient() (*OpenRouterClient, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		return nil, fmt.Errorf("missing required environment variable: OPENROUTER_API_KEY")
	}

	baseURL := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
	if baseURL == "" {
		baseURL = defaultOpenRouterBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	model := strings.TrimSpace(os.Getenv("OPENROUTER_MODEL"))
	if model == "" {
		model = defaultOpenRouterModel
	}

	return &OpenRouterClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		httpClient: &http.Client{
			Timeout: openRouterHTTPTimeout(),
		},
	}, nil
}

// openRouterHTTPTimeout caps the OpenRouter HTTP client at effectiveLLMCallTimeout() unless
// OPENROUTER_HTTP_TIMEOUT sets a stricter (shorter) limit.
func openRouterHTTPTimeout() time.Duration {
	max := effectiveLLMCallTimeout()
	d := strings.TrimSpace(os.Getenv("OPENROUTER_HTTP_TIMEOUT"))
	if d == "" {
		return max
	}
	parsed, err := time.ParseDuration(d)
	if err != nil || parsed < time.Second {
		return max
	}
	if parsed > max {
		return max
	}
	return parsed
}

// openRouterResumeMaxTokens is max_tokens for non-structured (resume/cover) completions.
// Default 8192; set OPENROUTER_RESUME_MAX_TOKENS to override (some free models still cap output low).
func openRouterResumeMaxTokens() int {
	s := strings.TrimSpace(os.Getenv("OPENROUTER_RESUME_MAX_TOKENS"))
	if s == "" {
		// Keep the free-tier output bounded so we don't frequently time out
		// while reading the response body.
		return 4096
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 256 {
		return 4096
	}
	if n > 128000 {
		return 128000
	}
	return n
}

func isLikelyTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "context canceled")
}

type openRouterChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterPlugin struct {
	ID string `json:"id"`
}

type openRouterChatRequest struct {
	Model            string                  `json:"model"`
	Messages         []openRouterChatMessage `json:"messages"`
	MaxTokens        int                     `json:"max_tokens"`
	Temperature      float64                 `json:"temperature"`
	TopP             float64                 `json:"top_p"`
	ResponseFormat   json.RawMessage         `json:"response_format,omitempty"`
	Plugins          []openRouterPlugin      `json:"plugins,omitempty"`
}

// jobJSONAssistantPrefill completes JSON when the API returns only the suffix after an assistant-starts message.
const jobJSONAssistantPrefill = "{"

// OpenRouter structured output for job_extractor (strict schema). Fallback to json_object if provider returns 400.
var openRouterJobRequirementsResponseFormat = json.RawMessage(`{"type":"json_schema","json_schema":{"name":"job_requirements","strict":true,"schema":{"type":"object","properties":{"company":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"}},"required":["company","title","description"],"additionalProperties":false}}}`)

var openRouterJSONObjectResponseFormat = json.RawMessage(`{"type":"json_object"}`)

type openRouterAssistantMessage struct {
	Content          json.RawMessage `json:"content"`
	Reasoning        string          `json:"reasoning"`
	ReasoningDetails []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"reasoning_details"`
}

type openRouterChatResponse struct {
	Choices []struct {
		Message *openRouterAssistantMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// openRouterAssistantText extracts assistant output. For resume/cover letter, includeReasoning
// must be false: reasoning-only models (e.g. StepFun) put chain-of-thought in reasoning and real
// output in content when using structured/json modes; using reasoning as fallback makes the
// resume parser print internal monologue instead of OBJECTIVE / CURRENT JOB sections.
func openRouterAssistantText(msg *openRouterAssistantMessage, includeReasoning bool) string {
	if msg == nil {
		return ""
	}
	out := strings.TrimSpace(decodeJSONStringContent(msg.Content))
	if out != "" {
		return out
	}
	if !includeReasoning {
		return ""
	}
	out = strings.TrimSpace(msg.Reasoning)
	if out != "" {
		return out
	}
	var b strings.Builder
	for _, d := range msg.ReasoningDetails {
		t := strings.TrimSpace(d.Text)
		if t != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(t)
		}
	}
	return strings.TrimSpace(b.String())
}

// looksLikeResumeOrCoverOutput returns true if s looks like REZUMATOR resume sections or a letter body,
// so we can safely use reasoning/reasoning_details when message.content is empty (some models only fill reasoning).
func looksLikeResumeOrCoverOutput(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 20 {
		return false
	}
	u := strings.ToUpper(s)
	if strings.Contains(u, "OBJECTIVE") && (strings.Contains(u, "CURRENT JOB") || strings.Contains(u, "PAST JOBS")) {
		return true
	}
	if strings.Contains(u, "CURRENT JOB") || strings.Contains(u, "PAST JOBS") {
		return len(s) >= 80
	}
	if strings.Contains(s, "Company:") && strings.Contains(s, "Title:") && strings.Contains(s, "Description:") {
		return len(s) >= 80
	}
	head := s
	if len(head) > 500 {
		head = head[:500]
	}
	hl := strings.ToLower(head)
	if strings.HasPrefix(strings.TrimSpace(s), "Dear ") || strings.Contains(hl, "здравствуйте") {
		return len(s) > 200
	}
	return false
}

func decodeJSONStringContent(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func (c *OpenRouterClient) GenerateContent(ctx context.Context, systemPrompt, userPrompt, input string) (string, error) {
	return c.chatCompletion(ctx, systemPrompt, userPrompt, input, false)
}

// GenerateContentJSON uses OpenRouter structured outputs for job-style JSON (schema first, then json_object on 400).
func (c *OpenRouterClient) GenerateContentJSON(ctx context.Context, systemPrompt, userPrompt, input string) (string, error) {
	return c.chatCompletion(ctx, systemPrompt, userPrompt, input, true)
}

func (c *OpenRouterClient) chatCompletion(ctx context.Context, systemPrompt, userPrompt, input string, structuredJobJSON bool) (string, error) {
	if verboseLogging {
		logInfo("=== LLM API CALL DEBUG ===")
		logInfo("Provider: OpenRouter, Model: %s", c.model)
		if structuredJobJSON {
			logInfo("OpenRouter structured job JSON (json_schema with json_object fallback on 400)")
		}
		logInfo("Temperature: 0.7, TopP: 0.9, MaxTokens (resume/cover): %d", openRouterResumeMaxTokens())
		logInfo("--- SYSTEM PROMPT ---")
		logInfo("%s", systemPrompt)
		logInfo("--- USER PROMPT ---")
		logInfo("%s", userPrompt)
		logInfo("--- INPUT DATA ---")
		logInfo("%s", input)
		logInfo("=== END LLM API CALL DEBUG ===")
	} else {
		logInfo("OpenRouter request: model=%s structuredJobJSON=%v (use -verbose for full prompts)", c.model, structuredJobJSON)
	}

	base := openRouterChatRequest{
		Model: c.model,
		Messages: []openRouterChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
			{Role: "user", Content: input},
		},
		MaxTokens:   openRouterResumeMaxTokens(),
		Temperature: 0.7,
		TopP:        0.9,
	}
	if structuredJobJSON {
		base.Temperature = 0.2
		base.MaxTokens = 8192
		base.Messages = append(base.Messages, openRouterChatMessage{
			Role: "assistant", Content: jobJSONAssistantPrefill,
		})
		if os.Getenv("OPENROUTER_DISABLE_RESPONSE_HEALING") == "" {
			base.Plugins = []openRouterPlugin{{ID: "response-healing"}}
		}
		logInfo("Job extraction: temp 0.2, max_tokens 8192, assistant prefill '{', response-healing plugin (set OPENROUTER_DISABLE_RESPONSE_HEALING=1 to skip plugin)")
	}

	if !structuredJobJSON {
		return c.postChatCompletion(ctx, base, false, "")
	}

	body := base
	body.ResponseFormat = openRouterJobRequirementsResponseFormat
	text, status, err := c.postChatCompletionStatus(ctx, body, true, jobJSONAssistantPrefill)
	if err == nil {
		return text, nil
	}
	if status == http.StatusBadRequest {
		logInfo("OpenRouter rejected json_schema; retrying with json_object")
		body.ResponseFormat = openRouterJSONObjectResponseFormat
		return c.postChatCompletion(ctx, body, true, jobJSONAssistantPrefill)
	}
	return "", err
}

func (c *OpenRouterClient) postChatCompletion(ctx context.Context, body openRouterChatRequest, includeReasoning bool, jobJSONPrefill string) (string, error) {
	text, _, err := c.postChatCompletionStatus(ctx, body, includeReasoning, jobJSONPrefill)
	return text, err
}

func (c *OpenRouterClient) postChatCompletionStatus(ctx context.Context, body openRouterChatRequest, includeReasoning bool, jobJSONPrefill string) (string, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", 0, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", 0, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if ref := strings.TrimSpace(os.Getenv("OPENROUTER_HTTP_REFERER")); ref != "" {
		req.Header.Set("HTTP-Referer", ref)
	}
	if title := strings.TrimSpace(os.Getenv("OPENROUTER_APP_TITLE")); title != "" {
		req.Header.Set("X-Title", title)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if isLikelyTimeoutErr(err) {
			return "", 0, fmt.Errorf("openrouter request: %w (limit %v per request; increase LLM_CALL_TIMEOUT if the model is still generating, e.g. LLM_CALL_TIMEOUT=90s)", err, openRouterHTTPTimeout())
		}
		return "", 0, fmt.Errorf("openrouter request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if isLikelyTimeoutErr(err) {
			return "", resp.StatusCode, fmt.Errorf("read response: %w (limit %v per request including reading the body; increase LLM_CALL_TIMEOUT for long resume/cover outputs)", err, openRouterHTTPTimeout())
		}
		return "", resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	var parsed openRouterChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", resp.StatusCode, fmt.Errorf("decode response (status %d): %w, body: %s", resp.StatusCode, err, truncateForLog(respBody))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		return "", resp.StatusCode, fmt.Errorf("openrouter chat completions failed: status=%d %s", resp.StatusCode, msg)
	}

	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", resp.StatusCode, fmt.Errorf("openrouter error: %s", parsed.Error.Message)
	}

	if len(parsed.Choices) == 0 {
		return "", resp.StatusCode, fmt.Errorf("no response choices received")
	}

	choice := parsed.Choices[0]
	if choice.Message == nil {
		return "", resp.StatusCode, fmt.Errorf("no message in response")
	}

	text := openRouterAssistantText(choice.Message, includeReasoning)
	if jobJSONPrefill != "" && text != "" {
		t := strings.TrimSpace(text)
		if !strings.HasPrefix(t, "{") {
			text = jobJSONPrefill + t
		}
	}
	if text == "" && !includeReasoning && choice.Message != nil {
		alt := openRouterAssistantText(choice.Message, true)
		if alt != "" && looksLikeResumeOrCoverOutput(alt) {
			logInfo("OpenRouter: model left message.content empty; using reasoning output (matched resume/cover markers)")
			text = alt
		}
	}
	if text == "" {
		if !includeReasoning {
			return "", resp.StatusCode, fmt.Errorf("empty message.content from model (reasoning-style models often omit content for free-form text; set OPENROUTER_MODEL to a non-reasoning chat model, or use Azure OpenAI provider)")
		}
		return "", resp.StatusCode, fmt.Errorf("no content in response (model returned empty message, reasoning, and reasoning_details)")
	}

	return text, resp.StatusCode, nil
}

func truncateForLog(b []byte) string {
	const max = 2000
	s := string(b)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
