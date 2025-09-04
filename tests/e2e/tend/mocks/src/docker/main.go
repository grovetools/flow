package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ps" {
		containerName := os.Getenv("MOCK_DOCKER_CONTAINER")
		if containerName == "" {
			containerName = "fake-container"
		}

		// Simple check if args contain the container name
		for _, arg := range os.Args {
			if strings.Contains(arg, containerName) {
				fmt.Println(containerName) // Simulate container is running
				os.Exit(0)
			}
		}
		// If no filter matches, output nothing to simulate container not found
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "[MOCK DOCKER] Called with: %v\n", os.Args)
}