package rezumator

import (
	"encoding/json"
	"time"
)

// The functions below are intentionally exported so unit tests can live in /tests
// (Go packages are directory-scoped, and unexported helpers in package main would
// otherwise be inaccessible from a different directory/package).

func ParseAdjustedContentForTests(content string) (AdjustedResume, error) {
	return parseAdjustedContent(content)
}

func FillAdjustedResumeFromResumeDataIfIncompleteForTests(adj AdjustedResume, raw ResumeData) AdjustedResume {
	return fillAdjustedResumeFromResumeDataIfIncomplete(adj, raw)
}

func LooksLikeResumeOrCoverOutputForTests(s string) bool {
	return looksLikeResumeOrCoverOutput(s)
}

func DecodeJSONStringContentForTests(raw json.RawMessage) string {
	return decodeJSONStringContent(raw)
}

func OpenRouterAssistantTextForTests(content json.RawMessage, reasoning string, reasoningDetailsTexts []string, includeReasoning bool) string {
	msg := &openRouterAssistantMessage{
		Content:   content,
		Reasoning: reasoning,
	}
	for _, t := range reasoningDetailsTexts {
		msg.ReasoningDetails = append(msg.ReasoningDetails, struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{
			Type: "",
			Text: t,
		})
	}
	return openRouterAssistantText(msg, includeReasoning)
}

func OpenRouterResumeMaxTokensForTests() int {
	return openRouterResumeMaxTokens()
}

func ParseJobRequirementsFromLLMResponseForTests(response string) (JobRequirements, error) {
	return parseJobRequirementsFromLLMResponse(response)
}

func ExtractFencedJSONForTests(s string) string {
	return extractFencedJSON(s)
}

func ExtractBalancedJSONObjectFromForTests(s string, start int) string {
	return extractBalancedJSONObjectFrom(s, start)
}

func EffectiveLLMCallTimeoutForTests() time.Duration {
	return effectiveLLMCallTimeout()
}

