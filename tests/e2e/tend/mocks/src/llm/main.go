package main

import (
	"fmt"
	"os"
)

func main() {
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