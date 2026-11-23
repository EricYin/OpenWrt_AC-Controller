package api

import (
	"context"
	"testing"

	"github.com/naoki66/ac-controller/shared/proto"
)

// The controller must persist what the agent reports and let a later clean
// heartbeat clear it; otherwise a fixed AP would keep the warning forever.
func TestHelloRecordsConfigDrift(t *testing.T) {
	f := newAPFixture(t, "")
	ctx := context.Background()
	f.hello("", proto.Hello{MAC: testMAC})

	f.hello("", proto.Hello{
		MAC:        testMAC,
		AppliedRev: 2,
		Drift:      true,
		DriftFiles: []string{"wireless", "network"},
	})
	d, err := f.store.GetDevice(ctx, testMAC)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if !d.Drift {
		t.Error("drift was not stored")
	}
	if d.DriftFiles != "wireless,network" {
		t.Errorf("drift_files = %q", d.DriftFiles)
	}

	// The next apply succeeds and the agent reports a clean state.
	f.hello("", proto.Hello{MAC: testMAC, AppliedRev: 3})
	d, err = f.store.GetDevice(ctx, testMAC)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.Drift || d.DriftFiles != "" {
		t.Errorf("drift was not cleared: %v %q", d.Drift, d.DriftFiles)
	}
}
