package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// This mock is simplified. A real implementation would merge JSON etc.
func main() {
	if os.Getenv("MOCK_GROVE_HOOKS_STATEFUL") != "true" {
		fmt.Fprintf(os.Stderr, "[MOCK] grove-hooks called with: %v\n", os.Args)
		return
	}

	stateDir := os.Getenv("MOCK_STATE_DIR")
	if stateDir == "" {
		stateDir = "/tmp/grove-hooks-mock-state"
	}
	os.MkdirAll(stateDir, 0755)

	if len(os.Args) < 3 {
		return
	}
	command := os.Args[1] + "/" + os.Args[2]

	stdin, _ := io.ReadAll(os.Stdin)
	var payload map[string]interface{}
	json.Unmarshal(stdin, &payload)

	jobID, _ := payload["job_id"].(string)
	if jobID == "" {
		return
	}
	stateFile := filepath.Join(stateDir, jobID+".json")

	switch command {
	case "oneshot/start":
		payload["start_time"] = time.Now().Format(time.RFC3339)
		data, _ := json.MarshalIndent(payload, "", "  ")
		os.WriteFile(stateFile, data, 0644)
		fmt.Fprintf(os.Stderr, "[MOCK] Started tracking job %s\n", jobID)
	case "oneshot/stop":
		// Simplified: just overwrite with stop payload
		payload["end_time"] = time.Now().Format(time.RFC3339)
		data, _ := json.MarshalIndent(payload, "", "  ")
		os.WriteFile(stateFile, data, 0644)
		fmt.Fprintf(os.Stderr, "[MOCK] Stopped tracking job %s\n", jobID)
	case "sessions/get":
		jobID = os.Args[3]
		stateFile = filepath.Join(stateDir, jobID+".json")
		if data, err := os.ReadFile(stateFile); err == nil {
			fmt.Print(string(data))
		}
	}
}