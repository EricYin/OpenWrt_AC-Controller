package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/naoki66/ac-controller/shared/model"
	"github.com/naoki66/ac-controller/shared/render"
)

// The screenshot bug: two APs adopt the same network-managing template with no
// per-unit override and both render the template's single static address. The
// second adopt must stagger instead: base stays with its first claimant, the
// next unit takes the next free host, and the allocation is persisted so a
// re-push keeps it.
func TestAdoptAutoStaggersTemplateAddress(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if err := svc.Store.UpsertTemplate(ctx, managedNetworkTemplate()); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	r1, err := svc.Adopt(ctx, ap1, "net")
	if err != nil {
		t.Fatalf("adopt ap1: %v", err)
	}
	r2, err := svc.Adopt(ctx, ap2, "net")
	if err != nil {
		t.Fatalf("adopt ap2: %v", err)
	}

	n1 := string(extract(t, svc, r1.MAC, r1.Rev)[render.FileNetwork])
	n2 := string(extract(t, svc, r2.MAC, r2.Rev)[render.FileNetwork])
	if !strings.Contains(n1, "list ipaddr '10.0.10.1/24'") {
		t.Errorf("ap1 should keep the template base address:\n%s", n1)
	}
	if !strings.Contains(n2, "list ipaddr '10.0.10.2/24'") {
		t.Errorf("ap2 should be staggered to the next free host:\n%s", n2)
	}

	// The staggered address must be durable: the override carries it, so the
	// device page shows it and the next push does not reshuffle.
	d, err := svc.Store.GetDevice(ctx, ap2)
	if err != nil {
		t.Fatalf("get ap2: %v", err)
	}
	var ov model.DeviceNetwork
	if err := json.Unmarshal([]byte(d.NetworkParams), &ov); err != nil {
		t.Fatalf("ap2 network_params: %v", err)
	}
	if got := ov.IPAddr["lan"]; len(got) != 1 || got[0] != "10.0.10.2/24" {
		t.Errorf("ap2 override ipaddr = %v, want [10.0.10.2/24]", got)
	}

	// And ap1 was left untouched: the first unit keeps the template as written.
	if d1, err := svc.Store.GetDevice(ctx, ap1); err == nil && d1.NetworkParams != "" {
		t.Errorf("ap1 gained an override it did not need: %q", d1.NetworkParams)
	}
}

// The address another unit wrote explicitly is reserved too, so the stagger
// skips past it rather than landing on it.
func TestAdoptStaggerSkipsUsedAddress(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if err := svc.Store.UpsertTemplate(ctx, managedNetworkTemplate()); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	// ap1 claims .2 through its own override before ap2 ever adopts, so the
	// base .1 is still free and ap2 must take it (not .2).
	blob, _ := json.Marshal(model.DeviceNetwork{
		IPAddr: map[string][]string{"lan": {"10.0.10.2/24"}},
	})
	if err := svc.Store.SetDeviceNetwork(ctx, ap1, string(blob)); err != nil {
		t.Fatalf("set ap1 network: %v", err)
	}

	r2, err := svc.Adopt(ctx, ap2, "net")
	if err != nil {
		t.Fatalf("adopt ap2: %v", err)
	}
	n2 := string(extract(t, svc, r2.MAC, r2.Rev)[render.FileNetwork])
	if !strings.Contains(n2, "list ipaddr '10.0.10.1/24'") {
		t.Errorf("ap2 should take the still-free base .1:\n%s", n2)
	}
	if strings.Contains(n2, "10.0.10.2/24") {
		t.Errorf("ap2 network collides with ap1's explicit override:\n%s", n2)
	}
}

