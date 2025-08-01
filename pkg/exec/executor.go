package exec

// CommandExecutor defines an interface for running external commands.
// This abstraction allows for easier testing by providing a mockable interface.
type CommandExecutor interface {
	// LookPath searches for an executable named file in the directories
	// named by the PATH environment variable.
	LookPath(file string) (string, error)
	
	// Execute runs the command with the given name and arguments.
	// It waits for the command to complete and returns any error.
	Execute(name string, arg ...string) error
}