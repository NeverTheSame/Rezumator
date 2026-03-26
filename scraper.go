package rezumator

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func scrapeJobRequirements(url string) (JobRequirements, error) {
	// Add a small delay to make requests look more human-like
	time.Sleep(2 * time.Second)

	// Create HTTP client with timeout and custom user agent
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Create request with custom headers
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return JobRequirements{}, fmt.Errorf("failed to create request: %v", err)
	}

	// Add headers to mimic a real browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Cache-Control", "max-age=0")

	// Make HTTP request
	resp, err := client.Do(req)
	if err != nil {
		return JobRequirements{}, fmt.Errorf("failed to fetch URL: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return JobRequirements{}, fmt.Errorf("HTTP request failed with status: %d - URL may be expired or inaccessible", resp.StatusCode)
	}

	// Log response headers for debugging
	logInfo("Response headers - Content-Encoding: '%s', Content-Type: '%s'",
		resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Type"))

	// Read the response body first
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return JobRequirements{}, fmt.Errorf("failed to read response body: %v", err)
	}

	// Check if content is gzip compressed and decompress if needed
	var htmlContent string
	if resp.Header.Get("Content-Encoding") == "gzip" {
		logInfo("Content is gzip compressed, decompressing...")
		gzReader, err := gzip.NewReader(strings.NewReader(string(bodyBytes)))
		if err != nil {
			return JobRequirements{}, fmt.Errorf("failed to create gzip reader: %v", err)
		}
		defer gzReader.Close()

		decompressedBytes, err := io.ReadAll(gzReader)
		if err != nil {
			return JobRequirements{}, fmt.Errorf("failed to decompress gzip content: %v", err)
		}
		htmlContent = string(decompressedBytes)
		logInfo("Decompressed content length: %d bytes", len(htmlContent))
	} else {
		htmlContent = string(bodyBytes)
		logInfo("Content is not compressed, length: %d bytes", len(htmlContent))
	}

	// Log the actual content preview
	if len(htmlContent) > 500 {
		logInfo("HTML content preview (first 500 chars): %s", htmlContent[:500])
	} else {
		logInfo("HTML content: %s", htmlContent)
	}

	// Parse HTML from the content
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return JobRequirements{}, fmt.Errorf("failed to parse HTML: %v", err)
	}

	// Log HTML structure information
	htmlElements := doc.Find("html").Length()
	headElements := doc.Find("head").Length()
	bodyElements := doc.Find("body").Length()
	logInfo("HTML structure - html: %d, head: %d, body: %d", htmlElements, headElements, bodyElements)

	// Log HTML content preview
	htmlText := doc.Text()
	if len(htmlText) > 500 {
		logInfo("HTML content preview (first 500 chars): %s", htmlText[:500])
	} else {
		logInfo("Full HTML content: %s", htmlText)
	}

	// Also log some key elements for debugging
	title := doc.Find("title").Text()
	h1Text := doc.Find("h1").Text()
	logInfo("Page title: '%s', H1 text: '%s'", title, h1Text)

	// Extract job information
	requirements := JobRequirements{}

	// Try to extract company name
	requirements.Company = extractCompanyName(doc)

	// Try to extract job title
	requirements.Title = extractJobTitle(doc)

	// Try to extract job description
	requirements.Description = extractJobDescription(doc)

	// Log extraction results
	logInfo("Extracted job information - Company: '%s', Title: '%s', Description length: %d",
		requirements.Company, requirements.Title, len(requirements.Description))

	// Validate that we got some meaningful data
	if requirements.Title == "Unknown Position" && requirements.Company == "Unknown Company" {
		return JobRequirements{}, fmt.Errorf("could not extract meaningful job information from the page - the site structure may have changed")
	}

	return requirements, nil
}

