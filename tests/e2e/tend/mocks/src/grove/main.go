package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 3 {
		return
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
	}
}