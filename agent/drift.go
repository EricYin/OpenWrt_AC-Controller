package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
)

// configDrift compares the digests recorded by the last successful apply with
// the files currently on disk. A missing file is drift too: something removed
// configuration the controller still believes is running. An empty baseline
// means "no known desired state", which must not be reported as drift.
func configDrift(configDir string, baseline map[string]string) (bool, []string) {
	if len(baseline) == 0 {
		return false, nil
	}
	names := make([]string, 0, len(baseline))
	for name := range baseline {
		names = append(names, name)
	}
	sort.Strings(names)

	var changed []string
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil || hashConfigBytes(body) != baseline[name] {
			changed = append(changed, name)
		}
	}
	return len(changed) > 0, changed
}

// hashConfig returns the digest of a rendered file, which is what gets stored
// as the drift baseline.
func hashConfig(content string) string { return hashConfigBytes([]byte(content)) }

func hashConfigBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ensureConfigBaseline adopts the wireless file already on disk as the drift
// baseline when a revision was applied by an older build that did not record
// hashes. Edits made before the upgrade cannot be reconstructed, but every
// later edit becomes visible without requiring a no-op repush.
//
// Only wireless is seeded: it is the file every adopted AP receives, whereas
// network is optional and a locally managed copy must not be reported as drift.
func (a *Agent) ensureConfigBaseline() {
	if len(a.state.ConfigHashes) != 0 || a.state.AppliedRev == 0 || a.Config.DryRun {
		return
	}
	body, err := os.ReadFile(a.Config.wirelessPath())
	if err != nil {
		return
	}
	a.state.ConfigHashes = map[string]string{"wireless": hashConfigBytes(body)}
	if err := a.saveState(); err != nil {
		a.Log.Warn("could not persist config drift baseline", "err", err)
	}
}
