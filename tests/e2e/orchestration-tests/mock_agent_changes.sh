#!/bin/bash
# Mock script for agent changes

# This script simulates what an agent would do when implementing changes
# It will be used by the test harness to mock agent behavior

JOB_FILE=$1

if [[ "$JOB_FILE" == *"health-handler"* ]]; then
    # Add health handler function
    cat >> source-code/main.go << 'EOF'

func healthHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    fmt.Fprintf(w, `{"status": "ok"}`)
}
EOF
elif [[ "$JOB_FILE" == *"register-route"* ]]; then
    # Register the route (mock implementation)
    echo "Would register /health route here"
fi