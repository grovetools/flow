package orchestration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var groveDirectiveRegex = regexp.MustCompile(`(?m)^<!-- grove: (.+?) -->`)

// ParseChatFile parses a chat notebook file into a slice of ChatTurn structs
func ParseChatFile(content []byte) ([]*ChatTurn, error) {
	// Split content by the --- separator
	rawContent := string(content)
	cells := strings.Split(rawContent, "\n---\n")
	
	if len(cells) == 0 {
		return nil, fmt.Errorf("empty chat file")
	}

	var turns []*ChatTurn
	
	// The first cell contains frontmatter and initial user prompt
	if len(cells) > 0 {
		firstCell := cells[0]
		
		// Find the end of frontmatter (second ---)
		frontmatterEnd := strings.Index(firstCell, "---\n")
		if frontmatterEnd != -1 {
			// Skip past the closing --- of frontmatter
			endIdx := frontmatterEnd + 4
			if endIdx < len(firstCell) {
				initialContent := strings.TrimSpace(firstCell[endIdx:])
				if initialContent != "" {
					turn := &ChatTurn{
						Speaker:   "user",
						Content:   initialContent,
						Timestamp: time.Now(),
					}
					turns = append(turns, turn)
				}
			}
		}
	}
	
	// Process remaining cells
	for i := 1; i < len(cells); i++ {
		cell := strings.TrimSpace(cells[i])
		if cell == "" {
			continue
		}
		
		turn, err := parseChatCell(cell)
		if err != nil {
			return nil, fmt.Errorf("error parsing cell %d: %w", i+1, err)
		}
		
		turns = append(turns, turn)
	}
	
	return turns, nil
}

func parseChatCell(cell string) (*ChatTurn, error) {
	lines := strings.Split(cell, "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty cell")
	}
	
	firstLine := lines[0]
	
	// Determine speaker based on first line
	var speaker string
	var contentStartIdx int
	
	if strings.HasPrefix(firstLine, "> ") {
		speaker = "user"
		contentStartIdx = 0
	} else if strings.HasPrefix(firstLine, "## LLM Response") {
		speaker = "llm"
		// Skip the header line
		if len(lines) > 1 {
			contentStartIdx = 1
		} else {
			return &ChatTurn{
				Speaker:   speaker,
				Content:   "",
				Timestamp: time.Now(),
			}, nil
		}
	} else {
		// Default to user if no clear marker
		speaker = "user"
		contentStartIdx = 0
	}
	
	// Extract content
	contentLines := lines[contentStartIdx:]
	content := strings.Join(contentLines, "\n")
	
	turn := &ChatTurn{
		Speaker:   speaker,
		Content:   content,
		Timestamp: time.Now(),
	}
	
	// Look for directive in both user and LLM turns
	directive, cleanContent := extractDirective(content)
	if directive != nil {
		turn.Directive = directive
		turn.Content = cleanContent
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