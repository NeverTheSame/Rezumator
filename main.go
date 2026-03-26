package rezumator

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

var verboseLogging bool

// fatalUser prints to stderr (always visible) and logs to the log file, then exits.
// After setupLogging(), package log only writes to the file, so log.Fatalf alone is invisible in the terminal.
func fatalUser(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "\nERROR: %s\n", strings.TrimSpace(msg))
	log.Printf("FATAL: %s", strings.TrimSpace(msg))
	os.Exit(1)
}

func resolveResumePick(pick string) (string, error) {
	p := strings.TrimSpace(strings.ToLower(pick))
	switch p {
	case "", "1", "sre":
		return "my_resumes/sre.json", nil
	case "2", "vp":
		return "my_resumes/vp.json", nil
	default:
		if strings.HasSuffix(p, ".json") && pick != "" {
			return pick, nil
		}
		return "", fmt.Errorf("invalid -resume value %q (use 1/sre, 2/vp, or path to .json)", pick)
	}
}

// setupLogging configures logging with timestamps to a file
func setupLogging() {
	// Create logs directory if it doesn't exist
	if err := os.MkdirAll("logs", 0755); err != nil {
		log.Fatalf("Failed to create logs directory: %v", err)
	}

	// Create log file with timestamp
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	logFileName := fmt.Sprintf("logs/rezumator_%s.log", timestamp)

	logFile, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	// IMPORTANT: all logs must be written to the log file only (no terminal spam).
	log.SetOutput(logFile)

	// Do not print anything to the terminal here; the user requested no logging on stdout/stderr.
}

// logLLMResponsePreview logs a model response; use -verbose for full text when responses are huge.
func logLLMResponsePreview(label string, response string) {
	n := len(response)
	if verboseLogging {
		log.Printf("[INFO] [%s] LLM raw response (%d chars, full):\n%s", label, n, response)
		return
	}
	log.Printf("[INFO] [%s] LLM raw response length: %d", label, n)
	const maxEdge = 4000
	if n <= 2*maxEdge {
		log.Printf("[INFO] [%s] LLM raw response:\n%s", label, response)
		return
	}
	log.Printf("[INFO] [%s] LLM raw response (first %d chars):\n%s", label, maxEdge, response[:maxEdge])
	log.Printf("[INFO] [%s] LLM raw response (last %d chars):\n%s", label, maxEdge, response[n-maxEdge:])
	log.Printf("[INFO] [%s] (truncated; run with -verbose for full response in log)", label)
}

// logInfo logs informational messages with timestamps
func logInfo(format string, args ...interface{}) {
	// If message is an extended debug log, use prefix [DEBUG_EXT]
	msg := format
	if verboseLogging && (strings.Contains(format, "detected") || strings.Contains(format, "set ") || strings.Contains(format, "parsed") || strings.Contains(format, "raw llm output") || strings.Contains(format, "prepared llm input")) {
		log.Printf("[DEBUG_EXT] "+msg, args...)
	} else if !verboseLogging && (strings.Contains(format, "detected") || strings.Contains(format, "set ") || strings.Contains(format, "parsed") || strings.Contains(format, "raw llm output") || strings.Contains(format, "prepared llm input")) {
		// skip verbose logs if not verbose
		return
	} else {
		log.Printf("[INFO] "+msg, args...)
	}
}

type JobExperience struct {
	Company     string `json:"company"`
	Title       string `json:"title"`
	Description string `json:"description"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
}

type ResumeData struct {
	Objective  string          `json:"objective"`
	CurrentJob JobExperience   `json:"current_job"`
	PastJobs   []JobExperience `json:"past_jobs"`
}

type JobRequirements struct {
	Company     string `json:"company"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type TechnologiesToAvoid struct {
	TechnologiesToAvoid []string `json:"technologies_to_avoid"`
	Instructions        string   `json:"instructions"`
}

type AdjustedResume struct {
	Objective  string          `json:"objective"`
	CurrentJob JobExperience   `json:"current_job"`
	PastJobs   []JobExperience `json:"past_jobs"`
}

type humanizeStats struct {
	EmDashReplacements      int
	CurlyQuoteReplacements  int
	FillerLineRemovals      int
	TransitionReplacements  int
	WhitespaceNormalizations int
}

type executionTimings struct {
	ReadDetails     time.Duration
	CreateResume    time.Duration
	CreateCover     time.Duration
	HumanizeCover   time.Duration
	TotalEndToEnd   time.Duration
}

// Matches "OBJECTIVE: ..." when the model puts the whole objective on the same line as the header.
var reObjectiveInline = regexp.MustCompile(`(?i)^OBJECTIVE:\s*(.*)$`)

// Splits a line into "one achievement sentence per line" chunks.
// Special case: do not split on '.' when it's between digits (e.g., 99.9%).
func splitAchievementLineIntoChunks(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	r := []rune(s)
	var chunks []string
	start := 0

	isDigit := func(ch rune) bool { return ch >= '0' && ch <= '9' }

	for i := 0; i < len(r); i++ {
		if r[i] != '.' && r[i] != '!' && r[i] != '?' {
			continue
		}
		// Don't split decimal numbers like 99.9
		if r[i] == '.' && i > 0 && i+1 < len(r) && isDigit(r[i-1]) && isDigit(r[i+1]) {
			continue
		}

		chunk := strings.TrimSpace(string(r[start : i+1]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		start = i + 1
	}

	// Remainder.
	if start < len(r) {
		tail := strings.TrimSpace(string(r[start:]))
		if tail != "" {
			chunks = append(chunks, tail)
		}
	}

	return chunks
}

var reDecimalNewline = regexp.MustCompile(`(\d+)\.\s*\n\s*(\d+)`)

func fixDecimalNewlines(s string) string {
	return reDecimalNewline.ReplaceAllString(s, `$1.$2`)
}

// Run is the CLI entry point (used by cmd/rezumator).
func Run() {
	runStart := time.Now()
	var timings executionTimings

	// Parse flags
	verbose := flag.Bool("verbose", false, "enable verbose extended logging")
	batch := flag.Bool("batch", false, "non-interactive: SRE resume + job_details.txt + OpenRouter (no prompts)")
	resumeFlag := flag.String("resume", "", "with -batch: sre|1 (default), vp|2, or path to resume JSON")
	jobFlag := flag.String("job", "", "with -batch: file|job_details.txt (default) or https URL to scrape")
	providerFlag := flag.String("provider", "", "with -batch: 1=OpenRouter (default), 2=Azure")
	flag.Parse()
	verboseLogging = *verbose

	// Setup logging
	setupLogging()
	logInfo("Starting Rezumator application")

	if verboseLogging {
		logInfo("Verbose extended logging ENABLED")
	} else {
		logInfo("Verbose extended logging DISABLED")
	}

	fmt.Println("🚀 Welcome to Rezumator - AI-Powered Resume Optimizer!")
	fmt.Println("This tool will help you customize your resume for specific job postings.")
	fmt.Println("You can provide job requirements via URL or use the job_details.txt file.")
	if *batch {
		fmt.Println("Running in -batch mode (no prompts). Use -resume, -job, -provider to override defaults.")
	}

	// Load environment variables
	if err := godotenv.Load("secret"); err != nil {
		log.Printf("Warning: Could not load .env file: %v", err)
	}

	var resumeFile string
	var jobSource string
	var providerChoice string

	if *batch {
		var errPick error
		resumeFile, errPick = resolveResumePick(*resumeFlag)
		if errPick != nil {
			fatalUser("%v", errPick)
		}
		js := strings.TrimSpace(*jobFlag)
		if js == "" || strings.EqualFold(js, "file") || strings.EqualFold(js, "job_details.txt") {
			jobSource = "job_details.txt"
			fmt.Println("✓ Using job_details.txt (batch)")
		} else {
			jobSource = js
			fmt.Printf("✓ Job source (batch): %s\n", jobSource)
		}
		p := strings.TrimSpace(*providerFlag)
		if p == "" || p == "1" {
			providerChoice = "1"
		} else if p == "2" {
			providerChoice = "2"
		} else {
			fatalUser("invalid -provider %q (use 1 or 2)", p)
		}
		if jobSource == "job_details.txt" {
			displayJobDetailsFile()
		}
	} else {
		// Interactive resume selection
		resumeFile = selectResume()
		jobSource = getJobURL()
		providerChoice = selectLLMProvider()
	}
	logInfo("Loading resume data from: %s", resumeFile)

	// Load resume data
	resumeData, err := loadResumeData(resumeFile)
	if err != nil {
		fatalUser("Error loading resume data: %v", err)
	}
	logInfo("Successfully loaded resume data")

	var client llmClient
	var providerLabel string
	client, providerLabel, err = newLLMClientFromChoice(providerChoice)
	if err != nil {
		fatalUser("Error initializing LLM client: %v", err)
	}
	logInfo("Using LLM provider: %s", providerLabel)
	fmt.Printf("✓ LLM provider: %s\n", providerLabel)

	// Load job requirements (scraping does not use the LLM; file path does)
	if jobSource == "job_details.txt" {
		fmt.Println("Extracting structured job requirements from file (LLM)…")
	}
	readDetailsStart := time.Now()
	jobRequirements, err := loadJobRequirementsWithLLM(jobSource, client)
	timings.ReadDetails = time.Since(readDetailsStart)
	if err != nil {
		fatalUser("Error loading job requirements: %v", err)
	}

	// Log the final job requirements that will be used
	logInfo("Final job requirements for resume generation:")
	logInfo("- Company: '%s'", jobRequirements.Company)
	logInfo("- Title: '%s'", jobRequirements.Title)
	logInfo("- Description length: %d characters", len(jobRequirements.Description))

	// Load technologies to avoid
	logInfo("Loading technologies to avoid from: technologies_to_avoid.json")
	techToAvoid, err := loadTechnologiesToAvoid("technologies_to_avoid.json")
	if err != nil {
		log.Printf("Warning: Could not load technologies_to_avoid.json: %v", err)
		// Create empty struct if file doesn't exist
		techToAvoid = TechnologiesToAvoid{
			TechnologiesToAvoid: []string{},
			Instructions:        "No specific technologies to avoid.",
		}
		logInfo("Using default technologies to avoid configuration")
	} else {
		logInfo("Successfully loaded technologies to avoid")
	}

	// Adjust resume content
	logInfo("Starting resume adjustment and cover letter generation")
	adjustedResume, coverLetter, phaseTimings, err := adjustResume(context.Background(), client, resumeData, jobRequirements, techToAvoid)
	timings.CreateResume = phaseTimings.CreateResume
	timings.CreateCover = phaseTimings.CreateCover
	timings.HumanizeCover = phaseTimings.HumanizeCover
	if err != nil {
		fatalUser("Error adjusting resume: %v", err)
	} else {
		logInfo("Successfully completed resume adjustment and cover letter generation")
	}

	// Print results
	logInfo("Displaying results")
	printResults(adjustedResume, coverLetter)
	timings.TotalEndToEnd = time.Since(runStart)
	fmt.Println("=== EXECUTION TIMINGS ===")
	fmt.Printf("Reading details: %v\n", timings.ReadDetails.Round(time.Millisecond))
	fmt.Printf("Creating resume: %v\n", timings.CreateResume.Round(time.Millisecond))
	fmt.Printf("Creating CV (cover letter): %v\n", timings.CreateCover.Round(time.Millisecond))
	fmt.Printf("Overall end-to-end: %v\n", timings.TotalEndToEnd.Round(time.Millisecond))
	logInfo("TIMINGS read_details=%v create_resume=%v create_cover=%v humanize_cover=%v total=%v",
		timings.ReadDetails, timings.CreateResume, timings.CreateCover, timings.HumanizeCover, timings.TotalEndToEnd)
	logInfo("Application completed successfully")
}

func loadResumeData(filename string) (ResumeData, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return ResumeData{}, err
	}

	var resumeData ResumeData
	err = json.Unmarshal(data, &resumeData)
	return resumeData, err
}

func loadJobRequirements(filename string) (JobRequirements, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return JobRequirements{}, err
	}

	var requirements JobRequirements
	err = json.Unmarshal(data, &requirements)
	return requirements, err
}

func loadTechnologiesToAvoid(filename string) (TechnologiesToAvoid, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return TechnologiesToAvoid{}, err
	}

	var techToAvoid TechnologiesToAvoid
	err = json.Unmarshal(data, &techToAvoid)
	return techToAvoid, err
}

