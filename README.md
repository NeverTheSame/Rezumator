# Rezumator

Rezumator is a Go CLI that uses LLMs (Azure OpenAI or OpenRouter) to improve and tailor your resume and cover letter to a specific job description. It reads a resume JSON plus a job posting, then outputs a job-aligned objective, updated experience bullets, and a personalized cover letter.

## Features

- **Interactive Interface**: Simple command-line interface for easy resume and job selection
- **Resume Optimization**: Adjusts objective and job descriptions to match target job requirements
- **Cover Letter Generation**: Creates personalized, human-like cover letters explaining your interest and value proposition
- **Multiple Input Sources**: Accepts job requirements from URLs, text files, or JSON files
- **Azure OpenAI Integration**: Uses advanced LLM capabilities for intelligent content generation
- **Truthful Enhancement**: Maintains authenticity while highlighting relevant experience
- **Professional Output**: Generates well-formatted, professional resume content
- **Comprehensive Logging**: All processing steps are logged to timestamped files for debugging

## Prerequisites

- Go 1.23 or higher
- Azure OpenAI service with API access
- Valid Azure OpenAI credentials

## Installation

1. Clone or download the project files
2. Install dependencies:
   ```bash
   go mod tidy
   ```

## Configuration

1. Ensure your `secret` file contains the required Azure OpenAI credentials:
   ```
   AZURE_OPENAI_API_KEY=your_api_key_here
   AZURE_OPENAI_ENDPOINT=your_endpoint_here
   AZURE_OPENAI_API_VERSION=2025-01-01-preview
   AZURE_OPENAI_DEPLOYMENT_NAME=your_deployment_name
   AZURE_OPENAI_REASONING_DEPLOYMENT_NAME=your_reasoning_deployment_name
   ```

2. The `prompts.json` file contains the AI prompts used for resume optimization. The "rezumator" prompt is specifically designed for this task.

3. NOTE: Recently, the prompts for rewriting (`human_style_rewriting`) and resume optimization (`rezumator`) have been updated to prioritize natural, human-like language with varied sentence structures and organic phrasing to improve authenticity and readability of generated content.

## Usage

### Simple Interactive Usage

The easiest way to use Rezumator is with the interactive interface:

```bash
go run ./cmd/rezumator
```

The program will guide you through:
1. **Resume Selection**: Choose between available resumes (SRE, VP, etc.)
2. **Job Requirements Input**: Either enter a job posting URL or use the `job_details.txt` file

### Job Requirements Options

#### Option 1: URL Scraping
Enter a job posting URL when prompted. The program will automatically scrape:
- Company name
- Job title  
- Job description

#### Option 2: Text File
Press Enter when prompted for URL to use `job_details.txt`. Simply copy-paste job details into this file.

### Advanced Usage (Legacy)

For advanced users, you can still use command-line flags:

```bash
# With JSON files
go run . -resume sample_resume.json -requirements sample_job_requirements.json

# With URL scraping
go run . -resume sample_resume.json -url "https://example.com/job-posting"
```

### Command Line Options

- `-resume`: Path to JSON file containing your resume data
- `-requirements`: Path to JSON file containing job requirements
- `-url`: URL to scrape job requirements from

## Input Format

### Resume JSON Format

```json
{
    "objective": "Your career objective statement",
    "current_job": {
        "company": "Company Name",
        "title": "Job Title",
        "description": "Detailed job description",
        "start_date": "YYYY-MM",
        "end_date": "Present or YYYY-MM"
    },
    "past_jobs": [
        {
            "company": "Previous Company",
            "title": "Previous Title",
            "description": "Previous job description",
            "start_date": "YYYY-MM",
            "end_date": "YYYY-MM"
        }
    ]
}
```

### Job Requirements JSON Format

```json
{
    "company": "Target Company",
    "title": "Target Position",
    "description": "Job description and requirements"
}
```

## Output

The program outputs:

### Adjusted Resume
- **Optimized Objective**: Tailored to the target position and company
- **Enhanced Current Job**: Emphasizes relevant experience and achievements
- **Improved Past Jobs**: Highlights transferable skills and relevant experience

### Cover Letter
- **Personalized Content**: Explains why you want the position and how you can contribute
- **Human-like Writing**: Uses advanced AI techniques to sound natural and authentic
- **Professional Format**: Standard business letter format with appropriate greeting and closing

### Logs
- **Timestamped Log Files**: All processing steps are logged to `logs/rezumator_YYYY-MM-DD_HH-MM-SS.log`
- **Debug Information**: Includes scraping details, LLM processing, and extraction results

## Example

### Interactive Session
```
Logs are being written to: logs/rezumator_2025-08-18_17-21-01.log

=== RESUME SELECTION ===
Available resumes:
1. SRE (Site Reliability Engineer)
2. VP (Vice President)

Please select a resume (1 or 2): 1

=== JOB REQUIREMENTS INPUT ===
Options:
1. Enter a job posting URL to scrape
2. Press Enter to use job_details.txt file

Please enter a job posting URL (or press Enter for job_details.txt): 
Using job_details.txt file
```

### Input Resume (SRE)
```json
{
    "objective": "Senior Site Reliability Engineer with over 12 years of experience...",
    "current_job": {
        "company": "Company A",
        "title": "Senior Site Reliability Engineer",
        "description": "Lead reliability and scalability efforts for a portfolio of customer-facing services..."
    }
}
```

### Target Job Requirements (VP Position)
```
VP of Solution Engineering & Delivery (US/Canada)
Our Platform Engineering Team is working to solve the Multiplicity Problem...
```

### Output
```
=== ADJUSTED RESUME ===

OBJECTIVE:
Senior AI & Automation Specialist with over 12 years of experience in designing, operating, and maintaining scalable, reliable infrastructure and distributed systems...

CURRENT JOB:
Company: Company A
Title: Senior Site Reliability Engineer (Reliability)
Description: Lead architecture and implementation of fault-tolerant, multi-region Azure systems...

=== COVER LETTER ===

Dear Hiring Team,

I'm writing to express my interest in the Senior AI & Automation Specialist position...
```

## Web Scraping

The program includes advanced web scraping capabilities for job postings. It attempts to extract:
- Company name
- Job title
- Job description

### Scraping Features
- **Multiple Extraction Methods**: Uses HTML selectors, meta tags, and JSON-LD structured data
- **Fallback Mechanisms**: If HTML parsing fails, falls back to structured data extraction
- **Anti-Detection**: Uses realistic browser headers and user agents
- **Error Handling**: Comprehensive error handling for various website structures

Note: Scraping effectiveness depends on the website structure. For best results, use the `job_details.txt` file when scraping fails.

## Error Handling

The program includes comprehensive error handling for:
- Missing or invalid credentials
- File reading errors
- Network issues during scraping
- LLM API errors
- Invalid JSON formats
- Web scraping failures with fallback options
- Interactive input validation

## Limitations

- Web scraping may not work perfectly for all job sites (use `job_details.txt` as fallback)
- LLM responses depend on the quality of the Azure OpenAI model
- Content parsing assumes a specific output format from the LLM
- Cover letter generation requires internet connectivity for Azure OpenAI API calls

## Contributing

Feel free to submit issues and enhancement requests!

## License

This project is for personal use and educational purposes. 