package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazhou/krunclaw/internal/clawbox"
	"github.com/yazhou/krunclaw/internal/mount"
	"github.com/yazhou/krunclaw/internal/state"
	"github.com/yazhou/krunclaw/internal/vm"
)

const testClawboxSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeBackend struct {
	mu           sync.Mutex
	nextPID      int
	running      map[int]bool
	lastSpec     vm.StartSpec
	startEntered chan struct{}
	startGate    <-chan struct{}
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{nextPID: 4000, running: map[int]bool{}}
}

func (f *fakeBackend) Start(_ context.Context, spec vm.StartSpec) (vm.StartResult, error) {
	if f.startEntered != nil {
		select {
		case f.startEntered <- struct{}{}:
		default:
		}
	}
	if f.startGate != nil {
		<-f.startGate
	}

	f.mu.Lock()
	defer f.mu.Unlock()

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
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, pid)
	return nil
}

func (f *fakeBackend) Suspend(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running[pid] {
		return os.ErrNotExist
	}
	return nil
}

func (f *fakeBackend) Resume(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running[pid] {
		return os.ErrNotExist
	}
	return nil
}

func (f *fakeBackend) IsRunning(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--port=65531", "--publish", "8080:80", "--no-wait", "--openclaw-model-primary", "openai/gpt-5", "--openclaw-openai-api-key", "test-key"}); err != nil {
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

func TestRunAndRemoveUpdateMountStateFile(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--port=65531", "--no-wait", "--openclaw-model-primary", "openai/gpt-5", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}

	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse claw id from run output: %s", out.String())
	}

	statePath := filepath.Join(data, "claws", id, "state.json")
	state := readMountStateFile(t, statePath)
	if !state.Active {
		t.Fatalf("expected mount state active=true, got false")
	}
	if state.InstanceID != id {
		t.Fatalf("unexpected state instance_id: %q", state.InstanceID)
	}
	if state.PID <= 0 {
		t.Fatalf("expected state pid > 0, got %d", state.PID)
	}
	if state.SourcePath == "" {
		t.Fatalf("expected source path in state")
	}

	out.Reset()
	if err := application.Run([]string{"rm", id}); err != nil {
		t.Fatalf("rm failed: %v", err)
	}

	state = readMountStateFile(t, statePath)
	if state.Active {
		t.Fatalf("expected mount state active=false after rm")
	}
	if state.InstanceID != "" || state.PID != 0 {
		t.Fatalf("expected cleared runtime state after rm, got %+v", state)
	}
}

func TestRunWithClawboxFileUsesComputedClawID(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")
	expectedClawID, err := clawbox.ComputeClawID(clawboxPath, "demo-openclaw")
	if err != nil {
		t.Fatalf("compute expected CLAWID: %v", err)
	}

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--port=65531", "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}

	id := parseClawIDFromRunOutput(out.String())
	if id != expectedClawID {
		t.Fatalf("unexpected CLAWID from run: got %q want %q", id, expectedClawID)
	}

	statePath := filepath.Join(data, "claws", id, "state.json")
	state := readMountStateFile(t, statePath)
	if state.SourcePath != clawboxPath {
		t.Fatalf("unexpected mount source path: got %q want %q", state.SourcePath, clawboxPath)
	}
	if !state.Active {
		t.Fatalf("expected active mount state")
	}
	if !strings.Contains(backend.lastSpec.OpenClawConfig, `"primary": "openai/gpt-5"`) {
		t.Fatalf("expected model primary fallback from clawbox header, got config: %s", backend.lastSpec.OpenClawConfig)
	}
	if !strings.Contains(backend.lastSpec.OpenClawConfig, `"mode": "token"`) {
		t.Fatalf("expected gateway auth mode fallback from clawbox header, got config: %s", backend.lastSpec.OpenClawConfig)
	}
	if backend.lastSpec.OpenClawEnvironment["OPENCLAW_GATEWAY_TOKEN"] != "test-gateway-token" {
		t.Fatalf("expected gateway token env to be propagated")
	}

	out.Reset()
	if err := application.Run([]string{"rm", id}); err != nil {
		t.Fatalf("rm failed: %v", err)
	}
}