// isLikelyTruncatedResumeLLMOutput is true when the model reply clearly did not include a full resume
// (e.g. missing PAST JOBS while the source resume has past roles).
func isLikelyTruncatedResumeLLMOutput(content string, source ResumeData) bool {
	c := strings.TrimSpace(content)
	if c == "" {
		return true
	}
	if len(source.PastJobs) > 0 && !strings.Contains(strings.ToLower(c), "past jobs") {
		return true
	}
	return false
}

// fillAdjustedResumeFromResumeDataIfIncomplete fills missing objective or past roles from the
// source JSON when the model omitted or truncated sections.
func fillAdjustedResumeFromResumeDataIfIncomplete(adj AdjustedResume, raw ResumeData) AdjustedResume {
	if strings.TrimSpace(adj.Objective) == "" && strings.TrimSpace(raw.Objective) != "" {
		adj.Objective = strings.TrimSpace(raw.Objective)
		logInfo("Objective was empty after parse; filled from source resume JSON")
	}

	// If current job was partially missing (common when the model output deviates),
	// fill missing fields from the source resume.
	if strings.TrimSpace(raw.CurrentJob.Company) != "" && strings.TrimSpace(adj.CurrentJob.Company) == "" {
		adj.CurrentJob.Company = raw.CurrentJob.Company
	}
	if strings.TrimSpace(raw.CurrentJob.Title) != "" && strings.TrimSpace(adj.CurrentJob.Title) == "" {
		adj.CurrentJob.Title = raw.CurrentJob.Title
	}
	if strings.TrimSpace(raw.CurrentJob.Description) != "" && strings.TrimSpace(adj.CurrentJob.Description) == "" {
		adj.CurrentJob.Description = raw.CurrentJob.Description
	}

	if len(adj.PastJobs) == 0 && len(raw.PastJobs) > 0 {
		adj.PastJobs = make([]JobExperience, len(raw.PastJobs))
		copy(adj.PastJobs, raw.PastJobs)
		logInfo("Past jobs missing from model output; filled %d past role(s) from source resume JSON", len(adj.PastJobs))
	}

	// Enforce title + description formatting even when we fallback to raw resume data.
	adj.CurrentJob.Title = normalizeAdjustedJobTitle(adj.CurrentJob.Title)
	adj.CurrentJob.Description = normalizeDescriptionToOnePerLine(adj.CurrentJob.Description)
	for i := range adj.PastJobs {
		adj.PastJobs[i].Title = normalizeAdjustedJobTitle(adj.PastJobs[i].Title)
		adj.PastJobs[i].Description = normalizeDescriptionToOnePerLine(adj.PastJobs[i].Description)
	}

	return adj
}

func humanizeCoverLetter(text string) (string, humanizeStats) {
	stats := humanizeStats{}
	out := text

	// 1) Em dash prohibition and curly quotes normalization.
	stats.EmDashReplacements += strings.Count(out, "—")
	out = strings.ReplaceAll(out, "—", ", ")
	stats.EmDashReplacements += strings.Count(out, "–")
	out = strings.ReplaceAll(out, "–", "-")

	curlyPairs := map[string]string{
		"“": "\"",
		"”": "\"",
		"‘": "'",
		"’": "'",
	}
	for k, v := range curlyPairs {
		c := strings.Count(out, k)
		if c > 0 {
			stats.CurlyQuoteReplacements += c
			out = strings.ReplaceAll(out, k, v)
		}
	}

	// 2) Remove explicit chatbot artifacts while preserving core message.
	lines := strings.Split(out, "\n")
	cleanLines := make([]string, 0, len(lines))
	reFiller := regexp.MustCompile(`(?i)^\s*(great question!?|i hope this helps!?|let me know if you'd like.*|certainly!?|of course!?)\s*$`)
	for _, ln := range lines {
		if reFiller.MatchString(strings.TrimSpace(ln)) {
			stats.FillerLineRemovals++
			continue
		}
		cleanLines = append(cleanLines, ln)
	}
	out = strings.Join(cleanLines, "\n")

	// 3) Tone-level substitutions for overused AI transitions.
	repls := []struct{ from, to string }{
		{"Additionally,", "Also,"},
		{"Additionally", "Also"},
		{"Furthermore,", "Also,"},
		{"Moreover,", "Also,"},
		{"In conclusion,", "Overall,"},
	}
	for _, r := range repls {
		c := strings.Count(out, r.from)
		if c > 0 {
			stats.TransitionReplacements += c
			out = strings.ReplaceAll(out, r.from, r.to)
		}
	}

	// 4) Light whitespace cleanup.
	before := out
	reTripleNewline := regexp.MustCompile(`\n{3,}`)
	out = reTripleNewline.ReplaceAllString(out, "\n\n")
	if before != out {
		stats.WhitespaceNormalizations++
	}

	return strings.TrimSpace(out), stats
}

func looksLikePlanningOrMetaText(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" {
		return false
	}
	bad := []string{
		"should be tailored",
		"original:",
		"i need to",
		"let me",
		"for example",
		"current job:",
		"past jobs:",
		"description:",
		"first,",
		"now,",
	}
	for _, b := range bad {
		if strings.Contains(l, b) {
			return true
		}
	}
	return false
}

func isWeakAdjustedResumeOutput(r AdjustedResume) bool {
	if strings.TrimSpace(r.Objective) == "" || looksLikePlanningOrMetaText(r.Objective) {
		return true
	}
	if strings.TrimSpace(r.CurrentJob.Company) == "" || strings.TrimSpace(r.CurrentJob.Description) == "" {
		return true
	}
	if looksLikePlanningOrMetaText(r.CurrentJob.Description) {
		return true
	}
	if len(r.PastJobs) == 0 {
		return true
	}
	for _, j := range r.PastJobs {
		if strings.TrimSpace(j.Company) == "" || strings.TrimSpace(j.Description) == "" {
			return true
		}
		if looksLikePlanningOrMetaText(j.Description) {
			return true
		}
	}
	return false
}

var reContainsDigit = regexp.MustCompile(`\d`)

func splitJobRequirementPoints(description string) []string {
	clean := strings.ReplaceAll(description, "\r", "\n")
	lines := strings.Split(clean, "\n")
	out := make([]string, 0, len(lines))
	seen := map[string]struct{}{}
	for _, l := range lines {
		l = strings.TrimSpace(strings.TrimPrefix(l, "-"))
		if l == "" {
			continue
		}
		low := strings.ToLower(l)
		// Skip section headers and generic lead-ins.
		if strings.Contains(low, "your new role") || strings.Contains(low, "what you'll need") || strings.Contains(low, "what you'll get") {
			continue
		}
		if len(l) < 20 {
			continue
		}
		if _, ok := seen[low]; ok {
			continue
		}
		seen[low] = struct{}{}
		out = append(out, l)
	}
	return out
}

