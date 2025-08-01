package exec

import (
	"fmt"
	"os/exec"
)

// ExecError wraps an execution error with the command output
type ExecError struct {
	Err    error
	Output string
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("%v: %s", e.Err, e.Output)
}

// RealCommandExecutor implements CommandExecutor using the actual os/exec package.
// This is the production implementation that executes real system commands.
type RealCommandExecutor struct{}

// LookPath searches for an executable named file in the directories
// named by the PATH environment variable.
func (e *RealCommandExecutor) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// Execute runs the command with the given name and arguments.
// It waits for the command to complete and returns any error.
func (e *RealCommandExecutor) Execute(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	// Capture stderr to include in error messages
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Include the output in the error so we can check for specific error messages
		return &ExecError{
			Err:    err,
			Output: string(output),
		}
	}
	return nil
}