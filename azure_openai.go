package rezumator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/ai/azopenai"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

type AzureOpenAIClient struct {
	keyClient   *azopenai.Client
	tokenClient *azopenai.Client
}

func NewAzureOpenAIClient() (*AzureOpenAIClient, error) {
	apiKey := os.Getenv("AZURE_OPENAI_API_KEY")
	endpoint := os.Getenv("AZURE_OPENAI_ENDPOINT")

	if endpoint == "" {
		return nil, fmt.Errorf("missing required environment variable: AZURE_OPENAI_ENDPOINT")
	}

	if os.Getenv("AZURE_CONFIG_DIR") == "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			srcAzureDir := filepath.Join(homeDir, ".azure")
			if _, statErr := os.Stat(srcAzureDir); statErr == nil {
				wd, err := os.Getwd()
				if err == nil {
					dstAzureDir := filepath.Join(wd, ".azure")
					azureProfileDst := filepath.Join(dstAzureDir, "azureProfile.json")
					needCopy := true
					if st, err := os.Stat(azureProfileDst); err == nil {
						needCopy = st.Size() < 1000
					}
					if needCopy {
						_ = os.RemoveAll(dstAzureDir)
						_ = os.MkdirAll(dstAzureDir, 0o700)
						_ = copyAzureDirExcluding(srcAzureDir, dstAzureDir, map[string]bool{"commands": true})
					} else {
						_ = os.MkdirAll(dstAzureDir, 0o700)
					}
					_ = os.Setenv("AZURE_CONFIG_DIR", dstAzureDir)
				}
			}
		}
	}

	var keyClient *azopenai.Client
	if apiKey != "" {
		cred, err := azopenai.NewKeyCredential(apiKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create AZURE_OPENAI_API_KEY credential: %v", err)
		}

		client, err := azopenai.NewClientWithKeyCredential(endpoint, cred, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create key-auth Azure OpenAI client: %v", err)
		}

		keyClient = client
	}

	var tokenCred azcore.TokenCredential
	var err error
	if staticToken := strings.TrimSpace(os.Getenv("AZURE_OPENAI_AD_TOKEN")); staticToken != "" {
		expiry := time.Now().Add(45 * time.Minute)
		if parsed, err := parseJWTExpiry(staticToken); err == nil {
			expiry = parsed
		}
		tokenCred = &staticTokenCredential{token: staticToken, expiry: expiry}
	} else {
		tokenCred, err = azidentity.NewAzureCLICredential(nil)
		if err != nil {
			tokenCred, err = azidentity.NewDefaultAzureCredential(nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create token credential (Azure CLI / DefaultAzureCredential): %v", err)
			}
		}
	}

	tokenClient, err := azopenai.NewClient(endpoint, tokenCred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create token-auth Azure OpenAI client: %v", err)
	}

	return &AzureOpenAIClient{
		keyClient:   keyClient,
		tokenClient: tokenClient,
	}, nil
}

func copyAzureDirExcluding(src, dst string, excludeDirNames map[string]bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if excludeDirNames[name] {
			continue
		}

		srcPath := filepath.Join(src, name)
		dstPath := filepath.Join(dst, name)

		if e.Type()&os.ModeSymlink != 0 {
			continue
		}

		if e.IsDir() {
			if err := os.MkdirAll(dstPath, 0o700); err != nil {
				continue
			}
			_ = copyAzureDirExcluding(srcPath, dstPath, excludeDirNames)
			continue
		}

		if err := copyFile(srcPath, dstPath, 0o600); err != nil {
			continue
		}
	}
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	_ = os.MkdirAll(filepath.Dir(dst), 0o700)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

type staticTokenCredential struct {
	token  string
	expiry time.Time
}

func (c *staticTokenCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{
		Token:     c.token,
		ExpiresOn: c.expiry,
	}, nil
}

func parseJWTExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("token is not a JWT")
	}

	payloadB64 := parts[1]
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return time.Time{}, err
	}

	var payload struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return time.Time{}, err
	}
	if payload.Exp == 0 {
		return time.Time{}, fmt.Errorf("missing exp in JWT payload")
	}
	return time.Unix(payload.Exp, 0), nil
}

func (c *AzureOpenAIClient) GenerateContent(ctx context.Context, systemPrompt, userPrompt, input string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, effectiveLLMCallTimeout())
	defer cancel()
	ctx = callCtx

	deploymentName := os.Getenv("AZURE_OPENAI_DEPLOYMENT_NAME")
	if deploymentName == "" {
		return "", fmt.Errorf("missing AZURE_OPENAI_DEPLOYMENT_NAME environment variable")
	}

	if verboseLogging {
		logInfo("=== LLM API CALL DEBUG ===")
		logInfo("Deployment: %s", deploymentName)
		logInfo("Temperature: 0.7, TopP: 0.9, MaxTokens: 4000")
		logInfo("--- SYSTEM PROMPT ---")
		logInfo("%s", systemPrompt)
		logInfo("--- USER PROMPT ---")
		logInfo("%s", userPrompt)
		logInfo("--- INPUT DATA ---")
		logInfo("%s", input)
		logInfo("=== END LLM API CALL DEBUG ===")
	} else {
		logInfo("Azure OpenAI request: deployment=%s (use -verbose for full prompts)", deploymentName)
	}

	systemRole := azopenai.ChatRoleSystem
	userRole := azopenai.ChatRoleUser

	messages := []azopenai.ChatMessage{
		{
			Role:    &systemRole,
			Content: &systemPrompt,
		},
		{
			Role:    &userRole,
			Content: &userPrompt,
		},
		{
			Role:    &userRole,
			Content: &input,
		},
	}

	maxTokens := int32(4000)
	temperature := float32(0.7)
	topP := float32(0.9)

	req := azopenai.ChatCompletionsOptions{
		Deployment:  deploymentName,
		Messages:    messages,
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
		TopP:        &topP,
	}

	generate := func(client *azopenai.Client) (string, error) {
		resp, err := client.GetChatCompletions(ctx, req, nil)
		if err != nil {
			return "", err
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response choices received")
		}

		choice := resp.Choices[0]
		if choice.Message == nil || choice.Message.Content == nil {
			return "", fmt.Errorf("no content in response")
		}

		return *choice.Message.Content, nil
	}

	shouldRetryWithToken := func(err error) bool {
		// Key auth can fail with 403 (key disabled) or 401 (invalid key, wrong audience, or resource expects AAD).
		// Retry with token client so AZURE_OPENAI_AD_TOKEN / Azure CLI can succeed without changing code paths.
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && (respErr.StatusCode == 403 || respErr.StatusCode == 401) {
			return true
		}
		msg := strings.ToLower(err.Error())
		return strings.Contains(msg, "authenticationtypedisabled") ||
			strings.Contains(msg, "forbidden") ||
			strings.Contains(msg, "401") && strings.Contains(msg, "unauthorized")
	}

	if c.keyClient != nil {
		content, err := generate(c.keyClient)
		if err == nil {
			return content, nil
		}
		if c.tokenClient != nil && shouldRetryWithToken(err) {
			logInfo("Azure OpenAI key-based auth failed (401/403). Retrying with Azure AD token auth...")
			return generate(c.tokenClient)
		}
		return "", fmt.Errorf("failed to get chat completions: %v", err)
	}

	if c.tokenClient == nil {
		return "", fmt.Errorf("no Azure OpenAI client available (missing both API key and DefaultAzureCredential)")
	}

	content, err := generate(c.tokenClient)
	if err != nil {
		return "", fmt.Errorf("failed to get chat completions: %v", err)
	}
	return content, nil
}