func extractCoverageKeywords(line string) []string {
	parts := regexp.MustCompile(`[^a-z0-9\+\#]+`).Split(strings.ToLower(line), -1)
	stop := map[string]struct{}{
		"and": {}, "the": {}, "with": {}, "for": {}, "that": {}, "this": {}, "will": {}, "from": {},
		"into": {}, "using": {}, "your": {}, "you": {}, "are": {}, "our": {}, "team": {}, "role": {},
		"what": {}, "need": {}, "succeed": {}, "get": {}, "return": {}, "have": {}, "has": {}, "been": {},
		"years": {}, "year": {}, "plus": {}, "strong": {}, "excellent": {}, "focused": {}, "focus": {},
	}
	keywords := make([]string, 0, 8)
	seen := map[string]struct{}{}
	for _, p := range parts {
		if len(p) < 4 {
			continue
		}
		if _, ok := stop[p]; ok {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		keywords = append(keywords, p)
		if len(keywords) >= 6 {
			break
		}
	}
	return keywords
}

func coverageMissingPoints(currentJobDescription string, requirementPoints []string) []string {
	desc := strings.ToLower(currentJobDescription)
	missing := []string{}
	for _, p := range requirementPoints {
		kw := extractCoverageKeywords(p)
		matched := false
		for _, k := range kw {
			if strings.Contains(desc, k) {
				matched = true
				break
			}
		}
		if !matched {
			missing = append(missing, p)
		}
	}
	return missing
}

func linesMissingQuantification(description string) []string {
	lines := strings.Split(description, "\n")
	missing := []string{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !reContainsDigit.MatchString(l) {
			missing = append(missing, l)
		}
	}
	return missing
}

const rezumatorFictionalExemplarSystemSuffix = "\n\nMODE: FICTIONAL EXEMPLAR SHOWCASE\n- Treat the generated resume as a synthetic exemplar of an ideal profile for the target role.\n- You may synthesize plausible, internally consistent experience with required technologies.\n- Keep synthetic achievements realistic, professional, and credible.\n- Output final resume text only (no planning, no reasoning)."

const rezumatorFictionalExemplarUserSuffix = "\n\nIMPORTANT MODE: This is a fictional exemplar project. Generate a strong sample resume profile tailored to the target role, including plausible experience related to required technologies. Keep it realistic and internally consistent."

func adjustResume(ctx context.Context, client llmClient, resumeData ResumeData, jobRequirements JobRequirements, techToAvoid TechnologiesToAvoid) (AdjustedResume, string, executionTimings, error) {
	var timings executionTimings
	// Load prompts
	prompts, err := loadPrompts()
	if err != nil {
		return AdjustedResume{}, "", timings, fmt.Errorf("error loading prompts: %v", err)
	}

	rezumatorPrompt, exists := prompts["rezumator"]
	if !exists {
		return AdjustedResume{}, "", timings, fmt.Errorf("rezumator prompt not found in prompts.json")
	}
	// Force fictional exemplar mode from code so behavior is consistent regardless of prompts.json edits.
	rezumatorPrompt.SystemPrompt += rezumatorFictionalExemplarSystemSuffix
	rezumatorPrompt.UserPrompt += rezumatorFictionalExemplarUserSuffix

	coverLetterPrompt, exists := prompts["cover_letter"]
	if !exists {
		return AdjustedResume{}, "", timings, fmt.Errorf("cover_letter prompt not found in prompts.json")
	}

	// Prepare input for LLM
	input := prepareLLMInput(resumeData, jobRequirements, techToAvoid)

	// Log the prepared LLM input for debugging
	logInfo("Prepared LLM input:\n%s", input)

	fmt.Println("Generating tailored resume (LLM)…")

	// Generate adjusted content
	resumeStart := time.Now()
	adjustedContent, err := client.GenerateContent(ctx, rezumatorPrompt.SystemPrompt, rezumatorPrompt.UserPrompt, input)
	timings.CreateResume += time.Since(resumeStart)
	if err != nil {
		return AdjustedResume{}, "", timings, fmt.Errorf("error generating content: %v", err)
	}

	// Log the raw LLM output for debugging
	logInfo("Raw LLM output:\n%s", adjustedContent)

	if isLikelyTruncatedResumeLLMOutput(adjustedContent, resumeData) {
		logInfo("Resume LLM output looks incomplete (e.g. missing PAST JOBS); retrying once with completeness hint")
		fmt.Println("Retrying tailored resume (incomplete first reply)…")
		retryInput := input + "\n\nCRITICAL: Your previous reply was incomplete. Output the FULL adjusted resume with three labeled sections: OBJECTIVE, CURRENT JOB, and PAST JOBS. Under PAST JOBS include every past role from the source resume. Finish each sentence; do not stop mid-line."
		retryStart := time.Now()
		adjustedContent2, err2 := client.GenerateContent(ctx, rezumatorPrompt.SystemPrompt, rezumatorPrompt.UserPrompt, retryInput)
		timings.CreateResume += time.Since(retryStart)
		if err2 == nil {
			a, b := strings.TrimSpace(adjustedContent), strings.TrimSpace(adjustedContent2)
			betterLen := len(b) > len(a)
			betterComplete := isLikelyTruncatedResumeLLMOutput(a, resumeData) && !isLikelyTruncatedResumeLLMOutput(b, resumeData)
			if betterLen || betterComplete {
				adjustedContent = adjustedContent2
				logInfo("Using resume retry result (longer=%v, completeness improved=%v)", betterLen, betterComplete)
			}
		} else {
			logInfo("Resume completeness retry failed: %v", err2)
		}
	}

	// Parse the response
	adjustedResume, err := parseAdjustedContent(adjustedContent)
	if err != nil {
		return AdjustedResume{}, "", timings, fmt.Errorf("error parsing adjusted content: %v", err)
	}

	// Log the parsed result for debugging
	logInfo("=== PARSED ADJUSTED RESUME RESULT ===")
	logInfo("Objective: '%s' (length: %d)", adjustedResume.Objective, len(adjustedResume.Objective))
	logInfo("CurrentJob - Company: '%s', Title: '%s', Description: '%s' (length: %d)",
		adjustedResume.CurrentJob.Company, adjustedResume.CurrentJob.Title,
		adjustedResume.CurrentJob.Description[:min(100, len(adjustedResume.CurrentJob.Description))],
		len(adjustedResume.CurrentJob.Description))
	logInfo("PastJobs count: %d", len(adjustedResume.PastJobs))
	for i, job := range adjustedResume.PastJobs {
		logInfo("PastJob[%d] - Company: '%s', Title: '%s'", i, job.Company, job.Title)
	}

	// If content is weak or does not fully cover requirements, force an LLM revision pass.
	reqPoints := splitJobRequirementPoints(jobRequirements.Description)
	missingPoints := coverageMissingPoints(adjustedResume.CurrentJob.Description, reqPoints)
	missingQuant := linesMissingQuantification(adjustedResume.CurrentJob.Description)
	if isWeakAdjustedResumeOutput(adjustedResume) || len(missingPoints) > 0 || len(missingQuant) > 0 {
		logInfo("Resume quality gate triggered; requesting LLM revision (weak=%v missing_points=%d missing_quant=%d)",
			isWeakAdjustedResumeOutput(adjustedResume), len(missingPoints), len(missingQuant))
		revisionInput := input + "\n\nREVISION REQUIRED:\n" +
			"- Rewrite OBJECTIVE, CURRENT JOB, and PAST JOBS.\n" +
			"- CURRENT JOB must cover EVERY requirement point from JOB REQUIREMENTS.\n" +
			"- Every CURRENT JOB achievement line must include a quantitative result (e.g., %, x, ms, minutes, hours, cost, latency, incidents).\n" +
			"- Output final resume only, no reasoning/planning."
		if len(missingPoints) > 0 {
			revisionInput += "\nMissing requirement coverage points:\n"
			for _, p := range missingPoints {
				revisionInput += "- " + p + "\n"
			}
		}
		if len(missingQuant) > 0 {
			revisionInput += "\nCurrent-job lines missing quantification:\n"
			for _, l := range missingQuant {
				revisionInput += "- " + l + "\n"
			}
		}
		revStart := time.Now()
		adjustedContent2, err2 := client.GenerateContent(ctx, rezumatorPrompt.SystemPrompt, rezumatorPrompt.UserPrompt, revisionInput)
		timings.CreateResume += time.Since(revStart)
		if err2 == nil {
			parsed2, errParse2 := parseAdjustedContent(adjustedContent2)
			if errParse2 == nil {
				missingPoints2 := coverageMissingPoints(parsed2.CurrentJob.Description, reqPoints)
				missingQuant2 := linesMissingQuantification(parsed2.CurrentJob.Description)
				if !isWeakAdjustedResumeOutput(parsed2) && len(missingPoints2) <= len(missingPoints) && len(missingQuant2) <= len(missingQuant) {
					adjustedResume = parsed2
					logInfo("LLM revision accepted (missing_points=%d, missing_quant=%d)", len(missingPoints2), len(missingQuant2))
				}
			}
		} else {
			logInfo("LLM revision failed: %v", err2)
		}
	}

	// Strict LLM-only mode: no deterministic/code-generated resume fallback.
	if isWeakAdjustedResumeOutput(adjustedResume) {
		return AdjustedResume{}, "", timings, fmt.Errorf("llm output still weak/incomplete after revision")
	}

	// Log the cover letter prompts being used
	logInfo("=== COVER LETTER GENERATION ===")
	logInfo("Cover Letter System Prompt length: %d", len(coverLetterPrompt.SystemPrompt))
	logInfo("Cover Letter User Prompt length: %d", len(coverLetterPrompt.UserPrompt))
	logInfo("Cover Letter Input length: %d", len(input))

	fmt.Println("Generating cover letter (LLM)…")

	// Generate cover letter
	coverStart := time.Now()
	coverLetter, err := client.GenerateContent(ctx, coverLetterPrompt.SystemPrompt, coverLetterPrompt.UserPrompt, input)
	timings.CreateCover += time.Since(coverStart)
	if err != nil {
		return AdjustedResume{}, "", timings, fmt.Errorf("error generating cover letter: %v", err)
	}

	// Log the raw cover letter output
	logInfo("Raw cover letter output:\n%s", coverLetter)
	hStart := time.Now()
	humanizedCoverLetter, hs := humanizeCoverLetter(coverLetter)
	timings.HumanizeCover += time.Since(hStart)
	logInfo("HUMANIZER cover_letter_applied=true source=llm em_dash=%d curly_quotes=%d filler_removed=%d transition_replacements=%d whitespace_normalizations=%d",
		hs.EmDashReplacements, hs.CurlyQuoteReplacements, hs.FillerLineRemovals, hs.TransitionReplacements, hs.WhitespaceNormalizations)

	return adjustedResume, humanizedCoverLetter, timings, nil
}

func prepareLLMInput(resumeData ResumeData, jobRequirements JobRequirements, techToAvoid TechnologiesToAvoid) string {
	var input strings.Builder

	input.WriteString("RESUME DATA:\n")
	input.WriteString("Objective: " + resumeData.Objective + "\n\n")

	input.WriteString("Current Job:\n")
	input.WriteString("- Company: " + resumeData.CurrentJob.Company + "\n")
	input.WriteString("- Title: " + resumeData.CurrentJob.Title + "\n")
	input.WriteString("- Description: " + resumeData.CurrentJob.Description + "\n")
	input.WriteString("- Period: " + resumeData.CurrentJob.StartDate + " - " + resumeData.CurrentJob.EndDate + "\n\n")

	input.WriteString("Past Jobs:\n")
	for i, job := range resumeData.PastJobs {
		input.WriteString(fmt.Sprintf("%d. Company: %s\n", i+1, job.Company))
		input.WriteString(fmt.Sprintf("   Title: %s\n", job.Title))
		input.WriteString(fmt.Sprintf("   Description: %s\n", job.Description))
		input.WriteString(fmt.Sprintf("   Period: %s - %s\n\n", job.StartDate, job.EndDate))
	}

	input.WriteString("JOB REQUIREMENTS:\n")
	input.WriteString("- Company: " + jobRequirements.Company + "\n")
	input.WriteString("- Title: " + jobRequirements.Title + "\n")
	input.WriteString("- Description: " + jobRequirements.Description + "\n")

	// Extract and highlight key technologies and skills from job requirements
	keyTechnologies := extractKeyTechnologies(jobRequirements.Description)
	keySkills := extractKeySkills(jobRequirements.Description)

	if len(keyTechnologies) > 0 || len(keySkills) > 0 {
		input.WriteString("\n=== KEY JOB REQUIREMENTS (PRIORITIZE THESE) ===\n")
		if len(keyTechnologies) > 0 {
			input.WriteString("KEY TECHNOLOGIES REQUIRED:\n")
			for _, tech := range keyTechnologies {
				input.WriteString(fmt.Sprintf("- %s\n", tech))
			}
			input.WriteString("\n")
		}
		if len(keySkills) > 0 {
			input.WriteString("KEY SKILLS/EXPERIENCE REQUIRED:\n")
			for _, skill := range keySkills {
				input.WriteString(fmt.Sprintf("- %s\n", skill))
			}
			input.WriteString("\n")
		}
		input.WriteString("CRITICAL: Prioritize these technologies and skills in resume descriptions. Reorder description items so achievements involving these technologies appear FIRST.\n")
		input.WriteString("=== END KEY JOB REQUIREMENTS ===\n\n")
	}

	input.WriteString("TECHNOLOGIES TO AVOID:\n")
	input.WriteString("- Instructions: " + techToAvoid.Instructions + "\n")
	input.WriteString("- Technologies: " + strings.Join(techToAvoid.TechnologiesToAvoid, ", ") + "\n")
	input.WriteString("- Note: For these technologies, either skip mentioning them or say the candidate is eager to learn them. Do not fabricate experience.\n")

	return input.String()
}

// extractKeyTechnologies extracts technologies mentioned in job description
func extractKeyTechnologies(description string) []string {
	description = strings.ToLower(description)
	var technologies []string
	techMap := make(map[string]bool)

	// Common technology patterns
	techPatterns := []string{
		"react", "node.js", "nodejs", "typescript", "javascript", "python", "java", "go", "golang",
		"mongodb", "redis", "postgresql", "mysql", "database",
		"kubernetes", "k8s", "docker", "containers",
		"aws", "azure", "gcp", "google cloud",
		"terraform", "ansible", "puppet", "chef",
		"github actions", "jenkins", "circleci", "ci/cd", "cicd",
		"grafana", "prometheus", "monitoring", "observability",
		"nats", "nats jetstream", "kafka", "rabbitmq", "message queue",
		"traefik", "nginx", "load balancer",
		"llm", "llms", "generative ai", "openai", "azure openai",
		"voice agent", "voice ai", "asr", "tts", "nlu", "nlp",
		"paas", "platform-as-a-service", "full-stack", "full stack",
		"webrtc", "twilio", "sip", "telephony",
	}

	for _, pattern := range techPatterns {
		if strings.Contains(description, pattern) {
			// Capitalize appropriately
			tech := strings.Title(pattern)
			if pattern == "node.js" || pattern == "nodejs" {
				tech = "Node.js"
			} else if pattern == "ci/cd" || pattern == "cicd" {
				tech = "CI/CD"
			} else if pattern == "k8s" {
				tech = "Kubernetes"
			} else if pattern == "llm" || pattern == "llms" {
				tech = "LLMs"
			} else if pattern == "asr" {
				tech = "ASR"
			} else if pattern == "tts" {
				tech = "TTS"
			} else if pattern == "nlu" {
				tech = "NLU"
			} else if pattern == "nlp" {
				tech = "NLP"
			} else if pattern == "paas" {
				tech = "PaaS"
			} else if pattern == "webrtc" {
				tech = "WebRTC"
			} else if pattern == "aws" {
				tech = "AWS"
			} else if pattern == "gcp" {
				tech = "GCP"
			} else if pattern == "azure" {
				tech = "Azure"
			} else if pattern == "github actions" {
				tech = "GitHub Actions"
			} else if pattern == "nats jetstream" {
				tech = "NATS Jetstream"
			} else if pattern == "nats" {
				tech = "NATS"
			} else if pattern == "full-stack" || pattern == "full stack" {
				tech = "Full-stack development"
			} else if pattern == "voice agent" || pattern == "voice ai" {
				tech = "Voice AI/Agents"
			} else if pattern == "generative ai" {
				tech = "Generative AI"
			} else if pattern == "platform-as-a-service" {
				tech = "PaaS"
			}

			if !techMap[tech] {
				technologies = append(technologies, tech)
				techMap[tech] = true
			}
		}
	}

	return technologies
}

// extractKeySkills extracts key skills and experience requirements from job description
func extractKeySkills(description string) []string {
	description = strings.ToLower(description)
	var skills []string
	skillMap := make(map[string]bool)

	// Look for common skill patterns
	skillPatterns := []string{
		"8+ years", "8 years", "years of experience",
		"technical leadership", "team lead", "leadership",
		"full-stack development", "full stack",
		"paas solutions", "platform architecture",
		"voice agent technologies", "voice ai",
		"generative ai tools", "llms", "prompt engineering",
		"ci/cd pipelines", "devops",
		"cloud-native", "cloud native",
		"real-time systems", "real time",
		"scalable", "scalability",
		"distributed systems",
		"microservices",
		"api development", "rest api",
		"agile", "scrum",
	}

	for _, pattern := range skillPatterns {
		if strings.Contains(description, pattern) {
			skill := strings.Title(pattern)
			if pattern == "ci/cd pipelines" {
				skill = "CI/CD Pipelines"
			} else if pattern == "llms" {
				skill = "LLMs"
			} else if pattern == "paas solutions" {
				skill = "PaaS Solutions"
			} else if pattern == "voice agent technologies" {
				skill = "Voice Agent Technologies"
			} else if pattern == "voice ai" {
				skill = "Voice AI"
			} else if pattern == "generative ai tools" {
				skill = "Generative AI Tools"
			} else if pattern == "cloud-native" || pattern == "cloud native" {
				skill = "Cloud-native Architecture"
			} else if pattern == "real-time systems" || pattern == "real time" {
				skill = "Real-time Systems"
			} else if pattern == "full-stack development" || pattern == "full stack" {
				skill = "Full-stack Development"
			} else if pattern == "technical leadership" {
				skill = "Technical Leadership"
			} else if pattern == "team lead" {
				skill = "Team Leadership"
			} else if pattern == "distributed systems" {
				skill = "Distributed Systems"
			} else if pattern == "api development" || pattern == "rest api" {
				skill = "API Development"
			}

			if !skillMap[skill] {
				skills = append(skills, skill)
				skillMap[skill] = true
			}
		}
	}

	return skills
}

func stripBulletPrefix(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return trimmed
	}

	bulletPrefixes := []string{"- ", "• ", "* ", "– ", "— ", "· ", "•\t", "-\t"}
	for _, prefix := range bulletPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			trimmed = strings.TrimSpace(trimmed[len(prefix):])
			break
		}
	}

	return trimmed
}

