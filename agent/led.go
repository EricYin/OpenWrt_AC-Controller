package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// defaultLEDRoot is where the kernel exposes LEDs on OpenWrt. The XR1710G
// declares four status colours (red/green/blue/white); other boards may have
// one. Matching on "status" keeps the action closed: the controller can name a
// state, never an arbitrary sysfs path.
const defaultLEDRoot = "/sys/class/leds"

// setStatusLED switches every LED whose sysfs name contains "status".
//
// "off" drives all of them dark. "on" drives the green one (the normal running
// colour) and leaves the other colours dark, so an RGB package does not turn
// white when the operator only wanted the indicator back. If no green LED
// exists, the first status LED is used.
func setStatusLED(root, state string) ([]string, error) {
	names, err := statusLEDNames(root)
	if err != nil {
		return nil, err
	}

	// Clear the trigger first, otherwise the kernel's heartbeat/netdev trigger
	// keeps overwriting brightness. A missing trigger file is not fatal: some
	// LEDs expose brightness only.
	for _, name := range names {
		_ = writeLEDAttr(root, name, "trigger", "none")
	}
	for _, name := range names {
		if err := writeLEDAttr(root, name, "brightness", "0"); err != nil {
			return names, fmt.Errorf("turn off %s: %w", name, err)
		}
	}
	if state != "on" {
		return names, nil
	}

	target := primaryStatusLED(names)
	max := "1"
	if body, err := os.ReadFile(filepath.Join(root, target, "max_brightness")); err == nil {
		if v := strings.TrimSpace(string(body)); v != "" && v != "0" {
			max = v
		}
	}
	if err := writeLEDAttr(root, target, "brightness", max); err != nil {
		return names, fmt.Errorf("turn on %s: %w", target, err)
	}
	return names, nil
}

// statusLEDNames lists the status LEDs under root, sorted for a stable order.
// /sys/class/leds entries are symlinks to the device directories, so DirEntry
// .IsDir is false for every one of them; follow the link before deciding.
func statusLEDNames(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var names []string
	for _, e := range entries {
		info, err := os.Stat(filepath.Join(root, e.Name()))
		if err != nil || !info.IsDir() {
			continue
		}
		if strings.Contains(strings.ToLower(e.Name()), "status") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no status LED under %s", root)
	}
	sort.Strings(names)
	return names, nil
}

// primaryStatusLED picks the LED "on" is expressed on: green when present, the
// first one otherwise.
func primaryStatusLED(names []string) string {
	for _, name := range names {
		if strings.Contains(strings.ToLower(name), "green") {
			return name
		}
	}
	return names[0]
}

// readStatusLED reports the LED state as seen on the device, so the controller
// can spot an AP that lost its schedule after a reboot or power cut. A
// brightness above zero on the primary status LED counts as "on"; reading
// nothing (no LED, permission denied, older kernel layout) reports false so the
// controller keeps using command results instead of guessing.
func readStatusLED(root string) (string, bool) {
	names, err := statusLEDNames(root)
	if err != nil {
		return "", false
	}
	target := primaryStatusLED(names)
	body, err := os.ReadFile(filepath.Join(root, target, "brightness"))
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(body))
	if v == "" {
		return "", false
	}
	if v == "0" {
		return "off", true
	}
	return "on", true
}

func writeLEDAttr(root, name, attr, value string) error {
	path := filepath.Join(root, name, attr)
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(value); err != nil {
		return err
	}
	return nil
}

// reportedLEDState reads the live status LED under the configured filesystem
// root. Empty means "not readable", which the hello leaves absent so the
// controller does not mistake it for "off".
func reportedLEDState(root string) string {
	state, ok := readStatusLED(filepath.Join(root, "sys/class/leds"))
	if !ok {
		return ""
	}
	return state
}

// validateLEDState keeps the agent's argument check aligned with the
// controller's closed set.
func validateLEDState(state string) error {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "on", "off":
		return nil
	default:
		return errors.New("led_set needs args.state: on or off")
	}
}
