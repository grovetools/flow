package orchestration

import "testing"

func TestValidateCoordMode(t *testing.T) {
	for _, mode := range []string{"", CoordModeManual, CoordModeAutonomous} {
		if err := ValidateCoordMode(mode); err != nil {
			t.Errorf("ValidateCoordMode(%q) = %v, want nil", mode, err)
		}
	}
	for _, mode := range []string{"auto", "AUTONOMOUS", "yes"} {
		if err := ValidateCoordMode(mode); err == nil {
			t.Errorf("ValidateCoordMode(%q) = nil, want error", mode)
		}
	}
	// Surrounding whitespace is tolerated, not a different mode.
	if err := ValidateCoordMode(" autonomous "); err != nil {
		t.Errorf("ValidateCoordMode with padding = %v, want nil", err)
	}
}

func TestEffectiveHandoffBounds(t *testing.T) {
	cfg := &FlowConfig{HandoffMax: 7, HandoffThreshold: 65}

	tests := []struct {
		name          string
		job           *Job
		cfg           *FlowConfig
		wantMax       int
		wantThreshold int
	}{
		{"built-in defaults", &Job{}, nil, DefaultHandoffMax, DefaultHandoffThreshold},
		{"config overrides defaults", &Job{}, cfg, 7, 65},
		{"job overrides config", &Job{HandoffMax: 2, HandoffThreshold: 90}, cfg, 2, 90},
		{"job overrides absent config", &Job{HandoffMax: 2, HandoffThreshold: 90}, nil, 2, 90},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveHandoffMax(tt.job, tt.cfg); got != tt.wantMax {
				t.Errorf("EffectiveHandoffMax() = %d, want %d", got, tt.wantMax)
			}
			if got := EffectiveHandoffThreshold(tt.job, tt.cfg); got != tt.wantThreshold {
				t.Errorf("EffectiveHandoffThreshold() = %d, want %d", got, tt.wantThreshold)
			}
		})
	}
}

func TestValidateHandoffFields(t *testing.T) {
	tests := []struct {
		name    string
		job     *Job
		cfg     *FlowConfig
		wantErr bool
	}{
		{"clean job", &Job{}, nil, false},
		{"autonomous coordinator at depth 0", &Job{CoordMode: CoordModeAutonomous}, nil, false},
		{"successor within budget", &Job{CoordMode: CoordModeAutonomous, HandoffFrom: "a", HandoffDepth: 2, HandoffMax: 3}, nil, false},
		{"successor at the bound", &Job{HandoffFrom: "a", HandoffDepth: 3, HandoffMax: 3}, nil, false},
		{"successor past the bound", &Job{HandoffFrom: "a", HandoffDepth: 4, HandoffMax: 3}, nil, true},
		{"successor past the built-in default", &Job{HandoffFrom: "a", HandoffDepth: 4}, nil, true},
		{"config raises the bound", &Job{HandoffFrom: "a", HandoffDepth: 4}, &FlowConfig{HandoffMax: 6}, false},
		{"unknown coord mode", &Job{CoordMode: "semi"}, nil, true},
		{"negative depth", &Job{HandoffDepth: -1}, nil, true},
		{"max over ceiling", &Job{HandoffMax: MaxHandoffMax + 1}, nil, true},
		{"threshold over 99", &Job{HandoffThreshold: 100}, nil, true},
		{"lineage without depth", &Job{HandoffFrom: "a"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHandoffFields(tt.job, tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateHandoffFields() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHandoffBudgetRemaining(t *testing.T) {
	tests := []struct {
		name string
		job  *Job
		want int
	}{
		{"fresh coordinator", &Job{}, DefaultHandoffMax},
		{"one handoff spent", &Job{HandoffDepth: 1, HandoffMax: 3}, 2},
		{"exhausted", &Job{HandoffDepth: 3, HandoffMax: 3}, 0},
		{"over budget never reports negative", &Job{HandoffDepth: 9, HandoffMax: 3}, 0},
		{"nil job", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HandoffBudgetRemaining(tt.job, nil); got != tt.want {
				t.Errorf("HandoffBudgetRemaining() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIsAutonomousCoordinator(t *testing.T) {
	if (&Job{}).IsAutonomousCoordinator() {
		t.Error("a job with no coord_mode must not be autonomous")
	}
	if (&Job{CoordMode: CoordModeManual}).IsAutonomousCoordinator() {
		t.Error("manual coord_mode must not be autonomous")
	}
	if !(&Job{CoordMode: CoordModeAutonomous}).IsAutonomousCoordinator() {
		t.Error("autonomous coord_mode must be autonomous")
	}
}