func stripLeadingEnumeration(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return trimmed
	}

	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}

	if i > 0 {
		for i < len(trimmed) && (trimmed[i] == '.' || trimmed[i] == ')' || trimmed[i] == '-' || trimmed[i] == ':') {
			i++
		}
		trimmed = strings.TrimSpace(trimmed[i:])
	}

	return trimmed
}

func normalizeLineForParsing(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return trimmed
	}
	trimmed = stripBulletPrefix(trimmed)
	trimmed = stripLeadingEnumeration(trimmed)
	return trimmed
}

func extractLabelValue(line string, label string) (string, bool) {
	lineLower := strings.ToLower(line)
	if strings.HasPrefix(lineLower, label) {
		value := strings.TrimSpace(line[len(label):])
		if value == "" {
			return "", false
		}
		return value, true
	}
	return "", false
}

func cleanLabelValue(value string) string {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return cleaned
	}

	labels := []string{"company:", "title:", "description:", "period:", "-"}
	for {
		changed := false
		for _, label := range labels {
			if strings.HasPrefix(strings.ToLower(cleaned), label) {
				cleaned = strings.TrimSpace(cleaned[len(label):])
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	return cleaned
}

func appendDescriptionLine(description *string, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	// If the model accidentally outputs meta-instructions or reasoning inside
	// the Description section, drop those lines so the output contains only achievements.
	if isMetaOrInstructionalDescriptionLine(line) {
		return
	}

	// If the model put multiple sentences on one line, split into separate lines.
	chunks := splitAchievementLineIntoChunks(line)
	if len(chunks) == 0 {
		chunks = []string{line}
	}

	for _, c := range chunks {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if *description != "" {
			*description += "\n"
		}
		*description += c
	}
}

func isMetaOrInstructionalDescriptionLine(line string) bool {
	l := strings.ToLower(strings.TrimSpace(line))
	if l == "" {
		return true
	}
	// Common instruction / reasoning fragments seen in model outputs.
	if strings.Contains(l, "reorder") || strings.Contains(l, "prioritize") {
		return true
	}
	if strings.HasPrefix(l, "also,") || strings.HasPrefix(l, "also ") {
		return true
	}
	if strings.Contains(l, "the job requires") {
		return true
	}
	if strings.Contains(l, "for example") || strings.HasPrefix(l, "example:") {
		return true
	}
	if strings.Contains(l, "better to ") || strings.HasPrefix(l, "better ") {
		return true
	}
	// Responsibility-style / requirements mapping (not achievements).
	if strings.HasPrefix(l, "build a responsible") || strings.HasPrefix(l, "build a responsible and") {
		return true
	}
	if strings.HasPrefix(l, "ensure engineers") || strings.HasPrefix(l, "ensure that") {
		return true
	}
	if strings.HasPrefix(l, "organize knowledge-sharing") || strings.HasPrefix(l, "organize") && strings.Contains(l, "knowledge-sharing") {
		return true
	}
	if strings.HasPrefix(l, "contribute to") && strings.Contains(l, "development") {
		return true
	}
	if strings.Contains(l, "drive adoption of best practices") {
		return true
	}
	if strings.Contains(l, "take part in") && strings.Contains(l, "feedback") {
		return true
	}
	if strings.HasPrefix(l, "i have ") || strings.HasPrefix(l, "i've ") || strings.Contains(l, ": i have") {
		return true
	}
	if strings.Contains(l, "not mentioned") || strings.Contains(l, "not explicit") {
		return true
	}
	if strings.Contains(l, "i cannot") || strings.Contains(l, "i can't") {
		return true
	}
	// Parenthetical notes like "(shows leadership, ...)" are not achievements.
	if strings.HasPrefix(l, "(") {
		return true
	}
	if strings.Contains(l, "i need to") || strings.Contains(l, "i should") || strings.Contains(l, "let me") {
		return true
	}
	if strings.HasPrefix(l, "okay,") || strings.HasPrefix(l, "first,") || strings.HasPrefix(l, "now,") || strings.HasPrefix(l, "next,") {
		return true
	}
	if strings.Contains(l, "must ") || strings.Contains(l, "you must") {
		return true
	}
	return false
}

func normalizeAdjustedJobTitle(title string) string {
	t := strings.TrimSpace(title)
	t = strings.Trim(t, "\"'")
	// Remove parenthetical hints like "(reframed from Senior SRE)".
	if idx := strings.Index(t, "("); idx >= 0 {
		t = strings.TrimSpace(t[:idx])
	}
	l := strings.ToLower(t)

	// Enforce only the allowed target titles.
	if strings.Contains(l, "senior") {
		return "Senior DevOps Engineer"
	}
	if strings.Contains(l, "lead") {
		return "DevOps Lead"
	}
	if strings.Contains(l, "devops") && strings.Contains(l, "engineer") {
		return "Senior DevOps Engineer"
	}
	// Fallback.
	return "DevOps Lead"
}

// normalizeDescriptionToOnePerLine rewrites a paragraph into one sentence/achievement
// chunk per line. This is used when we fall back to the source resume data and need
// to preserve formatting guarantees.
func normalizeDescriptionToOnePerLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}

	chunks := splitAchievementLineIntoChunks(s)
	if len(chunks) == 0 {
		return s
	}

	out := make([]string, 0, len(chunks))
	for _, piece := range chunks {
		piece = strings.TrimSpace(piece)
		if piece == "" || isMetaOrInstructionalDescriptionLine(piece) {
			continue
		}
		out = append(out, piece)
	}

	if len(out) == 0 {
		return s
	}
	return fixDecimalNewlines(strings.Join(out, "\n"))
}

func isLikelyAchievementLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}

	lower := strings.ToLower(line)
	verbs := []string{
		"led", "designed", "built", "implemented", "managed", "directed", "architected", "developed",
		"drove", "coordinated", "created", "automated", "reduced", "improved", "optimized", "delivered",
		"engineered", "migrated", "launched", "enhanced", "introduced", "established", "modernized",
		"secured", "transformed", "scaled", "owned", "owning",
	}

	for _, verb := range verbs {
		if strings.HasPrefix(lower, verb+" ") {
			return true
		}
	}

	return false
}

func shouldSkipDescriptionLine(line string) bool {
	if line == "" {
		return true
	}

	normalized := strings.ToLower(line)
	if normalized == "description" || normalized == "title" || normalized == "company" || normalized == "period" {
		return true
	}

	if strings.HasPrefix(normalized, "description content lines above") {
		return true
	}

	return false
}

