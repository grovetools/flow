package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLooksLikeFilePath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"simple title", "my-chat", false},
		{"title with hyphen", "my-feature-chat", false},
		{"title with underscore", "my_feature_chat", false},
		{"absolute path", "/path/to/file.md", true},
		{"relative path", "./path/to/file.md", true},
		{"path with slash", "plans/my-plan", true},
		{"markdown extension", "file.md", true},
		{"just .md", ".md", true},
		{"path and .md", "path/file.md", true},
		{"empty string", "", false},
		{"single word", "chat", false},
		{"numbers only", "12345", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := looksLikeFilePath(tt.input)
			assert.Equal(t, tt.expected, result, "looksLikeFilePath(%q) should return %v", tt.input, tt.expected)
		})
	}
}
