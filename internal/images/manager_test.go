package images

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestPrepareRuntimeDiskWithoutQemuForRaw(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "base.img")
	diskPath := filepath.Join(tmpDir, "disk.raw")

	if err := os.WriteFile(basePath, []byte("RAWCONTENT"), 0o644); err != nil {
		t.Fatalf("write base image: %v", err)
	}

	originalPath := os.Getenv("PATH")
	defer os.Setenv("PATH", originalPath)
	if err := os.Setenv("PATH", ""); err != nil {
		t.Fatalf("set PATH: %v", err)
	}

	format, err := prepareRuntimeDisk(basePath, diskPath)
	if err != nil {
		t.Fatalf("prepareRuntimeDisk failed: %v", err)
	}
	if format != "raw" {
		t.Fatalf("unexpected format: %s", format)
	}
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("disk.raw not created: %v", err)
	}
}

func TestPrepareRuntimeDiskWithoutQemuForQCOW2(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "base.img")
	diskPath := filepath.Join(tmpDir, "disk.raw")

	if err := os.WriteFile(basePath, append([]byte("QFI\xfb"), []byte("payload")...), 0o644); err != nil {
		t.Fatalf("write qcow image: %v", err)
	}

	originalPath := os.Getenv("PATH")
	defer os.Setenv("PATH", originalPath)
	if err := os.Setenv("PATH", ""); err != nil {
		t.Fatalf("set PATH: %v", err)
	}

	_, err := prepareRuntimeDisk(basePath, diskPath)
	if err == nil {
		t.Fatalf("expected error for qcow2 without qemu-img")
	}
	if !strings.Contains(err.Error(), "qemu-img is required") {
		t.Fatalf("unexpected error: %v", err)
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
	meta := Metadata{
		Ref:         "ubuntu:24.04",
		Version:     "24.04",
		Codename:    "noble",
		Arch:        runtime.GOARCH,
		ImageDir:    imageDir,
		KernelPath:  filepath.Join(imageDir, kernelFileName),
		InitrdPath:  filepath.Join(imageDir, initrdFileName),
		BaseImage:   filepath.Join(imageDir, baseImageName),
		RuntimeDisk: filepath.Join(imageDir, runtimeDiskName),
		Ready:       true,
	}
	for _, path := range []string{meta.KernelPath, meta.InitrdPath, meta.RuntimeDisk} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write file %s: %v", path, err)
		}
	}
	bytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, metadataFileName), bytes, 0o644); err != nil {
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

	if _, err := manager.Resolve("ubuntu:24.04"); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
}

func TestDownloadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("payload"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "artifact")
	if err := downloadFile(context.Background(), server.URL, path); err != nil {
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