func parseAdjustedContentLegacy(content string) (AdjustedResume, error) {
	logInfo("Starting to parse adjusted content. Content length: %d characters", len(content))

	lines := strings.Split(content, "\n")
	logInfo("Content split into %d lines", len(lines))

	var adjustedResume AdjustedResume
	var currentSection string
	var currentJob JobExperience
	var pastJob JobExperience
	var skipUntil int = -1 // Track lines we've already processed

	for i, line := range lines {
		// Skip lines we've already processed in forward-looking loops
		if i < skipUntil {
			continue
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		lowerLine := strings.ToLower(line)
		trimmedLower := strings.TrimSpace(lowerLine)

		// Parse sections - handle both "OBJECTIVE:" and "OBJECTIVE" formats
		// Only match if it's a section header (starts with or is exactly the section name)
		// Don't switch sections if we're currently processing a past job description
		isObjectiveSection := (strings.HasPrefix(trimmedLower, "objective") || trimmedLower == "objective") &&
			!strings.Contains(trimmedLower, "current") && !strings.Contains(trimmedLower, "past") &&
			currentSection != "past_jobs"
		if isObjectiveSection {
			currentSection = "objective"
			logInfo("Detected OBJECTIVE section at line %d: %s", i+1, line)

			// Get the objective text (skip the "OBJECTIVE" line itself)
			var objectiveText strings.Builder
			skipUntil = i + 1
			for j := i + 1; j < len(lines); j++ {
				nextLine := strings.TrimSpace(lines[j])
				nextLower := strings.ToLower(nextLine)

				// Stop if we hit a new section
				if strings.Contains(nextLower, "current job") || strings.Contains(nextLower, "past job") {
					break
				}

				skipUntil = j + 1
				if nextLine != "" {
					if objectiveText.Len() > 0 {
						objectiveText.WriteString(" ")
					}
					objectiveText.WriteString(nextLine)
				}
			}
			adjustedResume.Objective = strings.TrimSpace(objectiveText.String())
			logInfo("Set Objective: '%s' (length: %d)", adjustedResume.Objective[:min(100, len(adjustedResume.Objective))], len(adjustedResume.Objective))

		} else if strings.Contains(lowerLine, "current job") {
			currentSection = "current_job"
			logInfo("Detected CURRENT JOB section at line %d: %s", i+1, line)
			// Save current job if we have one (in case we're switching sections)
			if currentJob.Company != "" {
				adjustedResume.CurrentJob = currentJob
			}
			currentJob = JobExperience{}
			skipUntil = i + 1

		} else if strings.Contains(lowerLine, "past job") {
			currentSection = "past_jobs"
			logInfo("Detected PAST JOBS section at line %d: %s", i+1, line)
			// Save current job if we have one
			if currentJob.Company != "" {
				adjustedResume.CurrentJob = currentJob
				logInfo("Saved current job before switching to past jobs: Company='%s', Title='%s'", currentJob.Company, currentJob.Title)
			}
			currentJob = JobExperience{}
			// Don't skip ahead - let the next iteration process the first past job line
			// Note: skipUntil = i + 1 means we'll skip this line on the next iteration, but we want to process the next line
			// So we should NOT skip ahead - we want to process the first "Company:" line
			skipUntil = i + 1
			logInfo("Set skipUntil to %d, currentSection is now: %s", skipUntil, currentSection)

		} else if currentSection == "objective" {
			// Shouldn't get here due to forward processing, but handle just in case
			if adjustedResume.Objective == "" {
				adjustedResume.Objective = line
			} else {
				adjustedResume.Objective += " " + line
			}

		} else if currentSection == "current_job" {
			normalized := normalizeLineForParsing(line)
			if normalized == "" {
				continue
			}

			lowerNormalized := strings.ToLower(normalized)

			if value, ok := extractLabelValue(normalized, "company:"); ok {
				cleanValue := cleanLabelValue(value)
				if cleanValue != "" {
					currentJob.Company = cleanValue
					logInfo("Current Job - Set Company: '%s'", currentJob.Company)
				}
				continue
			}

			if value, ok := extractLabelValue(normalized, "title:"); ok {
				cleanValue := cleanLabelValue(value)
				if cleanValue != "" {
					currentJob.Title = cleanValue
					logInfo("Current Job - Set Title: '%s'", currentJob.Title)
				}
				continue
			}

			if strings.HasPrefix(lowerNormalized, "description:") {
				skipUntil = i + 1
				continue
			}

			if strings.HasPrefix(lowerNormalized, "period:") {
				continue
			}

			if currentJob.Company == "" && !strings.Contains(normalized, ":") && !isLikelyAchievementLine(normalized) {
				currentJob.Company = normalized
				logInfo("Current Job - Set Company (fallback): '%s'", currentJob.Company)
				continue
			}

			if currentJob.Title == "" && !strings.Contains(normalized, ":") && !isLikelyAchievementLine(normalized) {
				currentJob.Title = normalized
				logInfo("Current Job - Set Title (fallback): '%s'", currentJob.Title)
				continue
			}

			descLine := stripBulletPrefix(line)
			descLine = cleanLabelValue(descLine)
			if descLine == "" || strings.EqualFold(descLine, "period:") {
				continue
			}
			appendDescriptionLine(&currentJob.Description, descLine)
			logInfo("Current Job - Added description line: '%s'", descLine[:min(80, len(descLine))])

		} else if currentSection == "past_jobs" {
			// Parse past jobs
			nextLower := strings.ToLower(line)
			logInfo("Processing past_jobs section at line %d: '%s'", i+1, line)

			// Check if this is a new job - detect multiple formats:
			// 1. Numbered format like "1) Company" or "1. Company"
			// 2. "Company:" label
			// 3. A standalone company name (not a label, not empty, not a description line)
			// 4. Followed by a title on the next line
			// CRITICAL: Only look for new jobs if we're not currently processing descriptions for an existing job
			isCurrentlyProcessingJob := pastJob.Company != "" && pastJob.Title != "" && pastJob.Description != ""

			isNumberedJob := strings.HasPrefix(line, "1)") || strings.HasPrefix(line, "1.") ||
				strings.HasPrefix(line, "2)") || strings.HasPrefix(line, "2.") ||
				strings.HasPrefix(line, "3)") || strings.HasPrefix(line, "3.") ||
				strings.HasPrefix(line, "1. Company:") || strings.HasPrefix(line, "2. Company:") || strings.HasPrefix(line, "3. Company:")

			isCompanyLabel := strings.TrimSpace(nextLower) == "company:" ||
				strings.HasPrefix(strings.TrimSpace(nextLower), "company:")

			// Check if this looks like a standalone company name:
			// - Not a label (doesn't contain ":")
			// - Not empty
			// - Not a description line (doesn't start with bullet points, doesn't contain "description")
			// - The next line looks like a title (doesn't contain ":", not empty, not a label)
			// - Only check if we're not currently processing a job
			isStandaloneCompany := !isCurrentlyProcessingJob &&
				!strings.Contains(line, ":") &&
				strings.TrimSpace(line) != "" &&
				!strings.HasPrefix(strings.TrimSpace(line), "-") &&
				!strings.HasPrefix(strings.TrimSpace(line), "•") &&
				!strings.Contains(nextLower, "description") &&
				i+1 < len(lines) &&
				!strings.Contains(strings.ToLower(lines[i+1]), ":") &&
				strings.TrimSpace(lines[i+1]) != "" &&
				!strings.Contains(strings.ToLower(lines[i+1]), "description") &&
				// Additional check: ensure this doesn't look like a description
				!strings.Contains(strings.ToLower(line), "developed") &&
				!strings.Contains(strings.ToLower(line), "managed") &&
				!strings.Contains(strings.ToLower(line), "implemented") &&
				!strings.Contains(strings.ToLower(line), "built") &&
				!strings.Contains(strings.ToLower(line), "engineered") &&
				!strings.Contains(strings.ToLower(line), "led") &&
				!strings.Contains(strings.ToLower(line), "designed") &&
				// Most importantly: only treat as new company if we don't have a current job
				pastJob.Company == ""

			isNewJob := isNumberedJob || isCompanyLabel || isStandaloneCompany

			if isNewJob {
				// Save previous past job if exists
				if pastJob.Company != "" {
					adjustedResume.PastJobs = append(adjustedResume.PastJobs, pastJob)
					logInfo("Saved past job: Company='%s', Title='%s'", pastJob.Company, pastJob.Title)
				}
				pastJob = JobExperience{}

				// Extract company name
				if strings.TrimSpace(nextLower) == "company:" {
					// Company name is on the next line - look ahead
					if i+1 < len(lines) {
						nextLine := strings.TrimSpace(lines[i+1])
						if nextLine != "" && !strings.Contains(strings.ToLower(nextLine), "title:") {
							pastJob.Company = nextLine
							skipUntil = i + 2 // Skip both "Company:" and the company name line
							logInfo("Past Job - Set Company from next line: '%s'", pastJob.Company)
						}
					}
				} else if strings.HasPrefix(strings.TrimSpace(nextLower), "company:") {
					// Company name is on the same line as "Company:" (e.g., "Company: Veeam")
					pastJob.Company = strings.TrimSpace(strings.TrimPrefix(line, "Company:"))
					pastJob.Company = strings.TrimSpace(strings.TrimPrefix(pastJob.Company, "company:"))
					pastJob.Company = strings.TrimSpace(strings.TrimPrefix(pastJob.Company, "Company:"))
					logInfo("Past Job - Set Company from same line: '%s'", pastJob.Company)
				} else if strings.Contains(nextLower, "company:") {
					// Fallback: Company name might be on the same line
					pastJob.Company = strings.TrimSpace(strings.TrimPrefix(line, "Company:"))
					pastJob.Company = strings.TrimSpace(strings.TrimPrefix(pastJob.Company, "company:"))
					pastJob.Company = strings.TrimSpace(strings.TrimPrefix(pastJob.Company, "Company:"))

					// Also handle numbered prefixes that might come before "Company:"
					if strings.HasPrefix(pastJob.Company, "1.") {
						pastJob.Company = strings.TrimSpace(strings.TrimPrefix(pastJob.Company, "1."))
					} else if strings.HasPrefix(pastJob.Company, "2.") {
						pastJob.Company = strings.TrimSpace(strings.TrimPrefix(pastJob.Company, "2."))
					} else if strings.HasPrefix(pastJob.Company, "3.") {
						pastJob.Company = strings.TrimSpace(strings.TrimPrefix(pastJob.Company, "3."))
					}

					logInfo("Past Job - Set Company (fallback): '%s'", pastJob.Company)
				} else if isStandaloneCompany {
					// Handle standalone company name (current LLM output format)
					pastJob.Company = strings.TrimSpace(line)
					logInfo("Past Job - Set Company from standalone format: '%s'", pastJob.Company)
				} else {
					// Handle numbered format (e.g., "1. Company: Veeam" or "1) Veeam")
					companyText := line
					logInfo("Processing numbered format - original: '%s'", companyText)

					// Remove numbered prefixes
					if strings.Contains(companyText, ")") {
						parts := strings.SplitN(companyText, ")", 2)
						if len(parts) > 1 {
							companyText = strings.TrimSpace(parts[1])
						}
					} else if strings.Contains(companyText, ".") && !strings.HasPrefix(companyText, "•") {
						parts := strings.SplitN(companyText, ".", 2)
						if len(parts) > 1 {
							companyText = strings.TrimSpace(parts[1])
						}
					}

					// Remove "Company:" or "company:" prefixes if present
					if strings.HasPrefix(strings.ToLower(companyText), "company:") {
						companyText = strings.TrimSpace(strings.TrimPrefix(companyText, "Company:"))
						companyText = strings.TrimSpace(strings.TrimPrefix(companyText, "company:"))
					}

					pastJob.Company = strings.TrimSpace(companyText)
					logInfo("Past Job - Set Company from numbered format: '%s' (cleaned from: '%s')", pastJob.Company, line)
				}
			} else if strings.TrimSpace(nextLower) == "title:" {
				// Title label on its own line - look ahead for title
				if i+1 < len(lines) {
					nextLine := strings.TrimSpace(lines[i+1])
					if nextLine != "" && !strings.Contains(strings.ToLower(nextLine), "description:") {
						pastJob.Title = nextLine
						skipUntil = i + 2 // Skip both "Title:" and the title line
						logInfo("Past Job - Set Title from next line: '%s'", pastJob.Title)
					}
				}
			} else if strings.TrimSpace(nextLower) == "description:" {
				// Description label - skip only this label line, description lines follow
				// The description lines will be processed in the next iteration
				skipUntil = i + 1
				// Continue processing - don't break out, let description lines be collected
				continue
			} else if pastJob.Company != "" {
				// We're inside a job block
				if strings.Contains(nextLower, "title:") && strings.TrimSpace(nextLower) != "title:" {
					// Title on same line
					pastJob.Title = strings.TrimSpace(strings.TrimPrefix(line, "Title:"))
					pastJob.Title = strings.TrimSpace(strings.TrimPrefix(pastJob.Title, "title:"))
					logInfo("Past Job - Set Title from same line: '%s'", pastJob.Title)
				} else if pastJob.Title == "" &&
					// This is the title line after we set a company
					pastJob.Company != "" &&
					!strings.HasPrefix(line, "-") &&
					!strings.HasPrefix(line, "•") &&
					!strings.Contains(nextLower, "description:") &&
					!strings.Contains(nextLower, "company:") &&
					!strings.Contains(nextLower, "title:") &&
					// Make sure this isn't a new company (check if next line after this looks like a description or "Description:")
					(i+1 >= len(lines) || strings.Contains(strings.ToLower(lines[i+1]), "description") || strings.TrimSpace(lines[i+1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[i+1]), "-") || strings.HasPrefix(strings.TrimSpace(lines[i+1]), "•")) {
					// First non-bullet line after company is the title (if not already set)
					pastJob.Title = line
					logInfo("Past Job - Set Title as first line: '%s'", pastJob.Title)
				} else if !strings.Contains(nextLower, "description:") && !strings.Contains(nextLower, "company:") && !strings.Contains(nextLower, "title:") {
					// Description lines - check if we're past the description label
					// Stop only if we hit an actual section header (not just any occurrence of the word)
					// Check if line starts with section headers (strict matching)
					isSectionHeader := (strings.HasPrefix(trimmedLower, "objective") || trimmedLower == "objective") ||
						strings.HasPrefix(trimmedLower, "current job") ||
						strings.HasPrefix(trimmedLower, "past job") ||
						strings.HasPrefix(trimmedLower, "cover letter")
					shouldStop := isSectionHeader

					if !shouldStop {
						if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "•") {
							descLine := strings.TrimSpace(strings.TrimPrefix(line, "-"))
							descLine = strings.TrimSpace(strings.TrimPrefix(descLine, "•"))
							if descLine != "" {
								if pastJob.Description != "" {
									pastJob.Description += " "
								}
								pastJob.Description += descLine
								logInfo("Past Job - Added description line: '%s'", descLine[:min(50, len(descLine))])
							}
						} else if line != "" && !strings.Contains(nextLower, "technology") && !strings.Contains(nextLower, "skill") && !strings.Contains(nextLower, "note") {
							// Regular text, but stop at technology/skills sections or new jobs
							// Check if this looks like a new company line
							if strings.HasPrefix(nextLower, "company:") || (i+1 < len(lines) && strings.Contains(strings.ToLower(strings.TrimSpace(lines[i+1])), "company:")) {
								// This might be a new job, save current one
								if pastJob.Company != "" && pastJob.Title != "" {
									adjustedResume.PastJobs = append(adjustedResume.PastJobs, pastJob)
									logInfo("Saved past job before new company: Company='%s', Title='%s'", pastJob.Company, pastJob.Title)
									pastJob = JobExperience{}
								}
							} else {
								if pastJob.Description != "" {
									pastJob.Description += " "
								}
								pastJob.Description += line
								logInfo("Past Job - Added description text: '%s'", line[:min(50, len(line))])
							}
						}
					}
				}
			}
		}
	}

	// Save any remaining jobs
	if currentJob.Company != "" {
		adjustedResume.CurrentJob = currentJob
		logInfo("Saved final current job: Company='%s', Title='%s'", currentJob.Company, currentJob.Title)
	}
	if pastJob.Company != "" {
		adjustedResume.PastJobs = append(adjustedResume.PastJobs, pastJob)
		logInfo("Saved final past job: Company='%s', Title='%s'", pastJob.Company, pastJob.Title)
	}

	// Clean up any "Description:" prefix that might have been included in description text
	if strings.HasPrefix(strings.TrimSpace(adjustedResume.CurrentJob.Description), "Description:") {
		adjustedResume.CurrentJob.Description = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(adjustedResume.CurrentJob.Description), "Description:"))
	}
	for i := range adjustedResume.PastJobs {
		if strings.HasPrefix(strings.TrimSpace(adjustedResume.PastJobs[i].Description), "Description:") {
			adjustedResume.PastJobs[i].Description = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(adjustedResume.PastJobs[i].Description), "Description:"))
		}
	}

	// Log final parsed result
	logInfo("=== PARSING COMPLETE ===")
	logInfo("Parsed Objective length: %d", len(adjustedResume.Objective))
	logInfo("Parsed CurrentJob - Company: '%s', Title: '%s', Description length: %d",
		adjustedResume.CurrentJob.Company, adjustedResume.CurrentJob.Title, len(adjustedResume.CurrentJob.Description))
	logInfo("Parsed PastJobs count: %d", len(adjustedResume.PastJobs))

	return adjustedResume, nil
}

