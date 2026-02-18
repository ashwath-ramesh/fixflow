package config

import (
	"path/filepath"
	"testing"
)

func TestVersionCheckPathUsesStateDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	got, err := VersionCheckPath()
	if err != nil {
		t.Fatalf("version check path: %v", err)
	}
	want := filepath.Join(tmp, "autopr", "version-check.json")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
