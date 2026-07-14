package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TestResolveDispatchSatellite pins the dispatch-target precedence table:
// explicit `--at satellite:<name>` > explicit local --at target > the plan
// config designation > local; with "local" reserved as the force-local
// sentinel in both the flag and (defensively) the plan config.
func TestResolveDispatchSatellite(t *testing.T) {
	cases := []struct {
		name           string
		atSatellite    string
		atSatelliteSet bool
		hasLocalTarget bool
		planSatellite  string
		wantName       string
		wantOK         bool
	}{
		{"nothing set → local", "", false, false, "", "", false},
		{"explicit flag wins", "sat1", true, false, "", "sat1", true},
		{"explicit flag beats plan config", "sat1", true, false, "sat2", "sat1", true},
		{"satellite:local forces local over plan config", "local", true, false, "sat2", "", false},
		{"plan config default", "", false, false, "sat2", "sat2", true},
		{"plan config whitespace-trimmed", "", false, false, "  sat2  ", "sat2", true},
		{"blank plan config → local", "", false, false, "   ", "", false},
		{"reserved local in plan config → local", "", false, false, "local", "", false},
		{"explicit local --at target beats plan config", "", false, true, "sat2", "", false},
		{"local target without plan config → local", "", false, true, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveDispatchSatellite(tc.atSatellite, tc.atSatelliteSet, tc.hasLocalTarget, tc.planSatellite)
			if got != tc.wantName || ok != tc.wantOK {
				t.Errorf("resolveDispatchSatellite(%q, %v, %v, %q) = (%q, %v), want (%q, %v)",
					tc.atSatellite, tc.atSatelliteSet, tc.hasLocalTarget, tc.planSatellite,
					got, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}

// TestValidateSatelliteName pins the init-time shape check: empty is fine (no
// designation), registry-shaped names pass, "local" is reserved, and unsafe
// names are rejected without consulting any registry.
func TestValidateSatelliteName(t *testing.T) {
	for _, ok := range []string{"", "mysat", "grove-satellite", "sat.1", "A_b-2"} {
		if err := validateSatelliteName(ok); err != nil {
			t.Errorf("validateSatelliteName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"local", "  ", "a b", "sat;rm", "sat/x", "$(boom)"} {
		if err := validateSatelliteName(bad); err == nil {
			t.Errorf("validateSatelliteName(%q) = nil, want error", bad)
		}
	}
}

// TestCreateDefaultPlanConfigSatellite pins `flow plan init --satellite`'s
// on-disk effect: the generated .grove-plan.yml carries the satellite: key
// and orchestration.LoadPlan reads it back into Config.Satellite (the value
// resolveDispatchSatellite defaults from); without --satellite no satellite
// key is written.
func TestCreateDefaultPlanConfigSatellite(t *testing.T) {
	dir := t.TempDir()
	if err := createDefaultPlanConfig(dir, "m", "wt", "", "", "", "mysat", nil); err != nil {
		t.Fatalf("createDefaultPlanConfig: %v", err)
	}
	plan, err := orchestration.LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if plan.Config == nil || plan.Config.Satellite != "mysat" {
		t.Fatalf("Config.Satellite = %+v, want mysat", plan.Config)
	}

	bare := t.TempDir()
	if err := createDefaultPlanConfig(bare, "m", "wt", "", "", "", "", nil); err != nil {
		t.Fatalf("createDefaultPlanConfig (no satellite): %v", err)
	}
	data, err := os.ReadFile(filepath.Join(bare, ".grove-plan.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "satellite:") {
		t.Errorf("satellite key written without --satellite:\n%s", data)
	}
}
