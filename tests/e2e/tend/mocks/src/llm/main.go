package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	// Check if any args contain model names we should handle specially
	args := strings.Join(os.Args[1:], " ")
	
	// Read stdin to get the prompt content
	stdinContent := ""
	if stat, _ := os.Stdin.Stat(); (stat.Mode() & os.ModeCharDevice) == 0 {
		bytes, _ := io.ReadAll(os.Stdin)
		stdinContent = string(bytes)
	}
	
	// Combine args and stdin for content checking
	fullContent := args + " " + stdinContent
	
	// Handle test-specific models
	if strings.Contains(args, "mock-summarizer") {
		// This is a summarization request - check for specific content
		if strings.Contains(fullContent, "calculator") || strings.Contains(fullContent, "calculate") {
			fmt.Print("Created a calculator function with basic operations and error handling for division by zero.")
		} else if strings.Contains(fullContent, "15") && strings.Contains(fullContent, "27") {
			fmt.Print("Calculated the sum of 15 and 27, which equals 42.")
		} else {
			fmt.Print("This is a concise mock summary.")
		}
		return
	}
	
	if strings.Contains(args, "mock-llm") || strings.Contains(args, "oneshot_model") {
		// This is a regular LLM request for oneshot execution
		// Check for special responses based on context
		if strings.Contains(fullContent, "15") && strings.Contains(fullContent, "27") {
			// Oneshot calculation test
			fmt.Print("## Output\n\n15 + 27 = 42\n\nThe sum of 15 and 27 is 42.")
		} else {
			fmt.Print("This is a mock LLM response.")
		}
		return
	}
	
	if responseFile := os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE"); responseFile != "" {
		content, err := os.ReadFile(responseFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mock llm error: could not read response file: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(string(content))
		return
	}

	if response := os.Getenv("MOCK_LLM_RESPONSE"); response != "" {
		fmt.Print(response)
		return
	}

	fmt.Print("This is a generic default response from the mock LLM.")
}