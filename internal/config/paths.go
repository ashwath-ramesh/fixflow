package config

import (
	"os"
	"path/filepath"
)

// ConfigDir returns the autopr config directory, respecting XDG_CONFIG_HOME.
// Defaults to ~/.config/autopr/.
func ConfigDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "autopr"), nil
}

// GlobalConfigPath returns the path to the global config file.
func GlobalConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// CredentialsPath returns the path to the credentials file.
func CredentialsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.toml"), nil
}

// DataDir returns the autopr data directory, respecting XDG_DATA_HOME.
// Defaults to ~/.local/share/autopr/.
func DataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "autopr"), nil
}

// StateDir returns the autopr state directory, respecting XDG_STATE_HOME.
// Defaults to ~/.local/state/autopr/.
func StateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "autopr"), nil
}
