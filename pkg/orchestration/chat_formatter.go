package orchestration

import (
	"fmt"
	"regexp"
	"strings"
)

// FormatConversationXML converts parsed ChatTurns to structured XML format.
// This is used to send a clean, structured conversation to the LLM instead of
// raw markdown with HTML comment markers.
//
// Input:  []*ChatTurn - parsed conversation turns
// Output: XML string with <conversation><turn>...</turn></conversation> structure
//
// The template attribute appears on assistant turns (indicating what persona/template
// was used to generate that response). The last user turn gets special attributes:
// - status="awaiting_response" to indicate this is the turn needing a response
// - respond_as="<template>" to indicate what persona/template should be used
//
// Example output:
//
//	<conversation>
//	  <turn role="user">
//	    how about add fake syrup
//	  </turn>
//	  <turn role="assistant" template="chef" id="595424" timestamp="2026-01-08 05:34:09">
//	    *Sigh.* "Fake syrup"...
//	  </turn>
//	  <turn role="user" status="awaiting_response" respond_as="chef">
//	    respond to this
//	  </turn>
//	</conversation>
func FormatConversationXML(turns []*ChatTurn) string {
	if len(turns) == 0 {
		return "<conversation/>"
	}

	// First pass: filter out incomplete turns and find the last user turn
	var filteredTurns []*ChatTurn
	var lastUserIndex int = -1
	for _, turn := range turns {
		// Skip turns with state=running or state=pending (these are incomplete)
		if turn.Directive != nil {
			if state, ok := turn.Directive.Vars["state"].(string); ok {
				if state == "running" || state == "pending" {
					continue
				}
			}
		}
		if turn.Speaker == "user" {
			lastUserIndex = len(filteredTurns)
		}
		filteredTurns = append(filteredTurns, turn)
	}

	var sb strings.Builder
	sb.WriteString("<conversation>\n")

	// Regex to extract timestamp from LLM Response header
	timestampRegex := regexp.MustCompile(`## LLM Response \((\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\)`)

	// Track the template from user turns to apply to the following assistant turn
	var pendingTemplate string

	for i, turn := range filteredTurns {
		// Determine role based on speaker
		var role string
		if turn.Speaker == "user" {
			role = "user"
		} else {
			role = "assistant"
		}

		// Build attributes
		var attrs []string
		attrs = append(attrs, fmt.Sprintf(`role="%s"`, role))

		if role == "user" {
			// Capture template from user turn directive
			if turn.Directive != nil && turn.Directive.Template != "" {
				pendingTemplate = turn.Directive.Template
			}

			// If this is the last user turn, add special attributes
			if i == lastUserIndex {
				attrs = append(attrs, `status="awaiting_response"`)
				if pendingTemplate != "" {
					attrs = append(attrs, fmt.Sprintf(`respond_as="%s"`, pendingTemplate))
				}
			}
		} else if role == "assistant" {
			// Assistant turns get the template that was used to generate them
			if pendingTemplate != "" {
				attrs = append(attrs, fmt.Sprintf(`template="%s"`, pendingTemplate))
				pendingTemplate = "" // Reset after use
			}

			// Include id from directive
			if turn.Directive != nil && turn.Directive.ID != "" {
				attrs = append(attrs, fmt.Sprintf(`id="%s"`, turn.Directive.ID))
			}
		}

		// Extract timestamp from content if this is an assistant turn
		content := turn.Content
		if role == "assistant" {
			if matches := timestampRegex.FindStringSubmatch(content); len(matches) > 1 {
				attrs = append(attrs, fmt.Sprintf(`timestamp="%s"`, matches[1]))
				// Remove the header from content
				content = timestampRegex.ReplaceAllString(content, "")
			}
		}

		// Clean up the content
		content = cleanTurnContent(content)

		// Write the turn element
		sb.WriteString(fmt.Sprintf("  <turn %s>\n", strings.Join(attrs, " ")))
		sb.WriteString("    ")
		sb.WriteString(strings.ReplaceAll(content, "\n", "\n    "))
		sb.WriteString("\n  </turn>\n")
	}

	sb.WriteString("</conversation>")
	return sb.String()
}

// cleanTurnContent removes grove markers and normalizes whitespace in turn content.
func cleanTurnContent(content string) string {
	// Remove grove directive comments
	groveCommentRegex := regexp.MustCompile(`<!--\s*grove:\s*\{[^}]*\}\s*-->`)
	content = groveCommentRegex.ReplaceAllString(content, "")

	// Remove LLM Response headers (they're converted to timestamp attributes)
	headerRegex := regexp.MustCompile(`(?m)^## LLM Response \([^)]*\)\s*`)
	content = headerRegex.ReplaceAllString(content, "")

	// Trim leading/trailing whitespace
	content = strings.TrimSpace(content)

	return content
}