func TestRunDotResolvesUniqueClawboxFile(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workdir := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workdir, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")
	expectedClawID, err := clawbox.ComputeClawID(clawboxPath, "demo-openclaw")
	if err != nil {
		t.Fatalf("compute expected CLAWID: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer os.Chdir(originalWD)
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", ".", "--workspace=" + workdir, "--port=65532", "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id != expectedClawID {
		t.Fatalf("unexpected CLAWID from run .: got %q want %q", id, expectedClawID)
	}
	if !strings.Contains(backend.lastSpec.OpenClawConfig, `"primary": "openai/gpt-5"`) {
		t.Fatalf("expected model primary fallback from clawbox header, got config: %s", backend.lastSpec.OpenClawConfig)
	}
	if !strings.Contains(backend.lastSpec.OpenClawConfig, `"mode": "token"`) {
		t.Fatalf("expected gateway auth mode fallback from clawbox header, got config: %s", backend.lastSpec.OpenClawConfig)
	}
}

func TestRunClawboxExplicitOpenClawFlagsOverrideHeaderDefaults(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-model-primary", "anthropic/claude-3-5-sonnet-latest", "--openclaw-anthropic-api-key", "anthropic-key", "--openclaw-openai-api-key", "openai-required-by-spec", "--openclaw-gateway-auth-mode", "none"})
	if err != nil {
		t.Fatalf("run command failed: %v", err)
	}

	requirements, err := parseOpenClawRuntimeRequirements(backend.lastSpec.OpenClawConfig)
	if err != nil {
		t.Fatalf("parse generated OpenClaw config: %v", err)
	}
	if requirements.ModelPrimary != "anthropic/claude-3-5-sonnet-latest" {
		t.Fatalf("expected model primary override to win, got %q", requirements.ModelPrimary)
	}
	if requirements.GatewayAuthMode != "none" {
		t.Fatalf("expected gateway auth mode override to win, got %q", requirements.GatewayAuthMode)
	}
	if backend.lastSpec.OpenClawEnvironment["OPENCLAW_GATEWAY_TOKEN"] != "" {
		t.Fatalf("did not expect gateway token env when auth mode is none")
	}
}

func TestConcurrentRunSameClawboxReturnsBusy(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")

	startEntered := make(chan struct{}, 1)
	startGate := make(chan struct{})
	backend := newFakeBackend()
	backend.startEntered = startEntered
	backend.startGate = startGate

	var outOne bytes.Buffer
	var errOutOne bytes.Buffer
	var outTwo bytes.Buffer
	var errOutTwo bytes.Buffer
	appOne := NewWithBackend(&outOne, &errOutOne, backend)
	appTwo := NewWithBackend(&outTwo, &errOutTwo, backend)

	runArgs := []string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- appOne.Run(runArgs)
	}()

	select {
	case <-startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first run to reach backend start")
	}

	err := appTwo.Run(runArgs)
	if !errors.Is(err, mount.ErrBusy) {
		t.Fatalf("expected mount.ErrBusy for concurrent run, got %v", err)
	}

	close(startGate)
	select {
	case firstErr := <-errCh:
		if firstErr != nil {
			t.Fatalf("first run should succeed, got %v", firstErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first run completion")
	}
}

func TestRunClawboxRequiredEnvFailsFastWhenMissing(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")
	mutateTestClawboxFile(t, clawboxPath, func(header *clawbox.Header) {
		header.Spec.OpenClaw.GatewayAuthMode = "none"
		header.Spec.OpenClaw.RequiredEnv = []string{"CUSTOM_REQUIRED_TOKEN"}
	})

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key"})
	if err == nil {
		t.Fatal("expected missing required env error")
	}
	if !strings.Contains(err.Error(), "CUSTOM_REQUIRED_TOKEN") {
		t.Fatalf("unexpected error for missing required env: %v", err)
	}
	if backend.nextPID != 4000 {
		t.Fatalf("vm should not start before required env preflight")
	}
}

func TestRunClawboxRequiredEnvCanBeProvidedByOpenClawEnv(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")
	mutateTestClawboxFile(t, clawboxPath, func(header *clawbox.Header) {
		header.Spec.OpenClaw.GatewayAuthMode = "none"
		header.Spec.OpenClaw.RequiredEnv = []string{"CUSTOM_REQUIRED_TOKEN"}
	})

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-env", "CUSTOM_REQUIRED_TOKEN=custom-value"})
	if err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	if backend.lastSpec.OpenClawEnvironment["CUSTOM_REQUIRED_TOKEN"] != "custom-value" {
		t.Fatalf("missing required env propagated to backend")
	}
}

func TestRunDotFailsWhenMultipleClawboxFilesExist(t *testing.T) {
	workdir := t.TempDir()
	writeTestClawboxFile(t, workdir, "a.clawbox", "demo-openclaw-a", "ubuntu:24.04")
	writeTestClawboxFile(t, workdir, "b.clawbox", "demo-openclaw-b", "ubuntu:24.04")

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer os.Chdir(originalWD)
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err = application.Run([]string{"run", ".", "--workspace=" + workdir, "--no-wait", "--openclaw-model-primary", "openai/gpt-5", "--openclaw-openai-api-key", "test-key"})
	if err == nil {
		t.Fatalf("expected error for multiple .clawbox files")
	}
	if !strings.Contains(err.Error(), "multiple .clawbox files") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunJSONSpecClawboxDownloadsAndRunsWithoutMount(t *testing.T) {
	data := t.TempDir()
	home := t.TempDir()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME env: %v", err)
	}
	defer os.Unsetenv("HOME")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	basePayload := []byte("json-spec-base-image")
	baseSHA := sha256Hex(basePayload)
	layerPayload := []byte("json-spec-layer")
	layerSHA := sha256Hex(layerPayload)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/base.img":
			_, _ = writer.Write(basePayload)
		case "/layer.qcow2":
			_, _ = writer.Write(layerPayload)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	workspace := t.TempDir()
	specPath := filepath.Join(workspace, "demo-json.clawbox")
	specContent := `{
  "name": "demo-json",
  "spec": {
    "base_image": {
      "ref": "ubuntu:24.04",
      "url": "` + server.URL + `/base.img",
      "sha256": "` + baseSHA + `"
    },
    "layers": [
      {
        "ref": "layer-1",
        "url": "` + server.URL + `/layer.qcow2",
        "sha256": "` + layerSHA + `"
      }
    ],
    "openclaw": {
      "install_root": "/claw",
      "model_primary": "openai/gpt-5",
      "gateway_auth_mode": "none",
      "required_env": ["OPENAI_API_KEY"]
    }
  },
  "provision": [
    "echo provisioned > provisioned.txt"
  ]
}`
	if err := os.WriteFile(specPath, []byte(specContent), 0o644); err != nil {
		t.Fatalf("write json spec clawbox: %v", err)
	}

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", specPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}

	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	instanceDir := filepath.Join(data, "instances", id)
	provisionedPath := filepath.Join(instanceDir, "provisioned.txt")
	if _, err := os.Stat(provisionedPath); err != nil {
		t.Fatalf("expected provision output file %s: %v", provisionedPath, err)
	}

	mountStatePath := filepath.Join(data, "claws", id, "state.json")
	mountState := readMountStateFile(t, mountStatePath)
	if mountState.SourcePath != "" {
		t.Fatalf("json spec run should not set mount source, got %q", mountState.SourcePath)
	}

	baseBlobPath := filepath.Join(home, ".clawfarm", "blobs", baseSHA)
	if _, err := os.Stat(baseBlobPath); err != nil {
		t.Fatalf("expected base blob file %s: %v", baseBlobPath, err)
	}
	layerBlobPath := filepath.Join(home, ".clawfarm", "blobs", layerSHA)
	if _, err := os.Stat(layerBlobPath); err != nil {
		t.Fatalf("expected layer blob file %s: %v", layerBlobPath, err)
	}
}

func TestRunJSONSpecClawboxUsesCachedArtifactsWithoutRedownload(t *testing.T) {
	data := t.TempDir()
	home := t.TempDir()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME env: %v", err)
	}
	defer os.Unsetenv("HOME")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	basePayload := []byte("json-spec-cached-base")
	baseSHA := sha256Hex(basePayload)

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		_, _ = writer.Write(basePayload)
	}))
	defer server.Close()

	workspace := t.TempDir()
	specPath := filepath.Join(workspace, "cached-json.clawbox")
	specContent := `{
  "name": "cached-json",
  "spec": {
    "base_image": {
      "ref": "ubuntu:24.04",
      "url": "` + server.URL + `/base.img",
      "sha256": "` + baseSHA + `"
    },
    "openclaw": {
      "install_root": "/claw",
      "model_primary": "openai/gpt-5",
      "gateway_auth_mode": "none",
      "required_env": ["OPENAI_API_KEY"]
    }
  }
}`
	if err := os.WriteFile(specPath, []byte(specContent), 0o644); err != nil {
		t.Fatalf("write json spec clawbox: %v", err)
	}

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", specPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected first run to download once, got %d requests", requestCount)
	}

	firstID := parseClawIDFromRunOutput(out.String())
	if firstID == "" {
		t.Fatalf("failed to parse first CLAWID from run output: %s", out.String())
	}

	out.Reset()
	if err := application.Run([]string{"rm", firstID}); err != nil {
		t.Fatalf("rm first instance failed: %v", err)
	}

	out.Reset()
	if err := application.Run([]string{"run", specPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected second run to reuse cache without download, got %d requests", requestCount)
	}
	if !strings.Contains(out.String(), "using cached base") {
		t.Fatalf("expected cached marker in output, got: %s", out.String())
	}
}

