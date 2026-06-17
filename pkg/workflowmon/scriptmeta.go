package workflowmon

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ScriptMeta is the display-relevant subset of a workflow script's
// `export const meta = {...}` block.
type ScriptMeta struct {
	Name        string
	Description string
	Phases      []PhaseMeta
	// AgentLabels maps prompt substrings to agent labels extracted from
	// static label: 'value' patterns near agent() calls. Best-effort: labels
	// using dynamic template literals (backticks) cannot be recovered.
	AgentLabels map[string]string
}

// PhaseMeta is one entry of the meta block's phases array.
type PhaseMeta struct {
	Title  string
	Detail string
}

var (
	metaStartRe   = regexp.MustCompile(`export\s+const\s+meta\s*=\s*\{`)
	nameRe        = regexp.MustCompile(`name\s*:\s*['"` + "`" + `]([^'"` + "`" + `]*)['"` + "`" + `]`)
	descriptionRe = regexp.MustCompile(`description\s*:\s*['"` + "`" + `]([^'"` + "`" + `]*)['"` + "`" + `]`)
	phasesRe      = regexp.MustCompile(`phases\s*:\s*\[`)
	titleRe       = regexp.MustCompile(`title\s*:\s*['"` + "`" + `]([^'"` + "`" + `]*)['"` + "`" + `]`)
	detailRe      = regexp.MustCompile(`detail\s*:\s*['"` + "`" + `]([^'"` + "`" + `]*)['"` + "`" + `]`)
	// agentCallRe matches agent() calls (the harness function).
	agentCallRe = regexp.MustCompile(`\bagent\s*\(`)
	// staticLabelRe extracts static single/double-quoted labels only —
	// backtick template literals are dynamic and cannot be recovered.
	staticLabelRe = regexp.MustCompile(`label\s*:\s*['"]([^'"]+)['"]`)
	// staticPhaseRe extracts static single/double-quoted phase values.
	staticPhaseRe = regexp.MustCompile(`phase\s*:\s*['"]([^'"]+)['"]`)
)

// ParseScriptMeta extracts the meta block from a persisted workflow script.
// The block is a JS object literal, not JSON, so extraction is best-effort
// regex over a balanced-brace slice: a script the parser cannot understand
// yields nil rather than an error (display-only data, tolerate drift).
func ParseScriptMeta(src []byte) *ScriptMeta {
	loc := metaStartRe.FindIndex(src)
	if loc == nil {
		return nil
	}
	block := balancedSlice(src[loc[1]-1:], '{', '}')
	if block == "" {
		return nil
	}

	meta := &ScriptMeta{}
	if m := nameRe.FindStringSubmatch(block); m != nil {
		meta.Name = m[1]
	}
	if m := descriptionRe.FindStringSubmatch(block); m != nil {
		meta.Description = m[1]
	}
	if loc := phasesRe.FindStringIndex(block); loc != nil {
		phasesBlock := balancedSlice([]byte(block[loc[1]-1:]), '[', ']')
		for _, entry := range splitObjectEntries(phasesBlock) {
			var phase PhaseMeta
			if m := titleRe.FindStringSubmatch(entry); m != nil {
				phase.Title = m[1]
			}
			if m := detailRe.FindStringSubmatch(entry); m != nil {
				phase.Detail = m[1]
			}
			if phase.Title != "" {
				meta.Phases = append(meta.Phases, phase)
			}
		}
	}

	// Extract agent labels from agent() calls — best-effort for static
	// single/double-quoted labels only.
	meta.AgentLabels = extractAgentLabels(src)

	if meta.Name == "" && meta.Description == "" && len(meta.Phases) == 0 && len(meta.AgentLabels) == 0 {
		return nil
	}
	return meta
}

// FindRunScript locates the persisted orchestration script for a run inside
// scriptsDir. Scripts are saved as <name>-<runId>.js. Returns "" when no
// script matches.
func FindRunScript(scriptsDir, runID string) string {
	matches, err := filepath.Glob(filepath.Join(scriptsDir, "*-"+runID+".js"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// LoadRunMeta finds and parses the script meta for a run. Returns nil when
// the script is missing or unparseable.
func LoadRunMeta(scriptsDir, runID string) *ScriptMeta {
	path := FindRunScript(scriptsDir, runID)
	if path == "" {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return ParseScriptMeta(src)
}

// balancedSlice returns the text from the opening delimiter at src[0] through
// its matching closing delimiter, inclusive. Returns "" when unbalanced.
// Quote-aware enough for meta blocks: skips over single/double/backtick
// strings so braces inside literals don't break matching.
func balancedSlice(src []byte, open, close byte) string {
	if len(src) == 0 || src[0] != open {
		return ""
	}
	depth := 0
	var quote byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return string(src[:i+1])
			}
		}
	}
	return ""
}

// splitObjectEntries returns the top-level {...} object literals inside an
// array block like `[ {..}, {..} ]`.
func splitObjectEntries(arrayBlock string) []string {
	var entries []string
	rest := arrayBlock
	for {
		idx := strings.IndexByte(rest, '{')
		if idx < 0 {
			return entries
		}
		obj := balancedSlice([]byte(rest[idx:]), '{', '}')
		if obj == "" {
			return entries
		}
		entries = append(entries, obj)
		rest = rest[idx+len(obj):]
	}
}

// extractAgentLabels scans the entire script for agent() calls and extracts
// any static label: 'value' patterns from their options object. Returns a
// map from a prompt substring (the first ~100 chars of any static string
// literal argument) to the label. This is best-effort: dynamic template
// literals and variable prompts cannot be recovered.
func extractAgentLabels(src []byte) map[string]string {
	labels := make(map[string]string)
	srcStr := string(src)
	matches := agentCallRe.FindAllStringIndex(srcStr, -1)

	for _, loc := range matches {
		// Extract the agent() call's arguments: balanced parens from the '('.
		callStart := loc[1] - 1 // back up to include the '('
		if callStart >= len(srcStr) {
			continue
		}
		args := balancedSlice([]byte(srcStr[callStart:]), '(', ')')
		if args == "" {
			continue
		}

		// Look for a static label in the options object
		labelMatch := staticLabelRe.FindStringSubmatch(args)
		if labelMatch == nil {
			continue
		}
		label := labelMatch[1]

		// Try to extract the prompt argument — first string literal before
		// the options object (very heuristic: just grab the first quoted
		// string in the args, up to ~100 chars).
		promptKey := extractPromptKey(args)
		if promptKey == "" {
			continue
		}
		labels[promptKey] = label
	}
	return labels
}

// extractPromptKey extracts a key substring from the prompt argument of an
// agent() call — the first ~100 chars of the first string literal found.
// Returns "" when no static prompt is found.
func extractPromptKey(args string) string {
	// Try single quotes first, then double quotes.
	// Skip the leading '(' if present.
	s := strings.TrimPrefix(args, "(")

	for _, quote := range []byte{'"', '\''} {
		idx := strings.IndexByte(s, quote)
		if idx < 0 {
			continue
		}
		// Find the end of the string, handling escapes.
		end := idx + 1
		for end < len(s) {
			if s[end] == '\\' {
				end += 2
				continue
			}
			if s[end] == quote {
				break
			}
			end++
		}
		if end >= len(s) {
			continue
		}
		prompt := s[idx+1 : end]
		// Truncate to ~100 chars for the key.
		if len(prompt) > 100 {
			prompt = prompt[:100]
		}
		return prompt
	}
	return ""
}
