package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	imageDir := filepath.Join(cache, "images", "ubuntu_24.04")
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

	var out bytes.Buffer
	var errOut bytes.Buffer
	application := New(&out, &errOut)

	if err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=.", "--publish", "8080:80"}); err != nil {
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

	var out bytes.Buffer
	var errOut bytes.Buffer
	application := New(&out, &errOut)

	err := application.Run([]string{"run", "ubuntu:24.04", "--workspace=."})
	if err == nil {
		t.Fatalf("expected error for missing image")
	}
	if !strings.Contains(err.Error(), "image ubuntu:24.04 is not ready") {
		t.Fatalf("unexpected error: %v", err)
	}
}