func TestRunJSONSpecClawboxFailsOnSHA256Mismatch(t *testing.T) {
	data := t.TempDir()
	home := t.TempDir()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME env: %v", err)
	}
	defer os.Unsetenv("HOME")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("wrong-content"))
	}))
	defer server.Close()

	workspace := t.TempDir()
	specPath := filepath.Join(workspace, "sha-mismatch.clawbox")
	specContent := `{
  "name": "sha-mismatch",
  "spec": {
    "base_image": {
      "ref": "ubuntu:24.04",
      "url": "` + server.URL + `/base.img",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    },
    "openclaw": {
      "install_root": "/claw",
      "model_primary": "openai/gpt-5",
      "gateway_auth_mode": "none",
      "required_env": ["OPENAI_API_KEY"]
    }
  }
}`
	if err := os.WriteFile(specPath, []byte(specContent), 0o644); err != nil {
		t.Fatalf("write json spec clawbox: %v", err)
	}

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", specPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key"})
	if err == nil {
		t.Fatal("expected sha mismatch error")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.nextPID != 4000 {
		t.Fatalf("vm should not start when sha mismatches")
	}
}

func TestRunTarClawboxImportsRunImageAndClawDir(t *testing.T) {
	data := t.TempDir()
	home := t.TempDir()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME env: %v", err)
	}
	defer os.Unsetenv("HOME")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	workspace := t.TempDir()
	baseDisk := []byte("base-disk-content")
	runDisk := []byte("run-disk-content")
	baseSHA := sha256Hex(baseDisk)
	runSHA := sha256Hex(runDisk)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/base.qcow2":
			_, _ = writer.Write(baseDisk)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	clawboxPath := filepath.Join(workspace, "demo-v2.clawbox")
	writeTarClawboxV2(t, clawboxPath, tarClawboxV2Fixture{
		Name:    "demo-v2",
		BaseRef: "ubuntu:24.04",
		BaseURL: server.URL + "/base.qcow2",
		BaseSHA: baseSHA,
		RunRef:  "clawbox:///run.qcow2",
		RunSHA:  runSHA,
		RunDisk: runDisk,
		ClawFiles: map[string]string{
			"claw/SOUL.md": "hello",
		},
		RequiredEnv: []string{"OPENAI_API_KEY"},
		Provision:   []map[string]string{{"name": "setup", "shell": "bash", "script": "echo setup"}},
	})

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--name", "demo-a", "--openclaw-openai-api-key", "test-key"})
	if err != nil {
		t.Fatalf("run command failed: %v", err)
	}

	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("missing CLAWID output: %s", out.String())
	}
	if !strings.HasPrefix(id, "demo-a-") {
		t.Fatalf("expected id prefix demo-a-, got %s", id)
	}

	clawRoot := filepath.Join(data, "claws", id)
	runDiskPath := filepath.Join(clawRoot, "run.qcow2")
	runDiskOnDisk, err := os.ReadFile(runDiskPath)
	if err != nil {
		t.Fatalf("read imported run disk: %v", err)
	}
	if !bytes.Equal(runDiskOnDisk, runDisk) {
		t.Fatalf("unexpected run disk content")
	}

	if _, err := os.Stat(filepath.Join(clawRoot, "claw", "SOUL.md")); err != nil {
		t.Fatalf("expected extracted claw dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clawRoot, "clawspec.json")); err != nil {
		t.Fatalf("expected imported clawspec: %v", err)
	}

	if backend.lastSpec.SourceDiskPath != runDiskPath {
		t.Fatalf("unexpected source disk path: got %q want %q", backend.lastSpec.SourceDiskPath, runDiskPath)
	}
	if backend.lastSpec.ClawPath != filepath.Join(clawRoot, "claw") {
		t.Fatalf("unexpected claw path in start spec: %q", backend.lastSpec.ClawPath)
	}
	if len(backend.lastSpec.CloudInitProvision) != 1 || backend.lastSpec.CloudInitProvision[0] != "echo setup" {
		t.Fatalf("unexpected cloud-init provision scripts: %#v", backend.lastSpec.CloudInitProvision)
	}

	statePath := filepath.Join(data, "claws", id, "state.json")
	mountState := readMountStateFile(t, statePath)
	if mountState.SourcePath != "" {
		t.Fatalf("expected no mount source for v2 tar clawbox, got %q", mountState.SourcePath)
	}
	if !mountState.Active {
		t.Fatalf("expected mount state active=true")
	}
}