func parseAdjustedContent(content string) (AdjustedResume, error) {
	logInfo("Starting to parse adjusted content. Content length: %d characters", len(content))

	lines := strings.Split(content, "\n")
	logInfo("Content split into %d lines", len(lines))

	var adjustedResume AdjustedResume
	var currentSection string
	var currentJob JobExperience
	var pastJob JobExperience
	var pendingCurrentLabel string
	var pendingPastLabel string

	finalizeCurrentJob := func() {
		if currentJob.Company == "" && currentJob.Title == "" && currentJob.Description == "" {
			return
		}
		currentJob.Description = strings.TrimSpace(currentJob.Description)
		adjustedResume.CurrentJob = currentJob
		logInfo("Saved current job: Company='%s', Title='%s'", currentJob.Company, currentJob.Title)
	}

	finalizePastJob := func() {
		if pastJob.Company == "" && pastJob.Title == "" && pastJob.Description == "" {
			return
		}
		pastJob.Description = strings.TrimSpace(pastJob.Description)
		adjustedResume.PastJobs = append(adjustedResume.PastJobs, pastJob)
		logInfo("Saved past job: Company='%s', Title='%s'", pastJob.Company, pastJob.Title)
		pastJob = JobExperience{}
	}

	for i, rawLine := range lines {
		line := strings.TrimSpace(rawLine)

		// OBJECTIVE: full text on the same line (common model pattern)
		if m := reObjectiveInline.FindStringSubmatch(line); len(m) == 2 {
			currentSection = "objective"
			if rest := strings.TrimSpace(m[1]); rest != "" {
				if adjustedResume.Objective != "" {
					adjustedResume.Objective += " "
				}
				adjustedResume.Objective += rest
			}
			continue
		}

		if line == "" {
			continue
		}

		trimmedLower := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(line, ":")))
		trimmedLower = strings.Trim(trimmedLower, "`")

		// Detect headers only when the line is exactly the header.
		// This prevents the parser from being thrown off by reasoning text like:
		// "For the current job at Microsoft, ..."
		if trimmedLower == "objective" {
			logInfo("Detected OBJECTIVE header at line %d", i+1)
			currentSection = "objective"
			continue
		}

		if trimmedLower == "current job" {
			logInfo("Detected CURRENT JOB header at line %d", i+1)
			currentSection = "current_job"
			currentJob = JobExperience{}
			continue
		}

		if trimmedLower == "past jobs" || trimmedLower == "past job" {
			logInfo("Detected PAST JOBS header at line %d", i+1)
			finalizeCurrentJob()
			pastJob = JobExperience{}
			currentSection = "past_jobs"
			continue
		}

		switch currentSection {
		case "objective":
			if adjustedResume.Objective != "" {
				adjustedResume.Objective += " "
			}
			adjustedResume.Objective += line

		case "current_job":
			normalized := normalizeLineForParsing(line)
			if normalized == "" {
				continue
			}
			lowerNormalized := strings.ToLower(normalized)

			if pendingCurrentLabel != "" {
				switch pendingCurrentLabel {
				case "company":
					value := cleanLabelValue(line)
					if !strings.EqualFold(value, "company") && value != "" {
						currentJob.Company = value
						logInfo("Current Job - Set Company (pending): '%s'", currentJob.Company)
					}
				case "title":
					value := cleanLabelValue(line)
					if !strings.EqualFold(value, "title") && value != "" {
						currentJob.Title = value
						logInfo("Current Job - Set Title (pending): '%s'", currentJob.Title)
					}
				case "description":
					descLine := stripBulletPrefix(line)
					descLine = cleanLabelValue(descLine)
					if !shouldSkipDescriptionLine(descLine) {
						appendDescriptionLine(&currentJob.Description, descLine)
						logInfo("Current Job - Added description line (pending): '%s'", descLine[:min(80, len(descLine))])
					}
				}
				pendingCurrentLabel = ""
				continue
			}

			if value, ok := extractLabelValue(normalized, "company:"); ok {
				cleanValue := cleanLabelValue(value)
				if cleanValue != "" {
					currentJob.Company = cleanValue
					logInfo("Current Job - Set Company: '%s'", currentJob.Company)
				}
				continue
			}

			if value, ok := extractLabelValue(normalized, "title:"); ok {
				cleanValue := cleanLabelValue(value)
				if cleanValue != "" {
					currentJob.Title = cleanValue
					logInfo("Current Job - Set Title: '%s'", currentJob.Title)
				}
				continue
			}

			if strings.HasPrefix(lowerNormalized, "description:") {
				pendingCurrentLabel = "description"
				continue
			}

			if strings.HasPrefix(lowerNormalized, "period:") {
				continue
			}

			if strings.EqualFold(normalized, "company") {
				pendingCurrentLabel = "company"
				continue
			}

			if strings.EqualFold(normalized, "title") {
				pendingCurrentLabel = "title"
				continue
			}

			if strings.EqualFold(normalized, "description") {
				pendingCurrentLabel = "description"
				continue
			}

			if currentJob.Company == "" && !strings.Contains(normalized, ":") && !isLikelyAchievementLine(normalized) {
				currentJob.Company = normalized
				logInfo("Current Job - Set Company (fallback): '%s'", currentJob.Company)
				continue
			}

			if currentJob.Title == "" && !strings.Contains(normalized, ":") && !isLikelyAchievementLine(normalized) {
				currentJob.Title = normalized
				logInfo("Current Job - Set Title (fallback): '%s'", currentJob.Title)
				continue
			}

			descLine := stripBulletPrefix(line)
			descLine = cleanLabelValue(descLine)
			if descLine == "" || strings.EqualFold(descLine, "period:") {
				continue
			}
			appendDescriptionLine(&currentJob.Description, descLine)
			logInfo("Current Job - Added description line: '%s'", descLine[:min(80, len(descLine))])

		case "past_jobs":
			normalized := normalizeLineForParsing(line)
			if normalized == "" {
				continue
			}
			lowerNormalized := strings.ToLower(normalized)

			if pendingPastLabel != "" {
				switch pendingPastLabel {
				case "company":
					value := cleanLabelValue(line)
					if !strings.EqualFold(value, "company") && value != "" {
						pastJob.Company = value
						logInfo("Past Job - Set Company (pending): '%s'", pastJob.Company)
					}
				case "title":
					value := cleanLabelValue(line)
					if !strings.EqualFold(value, "title") && value != "" {
						pastJob.Title = value
						logInfo("Past Job - Set Title (pending): '%s'", pastJob.Title)
					}
				case "description":
					descLine := stripBulletPrefix(line)
					descLine = cleanLabelValue(descLine)
					if !shouldSkipDescriptionLine(descLine) {
						appendDescriptionLine(&pastJob.Description, descLine)
						logInfo("Past Job - Added description text (pending): '%s'", descLine[:min(50, len(descLine))])
					}
				}
				pendingPastLabel = ""
				continue
			}

			if value, ok := extractLabelValue(normalized, "company:"); ok {
				finalizePastJob()
				pastJob = JobExperience{}

				cleanValue := cleanLabelValue(value)
				if cleanValue != "" {
					pastJob.Company = cleanValue
					logInfo("Past Job - Set Company: '%s'", pastJob.Company)
				}
				continue
			}

			if value, ok := extractLabelValue(normalized, "title:"); ok {
				cleanValue := cleanLabelValue(value)
				if cleanValue != "" {
					pastJob.Title = cleanValue
					logInfo("Past Job - Set Title: '%s'", pastJob.Title)
				}
				continue
			}

			if strings.HasPrefix(lowerNormalized, "description:") ||
				strings.HasPrefix(lowerNormalized, "responsibilities:") {
				pendingPastLabel = "description"
				continue
			}

			if strings.HasPrefix(lowerNormalized, "period:") {
				continue
			}

			if strings.EqualFold(normalized, "company") {
				finalizePastJob()
				pendingPastLabel = "company"
				continue
			}

			if strings.EqualFold(normalized, "title") {
				pendingPastLabel = "title"
				continue
			}

			if strings.EqualFold(normalized, "description") {
				pendingPastLabel = "description"
				continue
			}

			if pastJob.Company == "" && !strings.Contains(normalized, ":") && !isLikelyAchievementLine(normalized) {
				pastJob.Company = normalized
				logInfo("Past Job - Set Company (fallback): '%s'", pastJob.Company)
				continue
			}

			if pastJob.Company != "" && pastJob.Title == "" && !strings.Contains(normalized, ":") && !isLikelyAchievementLine(normalized) {
				pastJob.Title = normalized
				logInfo("Past Job - Set Title (fallback): '%s'", pastJob.Title)
				continue
			}

			if pastJob.Company == "" {
				continue
			}

			descLine := stripBulletPrefix(line)
			descLine = cleanLabelValue(descLine)
			if descLine == "" || strings.EqualFold(descLine, "period:") || shouldSkipDescriptionLine(descLine) {
				continue
			}
			appendDescriptionLine(&pastJob.Description, descLine)
			logInfo("Past Job - Added description text: '%s'", descLine[:min(50, len(descLine))])
		}
	}

	finalizeCurrentJob()
	finalizePastJob()

	// Fallback extraction for cases where the model produced a "reasoning-like" output
	// (e.g. "Now, write the objective: ..." and "Then current job: Company: ..., Title: ...")
	// instead of strict headers. This keeps the app from returning empty resume sections.
	if strings.TrimSpace(adjustedResume.Objective) == "" || strings.TrimSpace(adjustedResume.CurrentJob.Company) == "" || len(adjustedResume.PastJobs) == 0 {
		// Line-based extraction is more robust for reasoning-style drafts.
		lines := strings.Split(content, "\n")
		contentLower := strings.ToLower(content)
		// pastTitleByCompany is used when the draft gives:
		// "Past jobs: Veeam: "DevOps Engineer"... Crossover: "DevOps Support Engineer"."
		// but the detailed sections use only "For <Company>:" without Title fields.
		pastTitleByCompany := map[string]string{}
		if len(adjustedResume.PastJobs) == 0 && strings.Contains(contentLower, "past jobs:") {
			reCompanyTitle := regexp.MustCompile(`(?i)\b([A-Za-z][A-Za-z0-9&\\-\\s]+?)\s*:\s*(?:\"|“)(.+?)(?:\"|”)`)
			idx := strings.Index(contentLower, "past jobs:")
			if idx >= 0 && idx < len(content) {
				sub := content[idx:]
				matches := reCompanyTitle.FindAllStringSubmatch(sub, -1)
				for _, mm := range matches {
					if len(mm) == 3 {
						company := strings.TrimSpace(mm[1])
						title := strings.TrimSpace(mm[2])
						if company != "" && title != "" {
							pastTitleByCompany[company] = title
						}
					}
				}
			}
		}

		// 1) Objective extraction (common patterns in reasoning output):
		// - "Let me draft the objective first: "....""
		// - "objective first: "....""
		if strings.TrimSpace(adjustedResume.Objective) == "" {
			reQuote := regexp.MustCompile(`(?is)(?:\"|“)(.+?)(?:\"|”)`)
			for i := 0; i < len(lines); i++ {
				l := lines[i]
				lo := strings.ToLower(l)
				if strings.Contains(lo, "objective first") || strings.Contains(lo, "objective:") || strings.Contains(lo, "write the objective") {
					// 1) Try quoted objective on the same line.
					if m := reQuote.FindStringSubmatch(l); len(m) == 2 {
						adjustedResume.Objective = strings.TrimSpace(m[1])
						break
					}
					// 2) Otherwise, objective is often on the next line(s).
					for k := i + 1; k < len(lines) && k < i+5; k++ {
						if m := reQuote.FindStringSubmatch(lines[k]); len(m) == 2 {
							adjustedResume.Objective = strings.TrimSpace(m[1])
							break
						}
					}
					if strings.TrimSpace(adjustedResume.Objective) != "" {
						break
					}
				}
			}
		}

		// 2) Current job extraction:
		// Look for a section like "Now, for CURRENT JOB at Microsoft:" then parse "- Company:", "- Title:", "- Description:"
		if strings.TrimSpace(adjustedResume.CurrentJob.Company) == "" || strings.TrimSpace(adjustedResume.CurrentJob.Title) == "" || strings.TrimSpace(adjustedResume.CurrentJob.Description) == "" {
			// Try single-line format first:
			// "Then current job: Company: Microsoft, Title: ..., Description: ..."
			reCJLine := regexp.MustCompile(`(?is)(?:then\s*)?current job:\s*Company:\s*(.+?),\s*Title:\s*(.+?)\s*(?:\(|\n|$).+?Description:\s*(.+?)\s*(?:\n|$)`)
			if strings.TrimSpace(adjustedResume.CurrentJob.Company) == "" || strings.TrimSpace(adjustedResume.CurrentJob.Title) == "" {
				if m := reCJLine.FindStringSubmatch(content); len(m) == 4 {
					company := strings.TrimSpace(m[1])
					title := strings.TrimSpace(m[2])
					desc := strings.TrimSpace(m[3])
					if company != "" && strings.TrimSpace(adjustedResume.CurrentJob.Company) == "" {
						adjustedResume.CurrentJob.Company = company
					}
					if title != "" && strings.TrimSpace(adjustedResume.CurrentJob.Title) == "" {
						adjustedResume.CurrentJob.Title = title
					}
					if desc != "" && strings.TrimSpace(adjustedResume.CurrentJob.Description) == "" {
						adjustedResume.CurrentJob.Description = desc
					}
				} else {
					// Less strict variant without parentheses.
					reCJLine2 := regexp.MustCompile(`(?is)(?:then\s*)?current job:\s*Company:\s*(.+?),\s*Title:\s*(.+?)\s*Description:\s*(.+?)\s*(?:\n|$)`)
					if m2 := reCJLine2.FindStringSubmatch(content); len(m2) == 4 {
						company := strings.TrimSpace(m2[1])
						title := strings.TrimSpace(m2[2])
						desc := strings.TrimSpace(m2[3])
						if company != "" && strings.TrimSpace(adjustedResume.CurrentJob.Company) == "" {
							adjustedResume.CurrentJob.Company = company
						}
						if title != "" && strings.TrimSpace(adjustedResume.CurrentJob.Title) == "" {
							adjustedResume.CurrentJob.Title = title
						}
						if desc != "" && strings.TrimSpace(adjustedResume.CurrentJob.Description) == "" {
							adjustedResume.CurrentJob.Description = desc
						}
					}
				}
			}

			for i := 0; i < len(lines); i++ {
				lo := strings.ToLower(strings.TrimSpace(lines[i]))
				if strings.Contains(lo, "current job") && (strings.Contains(lo, "at ") || strings.Contains(lo, "at") || strings.Contains(lo, ":")) {
					// Parse nearby bullet labels.
					var company, title, desc string
					for j := i + 1; j < len(lines) && j < i+80; j++ {
						ln := strings.TrimSpace(lines[j])
						if ln == "" {
							continue
						}
						lnLower := strings.ToLower(ln)
						if strings.Contains(lnLower, "past jobs") {
							break
						}
						if company == "" {
							if m := regexp.MustCompile(`(?i)^\s*-\s*company:\s*(.+?)\s*$`).FindStringSubmatch(lines[j]); len(m) == 2 {
								company = strings.TrimSpace(m[1])
								continue
							}
						}
						if title == "" {
							if m := regexp.MustCompile(`(?i)^\s*-\s*title:\s*(.+?)\s*$`).FindStringSubmatch(lines[j]); len(m) == 2 {
								title = strings.TrimSpace(m[1])
								continue
							}
						}
						if desc == "" {
							// Description can be "- Description: ..." or "- Description reorder: ..."
							if m := regexp.MustCompile(`(?i)^\s*-\s*description\s*[^:]*:\s*(.+?)\s*$`).FindStringSubmatch(lines[j]); len(m) == 2 {
								desc = strings.TrimSpace(m[1])
								continue
							}
						} else {
							// Continue accumulating description lines after the first "- Description: ..." line
							// until we hit the next major section.
							if strings.Contains(strings.ToLower(ln), "for past jobs") || strings.Contains(strings.ToLower(ln), "past jobs") {
								break
							}
							// Keep indented / bullet / numbered lines.
							if strings.HasPrefix(ln, "-") || regexp.MustCompile(`(?m)^\s*\d+\.\s*`).MatchString(lines[j]) {
								desc = strings.TrimSpace(desc + "\n" + ln)
							}
						}
					}

					if company != "" && strings.TrimSpace(adjustedResume.CurrentJob.Company) == "" {
						adjustedResume.CurrentJob.Company = company
					}
					if title != "" && strings.TrimSpace(adjustedResume.CurrentJob.Title) == "" {
						adjustedResume.CurrentJob.Title = title
					}
					if desc != "" && strings.TrimSpace(adjustedResume.CurrentJob.Description) == "" {
						adjustedResume.CurrentJob.Description = desc
					}
					// If we found something useful, stop.
					if adjustedResume.CurrentJob.Company != "" && adjustedResume.CurrentJob.Title != "" && adjustedResume.CurrentJob.Description != "" {
						break
					}
				}
			}
		}

		// 3) Past jobs extraction (reasoning draft style):
		// Look for "For PAST JOBS:" then parse numbered blocks: "1. Veeam:" followed by "- Title:" and "- Description: ..." / "- Description reorder:".
		if len(adjustedResume.PastJobs) == 0 {
			inPast := false
			var cur JobExperience
			haveCompany := false
			haveDesc := false

			flush := func() {
				if haveCompany && strings.TrimSpace(cur.Company) != "" && haveDesc && strings.TrimSpace(cur.Description) != "" {
					adjustedResume.PastJobs = append(adjustedResume.PastJobs, cur)
				}
				cur = JobExperience{}
				haveCompany = false
				haveDesc = false
			}

			for _, l := range lines {
				ln := strings.TrimSpace(l)
				lnLower := strings.ToLower(ln)
				if !inPast {
					if strings.Contains(lnLower, "for past jobs") || (lnLower == "past jobs:" || lnLower == "past jobs") {
						inPast = true
					}
					continue
				}

				// Stop if we reached the next unrelated section.
				if strings.Contains(lnLower, "now, for the objective") || strings.Contains(lnLower, "now, for the technologies") || strings.Contains(lnLower, "now, for the formatting") {
					flush()
					break
				}

				if ln == "" {
					continue
				}

				// Company marker: "1. Veeam:" or "2. Crossover:"
				if m := regexp.MustCompile(`^\s*(\d+)\.\s*([^:]+):\s*$`).FindStringSubmatch(l); len(m) == 3 {
					flush()
					cur.Company = strings.TrimSpace(m[2])
					haveCompany = cur.Company != ""
					continue
				}

				if !haveCompany {
					continue
				}

				// Title line
				if cur.Title == "" {
					if m := regexp.MustCompile(`(?i)^\s*-\s*title:\s*(.+?)\s*$`).FindStringSubmatch(l); len(m) == 2 {
						cur.Title = strings.TrimSpace(m[1])
						continue
					}
				}

				// Description line (supports "- Description:" and "- Description reorder:" etc.)
				if !haveDesc {
					if m := regexp.MustCompile(`(?i)^\s*-\s*description[^:]*:\s*(.+?)\s*$`).FindStringSubmatch(l); len(m) == 2 {
						cur.Description = strings.TrimSpace(m[1])
						haveDesc = cur.Description != ""
						continue
					}
				} else {
					// Keep subsequent bullet/number lines as continuation of the description.
					if strings.HasPrefix(strings.TrimSpace(l), "-") || regexp.MustCompile(`^\s*\d+\.\s*`).MatchString(l) {
						cur.Description = strings.TrimSpace(cur.Description + "\n" + strings.TrimSpace(l))
					}
				}
			}

			flush()
		}

		// 4) If we still don't have past jobs, use a simpler heuristic:
		// Find a top-level "Description:" label and then treat "For <Company>:" segments as past jobs.
		if len(adjustedResume.PastJobs) == 0 {
			descIdx := -1
			lines2 := lines
			for idx, l := range lines2 {
				if strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(l, ":")), "description") {
					descIdx = idx
					break
				}
			}
			if descIdx >= 0 && descIdx+1 < len(lines2) {
				rePastMarker := regexp.MustCompile(`(?i)^for\s+(.+?):\s*$`)
				var curPast *JobExperience
				for _, l := range lines2[descIdx+1:] {
					trimmed := strings.TrimSpace(l)
					if trimmed == "" {
						continue
					}
					if mm := rePastMarker.FindStringSubmatch(trimmed); len(mm) == 2 {
						if curPast != nil && strings.TrimSpace(curPast.Description) != "" {
							adjustedResume.PastJobs = append(adjustedResume.PastJobs, *curPast)
						}
						curPast = &JobExperience{Company: strings.TrimSpace(mm[1])}
						// If we have title info from the earlier "Past jobs:" summary line, use it.
						if t, ok := pastTitleByCompany[curPast.Company]; ok {
							curPast.Title = t
						}
						continue
					}
					if curPast != nil {
						curPast.Description = strings.TrimSpace(curPast.Description + "\n" + trimmed)
					}
				}
				if curPast != nil && strings.TrimSpace(curPast.Description) != "" {
					adjustedResume.PastJobs = append(adjustedResume.PastJobs, *curPast)
				}
			}
		}
	}

	adjustedResume.Objective = strings.TrimSpace(adjustedResume.Objective)

	if strings.HasPrefix(strings.TrimSpace(adjustedResume.CurrentJob.Description), "Description:") {
		adjustedResume.CurrentJob.Description = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(adjustedResume.CurrentJob.Description), "Description:"))
	}
	adjustedResume.CurrentJob.Description = strings.TrimSpace(adjustedResume.CurrentJob.Description)
	adjustedResume.CurrentJob.Description = fixDecimalNewlines(adjustedResume.CurrentJob.Description)

	for i := range adjustedResume.PastJobs {
		if strings.HasPrefix(strings.TrimSpace(adjustedResume.PastJobs[i].Description), "Description:") {
			adjustedResume.PastJobs[i].Description = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(adjustedResume.PastJobs[i].Description), "Description:"))
		}
		adjustedResume.PastJobs[i].Description = strings.TrimSpace(adjustedResume.PastJobs[i].Description)
		adjustedResume.PastJobs[i].Description = fixDecimalNewlines(adjustedResume.PastJobs[i].Description)
	}

	// Enforce allowed adjusted titles (no parenthetical explanations).
	adjustedResume.CurrentJob.Title = normalizeAdjustedJobTitle(adjustedResume.CurrentJob.Title)
	for i := range adjustedResume.PastJobs {
		adjustedResume.PastJobs[i].Title = normalizeAdjustedJobTitle(adjustedResume.PastJobs[i].Title)
	}

	logInfo("=== PARSING COMPLETE ===")
	logInfo("Parsed Objective length: %d", len(adjustedResume.Objective))
	logInfo("Parsed CurrentJob - Company: '%s', Title: '%s', Description length: %d",
		adjustedResume.CurrentJob.Company, adjustedResume.CurrentJob.Title, len(adjustedResume.CurrentJob.Description))
	logInfo("Parsed PastJobs count: %d", len(adjustedResume.PastJobs))

	return adjustedResume, nil
}

