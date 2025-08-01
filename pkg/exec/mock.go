package exec

import (
	"strings"
)

// MockCommandExecutor is a mock implementation of CommandExecutor for testing.
// It records all commands that would be executed without actually running them.
type MockCommandExecutor struct {
	// Commands records all commands that were executed
	Commands []string
	
	// LookPathFunc allows custom behavior for LookPath in tests
	LookPathFunc func(file string) (string, error)
	
	// ExecuteFunc allows custom behavior for Execute in tests
	ExecuteFunc func(name string, arg ...string) error
}

// LookPath implements the CommandExecutor interface for testing.
func (m *MockCommandExecutor) LookPath(file string) (string, error) {
	if m.LookPathFunc != nil {
		return m.LookPathFunc(file)
	}
	// By default, assume commands exist
	return "/path/to/" + file, nil
}

// Execute implements the CommandExecutor interface for testing.
// It records the command that would be executed.
func (m *MockCommandExecutor) Execute(name string, arg ...string) error {
	cmdStr := name
	if len(arg) > 0 {
		cmdStr = name + " " + strings.Join(arg, " ")
	}
	m.Commands = append(m.Commands, cmdStr)
	
	if m.ExecuteFunc != nil {
		return m.ExecuteFunc(name, arg...)
	}
	return nil
}