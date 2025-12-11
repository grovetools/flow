package main

import (
	"fmt"
	"os"
	"strings"
)

// Mock docker command that simulates docker compose operations
func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: docker [OPTIONS] COMMAND")
		fmt.Println()
		fmt.Println("A self-sufficient runtime for containers (mock)")
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "compose":
		handleCompose(os.Args[2:])

	case "version":
		fmt.Println("Docker version 24.0.0-mock, build abcdef0")
		fmt.Println("Mock Docker for testing")

	case "ps":
		// Check for -a flag
		showAll := false
		for _, arg := range os.Args[2:] {
			if arg == "-a" || arg == "--all" {
				showAll = true
				break
			}
		}

		fmt.Println("CONTAINER ID   IMAGE          COMMAND                  CREATED         STATUS         PORTS     NAMES")
		if showAll {
			fmt.Println("abc123def456   nginx:latest   \"nginx -g 'daemon of…\"   2 hours ago     Exited (0)     80/tcp    webserver")
		}
		fmt.Println("def456ghi789   redis:alpine   \"docker-entrypoint.s…\"   5 minutes ago   Up 5 minutes   6379/tcp  cache")

	default:
		fmt.Fprintf(os.Stderr, "docker: '%s' is not a docker command (mock)\n", command)
		os.Exit(1)
	}

	// Log the command for debugging
	fmt.Fprintf(os.Stderr, "[MOCK DOCKER] Executed: docker %s\n", strings.Join(os.Args[1:], " "))
}

func handleCompose(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: docker compose [OPTIONS] COMMAND")
		fmt.Println()
		fmt.Println("Define and run multi-container applications with Docker (mock)")
		os.Exit(1)
	}

	// Find the compose command (might have flags before it)
	var composeCommand string
	var composeFiles []string
	var projectName string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--file":
			if i+1 < len(args) {
				composeFiles = append(composeFiles, args[i+1])
				i++
			}
		case "-p", "--project-name":
			if i+1 < len(args) {
				projectName = args[i+1]
				i++
			}
		case "up", "down", "ps", "logs", "exec", "build", "pull":
			composeCommand = args[i]
		}
	}

	switch composeCommand {
	case "up":
		fmt.Println("[+] Running compose up (mock)")
		if projectName != "" {
			fmt.Printf("Project name: %s\n", projectName)
		}
		if len(composeFiles) > 0 {
			fmt.Printf("Using compose files: %s\n", strings.Join(composeFiles, ", "))
		}
		fmt.Println("✔ Container app-1  Started")

	case "down":
		fmt.Println("[+] Running compose down (mock)")
		fmt.Println("✔ Container app-1  Removed")
		fmt.Println("✔ Network default  Removed")

	case "ps":
		fmt.Println("NAME      IMAGE          COMMAND   SERVICE   CREATED         STATUS         PORTS")
		fmt.Println("app-1     nginx:latest             app       10 minutes ago  Up 10 minutes  0.0.0.0:8080->80/tcp")

	case "logs":
		fmt.Println("app-1  | Mock log output from container")

	default:
		if composeCommand == "" {
			fmt.Fprintf(os.Stderr, "docker compose: no command specified (mock)\n")
		} else {
			fmt.Fprintf(os.Stderr, "docker compose: '%s' command not mocked\n", composeCommand)
		}
		os.Exit(1)
	}
}
