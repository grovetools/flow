package orchestration

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const defaultSummaryPrompt = "Summarize the following job content and its output into a concise, one-sentence summary of what was accomplished. The summary should be no more than %d characters. Focus on the outcome, not the process. Here is the job content:\n\n---\n\n%s"
const defaultSummaryMaxChars = 150

// SummaryConfig holds configuration for job summarization.
type SummaryConfig struct {
	Enabled  bool
	Model    string
	Prompt   string
	MaxChars int
}

// SummarizeJobContent generates a summary for a completed job.
func SummarizeJobContent(ctx context.Context, job *Job, plan *Plan, cfg SummaryConfig) (string, error) {
	if !cfg.Enabled {
		return "", nil
	}
	if cfg.Model == "" {
		return "", fmt.Errorf("summary_model is not configured, but summarization is enabled")
	}

	// Read job content
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return "", fmt.Errorf("reading job file for summarization: %w", err)
	}
	_, body, _ := ParseFrontmatter(content)

	// Build content to summarize
	contentToSummarize := string(body)

	// Check for ## Output or ## Transcript section and include it
	outputRegex := regexp.MustCompile(`(?is)##\s+(Output|Transcript)\s*.*`)
	match := outputRegex.FindStringIndex(contentToSummarize)
	if match == nil {
		// If no output/transcript, just use the prompt body
		contentToSummarize = job.PromptBody
	}

	if strings.TrimSpace(contentToSummarize) == "" {
		return "", nil // Nothing to summarize
	}

	// Prepare prompt
	prompt := cfg.Prompt
	maxChars := cfg.MaxChars
	if prompt == "" {
		prompt = defaultSummaryPrompt
	}
	if maxChars == 0 {
		maxChars = defaultSummaryMaxChars
	}
	finalPrompt := fmt.Sprintf(prompt, maxChars, contentToSummarize)

	// Call LLM
	llmClient := NewCommandLLMClient(nil)
	opts := LLMOptions{
		Model:      cfg.Model,
		WorkingDir: plan.Directory,
	}

	// Use io.Discard for summarization since we don't need to stream the output
	summary, err := llmClient.Complete(ctx, job, plan, finalPrompt, opts, io.Discard)
	if err != nil {
		return "", fmt.Errorf("LLM completion for summary failed: %w", err)
	}

	// Process summary
	summary = strings.TrimSpace(summary)
	// Remove potential quotes around the summary
	summary = strings.Trim(summary, "\"'")
	if len(summary) > maxChars {
		summary = summary[:maxChars]
	}

	return summary, nil
}

// AddSummaryToJobFile updates a job's markdown file with a summary in its frontmatter.
func AddSummaryToJobFile(job *Job, summary string) error {
	updates := map[string]interface{}{"summary": summary}
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file to add summary: %w", err)
	}
	newContent, err := UpdateFrontmatter(content, updates)
	if err != nil {
		return fmt.Errorf("update frontmatter with summary: %w", err)
	}
	if err := os.WriteFile(job.FilePath, newContent, 0644); err != nil {
		return fmt.Errorf("write summary to job file: %w", err)
	}
	job.Summary = summary // update in-memory object
	return nil
}