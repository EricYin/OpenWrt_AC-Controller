package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The browser-facing hardening must be on every response, including the public
// ones an attacker can reach without credentials.
func TestSecurityHeadersOnPublicEndpoints(t *testing.T) {
	f := newFixtureWithAuth(t, "", "")

	resp, err := http.Get(f.server.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for name, value := range want {
		if got := resp.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors: %q", csp)
	}
	if id := resp.Header.Get("X-Request-Id"); id == "" {
		t.Error("X-Request-Id header is empty")
	}
	// Direct HTTP must not advertise HSTS: it would be ignored, and worse,
	// imply a TLS deployment that does not exist.
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("unexpected HSTS over plain HTTP: %q", hsts)
	}
}

func TestMetricsExposeAggregatesOnly(t *testing.T) {
	f := newFixtureWithAuth(t, "", "")

	resp, err := http.Get(f.server.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("metrics content type = %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	text := string(body)
	for _, want := range []string{"ac_build_info", "ac_devices_total 1", "ac_uptime_seconds", "go_goroutines"} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %q\n%s", want, text)
		}
	}
	// A station's identity must never leak through the unauthenticated scrape.
	if strings.Contains(text, testMAC) {
		t.Errorf("metrics leaked a device MAC:\n%s", text)
	}
}
