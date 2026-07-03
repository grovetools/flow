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
		// Empty body means a newly created chat file - return initial user turn with empty content
		return []*ChatTurn{{
			Speaker:   "user",
			Content:   "",
			Timestamp: time.Now(),
		}}, nil
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

		// Extract content from the start of the directive line until next directive or end
		// This ensures we capture any content on the same line as the directive
		startIdx := matchIndices[i][0]
		var endIdx int
		if i+1 < len(matchIndices) {
			endIdx = matchIndices[i+1][0]
		} else {
			endIdx = len(body)
		}

		// Get the full content including the directive line
		fullContent := body[startIdx:endIdx]

		// Remove the directive comment itself from the content
		content := groveDirectiveRegex.ReplaceAllString(fullContent, "")
		content = strings.TrimSpace(content)
		// Strip a leading blockquote marker ("> ") from each line so quoted
		// user asks (the convention for directive turns) come out clean.
		content = stripBlockquoteMarkers(content)

		if content != "" || speaker == "user" {
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

// TrailingChatTurn describes the final turn of a chat file for the purpose of
// deciding whether an explicit `flow plan run` actually has a user turn to
// dispatch. A chat file that ends on a bare '<!-- grove: {"template": "chat"}
// -->' marker with no content after it has no user turn — dispatching it is a
// silent no-op, which is almost always a mistake the user should hear about.
type TrailingChatTurn struct {
	// HasUserTurn is true when there is non-empty user content after the last
	// chat marker — i.e. an actual turn to send to the LLM.
	HasUserTurn bool
	// OrphanBeforeMarker is true when the last marker has no content after it
	// yet a preceding user turn does — the classic "typed the question, then
	// pasted a fresh marker below it" mistake, where the question sits before
	// the marker and is silently ignored.
	OrphanBeforeMarker bool
}

// InspectTrailingChatTurn reports whether a chat file has a dispatchable user
// turn after its last marker. It is used at the explicit run/dispatch layer to
// turn a silent "no user turn" no-op into a hard error. Normal batch runs
// (RunAll/RunNext) deliberately do NOT call this — a chat legitimately waiting
// for user input has the exact same on-disk shape as this error, and only the
// user's explicit `flow plan run <job>` distinguishes intent.
func InspectTrailingChatTurn(content []byte) TrailingChatTurn {
	turns, err := ParseChatFile(content)
	if err != nil || len(turns) == 0 {
		// Parsing problems are surfaced elsewhere; don't block here.
		return TrailingChatTurn{HasUserTurn: true}
	}

	last := turns[len(turns)-1]
	if last.Speaker == "user" && strings.TrimSpace(last.Content) != "" {
		// A real user turn with content — dispatchable.
		return TrailingChatTurn{HasUserTurn: true}
	}

	// No dispatchable user turn after the last marker. Detect the "content
	// before the marker" variant: two consecutive user turns (the earlier one
	// never answered by the LLM) means the user's ask is stranded above the
	// trailing marker. A normal chat waiting after an LLM response has an llm
	// turn immediately before the empty marker, so it is NOT flagged as orphan.
	orphan := false
	if len(turns) >= 2 {
		prev := turns[len(turns)-2]
		if prev.Speaker == "user" && strings.TrimSpace(prev.Content) != "" {
			orphan = true
		}
	}
	return TrailingChatTurn{HasUserTurn: false, OrphanBeforeMarker: orphan}
}

// NoUserTurnError builds the hard error reported when an explicitly-run chat
// job has no user turn after its last chat marker. The message names the
// problem and how to fix it.
func NoUserTurnError(jobPath string) error {
	return fmt.Errorf(
		"no user turn after last chat marker in %s: add your message on a new line below the last '<!-- grove: {\"template\": \"chat\"} -->' marker (content placed before the marker is ignored)",
		jobPath,
	)
}

// stripBlockquoteMarkers removes a single leading blockquote marker from each
// line ("> " or a bare ">"). Lines without a marker are left unchanged.
func stripBlockquoteMarkers(content string) string {
	if content == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "> "):
			lines[i] = line[2:]
		case line == ">":
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}
