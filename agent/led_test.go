package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLED(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	for file, body := range map[string]string{
		"trigger":        "none",
		"brightness":     "0",
		"max_brightness": "1",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", name, file, err)
		}
	}
	return dir
}

func readLED(t *testing.T, dir, attr string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, attr))
	if err != nil {
		t.Fatalf("read %s: %v", attr, err)
	}
	return string(body)
}

func TestSetStatusLEDOffTurnsEveryStatusLEDDark(t *testing.T) {
	root := t.TempDir()
	red := writeLED(t, root, "red:status")
	green := writeLED(t, root, "green:status")
	blue := writeLED(t, root, "blue:status")
	lan := writeLED(t, root, "lan3:green")
	if err := os.WriteFile(filepath.Join(lan, "brightness"), []byte("1"), 0o644); err != nil {
		t.Fatalf("seed lan brightness: %v", err)
	}

	names, err := setStatusLED(root, "off")
	if err != nil {
		t.Fatalf("set off: %v", err)
	}
	if len(names) != 3 {
		t.Errorf("names = %v, want three status LEDs", names)
	}
	for _, dir := range []string{red, green, blue} {
		if got := readLED(t, dir, "brightness"); got != "0" {
			t.Errorf("%s brightness = %q, want 0", dir, got)
		}
	}
	if got := readLED(t, lan, "brightness"); got != "1" {
		t.Errorf("non-status LED was touched: brightness = %q, want 1", got)
	}
}

func TestSetStatusLEDOnLightsOnlyGreen(t *testing.T) {
	root := t.TempDir()
	red := writeLED(t, root, "red:status")
	green := writeLED(t, root, "green:status")
	writeLED(t, root, "blue:status")

	names, err := setStatusLED(root, "on")
	if err != nil {
		t.Fatalf("set on: %v", err)
	}
	if len(names) != 3 {
		t.Errorf("names = %v, want three status LEDs", names)
	}
	if got := readLED(t, green, "brightness"); got != "1" {
		t.Errorf("green brightness = %q, want 1", got)
	}
	if got := readLED(t, red, "brightness"); got != "0" {
		t.Errorf("red brightness = %q, want 0", got)
	}
}

func TestSetStatusLEDWithoutStatusLEDFails(t *testing.T) {
	root := t.TempDir()
	writeLED(t, root, "lan3:green")

	if _, err := setStatusLED(root, "off"); err == nil {
		t.Error("missing status LED accepted")
	}
}

// sysfs exposes each LED as a symlink; the discovery must follow it rather than
// dropping every entry because DirEntry.IsDir is false for symlinks.
func TestSetStatusLEDFollowsSysfsSymlinks(t *testing.T) {
	root := t.TempDir()
	targets := t.TempDir()
	dir := filepath.Join(targets, "green")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	for file, body := range map[string]string{"trigger": "none", "brightness": "0", "max_brightness": "1"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatalf("write target %s: %v", file, err)
		}
	}
	if err := os.Symlink(dir, filepath.Join(root, "green:status")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := setStatusLED(root, "on"); err != nil {
		t.Fatalf("set on: %v", err)
	}
	if got := readLED(t, dir, "brightness"); got != "1" {
		t.Errorf("green brightness = %q, want 1", got)
	}
}

func TestReadStatusLEDReportsBrightness(t *testing.T) {
	root := t.TempDir()
	green := writeLED(t, root, "green_status")
	writeLED(t, root, "red_status")

	state, ok := readStatusLED(root)
	if !ok || state != "off" {
		t.Fatalf("state = %q ok=%v, want off", state, ok)
	}
	if err := os.WriteFile(filepath.Join(green, "brightness"), []byte("1"), 0o644); err != nil {
		t.Fatalf("seed brightness: %v", err)
	}
	state, ok = readStatusLED(root)
	if !ok || state != "on" {
		t.Errorf("state = %q ok=%v, want on", state, ok)
	}
}

// "on" is expressed on the green LED, so another lit colour must not be read
// as the running state.
func TestReadStatusLEDPrefersGreen(t *testing.T) {
	root := t.TempDir()
	red := writeLED(t, root, "red_status")
	writeLED(t, root, "green_status")
	if err := os.WriteFile(filepath.Join(red, "brightness"), []byte("1"), 0o644); err != nil {
		t.Fatalf("seed red brightness: %v", err)
	}
	if state, ok := readStatusLED(root); !ok || state != "off" {
		t.Errorf("state = %q ok=%v, want off from the green LED", state, ok)
	}
}

func TestReadStatusLEDWithoutStatusLED(t *testing.T) {
	root := t.TempDir()
	writeLED(t, root, "lan3_green")
	if state, ok := readStatusLED(root); ok || state != "" {
		t.Errorf("state = %q ok=%v, want no report", state, ok)
	}
}

func TestReportedLEDStateUsesConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	green := writeLED(t, filepath.Join(root, "sys/class/leds"), "green_status")
	if err := os.WriteFile(filepath.Join(green, "brightness"), []byte("1"), 0o644); err != nil {
		t.Fatalf("seed brightness: %v", err)
	}
	if got := reportedLEDState(root); got != "on" {
		t.Errorf("reported = %q, want on", got)
	}
}

func TestValidateLEDState(t *testing.T) {
	for _, state := range []string{"on", "off", " ON "} {
		if err := validateLEDState(state); err != nil {
			t.Errorf("state %q rejected: %v", state, err)
		}
	}
	for _, state := range []string{"", "blink", "1"} {
		if err := validateLEDState(state); err == nil {
			t.Errorf("state %q accepted", state)
		}
	}
}
