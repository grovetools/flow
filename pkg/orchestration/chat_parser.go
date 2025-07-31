package orchestration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var groveDirectiveRegex = regexp.MustCompile(`(?m)^<!-- grove: (.+?) -->`)

// ParseChatFile parses a chat notebook file to determine the speaker of each turn.
// It returns a simplified list of turns for determining runnability.
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

	// Find all grove directives in the content
	matches := groveDirectiveRegex.FindAllStringSubmatch(body, -1)
	matchIndices := groveDirectiveRegex.FindAllStringIndex(body, -1)
	
	// If no directives found, assume entire content is initial user prompt
	if len(matches) == 0 {
		return []*ChatTurn{{
			Speaker:   "user",
			Content:   body,
			Timestamp: time.Now(),
		}}, nil
	}
	
	var turns []*ChatTurn
	
	// Check if there's content before the first directive
	if matchIndices[0][0] > 0 {
		initialContent := strings.TrimSpace(body[:matchIndices[0][0]])
		if initialContent != "" {
			turns = append(turns, &ChatTurn{
				Speaker:   "user",
				Content:   initialContent,
				Timestamp: time.Now(),
			})
		}
	}
	
	// Process each directive
	for i, match := range matches {
		if len(match) < 2 {
			continue
		}
		
		// Parse the directive JSON
		var directive ChatDirective
		if err := json.Unmarshal([]byte(match[1]), &directive); err != nil {
			continue
		}
		
		// Determine speaker from directive
		speaker := "llm"
		if directive.Template != "" {
			speaker = "user"
		}
		
		// Extract content after this directive until next directive or end
		startIdx := matchIndices[i][1]
		var endIdx int
		if i+1 < len(matchIndices) {
			endIdx = matchIndices[i+1][0]
		} else {
			endIdx = len(body)
		}
		
		content := strings.TrimSpace(body[startIdx:endIdx])
		if content != "" {
			turns = append(turns, &ChatTurn{
				Speaker:   speaker,
				Content:   content,
				Directive: &directive,
				Timestamp: time.Now(),
			})
		}
	}
	
	return turns, nil
}