// Helper function for min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func printResults(adjustedResume AdjustedResume, coverLetter string) {
	logInfo("=== PRINTING RESULTS ===")
	logInfo("About to print - Objective length: %d", len(adjustedResume.Objective))
	logInfo("About to print - CurrentJob.Company: '%s', Title: '%s', Description length: %d",
		adjustedResume.CurrentJob.Company, adjustedResume.CurrentJob.Title, len(adjustedResume.CurrentJob.Description))
	logInfo("About to print - PastJobs count: %d", len(adjustedResume.PastJobs))

	fmt.Println("=== ADJUSTED RESUME ===")
	fmt.Println()

	fmt.Println("OBJECTIVE:")
	fmt.Println(adjustedResume.Objective)
	fmt.Println()

	fmt.Println("CURRENT JOB:")
	fmt.Printf("Company: %s\n", adjustedResume.CurrentJob.Company)
	fmt.Printf("Title: %s\n", adjustedResume.CurrentJob.Title)
	fmt.Println("Description:")
	description := adjustedResume.CurrentJob.Description
	if strings.HasPrefix(strings.TrimSpace(description), "Description:") {
		description = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(description), "Description:"))
	}
	description = fixDecimalNewlines(description)
	lines := strings.Split(description, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			fmt.Println(line)
		}
	}
	fmt.Println()

	fmt.Println("PAST JOBS:")
	for _, job := range adjustedResume.PastJobs {
		fmt.Printf("Company: %s\n", job.Company)
		fmt.Printf("Title: %s\n", job.Title)
		fmt.Println("Description:")
		description := job.Description
		if strings.HasPrefix(strings.TrimSpace(description), "Description:") {
			description = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(description), "Description:"))
		}
		description = fixDecimalNewlines(description)
		lines := strings.Split(description, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				fmt.Println(line)
			}
		}
		fmt.Println()
	}

	fmt.Println("=== COVER LETTER ===")
	fmt.Println()
	if strings.TrimSpace(coverLetter) == "" {
		fmt.Println("(No cover letter in this run.)")
	} else {
		fmt.Println(coverLetter)
	}
}

