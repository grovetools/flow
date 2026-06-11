package orchestration

import (
	"regexp"
	"strings"
)

// HasNewUserContent checks if a chat job file has new user content after the last assistant marker.
// This is used to detect if a completed or pending_user chat job should be auto-reopened.
func HasNewUserContent(jobContent []byte) bool {
	contentStr := string(jobContent)

	// Find the last occurrence of the assistant marker (LLM Response)
	// Pattern: ## LLM Response (... timestamp ...)
	assistantMarkerPattern := regexp.MustCompile(`(?s)## LLM Response \([^)]+\)\n\n`)
	matches := assistantMarkerPattern.FindAllStringIndex(contentStr, -1)

	if len(matches) == 0 {
		// No assistant marker found - this is unusual for a chat job that claims to be completed/pending_user
		// If the file has content beyond frontmatter, consider it as potential new content
		frontmatterEnd := strings.Index(contentStr, "---")
		if frontmatterEnd == -1 {
			return false
		}
		// Skip first frontmatter block
		contentAfterFrontmatter := contentStr[frontmatterEnd+3:]
		secondFrontmatterEnd := strings.Index(contentAfterFrontmatter, "---")
		if secondFrontmatterEnd == -1 {
			return false
		}
		// Get content after the second ---
		afterFrontmatter := contentAfterFrontmatter[secondFrontmatterEnd+3:]
		return strings.TrimSpace(afterFrontmatter) != ""
	}

	// Get the position after the last assistant response
	lastAssistantMatch := matches[len(matches)-1]
	posAfterLastAssistant := lastAssistantMatch[1]

	// Get content after the last assistant marker
	contentAfterLastAssistant := contentStr[posAfterLastAssistant:]
	contentAfterLastAssistant = strings.TrimSpace(contentAfterLastAssistant)

	// Check if there's any meaningful content after the last assistant response
	// and that it doesn't start with the next assistant marker (which would indicate
	// this response is already the latest)
	if contentAfterLastAssistant != "" && !strings.HasPrefix(contentAfterLastAssistant, "## LLM Response") {
		return true
	}

	return false
}

// ClearCompletionMarkers removes completion-related fields from a job to allow it to be re-run.
// This is used when auto-reopening a completed or pending_user chat job.
func ClearCompletionMarkers(updates map[string]interface{}) {
	// Clear markers that indicate the job is in a terminal state
	updates["last_error"] = nil
	updates["completed_at"] = nil
	updates["duration"] = nil
}
