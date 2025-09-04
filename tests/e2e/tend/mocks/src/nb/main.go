package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Printf("Mock nb archive called with: %s\n", strings.Join(os.Args[1:], " "))
}