func extractCompanyName(doc *goquery.Document) string {
	// Common selectors for company names
	selectors := []string{
		"[data-testid='company-name']",
		".company-name",
		".employer-name",
		"[class*='company']",
		"[class*='employer']",
		"h1 + div",
		".job-header .company",
		".job-company",
		"[data-cy='company-name']",
		".job-details .company",
		".job-meta .company",
		"span[class*='company']",
		"div[class*='company']",
		// GitLab specific selectors
		".logo img[alt*='GitLab']",
		"img[alt*='GitLab']",
		"a[href*='gitlab'] .logo",
		".logo",
	}

	for _, selector := range selectors {
		element := doc.Find(selector).First()
		if element.Length() > 0 {
			// For images, check alt text
			if element.Is("img") {
				if alt := element.AttrOr("alt", ""); alt != "" {
					alt = strings.TrimSpace(alt)
					if strings.Contains(strings.ToLower(alt), "gitlab") {
						logInfo("Found company in image alt text: '%s'", alt)
						return "GitLab"
					}
					if alt != "" && len(alt) > 2 {
						logInfo("Found company in image alt text: '%s'", alt)
						return alt
					}
				}
			} else {
				// For other elements, get text content
				if text := element.Text(); text != "" {
					text = strings.TrimSpace(text)
					if text != "" && len(text) > 2 {
						// Skip location text that contains "Remote" or "Europe" or "North America"
						if strings.Contains(strings.ToLower(text), "remote") ||
							strings.Contains(strings.ToLower(text), "europe") ||
							strings.Contains(strings.ToLower(text), "north america") {
							logInfo("Skipping location text: '%s'", text)
							continue
						}
						logInfo("Found company in selector '%s': '%s'", selector, text)
						return text
					}
				}
			}
		}
	}

	// Try to find company name in meta tags
	companyMeta := doc.Find("meta[property='og:site_name']").AttrOr("content", "")
	if companyMeta != "" {
		logInfo("Found company in og:site_name meta tag: '%s'", companyMeta)
		return companyMeta
	}

	// Try to find company name in page title
	pageTitle := doc.Find("title").Text()
	if pageTitle != "" {
		logInfo("Page title: '%s'", pageTitle)
		// Look for "at CompanyName" pattern
		if strings.Contains(pageTitle, " at ") {
			parts := strings.Split(pageTitle, " at ")
			if len(parts) > 1 {
				company := strings.TrimSpace(parts[1])
				if company != "" {
					logInfo("Found company in page title: '%s'", company)
					return company
				}
			}
		}
	}

	// Fallback: try to extract from JSON-LD structured data
	logInfo("Trying JSON-LD fallback for company extraction")
	jsonLdData := extractJSONLDData(doc)
	if jsonLdData.Company != "" {
		logInfo("Found company in JSON-LD: '%s'", jsonLdData.Company)
		return jsonLdData.Company
	}

	return "Unknown Company"
}

func extractJobTitle(doc *goquery.Document) string {
	// Common selectors for job titles
	selectors := []string{
		"[data-testid='job-title']",
		".job-title",
		".position-title",
		"h1",
		"[class*='title']",
		".job-header h1",
		".job-details h1",
		"[data-cy='job-title']",
		".job-meta .title",
		"h1[class*='title']",
		".job-header .title",
		"span[class*='title']",
		"div[class*='title']",
	}

	for _, selector := range selectors {
		if text := doc.Find(selector).First().Text(); text != "" {
			text = strings.TrimSpace(text)
			if text != "" && len(text) > 2 {
				return text
			}
		}
	}

	// Try to find title in meta tags
	titleMeta := doc.Find("meta[property='og:title']").AttrOr("content", "")
	if titleMeta != "" {
		return titleMeta
	}

	// Try page title as fallback
	pageTitle := doc.Find("title").Text()
	if pageTitle != "" {
		// Clean up page title (remove site name, etc.)
		title := strings.TrimSpace(pageTitle)
		if strings.Contains(title, " - ") {
			parts := strings.Split(title, " - ")
			if len(parts) > 0 {
				return strings.TrimSpace(parts[0])
			}
		}
		return title
	}

	// Fallback: try to extract from JSON-LD structured data
	logInfo("Trying JSON-LD fallback for title extraction")
	jsonLdData := extractJSONLDData(doc)
	if jsonLdData.Title != "" {
		logInfo("Found title in JSON-LD: '%s'", jsonLdData.Title)
		return jsonLdData.Title
	}

	return "Unknown Position"
}

