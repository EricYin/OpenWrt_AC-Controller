package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/naoki66/ac-controller/controller/internal/store"
	"github.com/naoki66/ac-controller/shared/model"
)

// lanTemplate returns a template that owns layer 2 with one static management
// address, i.e. the shape whose address two units would fight over.
func lanTemplate() model.Template {
	return model.Template{
		ID:   "lan-managed",
		Name: "LAN managed",
		Bridge: model.Bridge{
			Name:  "br-lan",
			Ports: []string{"lan2", "lan3", "wan"},
			VLANs: []model.BridgeVLAN{{VLAN: 10, Ports: []string{"lan2:u*", "wan:t"}}},
			Interfaces: []model.Interface{
				{Name: "lan", Device: "br-lan.10", Proto: "static", IPAddr: []string{"10.0.10.1/24"}},
			},
		},
		Radios: []model.Radio{{Index: 1, Band: model.Band5G, Channel: "36", HTMode: "EHT160", TxPower: 20}},
		SSIDs: []model.SSID{{
			SSID: "Corp", Bands: []string{model.Band5G}, Network: "lan",
			Security: model.SecWPA3, PSK: "corp-passphrase",
		}},
	}
}

// The operator flow the UI drives: the push is refused with 409 and a
// machine-readable conflict list, preflight reports the same list without
// failing, and a second call with force goes through.
func TestAdoptAddressConflictReportsThenForce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.store.UpsertTemplate(ctx, lanTemplate()); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	const otherMAC = "8c:de:52:11:22:44"
	if err := f.store.UpsertDiscovered(ctx, store.Device{
		MAC: otherMAC, Model: "XR1710G", Firmware: "20261026",
		IP: "10.0.10.2", Hostname: "ap2",
	}); err != nil {
		t.Fatalf("seed second device: %v", err)
	}

	// The first unit takes the template address verbatim.
	resp := f.do(http.MethodPost, "/api/v1/devices/"+testMAC+"/adopt",
		map[string]any{"template_id": "lan-managed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adopt first device = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The operator types the same address on the second unit.
	setResp := f.do(http.MethodPut, "/api/v1/devices/"+otherMAC+"/network",
		model.DeviceNetwork{IPAddr: map[string][]string{"lan": {"10.0.10.1/24"}}})
	if setResp.StatusCode != http.StatusOK {
		t.Fatalf("set network = %d", setResp.StatusCode)
	}
	setResp.Body.Close()

	// Preflight lists the conflict instead of failing.
	pfResp := f.do(http.MethodGet,
		"/api/v1/devices/"+otherMAC+"/preflight?template_id=lan-managed", nil)
	if pfResp.StatusCode != http.StatusOK {
		t.Fatalf("preflight = %d, want a report, not an error", pfResp.StatusCode)
	}
	var pf struct {
		Conflicts []struct {
			IP        string `json:"ip"`
			Interface string `json:"interface"`
			Device    string `json:"device"`
		} `json:"conflicts"`
	}
	if err := json.NewDecoder(pfResp.Body).Decode(&pf); err != nil {
		t.Fatalf("decode preflight: %v", err)
	}
	pfResp.Body.Close()
	if len(pf.Conflicts) != 1 || pf.Conflicts[0].IP != "10.0.10.1" ||
		pf.Conflicts[0].Interface != "lan" || pf.Conflicts[0].Device != testMAC {
		t.Fatalf("preflight conflicts = %+v", pf.Conflicts)
	}

	// Without force the push is a 409 carrying the same list.
	conflictResp := f.do(http.MethodPost, "/api/v1/devices/"+otherMAC+"/adopt",
		map[string]any{"template_id": "lan-managed"})
	if conflictResp.StatusCode != http.StatusConflict {
		t.Fatalf("unforced adopt = %d, want 409", conflictResp.StatusCode)
	}
	var body struct {
		Error     string `json:"error"`
		Conflicts []struct {
			IP     string `json:"ip"`
			Device string `json:"device"`
		} `json:"conflicts"`
	}
	if err := json.NewDecoder(conflictResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode conflict body: %v", err)
	}
	conflictResp.Body.Close()
	if body.Error == "" || len(body.Conflicts) != 1 || body.Conflicts[0].IP != "10.0.10.1" {
		t.Fatalf("409 body = %+v", body)
	}

	// With force the operator's duplicate address is pushed.
	forced := f.do(http.MethodPost, "/api/v1/devices/"+otherMAC+"/adopt",
		map[string]any{"template_id": "lan-managed", "force": true})
	if forced.StatusCode != http.StatusOK {
		t.Fatalf("forced adopt = %d, want 200", forced.StatusCode)
	}
	forced.Body.Close()
}

// A typo in a typed address must not reach the renderer: the AP would come up
// unreachable with a nonsense option in its network file. A bare address is
// likewise refused — without the prefix netifd would fall back to /32 on any
// template interface that declares no netmask option, silently breaking the
// subnet. The mask is mandatory everywhere.
func TestSetDeviceNetworkRejectsInvalidAddress(t *testing.T) {
	f := newFixture(t)

	resp := f.do(http.MethodPut, "/api/v1/devices/"+testMAC+"/network",
		map[string]any{"ipaddr": map[string][]string{"lan": {"10.0.10.999/24"}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid address accepted: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// A well-formed but mask-less address is rejected too: it would inherit
	// the template netmask — or /32 where the template declares none.
	bare := f.do(http.MethodPut, "/api/v1/devices/"+testMAC+"/network",
		map[string]any{"ipaddr": map[string][]string{"lan": {"10.0.10.50"}}})
	if bare.StatusCode != http.StatusBadRequest {
		t.Fatalf("bare address accepted: %d, want 400", bare.StatusCode)
	}
	bare.Body.Close()

	ok := f.do(http.MethodPut, "/api/v1/devices/"+testMAC+"/network",
		map[string]any{"ipaddr": map[string][]string{"lan": {"10.0.10.50/24"}}})
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("valid address rejected: %d", ok.StatusCode)
	}
	ok.Body.Close()
}