// An operator-typed override that collides with another unit's address is
// refused on the default path — but the refusal carries the structured list
// (not just a sentence) and an explicit force lets the push through.
func TestAdoptRefusesDuplicateExplicitAddress(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if err := svc.Store.UpsertTemplate(ctx, managedNetworkTemplate()); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	if _, err := svc.Adopt(ctx, ap1, "net"); err != nil {
		t.Fatalf("adopt ap1: %v", err)
	}

	blob, _ := json.Marshal(model.DeviceNetwork{
		IPAddr: map[string][]string{"lan": {"10.0.10.1/24"}},
	})
	if err := svc.Store.SetDeviceNetwork(ctx, ap2, string(blob)); err != nil {
		t.Fatalf("set ap2 network: %v", err)
	}

	_, err := svc.Adopt(ctx, ap2, "net")
	if err == nil {
		t.Fatal("adopting a second unit with ap1's address was allowed without force")
	}
	var conflict *AddressConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error is %T, want *AddressConflictError: %v", err, err)
	}
	if len(conflict.Conflicts) != 1 ||
		conflict.Conflicts[0].IP != "10.0.10.1" ||
		conflict.Conflicts[0].Interface != "lan" ||
		conflict.Conflicts[0].Device != ap1 {
		t.Errorf("conflicts = %+v, want one entry for 10.0.10.1@lan owned by %s", conflict.Conflicts, ap1)
	}

	// Forced push keeps the typed address; the operator owns the duplicate.
	res, err := svc.AdoptWith(ctx, ap2, "net", PushOptions{Force: true})
	if err != nil {
		t.Fatalf("forced adopt: %v", err)
	}
	network := string(extract(t, svc, ap2, res.Rev)[render.FileNetwork])
	if !strings.Contains(network, "list ipaddr '10.0.10.1/24'") {
		t.Errorf("forced push did not keep the operator's address:\n%s", network)
	}
}

// Preflight never fails on a conflict: it lists them, so the UI can warn and
// offer a forced push instead of showing a dead end.
func TestPreflightReportsAddressConflict(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if err := svc.Store.UpsertTemplate(ctx, managedNetworkTemplate()); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if _, err := svc.Adopt(ctx, ap1, "net"); err != nil {
		t.Fatalf("adopt ap1: %v", err)
	}

	blob, _ := json.Marshal(model.DeviceNetwork{
		IPAddr: map[string][]string{"lan": {"10.0.10.1/24"}},
	})
	if err := svc.Store.SetDeviceNetwork(ctx, ap2, string(blob)); err != nil {
		t.Fatalf("set ap2 network: %v", err)
	}

	pf, err := svc.Preflight(ctx, ap2, "net")
	if err != nil {
		t.Fatalf("preflight must not fail on a conflict: %v", err)
	}
	if len(pf.Conflicts) != 1 || pf.Conflicts[0].IP != "10.0.10.1" ||
		pf.Conflicts[0].Device != ap1 || pf.Conflicts[0].Interface != "lan" {
		t.Errorf("preflight conflicts = %+v", pf.Conflicts)
	}
	// The target address list still shows what would be pushed.
	if len(pf.TargetAddrs) != 1 || pf.TargetAddrs[0] != "10.0.10.1" {
		t.Errorf("target addrs = %v", pf.TargetAddrs)
	}

	// A clean unit has no conflicts, so the UI shows no warning at all.
	if pf1, err := svc.Preflight(ctx, ap1, "net"); err != nil {
		t.Fatalf("preflight ap1: %v", err)
	} else if len(pf1.Conflicts) != 0 {
		t.Errorf("ap1 conflicts = %+v, want none", pf1.Conflicts)
	}
}

// The persisted allocation is what makes stagger stable: a re-push of the
// staggered unit keeps its address instead of walking further up the subnet.
func TestStaggeredAddressSurvivesRepush(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if err := svc.Store.UpsertTemplate(ctx, managedNetworkTemplate()); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	if _, err := svc.Adopt(ctx, ap1, "net"); err != nil {
		t.Fatalf("adopt ap1: %v", err)
	}
	r2, err := svc.Adopt(ctx, ap2, "net")
	if err != nil {
		t.Fatalf("adopt ap2: %v", err)
	}

	again, err := svc.Repush(ctx, ap2)
	if err != nil {
		t.Fatalf("repush ap2: %v", err)
	}
	if again.Rev <= r2.Rev {
		t.Errorf("repush rev = %d, want > %d", again.Rev, r2.Rev)
	}
	network := string(extract(t, svc, ap2, again.Rev)[render.FileNetwork])
	if !strings.Contains(network, "list ipaddr '10.0.10.2/24'") {
		t.Errorf("repush moved ap2 off its allocated address:\n%s", network)
	}
}

