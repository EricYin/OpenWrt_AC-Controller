package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDrift(t *testing.T) {
	dir := t.TempDir()
	body := "config wifi-device 'radio0'\n\toption channel '36'\n"
	if err := os.WriteFile(filepath.Join(dir, "wireless"), []byte(body), 0o644); err != nil {
		t.Fatalf("write wireless: %v", err)
	}
	baseline := map[string]string{"wireless": hashConfig(body)}

	if drift, files := configDrift(dir, baseline); drift || len(files) != 0 {
		t.Fatalf("unchanged file reported as drift: %v %v", drift, files)
	}

	// A local UCI edit is exactly what an operator does over SSH.
	if err := os.WriteFile(filepath.Join(dir, "wireless"), []byte(body+"\n# local edit\n"), 0o644); err != nil {
		t.Fatalf("edit wireless: %v", err)
	}
	if drift, files := configDrift(dir, baseline); !drift || len(files) != 1 || files[0] != "wireless" {
		t.Fatalf("edit not detected: %v %v", drift, files)
	}

	// A removed file is drift too: the controller still believes it is running.
	if err := os.Remove(filepath.Join(dir, "wireless")); err != nil {
		t.Fatalf("remove wireless: %v", err)
	}
	if drift, files := configDrift(dir, baseline); !drift || len(files) != 1 || files[0] != "wireless" {
		t.Fatalf("missing file not detected: %v %v", drift, files)
	}

	// Nothing was ever applied: unknown is not the same as drifted.
	if drift, files := configDrift(dir, nil); drift || len(files) != 0 {
		t.Fatalf("empty baseline reported drift: %v %v", drift, files)
	}
}
