package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Mock nb command to simulate `nb new` for the flow plan demote test.
// It creates a note file in <cwd>/inbox/ and prints the path to stdout.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "[MOCK NB] No command provided\n")
		os.Exit(1)
	}

	if os.Args[1] == "new" {
		handleNew()
		return
	}

	fmt.Fprintf(os.Stderr, "[MOCK NB] Unhandled command: %s\n", strings.Join(os.Args[1:], " "))
	os.Exit(1)
}

func handleNew() {
	// Parse args: nb new <title> --type <type> --no-edit
	var title string
	var noteType string

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--type", "-t":
			if i+1 < len(args) {
				noteType = args[i+1]
				i++
			}
		case "--no-edit":
			// skip
		default:
			if !strings.HasPrefix(args[i], "-") && title == "" {
				title = args[i]
			}
		}
	}

	if noteType == "" {
		noteType = "inbox"
	}

	// Read stdin for body content
	var body string
	stdinContent, err := io.ReadAll(os.Stdin)
	if err == nil && len(stdinContent) > 0 {
		body = string(stdinContent)
	}

	// Sanitize title for filename
	slug := strings.ToLower(title)
	slug = strings.ReplaceAll(slug, " ", "-")
	sanitized := make([]byte, 0, len(slug))
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			sanitized = append(sanitized, c)
		}
	}

	datestamp := time.Now().Format("20060102")
	filename := fmt.Sprintf("%s-%s.md", datestamp, string(sanitized))

	// Create the note in <cwd>/<noteType>/
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Error getting cwd: %v\n", err)
		os.Exit(1)
	}

	noteDir := filepath.Join(cwd, noteType)
	if err := os.MkdirAll(noteDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Error creating note dir: %v\n", err)
		os.Exit(1)
	}

	notePath := filepath.Join(noteDir, filename)

	// Build note content with frontmatter
	content := fmt.Sprintf("---\ntitle: %s\ntype: %s\n---\n", title, noteType)
	if body != "" {
		content += "\n" + body + "\n"
	}

	if err := os.WriteFile(notePath, []byte(content), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Error writing note: %v\n", err)
		os.Exit(1)
	}

	// Print path to stdout so parseNotePathFromOutput can find it
	fmt.Println(notePath)

	fmt.Fprintf(os.Stderr, "[MOCK NB] Created note: %s\n", notePath)
}
