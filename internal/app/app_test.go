package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yazhou/krunclaw/internal/vm"
)

type fakeBackend struct {
	nextPID int
	running map[int]bool
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{nextPID: 4000, running: map[int]bool{}}
}

func (f *fakeBackend) Start(_ context.Context, spec vm.StartSpec) (vm.StartResult, error) {
	f.nextPID++
	pid := f.nextPID
	f.running[pid] = true
	return vm.StartResult{
		PID:           pid,
		DiskPath:      filepath.Join(spec.InstanceDir, "rootfs.qcow2"),
		DiskFormat:    "qcow2",
		SeedISOPath:   filepath.Join(spec.InstanceDir, "seed.iso"),
		SerialLogPath: filepath.Join(spec.InstanceDir, "serial.log"),
		QEMULogPath:   filepath.Join(spec.InstanceDir, "qemu.log"),
		PIDFilePath:   filepath.Join(spec.InstanceDir, "qemu.pid"),
		MonitorPath:   filepath.Join(spec.InstanceDir, "qemu-monitor.sock"),
		Accel:         "tcg",
	}, nil
}

func (f *fakeBackend) Stop(_ context.Context, pid int) error {
	delete(f.running, pid)
	return nil
}

func (f *fakeBackend) Suspend(pid int) error {
	if !f.running[pid] {
		return os.ErrNotExist
	}
	return nil
}

func (f *fakeBackend) Resume(pid int) error {
	if !f.running[pid] {
		return os.ErrNotExist
	}
	return nil
}

func (f *fakeBackend) IsRunning(pid int) bool {
	return f.running[pid]
}

func TestNormalizeRunArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "already flag first", in: []string{"--workspace=.", "ubuntu:24.04"}, want: []string{"--workspace=.", "ubuntu:24.04"}},
		{name: "ref first", in: []string{"ubuntu:24.04", "--workspace=.", "--port=18789"}, want: []string{"--workspace=.", "--port=18789", "ubuntu:24.04"}},
		{name: "empty", in: nil, want: nil},
	}

	for _, tc := range cases {
		got := normalizeRunArgs(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("%s: unexpected result %v", tc.name, got)
		}
	}
}

func TestRunFlowAndInstanceLifecycle(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("VCLAW_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("VCLAW_CACHE_DIR")
	if err := os.Setenv("VCLAW_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("VCLAW_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--publish", "8080:80", "--no-wait"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	if !strings.Contains(out.String(), "CLAWID:") {
		t.Fatalf("run output missing CLAWID: %s", out.String())
	}

	out.Reset()
	if err := application.Run([]string{"ps"}); err != nil {
		t.Fatalf("ps command failed: %v", err)
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("ps output missing status: %s", out.String())
	}

	lines := strings.Split(out.String(), "\n")
	var id string
	for _, line := range lines {
		if strings.HasPrefix(line, "claw-") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				id = fields[0]
				break
			}
		}
	}
	if id == "" {
		t.Fatalf("failed to parse claw id from ps output: %s", out.String())
	}

	out.Reset()
	if err := application.Run([]string{"suspend", id}); err != nil {
		t.Fatalf("suspend failed: %v", err)
	}
	if !strings.Contains(out.String(), "suspended") {
		t.Fatalf("suspend output missing status: %s", out.String())
	}

	out.Reset()
	if err := application.Run([]string{"resume", id}); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("resume output missing status: %s", out.String())
	}

	out.Reset()
	if err := application.Run([]string{"rm", id}); err != nil {
		t.Fatalf("rm failed: %v", err)
	}
	if !strings.Contains(out.String(), "removed") {
		t.Fatalf("rm output missing removed marker: %s", out.String())
	}
}

func TestRunRequiresImage(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("VCLAW_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("VCLAW_CACHE_DIR")
	if err := os.Setenv("VCLAW_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("VCLAW_DATA_DIR")

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--no-wait"})
	if err == nil {
		t.Fatalf("expected error for missing image")
	}
	if !strings.Contains(err.Error(), "image ubuntu:24.04 is not ready") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWaitTimeout(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("VCLAW_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("VCLAW_CACHE_DIR")
	if err := os.Setenv("VCLAW_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("VCLAW_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--ready-timeout-secs=1"})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !strings.Contains(err.Error(), "gateway is not reachable yet") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func seedFetchedImage(t *testing.T, cacheRoot string) {
	t.Helper()

	imageDir := filepath.Join(cacheRoot, "images", "ubuntu_24.04")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	for _, item := range []string{"kernel", "initrd", "disk.raw"} {
		if err := os.WriteFile(filepath.Join(imageDir, item), []byte("x"), 0o644); err != nil {
			t.Fatalf("write image artifact %s: %v", item, err)
		}
	}
	metadata := `{"ref":"ubuntu:24.04","version":"24.04","codename":"noble","arch":"amd64","image_dir":"` + imageDir + `","kernel_path":"` + filepath.Join(imageDir, "kernel") + `","initrd_path":"` + filepath.Join(imageDir, "initrd") + `","base_image":"` + filepath.Join(imageDir, "base.img") + `","runtime_disk":"` + filepath.Join(imageDir, "disk.raw") + `","ready":true,"disk_format":"raw","fetched_at_utc":"2026-02-08T00:00:00Z","updated_at_utc":"2026-02-08T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(imageDir, "image.json"), []byte(metadata), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}