func extractJobDescription(doc *goquery.Document) string {
	// GitLab specific selectors first
	selectors := []string{
		".job__description",
		".job__description .body",
		".job-post .body",
		// Common selectors for job descriptions
		"[data-testid='job-description']",
		".job-description",
		"[class*='description']",
		".job-details",
		"#job-description",
		".job-content",
		".job-body",
		"[data-cy='job-description']",
		".job-details .description",
		".job-meta .description",
		"div[class*='description']",
		"section[class*='description']",
		".job-summary",
		".job-overview",
		// Fallback selectors
		".description",
	}

	for _, selector := range selectors {
		element := doc.Find(selector).First()
		if element.Length() > 0 {
			text := element.Text()
			if text != "" {
				// Clean up the text
				text = strings.TrimSpace(text)
				text = strings.ReplaceAll(text, "\n\n", "\n")
				text = strings.ReplaceAll(text, "  ", " ")
				if len(text) > 50 { // Ensure we have meaningful content
					logInfo("Found description with selector '%s', length: %d", selector, len(text))
					return text
				}
			}
		}
	}

	// Fallback: try to get any text content that might be the description
	// Look for the largest text block that might contain the description
	var largestText string
	var maxLength int

	doc.Find("div, section, article").Each(func(i int, s *goquery.Selection) {
		text := s.Text()
		text = strings.TrimSpace(text)
		if len(text) > maxLength && len(text) > 100 && len(text) < 10000 {
			// Check if this looks like a job description (contains keywords)
			lowerText := strings.ToLower(text)
			if strings.Contains(lowerText, "experience") ||
				strings.Contains(lowerText, "requirements") ||
				strings.Contains(lowerText, "responsibilities") ||
				strings.Contains(lowerText, "qualifications") ||
				strings.Contains(lowerText, "skills") {
				largestText = text
				maxLength = len(text)
			}
		}
	})

	if largestText != "" {
		// Clean up the text
		largestText = strings.ReplaceAll(largestText, "\n\n", "\n")
		largestText = strings.ReplaceAll(largestText, "  ", " ")
		return largestText
	}

	// Last resort: get body text
	text := doc.Find("body").Text()
	if len(text) > 200 {
		// Take first 1000 characters as description
		if len(text) > 1000 {
			text = text[:1000] + "..."
		}
		return strings.TrimSpace(text)
	}

	// Final fallback: try to extract from JSON-LD structured data
	jsonLdData := extractJSONLDData(doc)
	if jsonLdData.Description != "" {
		logInfo("Found description in JSON-LD, length: %d", len(jsonLdData.Description))
		return jsonLdData.Description
	}

	return "No description available"
}

func extractFencedJSON(s string) string {
	const open = "```json"
	if idx := strings.Index(s, open); idx >= 0 {
		start := idx + len(open)
		rest := s[start:]
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	if idx := strings.Index(s, "```"); idx >= 0 {
		rest := strings.TrimLeft(s[idx+3:], "\n\r")
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

func extractBalancedJSONObjectFrom(s string, start int) string {
	if start < 0 || start >= len(s) || s[start] != '{' {
		return ""
	}
	depth := 0
	for j := start; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : j+1]
			}
		}
	}
	return ""
}

// stripCommonLLMWrappers removes `thinking`...`thinking` blocks some models emit before JSON.
var reStripThinkBlocks = regexp.MustCompile("(?is)`" + "thinking" + "`[\\s\\S]*?`" + "thinking" + "`")

func stripCommonLLMWrappers(s string) string {
	s = strings.TrimSpace(reStripThinkBlocks.ReplaceAllString(s, ""))
	return strings.TrimSpace(s)
}

func jobRequirementsCandidateScore(rawJSON string) int {
	var r JobRequirements
	if err := json.Unmarshal([]byte(rawJSON), &r); err != nil {
		return -1
	}
	score := 0
	if strings.TrimSpace(r.Description) != "" {
		score += 5
	}
	if strings.TrimSpace(r.Title) != "" {
		score += 2
	}
	if strings.TrimSpace(r.Company) != "" {
		score += 1
	}
	if strings.Contains(rawJSON, `"company"`) && strings.Contains(rawJSON, `"title"`) && strings.Contains(rawJSON, `"description"`) {
		score += 3
	}
	return score
}

