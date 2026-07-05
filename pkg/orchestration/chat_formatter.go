package orchestration

import (
	"fmt"
	"regexp"
	"strings"
)

// ConversationRegions is a parsed chat split into the two transcript cache
// regions of the Anthropic stability ladder (spec 19 D7 / P4):
//
//   - HistoryBlocks: one serialized <turn> element per COMPLETED prior turn
//     (everything before the user turn awaiting a response), in order. Each
//     block is byte-stable forever — once a turn has been serialized into
//     history, re-serializing the grown conversation on any later turn
//     reproduces exactly the same bytes for it, so the blocks form an
//     append-only sequence and the Anthropic history breakpoint keeps
//     hitting (TestFormatConversationRegions_HistoryAppendOnly is the
//     property test). The mutating attributes status="awaiting_response"
//     and respond_as NEVER appear here.
//
//   - CurrentTurn: the volatile tail — the last user turn (carrying
//     status="awaiting_response" and respond_as) plus any turns after it.
//     This is the only part of the transcript whose bytes change once
//     written, and it always travels in the uncached per-turn prompt block.
//
// Byte-stability invariants of a history block (audited for P4 — anything
// that could change a prior turn's serialization retroactively is frozen):
//
//   - a turn's serialization depends only on itself and EARLIER turns
//     (template threading is backward-looking; the old lastUserIndex-driven
//     awaiting_response attribute was the single forward-looking input and
//     is confined to CurrentTurn);
//   - grove directive comments are stripped by cleanTurnContent, so marker
//     edits (e.g. chat_reopen clearing completion markers, run flags stamped
//     into a directive) never change history bytes;
//   - the assistant "## LLM Response (…)" header is converted to a timestamp
//     attribute via a fixed-format regex — never re-formatted;
//   - turns with state=running/pending are filtered out; they only ever
//     occur at the tail of a chat file, so their later completion appends
//     blocks rather than inserting them.
type ConversationRegions struct {
	HistoryBlocks []string
	CurrentTurn   string
}

// timestampRegex extracts the timestamp from an LLM Response header.
var timestampRegex = regexp.MustCompile(`## LLM Response \((\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\)`)

// FormatConversationRegions converts parsed ChatTurns to structured XML,
// split into the byte-stable history region and the volatile current turn
// (see ConversationRegions). It replaces the pre-P4 FormatConversationXML,
// which serialized the whole dialogue into a single mutating string that
// could never cache.
//
// Serialization per turn:
//
//	<turn role="assistant" template="chef" id="595424" timestamp="2026-01-08 05:34:09">
//	  *Sigh.* "Fake syrup"...
//	</turn>
//
// The template attribute appears on assistant turns (the persona that
// generated the response, threaded from the preceding user turn's
// directive). The awaiting user turn additionally gets
// status="awaiting_response" and respond_as="<template>" — volatile-only.
func FormatConversationRegions(turns []*ChatTurn) ConversationRegions {
	// First pass: filter out incomplete turns and find the last user turn.
	var filteredTurns []*ChatTurn
	lastUserIndex := -1
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
	if len(filteredTurns) == 0 {
		return ConversationRegions{}
	}

	// Everything from the awaiting user turn onward is volatile. A chat with
	// no user turn is rejected upstream (executeChatJob's pre-flight); treat
	// it defensively as all-history with an empty volatile tail.
	splitAt := lastUserIndex
	if splitAt < 0 {
		splitAt = len(filteredTurns)
	}

	var regions ConversationRegions
	var current strings.Builder

	// Track the template from user turns to apply to the following assistant
	// turn. Strictly backward-looking — load-bearing for byte stability.
	var pendingTemplate string

	for i, turn := range filteredTurns {
		var role string
		if turn.Speaker == "user" {
			role = "user"
		} else {
			role = "assistant"
		}

		var attrs []string
		attrs = append(attrs, fmt.Sprintf(`role="%s"`, role))

		content := turn.Content
		if role == "user" {
			if turn.Directive != nil && turn.Directive.Template != "" {
				pendingTemplate = turn.Directive.Template
			}
			// ONLY the awaiting turn — always in the volatile region — carries
			// the response-routing attributes.
			if i == lastUserIndex {
				attrs = append(attrs, `status="awaiting_response"`)
				if pendingTemplate != "" {
					attrs = append(attrs, fmt.Sprintf(`respond_as="%s"`, pendingTemplate))
				}
			}
		} else {
			if pendingTemplate != "" {
				attrs = append(attrs, fmt.Sprintf(`template="%s"`, pendingTemplate))
				pendingTemplate = "" // Reset after use
			}
			if turn.Directive != nil && turn.Directive.ID != "" {
				attrs = append(attrs, fmt.Sprintf(`id="%s"`, turn.Directive.ID))
			}
			// Convert the LLM Response header to a timestamp attribute.
			if matches := timestampRegex.FindStringSubmatch(content); len(matches) > 1 {
				attrs = append(attrs, fmt.Sprintf(`timestamp="%s"`, matches[1]))
				content = timestampRegex.ReplaceAllString(content, "")
			}
		}

		serialized := formatTurnXML(attrs, cleanTurnContent(content))
		if i < splitAt {
			regions.HistoryBlocks = append(regions.HistoryBlocks, serialized)
		} else {
			current.WriteString(serialized)
		}
	}

	regions.CurrentTurn = current.String()
	return regions
}

// formatTurnXML renders one turn element. This is THE transcript
// serialization: any change to it re-serializes every prior turn differently
// and cold-busts the history cache of every open chat — treat the format as
// frozen.
func formatTurnXML(attrs []string, content string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<turn %s>\n", strings.Join(attrs, " ")))
	sb.WriteString("  ")
	sb.WriteString(strings.ReplaceAll(content, "\n", "\n  "))
	sb.WriteString("\n</turn>\n")
	return sb.String()
}

// FlattenConversationRegions joins the two regions back into one string for
// callers without a block-structured upload (gemini, the mock LLM client,
// and the briefing file).
func FlattenConversationRegions(regions ConversationRegions) string {
	var sb strings.Builder
	for _, h := range regions.HistoryBlocks {
		sb.WriteString(h)
	}
	sb.WriteString(regions.CurrentTurn)
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
