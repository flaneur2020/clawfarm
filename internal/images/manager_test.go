package images

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDetectDiskFormatByMagic(t *testing.T) {
	tmpDir := t.TempDir()
	qcowPath := filepath.Join(tmpDir, "disk.qcow2")
	rawPath := filepath.Join(tmpDir, "disk.raw")

	if err := os.WriteFile(qcowPath, append([]byte("QFI\xfb"), []byte("rest")...), 0o644); err != nil {
		t.Fatalf("write qcow2: %v", err)
	}
	if err := os.WriteFile(rawPath, []byte("RAW!"), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}

	format, err := detectDiskFormatByMagic(qcowPath)
	if err != nil {
		t.Fatalf("detect qcow2 failed: %v", err)
	}
	if format != "qcow2" {
		t.Fatalf("unexpected format: %s", format)
	}

	format, err = detectDiskFormatByMagic(rawPath)
	if err != nil {
		t.Fatalf("detect raw failed: %v", err)
	}
	if format != "raw" {
		t.Fatalf("unexpected format: %s", format)
	}
}

func TestManagerListAndResolve(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("unsupported architecture in test environment")
	}

	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, os.Stdout)

	imageDir := filepath.Join(tmpDir, "images", "ubuntu_24.04")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir imageDir: %v", err)
	}

	runtimePath := filepath.Join(imageDir, imageFileName)
	if err := os.WriteFile(runtimePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write runtime image: %v", err)
	}

	meta := Metadata{
		Ref:         "ubuntu:24.04",
		Version:     "24.04",
		Codename:    "noble",
		Arch:        runtime.GOARCH,
		ImageDir:    imageDir,
		RuntimeDisk: runtimePath,
		Ready:       true,
		DiskFormat:  "raw",
	}
	if err := writeMetadata(filepath.Join(imageDir, metadataFileName), meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	items, err := manager.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("unexpected item count: %d", len(items))
	}
	if items[0].Ref != "ubuntu:24.04" {
		t.Fatalf("unexpected ref: %s", items[0].Ref)
	}
	if !items[0].Ready {
		t.Fatalf("expected ready image")
	}

	if _, err := manager.Resolve("ubuntu:24.04"); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
}

func TestListAvailableIncludesDownloadedMarker(t *testing.T) {
	tmpDir := t.TempDir()
	manager := NewManager(tmpDir, nil)

	items, err := manager.ListAvailable()
	if err != nil {
		t.Fatalf("ListAvailable failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected at least one supported image")
	}
	if items[0].Ref != "ubuntu:24.04" {
		t.Fatalf("expected ubuntu:24.04, got %s", items[0].Ref)
	}
	if items[0].Ready {
		t.Fatalf("expected not-downloaded image")
	}

	imageDir := filepath.Join(tmpDir, "images", "ubuntu_24.04")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir imageDir: %v", err)
	}
	runtimePath := filepath.Join(imageDir, imageFileName)
	if err := os.WriteFile(runtimePath, []byte("img"), 0o644); err != nil {
		t.Fatalf("write runtime image: %v", err)
	}
	now := time.Now().UTC()
	if err := writeMetadata(filepath.Join(imageDir, metadataFileName), Metadata{
		Ref:          "ubuntu:24.04",
		Version:      "24.04",
		Codename:     "noble",
		Arch:         runtime.GOARCH,
		ImageDir:     imageDir,
		RuntimeDisk:  runtimePath,
		Ready:        true,
		DiskFormat:   "raw",
		FetchedAtUTC: now,
		UpdatedAtUTC: now,
	}); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	items, err = manager.ListAvailable()
	if err != nil {
		t.Fatalf("ListAvailable failed: %v", err)
	}
	if len(items) == 0 || !items[0].Ready {
		t.Fatalf("expected downloaded image to be marked ready")
	}
}

func TestDownloadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("payload"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "artifact")
	if err := downloadFile(context.Background(), server.URL, path, nil, ""); err != nil {
		t.Fatalf("downloadFile failed: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(body) != "payload" {
		t.Fatalf("unexpected body: %q", string(body))
	}
}

func TestDownloadFileWithProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Length", "7")
		_, _ = writer.Write([]byte("payload"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "artifact")
	var output strings.Builder
	if err := downloadFile(context.Background(), server.URL, path, &output, "image"); err != nil {
		t.Fatalf("downloadFile failed: %v", err)
	}
	if !strings.Contains(output.String(), "100.0%") {
		t.Fatalf("expected progress output, got %q", output.String())
	}
}

func TestFetchUsesCachedArtifactsWithoutDownloading(t *testing.T) {
	tmpDir := t.TempDir()
	var output strings.Builder
	manager := NewManager(tmpDir, &output)

	imageDir := filepath.Join(tmpDir, "images", "ubuntu_24.04")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}

	runtimePath := filepath.Join(imageDir, imageFileName)
	if err := os.WriteFile(runtimePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("write runtime image: %v", err)
	}

	now := time.Now().UTC()
	meta := Metadata{
		Ref:          "ubuntu:24.04",
		Version:      "24.04",
		Codename:     "noble",
		Arch:         runtime.GOARCH,
		ImageDir:     imageDir,
		RuntimeDisk:  runtimePath,
		Ready:        true,
		DiskFormat:   "raw",
		FetchedAtUTC: now,
		UpdatedAtUTC: now,
	}
	if err := writeMetadata(filepath.Join(imageDir, metadataFileName), meta); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	before, err := os.Stat(runtimePath)
	if err != nil {
		t.Fatalf("stat runtime before: %v", err)
	}

	result, err := manager.Fetch(context.Background(), "ubuntu:24.04")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if result.Ref != "ubuntu:24.04" {
		t.Fatalf("unexpected ref: %s", result.Ref)
	}
	if !strings.Contains(output.String(), "using cached image") {
		t.Fatalf("expected cached image message, got %q", output.String())
	}

	after, err := os.Stat(runtimePath)
	if err != nil {
		t.Fatalf("stat runtime after: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("expected cached artifact unchanged")
	}
}
