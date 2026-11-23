package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/naoki66/ac-controller/controller/internal/provision"
	"github.com/naoki66/ac-controller/shared/bundle"
	"github.com/naoki66/ac-controller/shared/model"
	"github.com/naoki66/ac-controller/shared/render"
)

// The SSID page and the ports page edit the same shared template from two
// directions. These tests pin the partial-update contract: an SSID save must
// never rewrite the layer-2 fabric, and a rename must never need the SSIDs.

func TestTemplateSSIDsPartialUpdate(t *testing.T) {
	f := newFixture(t)

	// Change ONLY the SSID list; bridge and radios must survive untouched.
	newSSIDs := []model.SSID{{
		SSID:     "Replaced",
		Bands:    []string{model.Band2G},
		Network:  "lan",
		Security: model.SecWPA2,
		PSK:      "12345678",
	}}
	resp := f.do(http.MethodPut, "/api/v1/templates/default/ssids", map[string]any{"ssids": newSSIDs})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put ssids status = %d", resp.StatusCode)
	}
	saved := decode[model.Template](t, resp)
	if len(saved.SSIDs) != 1 || saved.SSIDs[0].SSID != "Replaced" {
		t.Fatalf("saved ssids = %+v", saved.SSIDs)
	}
	// The default template is wireless-only, so its zeroed bridge must survive
	// an SSID-only save exactly as a declared one would.
	if saved.Bridge.Name != "" || len(saved.Bridge.Ports) != 0 ||
		len(saved.Bridge.VLANs) != 0 || len(saved.Bridge.Interfaces) != 0 {
		t.Errorf("bridge was clobbered: %+v", saved.Bridge)
	}
	if len(saved.Radios) == 0 {
		t.Errorf("radios were clobbered")
	}

	// Read back through the store: what the API returned is what is stored.
	stored, err := f.store.GetTemplate(t.Context(), "default")
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if len(stored.SSIDs) != 1 || stored.SSIDs[0].SSID != "Replaced" {
		t.Fatalf("stored ssids = %+v", stored.SSIDs)
	}
	if stored.Bridge.Name != provision.DefaultTemplate().Bridge.Name {
		t.Errorf("stored bridge name = %q", stored.Bridge.Name)
	}
}

