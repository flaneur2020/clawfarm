package config

import (
	"os"
	"path/filepath"
)

const (
	envClawfarmHome = "CLAWFARM_HOME"
	envCacheDir     = "CLAWFARM_CACHE_DIR"
	envDataDir      = "CLAWFARM_DATA_DIR"
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
	if custom := os.Getenv(envClawfarmHome); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clawfarm"), nil
}
