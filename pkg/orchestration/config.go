package orchestration

// Config holds orchestration-specific settings, decoupled from grove-core.
type Config struct {
	OneshotModel        string
	PlansDirectory      string
	MaxConsecutiveSteps int
}