// splitIntoSentences splits text into sentences based on common sentence endings
func splitIntoSentences(text string) []string {
	// Replace common sentence endings with a unique delimiter
	text = strings.ReplaceAll(text, ". ", ".\n")
	text = strings.ReplaceAll(text, "! ", "!\n")
	text = strings.ReplaceAll(text, "? ", "?\n")

	// Split by the delimiter
	lines := strings.Split(text, "\n")
	var sentences []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			sentences = append(sentences, line)
		}
	}

	return sentences
}

// selectResume prompts the user to select a resume file
func selectResume() string {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n=== RESUME SELECTION ===")
	fmt.Println("Available resumes:")
	fmt.Println("1. SRE (Site Reliability Engineer)")
	fmt.Println("2. VP (Vice President)")
	fmt.Print("\nPlease select a resume (1 or 2): ")

	for {
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		switch choice {
		case "1":
			return "my_resumes/sre.json"
		case "2":
			return "my_resumes/vp.json"
		default:
			fmt.Print("Invalid choice. Please enter 1 or 2: ")
		}
	}
}

func selectLLMProvider() string {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n=== LLM PROVIDER ===")
	fmt.Println("1. OpenRouter (default)")
	fmt.Println("2. Azure OpenAI")
	fmt.Print("\nSelect provider (1 or 2, press Enter for 1): ")

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("Error reading provider choice: %v", err)
			return "1"
		}
		line = strings.TrimSpace(line)
		if line == "" || line == "1" {
			return "1"
		}
		if line == "2" {
			return "2"
		}
		fmt.Print("Invalid choice. Enter 1 or 2 (Enter = 1): ")
	}
}

func newLLMClientFromChoice(choice string) (llmClient, string, error) {
	switch choice {
	case "2":
		c, err := NewAzureOpenAIClient()
		if err != nil {
			return nil, "", err
		}
		return c, "Azure OpenAI", nil
	default:
		c, err := NewOpenRouterClient()
		if err != nil {
			return nil, "", err
		}
		return c, fmt.Sprintf("OpenRouter (%s)", c.model), nil
	}
}

func loadJobRequirementsWithLLM(jobSource string, llm llmClient) (JobRequirements, error) {
	if jobSource == "job_details.txt" {
		logInfo("Reading job requirements from file: %s", jobSource)
		jobRequirements, err := readJobRequirementsFromFile(jobSource, llm)
		if err != nil {
			return JobRequirements{}, err
		}
		logInfo("Successfully read job requirements from file")
		// job file was already shown in getJobURL(); avoid printing it twice
		return jobRequirements, nil
	}

	logInfo("Scraping job requirements from URL: %s", jobSource)
	jobRequirements, err := scrapeJobRequirements(jobSource)
	if err != nil {
		logInfo("Scraping failed: %v", err)
		fmt.Printf("\n=== SCRAPING FAILED ===\n")
		fmt.Printf("Unable to scrape job information from the URL.\n")
		fmt.Printf("Error: %v\n", err)
		fmt.Printf("\nThis often happens when job sites block automated requests.\n")
		fmt.Printf("Would you like to use job_details.txt file instead? (y/n): ")

		reader := bufio.NewReader(os.Stdin)
		response, errRead := reader.ReadString('\n')
		if errRead != nil {
			return JobRequirements{}, fmt.Errorf("reading user input: %w", errRead)
		}

		response = strings.TrimSpace(strings.ToLower(response))
		if response == "y" || response == "yes" {
			logInfo("User approved fallback to job_details.txt")
			jobRequirements, err = readJobRequirementsFromFile("job_details.txt", llm)
			if err != nil {
				return JobRequirements{}, err
			}
			logInfo("Successfully read job requirements from fallback file")
			displayJobDetailsFile()
			return jobRequirements, nil
		}
		return JobRequirements{}, fmt.Errorf("scraping failed and user declined job_details.txt fallback")
	}

	if jobRequirements.Title == "Unknown Position" || jobRequirements.Title == "Career Portal" ||
		jobRequirements.Company == "Unknown Company" || len(jobRequirements.Description) < 100 {
		logInfo("Scraped data appears invalid or incomplete")
		logInfo("Scraped - Company: '%s', Title: '%s', Description length: %d",
			jobRequirements.Company, jobRequirements.Title, len(jobRequirements.Description))

		fmt.Printf("\n=== SCRAPING FAILED ===\n")
		fmt.Printf("Unable to extract meaningful job information from the URL.\n")
		fmt.Printf("Scraped data:\n")
		fmt.Printf("- Company: '%s'\n", jobRequirements.Company)
		fmt.Printf("- Title: '%s'\n", jobRequirements.Title)
		fmt.Printf("- Description length: %d characters\n", len(jobRequirements.Description))
		fmt.Printf("\nWould you like to use job_details.txt file instead? (y/n): ")

		reader := bufio.NewReader(os.Stdin)
		response, errRead := reader.ReadString('\n')
		if errRead != nil {
			return JobRequirements{}, fmt.Errorf("reading user input: %w", errRead)
		}

		response = strings.TrimSpace(strings.ToLower(response))
		if response == "y" || response == "yes" {
			logInfo("User approved fallback to job_details.txt")
			jobRequirements, err = readJobRequirementsFromFile("job_details.txt", llm)
			if err != nil {
				return JobRequirements{}, err
			}
			logInfo("Successfully read job requirements from fallback file")
			displayJobDetailsFile()
			return jobRequirements, nil
		}
		return JobRequirements{}, fmt.Errorf("scraped job data invalid and user declined job_details.txt fallback")
	}

	logInfo("Successfully scraped job requirements from URL")
	return jobRequirements, nil
}

// getJobURL prompts the user to enter a job URL or use job_details.txt
func getJobURL() string {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n=== JOB REQUIREMENTS INPUT ===")
	fmt.Println("How would you like to provide job requirements?")
	fmt.Println("1. Enter a job posting URL to scrape automatically")
	fmt.Println("2. Use the existing job_details.txt file")
	fmt.Println("3. Enter 'file' to use job_details.txt")
	fmt.Print("\nPlease enter a job posting URL, 'file', or press Enter for job_details.txt: ")

	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	// Handle different ways to specify file usage
	if input == "" || strings.ToLower(input) == "file" || strings.ToLower(input) == "job_details.txt" {
		fmt.Println("✓ Using job_details.txt file")
		displayJobDetailsFile() // Show the file content
		return "job_details.txt"
	}

	// Check if input looks like a URL
	if strings.HasPrefix(strings.ToLower(input), "http") {
		fmt.Printf("✓ Will scrape job requirements from: %s\n", input)
		return input
	}

	// If it doesn't look like a URL, ask for clarification
	fmt.Printf("The input '%s' doesn't appear to be a URL.\n", input)
	fmt.Print("Would you like to use job_details.txt instead? (y/n): ")

	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response == "y" || response == "yes" {
		fmt.Println("✓ Using job_details.txt file")
		return "job_details.txt"
	}

	// If they still want to use the original input, treat it as a URL
	fmt.Printf("✓ Will attempt to scrape from: %s\n", input)
	return input
}

// displayJobDetailsFile shows the current content of job_details.txt
func displayJobDetailsFile() {
	fmt.Println("\n=== CURRENT JOB DETAILS FILE CONTENT ===")

	content, err := os.ReadFile("job_details.txt")
	if err != nil {
		fmt.Println("❌ Could not read job_details.txt file")
		return
	}

	fmt.Println(string(content))
	fmt.Println("=== END OF FILE CONTENT ===")
}