func parseJobRequirementsFromLLMResponse(response string) (JobRequirements, error) {
	response = stripCommonLLMWrappers(response)
	response = strings.TrimSpace(response)
	var req JobRequirements
	if err := json.Unmarshal([]byte(response), &req); err == nil && (req.Title != "" || req.Description != "") {
		return req, nil
	}
	if fenced := extractFencedJSON(response); fenced != "" {
		if err := json.Unmarshal([]byte(fenced), &req); err == nil {
			return req, nil
		}
	}
	seen := make(map[string]bool)
	var candidates []string
	for i := 0; i < len(response); i++ {
		if response[i] != '{' {
			continue
		}
		obj := extractBalancedJSONObjectFrom(response, i)
		if obj != "" && !seen[obj] {
			seen[obj] = true
			candidates = append(candidates, obj)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		si, sj := jobRequirementsCandidateScore(candidates[i]), jobRequirementsCandidateScore(candidates[j])
		if si != sj {
			return si > sj
		}
		return len(candidates[i]) > len(candidates[j])
	})
	for _, c := range candidates {
		var r JobRequirements
		if err := json.Unmarshal([]byte(c), &r); err != nil {
			continue
		}
		if strings.Contains(c, `"company"`) || strings.Contains(c, `"title"`) || strings.Contains(c, `"description"`) {
			return r, nil
		}
	}
	for _, c := range candidates {
		var r JobRequirements
		if err := json.Unmarshal([]byte(c), &r); err == nil && (r.Title != "" || r.Description != "") {
			return r, nil
		}
	}
	preview := response
	if len(preview) > 400 {
		preview = preview[:400] + "..."
	}
	return JobRequirements{}, fmt.Errorf("model did not return parseable JSON (showing start of output): %q", preview)
}

// Appended on retry when structured output returns chain-of-thought instead of JSON (must stay in code; not user-facing copy).
const jobExtractorPlainRetrySystemSuffix = `

STRICT OUTPUT — follow exactly:
- Your entire reply must be ONE JSON object only, with keys "company", "title", and "description".
- Do not write thinking steps, plans, or explanations. Do not use markdown or code fences.
- Your first character must be { and your last must be }.`

func readJobRequirementsFromFile(filename string, llm llmClient) (JobRequirements, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return JobRequirements{}, fmt.Errorf("failed to read file: %v", err)
	}

	logInfo("Successfully read file, content length: %d characters", len(string(content)))
	logInfo("File content length: %d characters", len(string(content)))

	// Load prompts from prompts.json (no hardcoded prompts allowed)
	prompts, err := loadPrompts()
	if err != nil {
		return JobRequirements{}, fmt.Errorf("error loading prompts: %v", err)
	}

	extractorPrompt, exists := prompts["job_extractor"]
	if !exists {
		return JobRequirements{}, fmt.Errorf("job_extractor prompt not found in prompts.json")
	}

	ctx := context.Background()
	var response string
	if orc, ok := llm.(*OpenRouterClient); ok {
		response, err = orc.GenerateContentJSON(ctx, extractorPrompt.SystemPrompt, extractorPrompt.UserPrompt, string(content))
	} else {
		response, err = llm.GenerateContent(ctx, extractorPrompt.SystemPrompt, extractorPrompt.UserPrompt, string(content))
	}
	if err != nil {
		// Graceful fallback when provider is rate-limited or transiently unavailable.
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "status=429") ||
			strings.Contains(lower, "too many requests") ||
			strings.Contains(lower, "rate limit") ||
			strings.Contains(lower, "timeout") ||
			strings.Contains(lower, "deadline exceeded") ||
			strings.Contains(lower, "connection reset") {
			logInfo("job_extractor LLM unavailable (%v), falling back to direct file content", err)
			return fallbackJobRequirementsFromText(string(content)), nil
		}
		return JobRequirements{}, fmt.Errorf("failed to extract job requirements using LLM: %v", err)
	}

	logLLMResponsePreview("job_extractor", response)

	requirements, err := parseJobRequirementsFromLLMResponse(response)
	needRetry := err != nil || strings.TrimSpace(requirements.Description) == ""
	if needRetry {
		logInfo("job_extractor: structured/plain first pass did not yield usable JSON; retrying with JSON-only instructions")
		retrySystem := extractorPrompt.SystemPrompt + jobExtractorPlainRetrySystemSuffix
		if orc, ok := llm.(*OpenRouterClient); ok {
			response, err = orc.GenerateContent(ctx, retrySystem, extractorPrompt.UserPrompt, string(content))
		} else {
			response, err = llm.GenerateContent(ctx, retrySystem, extractorPrompt.UserPrompt, string(content))
		}
		if err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "status=429") ||
				strings.Contains(lower, "too many requests") ||
				strings.Contains(lower, "rate limit") ||
				strings.Contains(lower, "timeout") ||
				strings.Contains(lower, "deadline exceeded") ||
				strings.Contains(lower, "connection reset") {
				logInfo("job_extractor retry LLM unavailable (%v), falling back to direct file content", err)
				return fallbackJobRequirementsFromText(string(content)), nil
			}
			return JobRequirements{}, fmt.Errorf("failed to extract job requirements using LLM (retry): %v", err)
		}
		logLLMResponsePreview("job_extractor_retry", response)
		requirements, err = parseJobRequirementsFromLLMResponse(response)
	}
	if err != nil {
		logInfo("job_extractor parse failed: %v", err)
		return JobRequirements{}, fmt.Errorf("failed to parse LLM response as JSON: %w", err)
	}

	logInfo("LLM extracted - Company: '%s', Title: '%s'", requirements.Company, requirements.Title)

	// Validate that we got the required fields
	if requirements.Company == "" {
		requirements.Company = "Unknown Company"
	}
	if requirements.Title == "" {
		requirements.Title = "Unknown Position"
	}
	if requirements.Description == "" {
		return JobRequirements{}, fmt.Errorf("job description not found in file")
	}

	return requirements, nil
}

