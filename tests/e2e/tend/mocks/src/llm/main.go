package main

import (
	"fmt"
	"os"
)

func main() {
	responseFile := os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE")
	if responseFile == "" {
		fmt.Println("This is a default mock llm response.")
		return
	}

	content, err := os.ReadFile(responseFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock llm error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(string(content))
}
