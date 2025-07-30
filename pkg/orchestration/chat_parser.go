package orchestration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var groveDirectiveRegex = regexp.MustCompile(`(?m)^<!-- grove: (.+?) -->`)

// ParseChatFile parses a chat notebook file into a slice of ChatTurn structs.
// This is the corrected, more robust implementation.
func ParseChatFile(content []byte) ([]*ChatTurn, error) {
	_, bodyBytes, err := ParseFrontmatter(content)
	if err != nil {
		// If frontmatter is malformed, we can't proceed.
		return nil, fmt.Errorf("could not parse frontmatter: %w", err)
	}

	body := strings.TrimSpace(string(bodyBytes))
	if body == "" {
		// No content after frontmatter, so no turns.
		return []*ChatTurn{}, nil
	}

	// Split the body (everything AFTER the frontmatter) by the turn separator.
	cells := strings.Split(body, "\n---\n")

	var turns []*ChatTurn
	for i, cell := range cells {
		trimmedCell := strings.TrimSpace(cell)
		if trimmedCell == "" {
			continue // Skip empty turns
		}

		turn, err := parseChatCell(trimmedCell)
		if err != nil {
			return nil, fmt.Errorf("error parsing turn %d: %w", i+1, err)
		}

		if turn != nil && strings.TrimSpace(turn.Content) != "" {
			turns = append(turns, turn)
		}
	}

	return turns, nil
}

// parseChatCell determines the speaker and content for a single turn.
func parseChatCell(cell string) (*ChatTurn, error) {
	// Extract directive first to check its content
	directive, cleanContent := extractDirective(cell)
	
	// Determine speaker based on content and directive
	speaker := "llm" // Default to LLM
	
	// User turns are identified by:
	// 1. Starting with a blockquote (>)
	// 2. Having a directive with "template" field (user directives specify template)
	if strings.HasPrefix(cell, ">") || (directive != nil && directive.Template != "") {
		speaker = "user"
	}
	
	// LLM responses have directives with only "id" field
	// If we have a directive with only ID and no template, it's an LLM response
	if directive != nil && directive.ID != "" && directive.Template == "" {
		speaker = "llm"
	}

	turn := &ChatTurn{
		Speaker:   speaker,
		Content:   cleanContent,
		Directive: directive,
		Timestamp: time.Now(),
	}

	return turn, nil
}

func extractDirective(content string) (*ChatDirective, string) {
	matches := groveDirectiveRegex.FindStringSubmatch(content)
	if len(matches) < 2 {
		return nil, content
	}
	
	// Parse the JSON from the comment
	var directive ChatDirective
	if err := json.Unmarshal([]byte(matches[1]), &directive); err != nil {
		// If parsing fails, return original content
		return nil, content
	}
	
	// Remove the directive from content
	cleanContent := groveDirectiveRegex.ReplaceAllString(content, "")
	cleanContent = strings.TrimSpace(cleanContent)
	
	// Handle quoted user messages
	if strings.HasPrefix(cleanContent, "> ") {
		lines := strings.Split(cleanContent, "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "> ") {
				lines[i] = strings.TrimPrefix(line, "> ")
			}
		}
		cleanContent = strings.Join(lines, "\n")
	}
	
	return &directive, cleanContent
}