func fallbackJobRequirementsFromText(content string) JobRequirements {
	title := "Unknown Position"
	lines := strings.Split(content, "\n")
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t != "" {
			title = t
			break
		}
	}
	return JobRequirements{
		Company:     "Unknown Company",
		Title:       title,
		Description: strings.TrimSpace(content),
	}
}

// extractJSONLDData extracts job information from JSON-LD structured data
func extractJSONLDData(doc *goquery.Document) JobRequirements {
	var result JobRequirements

	// Log script tag information
	totalScripts := doc.Find("script").Length()
	logInfo("Found %d total script tags", totalScripts)

	// Log script tag types
	doc.Find("script").Each(func(i int, s *goquery.Selection) {
		scriptType := s.AttrOr("type", "no-type")
		logInfo("Script %d - type: '%s'", i+1, scriptType)
	})

	// Find JSON-LD script tags
	scriptCount := 0
	doc.Find("script[type='application/ld+json']").Each(func(i int, s *goquery.Selection) {
		scriptCount++
		jsonText := s.Text()
		if jsonText == "" {
			return
		}

		logInfo("Debug: Found JSON-LD script %d with length: %d", scriptCount, len(jsonText))

		// Parse the JSON-LD data
		var jsonData map[string]interface{}
		if err := json.Unmarshal([]byte(jsonText), &jsonData); err != nil {
			logInfo("Debug: Failed to parse JSON-LD: %v", err)
			return
		}

		// Check if this is a JobPosting
		if jsonType, ok := jsonData["@type"].(string); ok && jsonType == "JobPosting" {
			logInfo("Debug: Found JobPosting in JSON-LD")

			// Extract title
			if title, ok := jsonData["title"].(string); ok {
				result.Title = strings.TrimSpace(title)
				logInfo("Debug: Extracted title from JSON-LD: '%s'", result.Title)
			}

			// Extract company name from hiringOrganization
			if hiringOrg, ok := jsonData["hiringOrganization"].(map[string]interface{}); ok {
				if companyName, ok := hiringOrg["name"].(string); ok {
					result.Company = strings.TrimSpace(companyName)
					logInfo("Debug: Extracted company from JSON-LD: '%s'", result.Company)
				}
			}

			// Extract description
			if description, ok := jsonData["description"].(string); ok {
				// Clean up HTML tags from description
				description = cleanHTMLTags(description)
				result.Description = strings.TrimSpace(description)
				logInfo("Debug: Extracted description from JSON-LD, length: %d", len(result.Description))
			}
		}
	})

	return result
}

// cleanHTMLTags removes HTML tags from text
func cleanHTMLTags(text string) string {
	// Simple regex to remove HTML tags
	re := regexp.MustCompile(`<[^>]*>`)
	return re.ReplaceAllString(text, "")
}