func TestRunTarClawboxAllowsMultipleInstancesFromSameFile(t *testing.T) {
	data := t.TempDir()
	home := t.TempDir()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME env: %v", err)
	}
	defer os.Unsetenv("HOME")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	workspace := t.TempDir()
	baseDisk := []byte("base-for-multi")
	runDisk := []byte("run-for-multi")
	baseSHA := sha256Hex(baseDisk)
	runSHA := sha256Hex(runDisk)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(baseDisk)
	}))
	defer server.Close()

	clawboxPath := filepath.Join(workspace, "multi-v2.clawbox")
	writeTarClawboxV2(t, clawboxPath, tarClawboxV2Fixture{
		Name:        "multi-v2",
		BaseRef:     "ubuntu:24.04",
		BaseURL:     server.URL + "/base.qcow2",
		BaseSHA:     baseSHA,
		RunRef:      "clawbox:///run.qcow2",
		RunSHA:      runSHA,
		RunDisk:     runDisk,
		RequiredEnv: []string{"OPENAI_API_KEY"},
	})

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--name", "multi-a", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	idA := parseClawIDFromRunOutput(out.String())
	if idA == "" {
		t.Fatalf("missing first CLAWID")
	}

	out.Reset()
	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--name", "multi-b", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	idB := parseClawIDFromRunOutput(out.String())
	if idB == "" {
		t.Fatalf("missing second CLAWID")
	}

	if idA == idB {
		t.Fatalf("expected different CLAWID for two runs from same .clawbox")
	}
	if !strings.HasPrefix(idA, "multi-a-") || !strings.HasPrefix(idB, "multi-b-") {
		t.Fatalf("expected name-prefixed ids, got %q and %q", idA, idB)
	}
}

func TestRunTarClawboxFailsWhenMissingSpec(t *testing.T) {
	data := t.TempDir()
	home := t.TempDir()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME env: %v", err)
	}
	defer os.Unsetenv("HOME")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	workspace := t.TempDir()
	clawboxPath := filepath.Join(workspace, "broken-v2.clawbox")
	writeTarClawboxWithoutSpec(t, clawboxPath)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key"})
	if err == nil {
		t.Fatal("expected run to fail when clawspec.json is missing")
	}
	if !strings.Contains(err.Error(), "missing clawspec.json") {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.nextPID != 4000 {
		t.Fatalf("vm should not start on invalid tar clawbox")
	}
}

