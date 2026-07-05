package orchestration

import (
	"strings"
	"testing"
)

// mkUserTurn / mkAssistantTurn build ChatTurns the way ParseChatFile produces
// them for a normal chat: user turns optionally carry a template directive,
// assistant turns carry an id directive and the "## LLM Response (ts)" header
// in their content.
func mkUserTurn(content, template string) *ChatTurn {
	turn := &ChatTurn{Speaker: "user", Content: content}
	if template != "" {
		turn.Directive = &ChatDirective{Template: template}
	}
	return turn
}

func mkAssistantTurn(id, ts, content string) *ChatTurn {
	return &ChatTurn{
		Speaker:   "llm",
		Content:   "## LLM Response (" + ts + ")\n\n" + content,
		Directive: &ChatDirective{ID: id},
	}
}

// growingConversation returns the turn sequence of a chat as it looks when
// running turn k (1-based): k user turns, k-1 assistant responses in between.
func growingConversation(k int) []*ChatTurn {
	var turns []*ChatTurn
	questions := []string{"first question", "second question\nwith a second line", "third question", "fourth question"}
	answers := []string{"answer one", "answer two", "answer three", "answer four"}
	for i := 0; i < k; i++ {
		tmpl := ""
		if i == 0 {
			tmpl = "chat"
		}
		turns = append(turns, mkUserTurn(questions[i], tmpl))
		if i < k-1 {
			turns = append(turns, mkAssistantTurn("id-"+string(rune('a'+i)), "2026-07-05 10:0"+string(rune('0'+i))+":00", answers[i]))
		}
	}
	return turns
}

// TestFormatConversationRegions_HistoryAppendOnly is the P4 history-stability
// property test (spec 19 §8, scenario 17 at unit level): as the chat grows
// turn by turn, the history region only ever APPENDS blocks — every block
// serialized at turn K reappears byte-identically at turn K+1, so turn K's
// concatenated history is a byte-prefix of turn K+1's.
func TestFormatConversationRegions_HistoryAppendOnly(t *testing.T) {
	var prev ConversationRegions
	for k := 1; k <= 4; k++ {
		regions := FormatConversationRegions(growingConversation(k))

		// Turn k has k-1 completed exchanges = 2*(k-1) history blocks.
		if got, want := len(regions.HistoryBlocks), 2*(k-1); got != want {
			t.Fatalf("turn %d: %d history blocks, want %d", k, got, want)
		}

		// Block-level append-only: every earlier block byte-identical.
		for i, b := range prev.HistoryBlocks {
			if regions.HistoryBlocks[i] != b {
				t.Errorf("turn %d: history block %d changed retroactively:\nbefore: %q\nafter:  %q", k, i, b, regions.HistoryBlocks[i])
			}
		}
		// Byte-level: previous concatenation is a strict prefix.
		prevConcat := strings.Join(prev.HistoryBlocks, "")
		concat := strings.Join(regions.HistoryBlocks, "")
		if !strings.HasPrefix(concat, prevConcat) {
			t.Errorf("turn %d: history is not append-only:\nprev: %q\nnow:  %q", k, prevConcat, concat)
		}

		// The volatile region always exists and marks the awaiting turn.
		if !strings.Contains(regions.CurrentTurn, `status="awaiting_response"`) {
			t.Errorf("turn %d: current turn missing awaiting_response: %q", k, regions.CurrentTurn)
		}
		prev = regions
	}
}

// TestFormatConversationRegions_MutatingAttrsOnlyVolatile asserts the two
// per-request attributes can never leak into a history block — the P4
// precondition ("awaiting_response must live only in the volatile block").
func TestFormatConversationRegions_MutatingAttrsOnlyVolatile(t *testing.T) {
	for k := 1; k <= 4; k++ {
		regions := FormatConversationRegions(growingConversation(k))
		for i, b := range regions.HistoryBlocks {
			for _, forbidden := range []string{"awaiting_response", "respond_as"} {
				if strings.Contains(b, forbidden) {
					t.Errorf("turn %d history block %d contains %q: %q", k, i, forbidden, b)
				}
			}
		}
	}
}

// TestFormatConversationRegions_Serialization pins the concrete per-turn
// serialization: attribute threading (template from the preceding user turn's
// directive), timestamp extraction from the LLM Response header, directive
// comment stripping, and respond_as on the awaiting turn. This format is
// cache-load-bearing — changing it re-serializes every prior turn and
// cold-busts the history region of every open chat.
func TestFormatConversationRegions_Serialization(t *testing.T) {
	turns := []*ChatTurn{
		mkUserTurn("how about add fake syrup", "chef"),
		mkAssistantTurn("595424", "2026-01-08 05:34:09", `*Sigh.* "Fake syrup"...`),
		// ParseChatFile assigns the response's trailing template marker to the
		// next user turn as its directive; any leftover marker text in the
		// content is stripped by cleanTurnContent.
		mkUserTurn("respond to this\n<!-- grove: {\"template\": \"chef\"} -->", "chef"),
	}
	regions := FormatConversationRegions(turns)

	wantHistory := []string{
		"<turn role=\"user\">\n  how about add fake syrup\n</turn>\n",
		"<turn role=\"assistant\" template=\"chef\" id=\"595424\" timestamp=\"2026-01-08 05:34:09\">\n  *Sigh.* \"Fake syrup\"...\n</turn>\n",
	}
	if len(regions.HistoryBlocks) != len(wantHistory) {
		t.Fatalf("history blocks = %q, want %d blocks", regions.HistoryBlocks, len(wantHistory))
	}
	for i, want := range wantHistory {
		if regions.HistoryBlocks[i] != want {
			t.Errorf("history block %d:\n got %q\nwant %q", i, regions.HistoryBlocks[i], want)
		}
	}
	// respond_as comes from the awaiting turn's own directive template; the
	// grove comment is stripped from the content.
	wantCurrent := "<turn role=\"user\" status=\"awaiting_response\" respond_as=\"chef\">\n  respond to this\n</turn>\n"
	if regions.CurrentTurn != wantCurrent {
		t.Errorf("current turn:\n got %q\nwant %q", regions.CurrentTurn, wantCurrent)
	}
}

// TestFormatConversationRegions_FiltersIncompleteTurns: running/pending turns
// are excluded from both regions, and an empty conversation yields empty
// regions.
func TestFormatConversationRegions_FiltersIncompleteTurns(t *testing.T) {
	pending := &ChatTurn{
		Speaker:   "llm",
		Content:   "still thinking...",
		Directive: &ChatDirective{Vars: map[string]interface{}{"state": "running"}},
	}
	turns := []*ChatTurn{
		mkUserTurn("q1", "chat"),
		mkAssistantTurn("id-a", "2026-07-05 10:00:00", "a1"),
		mkUserTurn("q2", ""),
		pending,
	}
	regions := FormatConversationRegions(turns)
	flat := strings.Join(regions.HistoryBlocks, "") + regions.CurrentTurn
	if strings.Contains(flat, "still thinking") {
		t.Errorf("incomplete turn leaked into the serialization: %q", flat)
	}
	if len(regions.HistoryBlocks) != 2 {
		t.Errorf("history blocks = %d, want 2", len(regions.HistoryBlocks))
	}

	empty := FormatConversationRegions(nil)
	if len(empty.HistoryBlocks) != 0 || empty.CurrentTurn != "" {
		t.Errorf("empty conversation regions = %+v, want zero value", empty)
	}
}
