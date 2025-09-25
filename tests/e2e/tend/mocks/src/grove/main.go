package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type WorkspaceInfo struct {
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	Worktrees []WorktreeInfo `json:"worktrees"`
}

type WorktreeInfo struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	IsMain bool   `json:"is_main"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Not enough arguments: %v\n", os.Args)
		os.Exit(1)
	}
	command := os.Args[1] + " " + os.Args[2]
	wd, _ := os.Getwd()

	switch command {
	case "dev list":
		// Simplified mock, doesn't distinguish worktrees correctly but is sufficient
		fmt.Println("Binary: flow")
		fmt.Println("  main (/test/repo)")
		fmt.Printf("* finish-test (%s)\n", filepath.Join(wd, ".grove-worktrees", "finish-test"))
	case "dev unlink":
		fmt.Printf("Removed version '%s' of '%s'\n", os.Args[4], os.Args[3])
	case "dev use":
		fmt.Printf("Switched '%s' to version '%s'\n", os.Args[3], os.Args[4])
	case "ws list":
		// Check if --json flag is present
		if len(os.Args) > 3 && os.Args[3] == "--json" {
			handleWsListJson(wd)
		} else {
			fmt.Fprintf(os.Stderr, "[GROVE MOCK] ws list without --json not implemented\n")
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Unknown command: %s\n", command)
		os.Exit(1)
	}
}

func handleWsListJson(wd string) {
	// Check if we have a mock workspace list in the environment
	if mockData := os.Getenv("MOCK_GROVE_WS_LIST"); mockData != "" {
		fmt.Fprint(os.Stderr, "[GROVE MOCK] Using MOCK_GROVE_WS_LIST from environment\n")
		fmt.Print(mockData)
		return
	}
	fmt.Fprintf(os.Stderr, "[GROVE MOCK] No MOCK_GROVE_WS_LIST found, discovering from filesystem\n")
	
	// Default behavior: look for submodule source directories in the test setup
	// The test creates grove-core-source and grove-context-source directories
	var workspaces []WorkspaceInfo
	
	// Find the test root (where our submodule sources are)
	testRoot := wd
	// Try to find the test root by looking for a directory containing grove-tend pattern in path
	for {
		// Check if current dir or any parent contains the test pattern
		if strings.Contains(testRoot, "grove-tend-flow-ecosystem-worktree-lifecycle-") {
			// Found it - now get the root of this test directory
			parts := strings.Split(testRoot, "grove-tend-flow-ecosystem-worktree-lifecycle-")
			if len(parts) >= 2 {
				// Reconstruct to get the exact test root
				testRoot = parts[0] + "grove-tend-flow-ecosystem-worktree-lifecycle-" + strings.Split(parts[1], "/")[0]
				break
			}
		}
		
		parent := filepath.Dir(testRoot)
		if parent == testRoot || parent == "/" || parent == "." {
			// Couldn't find test root, try current directory
			testRoot = wd
			break
		}
		testRoot = parent
	}
	
	// Check for grove-core.git (the source repository)
	coreSourcePath := filepath.Join(testRoot, "grove-core.git")
	fmt.Fprintf(os.Stderr, "[GROVE MOCK] Checking for grove-core at: %s\n", coreSourcePath)
	if _, err := os.Stat(coreSourcePath); err == nil {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Found grove-core repository\n")
		workspaces = append(workspaces, WorkspaceInfo{
			Name: "grove-core",
			Path: coreSourcePath,
			Worktrees: []WorktreeInfo{
				{
					Path:   coreSourcePath,
					Branch: "main",
					IsMain: true,
				},
			},
		})
	} else {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] grove-core not found: %v\n", err)
	}
	
	// Check for grove-context.git (the source repository)
	contextSourcePath := filepath.Join(testRoot, "grove-context.git")
	fmt.Fprintf(os.Stderr, "[GROVE MOCK] Checking for grove-context at: %s\n", contextSourcePath)
	if _, err := os.Stat(contextSourcePath); err == nil {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Found grove-context repository\n")
		workspaces = append(workspaces, WorkspaceInfo{
			Name: "grove-context",
			Path: contextSourcePath,
			Worktrees: []WorktreeInfo{
				{
					Path:   contextSourcePath,
					Branch: "main",
					IsMain: true,
				},
			},
		})
	} else {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] grove-context not found: %v\n", err)
	}
	
	// Output as JSON (empty array if no workspaces found)
	data, _ := json.Marshal(workspaces)
	if len(workspaces) == 0 {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Returning empty workspace list\n")
		fmt.Print("[]")
	} else {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Returning %d workspaces: %s\n", len(workspaces), string(data))
		fmt.Print(string(data))
	}
}