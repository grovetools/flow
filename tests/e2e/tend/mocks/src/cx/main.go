package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "edit" {
		os.MkdirAll(".grove", 0755)
		os.WriteFile(filepath.Join(".grove", "rules"), []byte("*.go\n"), 0644)
		fmt.Println("Mock cx edit completed - created .grove/rules")
	}
}