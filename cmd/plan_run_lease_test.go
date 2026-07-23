package cmd

import (
	"os"
	"strings"
	"testing"
	"time"

	coreplan "github.com/grovetools/core/pkg/plan"
)

func TestEnforcePlanLeaseFailsClosedOnMalformedClaim(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(coreplan.LeasePath(dir), []byte("holder_origin: sat\nttl: nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := enforcePlanLease(dir, false); err == nil || !strings.Contains(err.Error(), "cannot verify execution lease") {
		t.Fatalf("enforcePlanLease = %v, want fail-closed error", err)
	}
	if err := enforcePlanLease(dir, true); err != nil {
		t.Fatalf("explicit force did not override malformed advisory lease: %v", err)
	}
}

func TestEnforcePlanLeaseRefusesLiveAndAllowsExpired(t *testing.T) {
	dir := t.TempDir()
	lease := coreplan.Lease{HolderOrigin: "sat", JobID: "job-1", AcquiredAt: time.Now(), TTL: time.Hour}
	if err := coreplan.WriteLease(dir, lease); err != nil {
		t.Fatal(err)
	}
	if err := enforcePlanLease(dir, false); err == nil || !strings.Contains(err.Error(), "leased to satellite") {
		t.Fatalf("live lease accepted: %v", err)
	}
	lease.AcquiredAt = time.Now().Add(-2 * time.Hour)
	if err := coreplan.WriteLease(dir, lease); err != nil {
		t.Fatal(err)
	}
	if err := enforcePlanLease(dir, false); err != nil {
		t.Fatalf("expired lease refused: %v", err)
	}
}
