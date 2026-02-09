package app

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yazhou/krunclaw/internal/vm"
)

type fakeBackend struct {
	nextPID  int
	running  map[int]bool
	lastSpec vm.StartSpec
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{nextPID: 4000, running: map[int]bool{}}
}

func (f *fakeBackend) Start(_ context.Context, spec vm.StartSpec) (vm.StartResult, error) {
	f.nextPID++
	pid := f.nextPID
	f.running[pid] = true
	f.lastSpec = spec
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

	if err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--port=65531", "--publish", "8080:80", "--no-wait"}); err != nil {
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

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--port=65530", "--ready-timeout-secs=1"})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !strings.Contains(err.Error(), "gateway is not reachable yet") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImageLSShowsDownloadedMarker(t *testing.T) {
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

	if err := application.Run([]string{"image", "ls"}); err != nil {
		t.Fatalf("image ls failed: %v", err)
	}
	if !strings.Contains(out.String(), "ubuntu:24.04") {
		t.Fatalf("image ls missing available image: %s", out.String())
	}
	if !strings.Contains(out.String(), "	no") && !strings.Contains(out.String(), "  no") {
		t.Fatalf("image ls missing non-downloaded marker: %s", out.String())
	}

	seedFetchedImage(t, cache)
	out.Reset()
	if err := application.Run([]string{"image", "ls"}); err != nil {
		t.Fatalf("image ls failed: %v", err)
	}
	if !strings.Contains(out.String(), "	yes") && !strings.Contains(out.String(), "  yes") {
		t.Fatalf("image ls missing downloaded marker: %s", out.String())
	}
}

func TestRunPassesExpandedOpenClawParameters(t *testing.T) {
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

	configPath := filepath.Join(t.TempDir(), "openclaw.json")
	config := `{"agents":{"defaults":{"workspace":"/existing"}},"gateway":{"mode":"local"}}`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	envFile := filepath.Join(t.TempDir(), "openclaw.env")
	envBody := "OPENAI_API_KEY=file-key\n# comment\nexport TELEGRAM_TOKEN=file-telegram\n"
	if err := os.WriteFile(envFile, []byte(envBody), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{
		"run", "ubuntu:24.04",
		"--workspace=.",
		"--no-wait",
		"--openclaw-config", configPath,
		"--openclaw-env-file", envFile,
		"--openclaw-agent-workspace", "/workspace",
		"--openclaw-model-primary", "openai/gpt-5",
		"--openclaw-gateway-auth-mode", "token",
		"--openclaw-gateway-token", "token-from-flag",
		"--openclaw-env", "OPENAI_API_KEY=flag-key",
	})
	if err != nil {
		t.Fatalf("run command failed: %v", err)
	}

	if !strings.Contains(backend.lastSpec.OpenClawConfig, `"primary": "openai/gpt-5"`) {
		t.Fatalf("missing primary model in config: %s", backend.lastSpec.OpenClawConfig)
	}
	if !strings.Contains(backend.lastSpec.OpenClawConfig, `"workspace": "/workspace"`) {
		t.Fatalf("missing workspace override in config: %s", backend.lastSpec.OpenClawConfig)
	}
	if !strings.Contains(backend.lastSpec.OpenClawConfig, `"mode": "token"`) {
		t.Fatalf("missing auth mode in config: %s", backend.lastSpec.OpenClawConfig)
	}

	if backend.lastSpec.OpenClawEnvironment["OPENAI_API_KEY"] != "flag-key" {
		t.Fatalf("expected flag to override env file key")
	}
	if backend.lastSpec.OpenClawEnvironment["OPENCLAW_GATEWAY_TOKEN"] != "token-from-flag" {
		t.Fatalf("missing gateway token env")
	}
	if backend.lastSpec.OpenClawEnvironment["TELEGRAM_TOKEN"] != "file-telegram" {
		t.Fatalf("missing env file token")
	}
}

func TestPSShowsUnhealthyStatusWhenGatewayUnavailable(t *testing.T) {
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

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--port=65531", "--ready-timeout-secs=1"})
	if err == nil {
		t.Fatalf("expected run timeout error")
	}
	if !strings.Contains(err.Error(), "gateway is not reachable yet") {
		t.Fatalf("unexpected run error: %v", err)
	}

	out.Reset()
	err = application.Run([]string{"ps"})
	if err != nil {
		t.Fatalf("ps failed: %v", err)
	}

	psOutput := out.String()
	if !strings.Contains(psOutput, "LAST_ERROR") {
		t.Fatalf("ps output missing LAST_ERROR column: %s", psOutput)
	}
	if !strings.Contains(psOutput, "unhealthy") {
		t.Fatalf("ps output missing unhealthy status: %s", psOutput)
	}
	if !strings.Contains(psOutput, "timeout waiting") && !strings.Contains(psOutput, "connection refused") && !strings.Contains(psOutput, "gateway is unreachable") {
		t.Fatalf("ps output missing error detail: %s", psOutput)
	}
}

func TestPSMarksHTTP5xxAsUnhealthy(t *testing.T) {
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

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	})}
	defer server.Close()
	go func() {
		_ = server.Serve(listener)
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	backend := newFakeBackend()
	backend.nextPID = 4999
	backend.running[5000] = true

	instanceStore := filepath.Join(data, "instances")
	if err := os.MkdirAll(filepath.Join(instanceStore, "claw-test5xx"), 0o755); err != nil {
		t.Fatalf("mkdir instance: %v", err)
	}

	metadata := `{"id":"claw-test5xx","image_ref":"ubuntu:24.04","workspace_path":".","state_path":".","gateway_port":` + strconv.Itoa(port) + `,"published_ports":[],"status":"ready","backend":"qemu","pid":5000,"created_at_utc":"` + time.Now().UTC().Format(time.RFC3339) + `","updated_at_utc":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(instanceStore, "claw-test5xx", "instance.json"), []byte(metadata), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err = application.Run([]string{"ps"})
	if err != nil {
		t.Fatalf("ps failed: %v", err)
	}

	psOutput := out.String()
	if !strings.Contains(psOutput, "unhealthy") {
		t.Fatalf("ps output missing unhealthy status: %s", psOutput)
	}
	if !strings.Contains(psOutput, "HTTP 500") {
		t.Fatalf("ps output missing HTTP 500 error: %s", psOutput)
	}
}

func seedFetchedImage(t *testing.T, cacheRoot string) {
	t.Helper()

	imageDir := filepath.Join(cacheRoot, "images", "ubuntu_24.04")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "image.img"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write image artifact: %v", err)
	}
	metadata := `{"ref":"ubuntu:24.04","version":"24.04","codename":"noble","arch":"amd64","image_dir":"` + imageDir + `","runtime_disk":"` + filepath.Join(imageDir, "image.img") + `","ready":true,"disk_format":"raw","fetched_at_utc":"2026-02-08T00:00:00Z","updated_at_utc":"2026-02-08T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(imageDir, "image.json"), []byte(metadata), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}
