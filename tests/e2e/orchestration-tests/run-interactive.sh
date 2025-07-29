#!/bin/bash
# Wrapper script to ensure tests run in an interactive terminal

if [ -t 0 ]; then
    # Already in an interactive terminal
    GROVE_TEST_STEP_THROUGH=true exec ./test-orchestration-e2e.sh "$@"
else
    # Not in an interactive terminal, use script command to create a pty
    echo "Running in non-interactive terminal, using 'script' to enable interaction..."
    GROVE_TEST_STEP_THROUGH=true script -q -c "./test-orchestration-e2e.sh $*" /dev/null
fi