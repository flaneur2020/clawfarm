package config

import (
	"os"
	"path/filepath"
)

const (
	envCacheDir = "VCLAW_CACHE_DIR"
	envDataDir  = "VCLAW_DATA_DIR"
)

func CacheDir() (string, error) {
	if custom := os.Getenv(envCacheDir); custom != "" {
		return custom, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "vclaw"), nil
}

func DataDir() (string, error) {
	if custom := os.Getenv(envDataDir); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "vclaw"), nil
}