func TestExportCopiesClawboxSource(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	exportPath := filepath.Join(t.TempDir(), "exported.clawbox")
	out.Reset()
	if err := application.Run([]string{"export", id, exportPath}); err != nil {
		t.Fatalf("export command failed: %v", err)
	}
	if !strings.Contains(out.String(), "exported "+id) {
		t.Fatalf("export output missing success marker: %s", out.String())
	}

	sourceContent, err := os.ReadFile(clawboxPath)
	if err != nil {
		t.Fatalf("read source clawbox: %v", err)
	}
	exportedContent, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("read exported clawbox: %v", err)
	}
	if !bytes.Equal(sourceContent, exportedContent) {
		t.Fatal("exported clawbox content does not match source clawbox")
	}
}

func TestExportWithNameOverridesHeaderName(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	exportPath := filepath.Join(t.TempDir(), "renamed.clawbox")
	if err := application.Run([]string{"export", id, exportPath, "--name", "renamed-openclaw"}); err != nil {
		t.Fatalf("export with --name failed: %v", err)
	}

	header, err := clawbox.LoadHeaderJSON(exportPath)
	if err != nil {
		t.Fatalf("load exported clawbox header: %v", err)
	}
	if header.Name != "renamed-openclaw" {
		t.Fatalf("expected renamed header name, got %q", header.Name)
	}
	if header.Spec.BaseImage.Ref != "ubuntu:24.04" {
		t.Fatalf("unexpected base image ref in exported clawbox: %s", header.Spec.BaseImage.Ref)
	}
}

func TestExportWithInvalidNameFails(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	err := application.Run([]string{"export", id, filepath.Join(t.TempDir(), "invalid-name.clawbox"), "--name", "INVALID_NAME"})
	if err == nil {
		t.Fatal("expected export to fail for invalid --name")
	}
	if !strings.Contains(err.Error(), "invalid --name") {
		t.Fatalf("unexpected error for invalid --name: %v", err)
	}
}

func TestExportBlocksPossibleSecretsByDefault(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	if err := os.WriteFile(clawboxPath, []byte("{\"OPENAI_API_KEY\":\"sk-secret-value-1234567890123456\"}\n"), 0o644); err != nil {
		t.Fatalf("inject possible secret into source clawbox: %v", err)
	}

	exportPath := filepath.Join(t.TempDir(), "blocked.clawbox")
	err := application.Run([]string{"export", id, exportPath})
	if err == nil {
		t.Fatal("expected export to be blocked by secret scan")
	}
	if !strings.Contains(err.Error(), "export blocked: detected possible secrets") {
		t.Fatalf("unexpected export error: %v", err)
	}
	if _, statErr := os.Stat(exportPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no exported artifact after blocked export")
	}
}

func TestExportAllowsPossibleSecretsWithFlag(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	if err := os.WriteFile(clawboxPath, []byte("{\"OPENAI_API_KEY\":\"sk-secret-value-1234567890123456\"}\n"), 0o644); err != nil {
		t.Fatalf("inject possible secret into source clawbox: %v", err)
	}

	out.Reset()
	errOut.Reset()
	exportPath := filepath.Join(t.TempDir(), "allowed.clawbox")
	err := application.Run([]string{"export", id, exportPath, "--allow-secrets"})
	if err != nil {
		t.Fatalf("expected export success with --allow-secrets, got %v", err)
	}
	if !strings.Contains(errOut.String(), "warning: exporting with possible secrets") {
		t.Fatalf("expected warning output when using --allow-secrets, got: %s", errOut.String())
	}
	if _, statErr := os.Stat(exportPath); statErr != nil {
		t.Fatalf("expected exported artifact with --allow-secrets: %v", statErr)
	}
}

func TestExportFailsForNonClawboxInstance(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--no-wait", "--openclaw-model-primary", "openai/gpt-5", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	err := application.Run([]string{"export", id, filepath.Join(t.TempDir(), "not-clawbox.clawbox")})
	if err == nil {
		t.Fatal("expected export to fail for non-clawbox-backed instance")
	}
	if !strings.Contains(err.Error(), "not clawbox-backed") {
		t.Fatalf("unexpected export error: %v", err)
	}
}