// Preflight drives the adopt confirmation, so it must predict the staggered
// address, not the colliding template base.
func TestPreflightShowsStaggeredAddress(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if err := svc.Store.UpsertTemplate(ctx, managedNetworkTemplate()); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	if _, err := svc.Adopt(ctx, ap1, "net"); err != nil {
		t.Fatalf("adopt ap1: %v", err)
	}

	pf, err := svc.Preflight(ctx, ap2, "net")
	if err != nil {
		t.Fatalf("preflight ap2: %v", err)
	}
	if len(pf.TargetAddrs) != 1 || pf.TargetAddrs[0] != "10.0.10.2" {
		t.Errorf("target addrs = %v, want the staggered 10.0.10.2", pf.TargetAddrs)
	}
	if pf.NewMgmt != "10.0.10.2" {
		t.Errorf("new mgmt = %q, want 10.0.10.2", pf.NewMgmt)
	}
}

// Allocation order: base itself when free, then upward, then wrap past the
// broadcast address to the lowest host.
func TestNextFreeOrderAndWraparound(t *testing.T) {
	used := func(m map[string]bool) map[string]string {
		out := map[string]string{}
		for k := range m {
			out[k] = "other"
		}
		return out
	}

	base := net.ParseIP("10.0.10.250").To4()
	mask := net.CIDRMask(24, 32)

	// Base free → base.
	ip, err := nextFree(base, mask, map[string]string{})
	if err != nil || ip.String() != "10.0.10.250" {
		t.Errorf("free base → %v (%v), want 10.0.10.250", ip, err)
	}

	// .250 and .251 taken → the next free host upward, .252.
	ip, err = nextFree(base, mask, used(map[string]bool{"10.0.10.250": true, "10.0.10.251": true}))
	if err != nil || ip.String() != "10.0.10.252" {
		t.Errorf("next upward → %v (%v), want 10.0.10.252", ip, err)
	}

	// Everything from .250 to the last host (.254) taken → wraps to the
	// lowest host, .1.
	wrapped := map[string]bool{}
	for i := 250; i <= 254; i++ {
		wrapped[fmt.Sprintf("10.0.10.%d", i)] = true
	}
	ip, err = nextFree(base, mask, used(wrapped))
	if err != nil || ip.String() != "10.0.10.1" {
		t.Errorf("wraparound → %v (%v), want 10.0.10.1", ip, err)
	}

	// Whole /24 exhausted → explicit error, not an infinite loop.
	full := map[string]string{}
	for i := 1; i <= 254; i++ {
		full[fmt.Sprintf("10.0.10.%d", i)] = "other"
	}
	if _, err := nextFree(base, mask, full); err == nil {
		t.Error("exhausted /24 allocated an address")
	}
}

// A bare template address with a separate netmask (no CIDR suffix) must
// allocate without growing a suffix: the interface's netmask option already
// carries the mask, and the rendered list keeps the input style.
func TestAdoptStaggerKeepsBareAddressStyle(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	tpl := managedNetworkTemplate()
	tpl.Bridge.Interfaces[0].IPAddr = []string{"10.0.10.1"}
	tpl.Bridge.Interfaces[0].Netmask = "255.255.255.0"
	if err := svc.Store.UpsertTemplate(ctx, tpl); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	if _, err := svc.Adopt(ctx, ap1, "net"); err != nil {
		t.Fatalf("adopt ap1: %v", err)
	}
	r2, err := svc.Adopt(ctx, ap2, "net")
	if err != nil {
		t.Fatalf("adopt ap2: %v", err)
	}
	network := string(extract(t, svc, ap2, r2.Rev)[render.FileNetwork])
	if !strings.Contains(network, "list ipaddr '10.0.10.2'") {
		t.Errorf("staggered bare address grew a suffix or missed:\n%s", network)
	}
	if strings.Contains(network, "10.0.10.2/24") {
		t.Errorf("allocated address carries a CIDR suffix the template never used:\n%s", network)
	}

	d, err := svc.Store.GetDevice(ctx, ap2)
	if err != nil {
		t.Fatalf("get ap2: %v", err)
	}
	var ov model.DeviceNetwork
	if err := json.Unmarshal([]byte(d.NetworkParams), &ov); err != nil {
		t.Fatalf("ap2 network_params: %v", err)
	}
	if got := ov.IPAddr["lan"]; len(got) != 1 || got[0] != "10.0.10.2" {
		t.Errorf("ap2 override ipaddr = %v, want the bare 10.0.10.2", got)
	}
}
