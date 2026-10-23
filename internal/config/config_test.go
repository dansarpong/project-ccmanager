package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDataDirsArePrivate(t *testing.T) {
	for _, existing := range []bool{false, true} {
		data := filepath.Join(t.TempDir(), "ccmanager")
		cfg := Config{DataDir: data, TrashDir: filepath.Join(data, "trash")}
		if existing {
			// An earlier install left both directories readable by everyone.
			if err := os.MkdirAll(cfg.TrashDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{cfg.DataDir, cfg.TrashDir} {
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}

		if err := cfg.EnsureDataDirs(); err != nil {
			t.Fatalf("EnsureDataDirs (existing=%v): %v", existing, err)
		}
		for _, dir := range []string{cfg.DataDir, cfg.TrashDir} {
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Errorf("existing=%v: %s mode = %o, want 700", existing, dir, got)
			}
		}
	}
}
