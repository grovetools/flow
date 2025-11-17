package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// DevLinksConfig represents the dev-links.json structure
type DevLinksConfig struct {
	Binaries map[string]*BinaryLinks `json:"binaries"`
}

type BinaryLinks struct {
	Links   map[string]*LinkInfo `json:"links"`
	Current string               `json:"current"`
}

type LinkInfo struct {
	Path         string `json:"path"`
	WorktreePath string `json:"worktree_path"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Not enough arguments: %v\n", os.Args)
		os.Exit(1)
	}
	command := os.Args[1] + " " + os.Args[2]

	switch command {
	case "dev link":
		handleDevLink(os.Args)
	case "dev list":
		handleDevList()
	case "dev prune":
		handleDevPrune()
	case "dev unlink":
		fmt.Printf("Removed version '%s' of '%s'\n", os.Args[4], os.Args[3])
	case "dev use":
		fmt.Printf("Switched '%s' to version '%s'\n", os.Args[3], os.Args[4])
	case "llm request":
		// Forward to mock-llm binary for LLM requests
		mockDir := filepath.Dir(os.Args[0])
		llmBinary := filepath.Join(mockDir, "mock-llm")
		// Pass all remaining args after "grove llm request"
		cmd := exec.Command(llmBinary, os.Args[3:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "[GROVE MOCK] Unknown command: %s\n", command)
		os.Exit(1)
	}
}

func handleDevLink(args []string) {
	// Parse: grove dev link <dir> --as <alias>
	if len(args) < 4 {
		fmt.Fprintf(os.Stderr, "Usage: grove dev link <dir> --as <alias>\n")
		os.Exit(1)
	}

	dir := args[3]
	alias := "default"
	for i := 4; i < len(args); i++ {
		if args[i] == "--as" && i+1 < len(args) {
			alias = args[i+1]
			break
		}
	}

	// Load or create dev-links config
	config := loadDevLinksConfig()
	if config.Binaries == nil {
		config.Binaries = make(map[string]*BinaryLinks)
	}

	// Discover binaries in the directory (mock: always assume "testbin" exists)
	binPath := filepath.Join(dir, "bin", "testbin")

	if config.Binaries["testbin"] == nil {
		config.Binaries["testbin"] = &BinaryLinks{
			Links:   make(map[string]*LinkInfo),
			Current: alias,
		}
	}

	config.Binaries["testbin"].Links[alias] = &LinkInfo{
		Path:         binPath,
		WorktreePath: dir,
	}
	config.Binaries["testbin"].Current = alias

	saveDevLinksConfig(config)
	fmt.Printf("Linked testbin from %s as '%s'\n", dir, alias)
}

func handleDevList() {
	config := loadDevLinksConfig()
	if len(config.Binaries) == 0 {
		fmt.Println("No development binaries linked")
		return
	}

	for binName, binInfo := range config.Binaries {
		fmt.Printf("Binary: %s\n", binName)
		for alias, linkInfo := range binInfo.Links {
			marker := " "
			if alias == binInfo.Current {
				marker = "*"
			}
			fmt.Printf("%s %s (%s)\n", marker, alias, linkInfo.WorktreePath)
		}
	}
}

func handleDevPrune() {
	config := loadDevLinksConfig()
	removed := 0

	for binName, binInfo := range config.Binaries {
		toRemove := []string{}
		for alias, linkInfo := range binInfo.Links {
			// Check if path exists
			if _, err := os.Stat(linkInfo.Path); os.IsNotExist(err) {
				toRemove = append(toRemove, alias)
				fmt.Printf("Removing %s:%s (path no longer exists: %s)\n", binName, alias, linkInfo.Path)
				removed++
			}
		}

		// Remove broken links
		for _, alias := range toRemove {
			wasCurrent := binInfo.Current == alias
			delete(binInfo.Links, alias)

			// If the pruned link was active, find a fallback
			if wasCurrent {
				if _, hasMain := binInfo.Links["main-repo"]; hasMain {
					binInfo.Current = "main-repo"
					fmt.Printf("Active link removed. Falling back to 'main-repo' version for '%s'.\n", binName)
				} else {
					binInfo.Current = ""
					fmt.Printf("Active link removed. No fallback available for '%s'.\n", binName)
				}
			}
		}

		// Clean up binary entry if no links remain
		if len(binInfo.Links) == 0 {
			delete(config.Binaries, binName)
		}
	}

	saveDevLinksConfig(config)
	if removed == 0 {
		fmt.Println("No broken links found.")
	} else {
		fmt.Printf("\nRemoved %d broken link(s).\n", removed)
	}
}

func getDevLinksPath() string {
	home := os.Getenv("HOME")
	return filepath.Join(home, ".grove", "dev-links.json")
}

func loadDevLinksConfig() *DevLinksConfig {
	path := getDevLinksPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return &DevLinksConfig{Binaries: make(map[string]*BinaryLinks)}
	}

	var config DevLinksConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return &DevLinksConfig{Binaries: make(map[string]*BinaryLinks)}
	}

	return &config
}

func saveDevLinksConfig(config *DevLinksConfig) {
	path := getDevLinksPath()
	os.MkdirAll(filepath.Dir(path), 0755)

	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(path, data, 0644)
}