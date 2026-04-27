package orchestration

// Config holds orchestration-specific settings, decoupled from grove-core.
type Config struct {
	OneshotModel        string
	MaxConsecutiveSteps int
	AgentTarget         string // "native" or "tmux" — resolved at submission time, never "auto"
}