func TestTemplateSSIDsRejectsInvalid(t *testing.T) {
	f := newFixture(t)

	// An enabled SSID without a network is invalid, so the stored template
	// must stay unchanged.
	resp := f.do(http.MethodPut, "/api/v1/templates/default/ssids",
		map[string]any{"ssids": []model.SSID{{
			SSID: "Broken", Bands: []string{model.Band2G}, Security: model.SecOpen,
		}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid ssids status = %d, want 400", resp.StatusCode)
	}

	// Unknown fields are a client bug; reject rather than silently ignore.
	resp = f.do(http.MethodPut, "/api/v1/templates/default/ssids",
		map[string]any{"ssids": []model.SSID{}, "bridge": map[string]any{}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", resp.StatusCode)
	}

	// Unknown template.
	resp = f.do(http.MethodPut, "/api/v1/templates/nope/ssids",
		map[string]any{"ssids": []model.SSID{}})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing template status = %d, want 404", resp.StatusCode)
	}
}

func TestTemplateSteerPolicyPartialUpdate(t *testing.T) {
	f := newFixture(t)

	// Change ONLY the roaming policy; SSIDs, radios and the bridge must survive.
	resp := f.do(http.MethodPut, "/api/v1/templates/default/steer-policy", map[string]any{
		"policy": map[string]any{"btm_snr": -70, "btm_consecutive": 3},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put steer-policy status = %d", resp.StatusCode)
	}
	saved := decode[model.Template](t, resp)
	if saved.SteerPolicy == nil || saved.SteerPolicy.BtmSNR != -70 {
		t.Fatalf("saved policy = %+v", saved.SteerPolicy)
	}
	// Defaults are resolved before storing, so the page and the AP agree.
	if saved.SteerPolicy.MinConnectedMS != 30000 || saved.SteerPolicy.CooldownMS != 120000 {
		t.Errorf("defaults not normalized: %+v", saved.SteerPolicy)
	}
	if len(saved.SSIDs) != len(provision.DefaultTemplate().SSIDs) {
		t.Errorf("ssids clobbered by policy save: %d", len(saved.SSIDs))
	}
	if len(saved.Radios) == 0 {
		t.Errorf("radios clobbered by policy save")
	}

	// An explicit null removes the policy; that is a real operator choice.
	resp = f.do(http.MethodPut, "/api/v1/templates/default/steer-policy",
		map[string]any{"policy": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear steer-policy status = %d", resp.StatusCode)
	}
	stored, err := f.store.GetTemplate(t.Context(), "default")
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if stored.SteerPolicy != nil {
		t.Errorf("policy not cleared: %+v", stored.SteerPolicy)
	}
}

// Saving a roaming policy applies it at once: every adopted device on the
// template gets a new revision whose bundle has no network file, so the change
// cannot move an address or reload layer 2.
func TestSteerPolicyPushesWirelessOnly(t *testing.T) {
	f := newFixture(t)
	f.adopt()
	ctx := context.Background()

	resp := f.do(http.MethodPut, "/api/v1/templates/default/steer-policy", map[string]any{
		"policy": map[string]any{"btm_snr": -70, "btm_consecutive": 3},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put steer-policy status = %d", resp.StatusCode)
	}
	var body struct {
		WirelessPush *struct {
			Pushed int `json:"pushed"`
			Failed []struct {
				MAC   string `json:"mac"`
				Error string `json:"error"`
			} `json:"failed"`
		} `json:"wireless_push"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	resp.Body.Close()
	if body.WirelessPush == nil || body.WirelessPush.Pushed != 1 || len(body.WirelessPush.Failed) != 0 {
		t.Fatalf("wireless push = %+v, want one device", body.WirelessPush)
	}

	d, err := f.store.GetDevice(ctx, testMAC)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.ConfigRev != 2 {
		t.Errorf("config rev = %d, want 2", d.ConfigRev)
	}

	_, blob, err := f.store.LatestPackage(ctx, testMAC)
	if err != nil {
		t.Fatalf("latest package: %v", err)
	}
	_, files, err := bundle.Extract(blob, f.key.Public)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, ok := files[render.FileNetwork]; ok {
		t.Error("wireless-only bundle contains a network file")
	}
	if _, ok := files[render.FileWireless]; !ok {
		t.Error("wireless-only bundle has no wireless file")
	}
	if _, ok := files[render.FileSteerPolicy]; !ok {
		t.Error("wireless-only bundle has no steer-policy file")
	}
}

func TestTemplateSteerPolicyRejectsInvalid(t *testing.T) {
	f := newFixture(t)

	// A non-negative dBm threshold is meaningless and must be refused.
	resp := f.do(http.MethodPut, "/api/v1/templates/default/steer-policy", map[string]any{
		"policy": map[string]any{"btm_snr": 0, "btm_consecutive": 3},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("btm_snr=0 status = %d, want 400", resp.StatusCode)
	}

	// Unknown fields are a client bug, not something to ignore.
	resp = f.do(http.MethodPut, "/api/v1/templates/default/steer-policy", map[string]any{
		"policy": map[string]any{"btm_snr": -70},
		"ssids":  []model.SSID{},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", resp.StatusCode)
	}

	resp = f.do(http.MethodPut, "/api/v1/templates/nope/steer-policy", map[string]any{
		"policy": map[string]any{"btm_snr": -70},
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing template status = %d, want 404", resp.StatusCode)
	}
}

func TestTemplateRename(t *testing.T) {
	f := newFixture(t)

	resp := f.do(http.MethodPut, "/api/v1/templates/default/name",
		map[string]string{"name": "  站点主模板  "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status = %d", resp.StatusCode)
	}
	renamed := decode[model.Template](t, resp)
	if renamed.Name != "站点主模板" {
		t.Errorf("name = %q, want trimmed 站点主模板", renamed.Name)
	}
	if renamed.ID != "default" {
		t.Errorf("id changed to %q", renamed.ID)
	}
	if len(renamed.SSIDs) != len(provision.DefaultTemplate().SSIDs) {
		t.Errorf("ssids changed by rename: %d", len(renamed.SSIDs))
	}

	// Empty name fails validation and leaves the stored name intact.
	resp = f.do(http.MethodPut, "/api/v1/templates/default/name", map[string]string{"name": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty rename status = %d, want 400", resp.StatusCode)
	}
	stored, err := f.store.GetTemplate(t.Context(), "default")
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if stored.Name != "站点主模板" {
		t.Errorf("stored name = %q, want unchanged", stored.Name)
	}
}
