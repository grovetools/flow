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
	if meta.Name == "" && meta.Description == "" && len(meta.Phases) == 0 {
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
