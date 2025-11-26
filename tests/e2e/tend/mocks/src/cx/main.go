package main

import (
	"os"
	"path/filepath"
)

func main() {
	// Simulate creating a context file
	if err := os.MkdirAll(".grove", 0755); err != nil {
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(".grove", "context"), []byte("Mock context file content."), 0644); err != nil {
		os.Exit(1)
	}
}
