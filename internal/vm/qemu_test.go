package vm

import (
	"strings"
	"testing"
)

func TestNormalizePortForwards(t *testing.T) {
	forwards, err := normalizePortForwards(18789, 18789, []PortMapping{{HostPort: 8080, GuestPort: 80}, {HostPort: 18789, GuestPort: 18789}})
	if err != nil {
		t.Fatalf("normalizePortForwards failed: %v", err)
	}
	if len(forwards) != 2 {
		t.Fatalf("unexpected forward count: %d", len(forwards))
	}
	if forwards[0].HostPort != 18789 || forwards[0].GuestPort != 18789 {
		t.Fatalf("unexpected gateway mapping: %+v", forwards[0])
	}
	if forwards[1].HostPort != 8080 || forwards[1].GuestPort != 80 {
		t.Fatalf("unexpected publish mapping: %+v", forwards[1])
	}
}

func TestNormalizePortForwardsRejectsConflict(t *testing.T) {
	_, err := normalizePortForwards(18789, 18789, []PortMapping{{HostPort: 8080, GuestPort: 80}, {HostPort: 8080, GuestPort: 81}})
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	if !strings.Contains(err.Error(), "duplicate host port") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildCloudInitUserData(t *testing.T) {
	spec := StartSpec{GatewayGuestPort: 18789, OpenClawPackage: "openclaw@latest", CloudInitProvision: []string{"echo setup"}}
	userData := buildCloudInitUserData(spec)

	for _, expected := range []string{
		"#cloud-config",
		"name: claw",
		"NOPASSWD:ALL",
		"/usr/local/bin/clawfarm-bootstrap.sh",
		"npm install -g openclaw@latest",
		"openclaw gateway --allow-unconfigured --port 18789",
		"/usr/local/bin/clawfarm-provision.sh",
	} {
		if !strings.Contains(userData, expected) {
			t.Fatalf("cloud-init user-data missing %q", expected)
		}
	}
}

func TestBuildBootstrapScriptWithConfigAndEnv(t *testing.T) {
	spec := StartSpec{
		GatewayGuestPort:    18789,
		OpenClawPackage:     "openclaw@latest",
		OpenClawConfig:      `{"gateway":{"mode":"local","port":18789}}`,
		OpenClawEnvironment: map[string]string{"OPENAI_API_KEY": "abc123", "OPENCLAW_GATEWAY_TOKEN": "token-value"},
		ClawPath:            "/tmp/claw",
		CloudInitProvision:  []string{"echo setup"},
	}
	script := buildBootstrapScript(spec)

	for _, expected := range []string{
		"/etc/clawfarm/openclaw.env",
		"source /etc/clawfarm/openclaw.env",
		"OPENAI_API_KEY",
		"OPENCLAW_GATEWAY_TOKEN",
		"\"gateway\":{\"mode\":\"local\",\"port\":18789}",
		"mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 claw /claw",
		"/usr/local/bin/clawfarm-provision.sh",
		"echo setup",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("bootstrap script missing %q", expected)
		}
	}
}

func TestBuildQEMUArgsIncludesClawVirtfs(t *testing.T) {
	args, err := buildQEMUArgs(
		StartSpec{
			WorkspacePath:    "/tmp/workspace",
			StatePath:        "/tmp/state",
			ClawPath:         "/tmp/claw",
			GatewayHostPort:  18789,
			GatewayGuestPort: 18789,
			CPUs:             2,
			MemoryMiB:        2048,
		},
		qemuPlatform{Machine: "q35", CPU: "host", NetDevice: "virtio-net-pci", Accel: "hvf"},
		"/tmp/disk.qcow2",
		"qcow2",
		"/tmp/seed.iso",
		"/tmp/serial.log",
		"/tmp/qemu.log",
		"/tmp/qemu.pid",
		"/tmp/qemu.sock",
	)
	if err != nil {
		t.Fatalf("buildQEMUArgs failed: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "mount_tag=claw") {
		t.Fatalf("expected claw virtfs mount, got args: %s", joined)
	}
}

func TestBuildQEMUArgsIncludesVolumeVirtfs(t *testing.T) {
	args, err := buildQEMUArgs(
		StartSpec{
			WorkspacePath:    "/tmp/workspace",
			StatePath:        "/tmp/state",
			GatewayHostPort:  18789,
			GatewayGuestPort: 18789,
			VolumeMounts: []VolumeMount{
				{Name: ".openclaw", HostPath: "/tmp/instance/volumes/.openclaw", GuestPath: "/root/.openclaw"},
			},
			CPUs:      2,
			MemoryMiB: 2048,
		},
		qemuPlatform{Machine: "q35", CPU: "host", NetDevice: "virtio-net-pci", Accel: "hvf"},
		"/tmp/disk.qcow2",
		"qcow2",
		"/tmp/seed.iso",
		"/tmp/serial.log",
		"/tmp/qemu.log",
		"/tmp/qemu.pid",
		"/tmp/qemu.sock",
	)
	if err != nil {
		t.Fatalf("buildQEMUArgs failed: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "mount_tag=volume1") {
		t.Fatalf("expected volume virtfs mount tag, got args: %s", joined)
	}
	if !strings.Contains(joined, "path=/tmp/instance/volumes/.openclaw") {
		t.Fatalf("expected volume host path in args: %s", joined)
	}
}

func TestBuildBootstrapScriptIncludesVolumeMount(t *testing.T) {
	spec := StartSpec{
		GatewayGuestPort: 18789,
		VolumeMounts: []VolumeMount{
			{Name: ".openclaw", HostPath: "/tmp/instance/volumes/.openclaw", GuestPath: "/root/.openclaw"},
		},
	}
	script := buildBootstrapScript(spec)

	for _, expected := range []string{
		"install -d -m 0755 '/root/.openclaw'",
		"mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 volume1 '/root/.openclaw'",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("bootstrap script missing %q", expected)
		}
	}
}

func TestIndentForCloudConfig(t *testing.T) {
	content := "line1\nline2\n"
	indented := indentForCloudConfig(content, 4)
	if indented != "    line1\n    line2" {
		t.Fatalf("unexpected indent result: %q", indented)
	}
}