func TestExportFailsWhenInstanceLockBusy(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)
	workspace := t.TempDir()
	clawboxPath := writeTestClawboxFile(t, workspace, "demo-openclaw.clawbox", "demo-openclaw", "ubuntu:24.04")

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", clawboxPath, "--workspace=" + workspace, "--no-wait", "--openclaw-openai-api-key", "test-key", "--openclaw-gateway-token", "test-gateway-token"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	mountManager, err := application.mountManager()
	if err != nil {
		t.Fatalf("mount manager: %v", err)
	}

	lockReady := make(chan struct{})
	lockDone := make(chan error, 1)
	releaseLock := make(chan struct{})
	go func() {
		lockDone <- mountManager.WithInstanceLock(id, func() error {
			close(lockReady)
			<-releaseLock
			return nil
		})
	}()

	select {
	case <-lockReady:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for lock holder")
	}

	err = application.Run([]string{"export", id, filepath.Join(t.TempDir(), "busy.clawbox")})
	if !errors.Is(err, mount.ErrBusy) {
		t.Fatalf("expected mount.ErrBusy, got %v", err)
	}

	close(releaseLock)
	select {
	case lockErr := <-lockDone:
		if lockErr != nil {
			t.Fatalf("lock holder failed: %v", lockErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting lock holder to exit")
	}
}

func TestCheckpointAndRestoreCopiesDisk(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--no-wait", "--openclaw-model-primary", "openai/gpt-5", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	store := state.NewStore(filepath.Join(data, "instances"))
	instance, err := store.Load(id)
	if err != nil {
		t.Fatalf("load instance: %v", err)
	}
	if strings.TrimSpace(instance.DiskPath) == "" {
		t.Fatalf("instance disk path should not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(instance.DiskPath), 0o755); err != nil {
		t.Fatalf("mkdir instance disk dir: %v", err)
	}
	if err := os.WriteFile(instance.DiskPath, []byte("disk-v1"), 0o644); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	out.Reset()
	if err := application.Run([]string{"checkpoint", id, "--name", "snap-one"}); err != nil {
		t.Fatalf("checkpoint command failed: %v", err)
	}
	checkpointPath := checkpointPathForName(filepath.Join(data, "instances"), id, "snap-one")
	checkpointContent, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatalf("read checkpoint file: %v", err)
	}
	if string(checkpointContent) != "disk-v1" {
		t.Fatalf("unexpected checkpoint content: %q", string(checkpointContent))
	}

	if err := os.WriteFile(instance.DiskPath, []byte("disk-v2"), 0o644); err != nil {
		t.Fatalf("overwrite disk: %v", err)
	}

	out.Reset()
	if err := application.Run([]string{"restore", id, "snap-one"}); err != nil {
		t.Fatalf("restore command failed: %v", err)
	}
	restoredContent, err := os.ReadFile(instance.DiskPath)
	if err != nil {
		t.Fatalf("read restored disk: %v", err)
	}
	if string(restoredContent) != "disk-v1" {
		t.Fatalf("unexpected restored content: %q", string(restoredContent))
	}
}

func TestCheckpointRequiresName(t *testing.T) {
	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"checkpoint", "claw-1234"})
	if err == nil {
		t.Fatal("expected checkpoint usage error")
	}
	if !strings.Contains(err.Error(), "checkpoint name is required") {
		t.Fatalf("unexpected checkpoint error: %v", err)
	}
}

func TestRestoreFailsWhenCheckpointMissing(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	if err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--no-wait", "--openclaw-model-primary", "openai/gpt-5", "--openclaw-openai-api-key", "test-key"}); err != nil {
		t.Fatalf("run command failed: %v", err)
	}
	id := parseClawIDFromRunOutput(out.String())
	if id == "" {
		t.Fatalf("failed to parse CLAWID from run output: %s", out.String())
	}

	err := application.Run([]string{"restore", id, "missing-snapshot"})
	if err == nil {
		t.Fatal("expected restore failure for missing checkpoint")
	}
	if !strings.Contains(err.Error(), "checkpoint missing-snapshot not found") {
		t.Fatalf("unexpected restore error: %v", err)
	}
}

