package orchestration

// Config holds orchestration-specific settings, decoupled from grove-core.
type Config struct {
	OneshotModel         string
	TargetAgentContainer string
	PlansDirectory       string
	MaxConsecutiveSteps  int
}