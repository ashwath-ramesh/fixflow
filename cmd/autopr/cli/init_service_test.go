package cli

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"autopr/internal/config"
)

func TestMaybeInstallServiceFromInitSkipsOnNonDarwin(t *testing.T) {
	t.Parallel()

	called := false
	installed, err := maybeInstallServiceFromInit(
		"linux",
		bufio.NewReader(strings.NewReader("y\n")),
		&bytes.Buffer{},
		&config.Config{},
		"config.toml",
		func(*config.Config, string) error {
			called = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if installed {
		t.Fatalf("expected installed=false")
	}
	if called {
		t.Fatalf("install function should not be called")
	}
}

func TestMaybeInstallServiceFromInitNoChoice(t *testing.T) {
	t.Parallel()

	called := false
	installed, err := maybeInstallServiceFromInit(
		"darwin",
		bufio.NewReader(strings.NewReader("n\n")),
		&bytes.Buffer{},
		&config.Config{},
		"config.toml",
		func(*config.Config, string) error {
			called = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if installed {
		t.Fatalf("expected installed=false")
	}
	if called {
		t.Fatalf("install function should not be called")
	}
}

func TestMaybeInstallServiceFromInitYesChoice(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	var gotCfg *config.Config
	var gotPath string
	var out bytes.Buffer

	installed, err := maybeInstallServiceFromInit(
		"darwin",
		bufio.NewReader(strings.NewReader("y\n")),
		&out,
		cfg,
		"/tmp/config.toml",
		func(inCfg *config.Config, inPath string) error {
			gotCfg = inCfg
			gotPath = inPath
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !installed {
		t.Fatalf("expected installed=true")
	}
	if gotCfg != cfg {
		t.Fatalf("expected same config pointer")
	}
	if gotPath != "/tmp/config.toml" {
		t.Fatalf("expected config path /tmp/config.toml, got %q", gotPath)
	}
	if !strings.Contains(out.String(), "Service installed: io.autopr.daemon") {
		t.Fatalf("missing install output: %q", out.String())
	}
}

func TestMaybeInstallServiceFromInitInstallError(t *testing.T) {
	t.Parallel()

	expected := errors.New("launchctl bootstrap failed")
	_, err := maybeInstallServiceFromInit(
		"darwin",
		bufio.NewReader(strings.NewReader("yes\n")),
		&bytes.Buffer{},
		&config.Config{},
		"config.toml",
		func(*config.Config, string) error {
			return expected
		},
	)
	if !errors.Is(err, expected) {
		t.Fatalf("expected %v, got %v", expected, err)
	}
}