func TestRunRequiresImage(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

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
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--port=65530", "--ready-timeout-secs=1", "--openclaw-model-primary", "openai/gpt-5", "--openclaw-openai-api-key", "test-key"})
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
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

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
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

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
		"--openclaw-openai-api-key", "openai-from-flag",
		"--openclaw-anthropic-api-key", "anthropic-from-flag",
		"--openclaw-google-generative-ai-api-key", "google-from-flag",
		"--openclaw-xai-api-key", "xai-from-flag",
		"--openclaw-openrouter-api-key", "openrouter-from-flag",
		"--openclaw-zai-api-key", "zai-from-flag",
		"--openclaw-discord-token", "discord-from-flag",
		"--openclaw-telegram-token", "telegram-from-flag",
		"--openclaw-whatsapp-phone-number-id", "whatsapp-phone-id",
		"--openclaw-whatsapp-access-token", "whatsapp-access-token",
		"--openclaw-whatsapp-verify-token", "whatsapp-verify-token",
		"--openclaw-whatsapp-app-secret", "whatsapp-app-secret",
		"--openclaw-env", "OPENAI_API_KEY=from-openclaw-env",
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

	if backend.lastSpec.OpenClawEnvironment["OPENAI_API_KEY"] != "openai-from-flag" {
		t.Fatalf("expected explicit OpenAI arg to override env file and --openclaw-env")
	}
	if backend.lastSpec.OpenClawEnvironment["OPENCLAW_GATEWAY_TOKEN"] != "token-from-flag" {
		t.Fatalf("missing gateway token env")
	}
	if backend.lastSpec.OpenClawEnvironment["ANTHROPIC_API_KEY"] != "anthropic-from-flag" {
		t.Fatalf("missing Anthropic key env")
	}
	if backend.lastSpec.OpenClawEnvironment["GOOGLE_GENERATIVE_AI_API_KEY"] != "google-from-flag" {
		t.Fatalf("missing Google key env")
	}
	if backend.lastSpec.OpenClawEnvironment["XAI_API_KEY"] != "xai-from-flag" {
		t.Fatalf("missing xAI key env")
	}
	if backend.lastSpec.OpenClawEnvironment["OPENROUTER_API_KEY"] != "openrouter-from-flag" {
		t.Fatalf("missing OpenRouter key env")
	}
	if backend.lastSpec.OpenClawEnvironment["ZAI_API_KEY"] != "zai-from-flag" {
		t.Fatalf("missing Z.AI key env")
	}
	if backend.lastSpec.OpenClawEnvironment["DISCORD_TOKEN"] != "discord-from-flag" {
		t.Fatalf("missing Discord token env")
	}
	if backend.lastSpec.OpenClawEnvironment["TELEGRAM_TOKEN"] != "telegram-from-flag" {
		t.Fatalf("expected explicit Telegram arg to override env file")
	}
	if backend.lastSpec.OpenClawEnvironment["WHATSAPP_PHONE_NUMBER_ID"] != "whatsapp-phone-id" {
		t.Fatalf("missing WhatsApp phone number id env")
	}
	if backend.lastSpec.OpenClawEnvironment["WHATSAPP_ACCESS_TOKEN"] != "whatsapp-access-token" {
		t.Fatalf("missing WhatsApp access token env")
	}
	if backend.lastSpec.OpenClawEnvironment["WHATSAPP_VERIFY_TOKEN"] != "whatsapp-verify-token" {
		t.Fatalf("missing WhatsApp verify token env")
	}
	if backend.lastSpec.OpenClawEnvironment["WHATSAPP_APP_SECRET"] != "whatsapp-app-secret" {
		t.Fatalf("missing WhatsApp app secret env")
	}
}

func TestRunFailsFastForMissingRequiredOpenClawParameters(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--no-wait"})
	if err == nil {
		t.Fatalf("expected missing parameter error")
	}
	if !strings.Contains(err.Error(), "--openclaw-model-primary") {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.nextPID != 4000 {
		t.Fatalf("vm should not start before preflight validation")
	}
}

func TestRunPromptsForMissingRequiredOpenClawParameters(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	input := strings.NewReader("openai/gpt-5\nprompt-openai-key\n")
	application := NewWithIOAndBackend(&out, &errOut, input, backend)

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--no-wait"})
	if err != nil {
		t.Fatalf("run should succeed with prompted values: %v", err)
	}
	promptOutput := out.String()
	if !strings.Contains(promptOutput, "openclaw> OpenClaw primary model") {
		t.Fatalf("missing model prompt output: %s", promptOutput)
	}
	if !strings.Contains(promptOutput, "*****************") {
		t.Fatalf("missing masked secret output: %s", promptOutput)
	}
	if strings.Contains(promptOutput, "prompt-openai-key") {
		t.Fatalf("secret should not be printed in clear text: %s", promptOutput)
	}
	if backend.lastSpec.OpenClawEnvironment["OPENAI_API_KEY"] != "prompt-openai-key" {
		t.Fatalf("missing prompted OPENAI_API_KEY")
	}
	if !strings.Contains(backend.lastSpec.OpenClawConfig, `"primary": "openai/gpt-5"`) {
		t.Fatalf("missing prompted primary model in config: %s", backend.lastSpec.OpenClawConfig)
	}
}

func TestRunRejectsUnsupportedModelProvider(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--no-wait", "--openclaw-model-primary", "foo/bar"})
	if err == nil {
		t.Fatalf("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported model provider") {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.nextPID != 4000 {
		t.Fatalf("vm should not start for invalid model provider")
	}
}

func TestPSShowsUnhealthyStatusWhenGatewayUnavailable(t *testing.T) {
	cache := t.TempDir()
	data := t.TempDir()
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

	seedFetchedImage(t, cache)

	backend := newFakeBackend()
	var out bytes.Buffer
	var errOut bytes.Buffer
	application := NewWithBackend(&out, &errOut, backend)

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--port=65531", "--ready-timeout-secs=1", "--openclaw-model-primary", "openai/gpt-5", "--openclaw-openai-api-key", "test-key"})
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
	if err := os.Setenv("CLAWFARM_CACHE_DIR", cache); err != nil {
		t.Fatalf("set cache env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_CACHE_DIR")
	if err := os.Setenv("CLAWFARM_DATA_DIR", data); err != nil {
		t.Fatalf("set data env: %v", err)
	}
	defer os.Unsetenv("CLAWFARM_DATA_DIR")

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

type mountStateFile struct {
	Active     bool   `json:"active"`
	InstanceID string `json:"instance_id"`
	PID        int    `json:"pid"`
	SourcePath string `json:"source_path"`
}

func parseClawIDFromRunOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CLAWID:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "CLAWID:"))
		}
	}
	return ""
}

