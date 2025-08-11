package cmd

import "os"

// shouldSkipDockerCheck returns true if docker checks should be skipped for testing
func shouldSkipDockerCheck() bool {
	return os.Getenv("GROVE_FLOW_SKIP_DOCKER_CHECK") == "true"
}