package config

import (
	"os"
	"path/filepath"
)

const (
	envVClawHome = "VCLAW_HOME"
	envCacheDir  = "VCLAW_CACHE_DIR"
	envDataDir   = "VCLAW_DATA_DIR"
)

func CacheDir() (string, error) {
	if custom := os.Getenv(envCacheDir); custom != "" {
		return custom, nil
	}
	return baseDir()
}

func DataDir() (string, error) {
	if custom := os.Getenv(envDataDir); custom != "" {
		return custom, nil
	}
	return baseDir()
}

func baseDir() (string, error) {
	if custom := os.Getenv(envVClawHome); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".vclaw"), nil
}
