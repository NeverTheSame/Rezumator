package rezumator

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Prompt struct {
	SystemPrompt string `json:"system_prompt"`
	UserPrompt   string `json:"user_prompt"`
}

type Prompts map[string]Prompt

type CriticalFormattingRules struct {
	Description string   `json:"description"`
	Rules       []string `json:"rules"`
}

type PromptsConfig struct {
	CriticalFormattingRules CriticalFormattingRules `json:"critical_formatting_rules"`
	Prompts                 Prompts                 `json:"-"`
}

func loadPrompts() (Prompts, error) {
	data, err := os.ReadFile("prompts.json")
	if err != nil {
		return nil, fmt.Errorf("failed to read prompts.json: %v", err)
	}

	var config PromptsConfig
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompts.json: %v", err)
	}

	// Extract prompts from the config
	prompts := make(Prompts)

	// Parse the JSON again to extract prompt sections
	var rawData map[string]interface{}
	err = json.Unmarshal(data, &rawData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompts.json: %v", err)
	}

	// Extract each prompt section and apply formatting rules
	for key, value := range rawData {
		if key == "critical_formatting_rules" {
			continue // Skip the formatting rules section
		}

		if promptData, ok := value.(map[string]interface{}); ok {
			systemPrompt := ""
			userPrompt := ""

			if sp, exists := promptData["system_prompt"]; exists {
				systemPrompt = sp.(string)
			}
			if up, exists := promptData["user_prompt"]; exists {
				userPrompt = up.(string)
			}

			// Apply critical formatting rules to both prompts
			systemPrompt = applyFormattingRules(systemPrompt, config.CriticalFormattingRules)
			userPrompt = applyFormattingRules(userPrompt, config.CriticalFormattingRules)

			prompts[key] = Prompt{
				SystemPrompt: systemPrompt,
				UserPrompt:   userPrompt,
			}
		}
	}

	return prompts, nil
}

func applyFormattingRules(prompt string, rules CriticalFormattingRules) string {
	// Replace placeholder with actual formatting rules
	if strings.Contains(prompt, "CRITICAL FORMATTING RULES: Apply all critical formatting rules from the shared configuration.") {
		var rulesText strings.Builder
		rulesText.WriteString("\n\nCRITICAL FORMATTING RULES:\n")
		for i, rule := range rules.Rules {
			rulesText.WriteString(fmt.Sprintf("%d. %s\n", i+1, rule))
		}
		prompt = strings.Replace(prompt, "CRITICAL FORMATTING RULES: Apply all critical formatting rules from the shared configuration.", rulesText.String(), 1)
	}
	return prompt
}