func readMountStateFile(t *testing.T, path string) mountStateFile {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mount state %s: %v", path, err)
	}

	var state mountStateFile
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode mount state %s: %v", path, err)
	}
	return state
}

func mutateTestClawboxFile(t *testing.T, path string, mutate func(*clawbox.Header)) {
	t.Helper()

	header, err := clawbox.LoadHeaderJSON(path)
	if err != nil {
		t.Fatalf("load clawbox file %s: %v", path, err)
	}
	mutate(&header)
	if err := clawbox.SaveHeaderJSON(path, header); err != nil {
		t.Fatalf("write clawbox file %s: %v", path, err)
	}
}

func writeTestClawboxFile(t *testing.T, dir string, fileName string, name string, baseImageRef string) string {
	t.Helper()

	path := filepath.Join(dir, fileName)
	header := clawbox.Header{
		SchemaVersion: clawbox.SchemaVersionV1,
		Name:          name,
		CreatedAtUTC:  time.Date(2026, time.February, 10, 0, 0, 0, 0, time.UTC),
		Payload: clawbox.Payload{
			FSType: "squashfs",
			Offset: 4096,
			Size:   123456,
			SHA256: testClawboxSHA256,
		},
		Spec: clawbox.RuntimeSpec{
			BaseImage: clawbox.BaseImage{
				Ref:    baseImageRef,
				URL:    "https://example.com/base.img",
				SHA256: testClawboxSHA256,
			},
			Layers: []clawbox.Layer{
				{
					Ref:    "xfce",
					URL:    "https://example.com/xfce.qcow2",
					SHA256: testClawboxSHA256,
				},
			},
			OpenClaw: clawbox.OpenClawSpec{
				InstallRoot:     "/claw",
				ModelPrimary:    "openai/gpt-5",
				GatewayAuthMode: "token",
				RequiredEnv:     []string{"OPENAI_API_KEY"},
				OptionalEnv:     []string{"DISCORD_TOKEN"},
			},
		},
	}
	if err := clawbox.SaveHeaderJSON(path, header); err != nil {
		t.Fatalf("write clawbox file: %v", err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs clawbox path: %v", err)
	}
	return absolutePath
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

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

type tarClawboxV2Fixture struct {
	Name        string
	BaseRef     string
	BaseURL     string
	BaseSHA     string
	RunRef      string
	RunSHA      string
	RunDisk     []byte
	RequiredEnv []string
	ClawFiles   map[string]string
	Provision   []map[string]string
}

func writeTarClawboxV2(t *testing.T, path string, fixture tarClawboxV2Fixture) {
	t.Helper()

	if fixture.Name == "" {
		fixture.Name = "demo"
	}
	if fixture.BaseRef == "" {
		fixture.BaseRef = "ubuntu:24.04"
	}
	if fixture.BaseSHA == "" {
		t.Fatal("BaseSHA is required")
	}
	if len(fixture.RequiredEnv) == 0 {
		fixture.RequiredEnv = []string{"OPENAI_API_KEY"}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir clawbox dir: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create clawbox file: %v", err)
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	images := []map[string]string{
		{
			"name":   "base",
			"ref":    fixture.BaseURL,
			"sha256": fixture.BaseSHA,
		},
	}
	if fixture.BaseURL == "" {
		images[0]["ref"] = fixture.BaseRef
	}
	if fixture.RunRef != "" {
		images = append(images, map[string]string{
			"name":   "run",
			"ref":    fixture.RunRef,
			"sha256": fixture.RunSHA,
		})
	}

	spec := map[string]interface{}{
		"schema_version": 2,
		"name":           fixture.Name,
		"image":          images,
		"openclaw": map[string]interface{}{
			"model_primary":     "openai/gpt-5",
			"gateway_auth_mode": "none",
			"required_env":      fixture.RequiredEnv,
		},
	}
	if len(fixture.Provision) > 0 {
		spec["provision"] = fixture.Provision
	}

	payload, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal clawspec json: %v", err)
	}
	writeTarRegularFile(t, tarWriter, "clawspec.json", payload, 0o644)

	if fixture.RunRef != "" {
		if len(fixture.RunDisk) == 0 {
			t.Fatal("RunDisk is required when RunRef is set")
		}
		writeTarRegularFile(t, tarWriter, "run.qcow2", fixture.RunDisk, 0o644)
	}

	for name, content := range fixture.ClawFiles {
		writeTarRegularFile(t, tarWriter, name, []byte(content), 0o644)
	}

	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close clawbox file: %v", err)
	}
}

func writeTarClawboxWithoutSpec(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir clawbox dir: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create clawbox file: %v", err)
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()
	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	writeTarRegularFile(t, tarWriter, "README.txt", []byte("broken"), 0o644)

	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close clawbox file: %v", err)
	}
}

func writeTarRegularFile(t *testing.T, writer *tar.Writer, name string, content []byte, mode int64) {
	t.Helper()
	if err := writer.WriteHeader(&tar.Header{
		Name: name,
		Mode: mode,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatalf("write tar header for %s: %v", name, err)
	}
	if _, err := io.Copy(writer, bytes.NewReader(content)); err != nil {
		t.Fatalf("write tar body for %s: %v", name, err)
	}
}
