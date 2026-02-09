package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultCPUs      = 2
	defaultMemoryMiB = 4096
)

type QEMUBackend struct {
	out io.Writer
}

type qemuPlatform struct {
	Binary    string
	Machine   string
	CPU       string
	Console   string
	NetDevice string
	Accel     string
}

func NewQEMUBackend(out io.Writer) *QEMUBackend {
	return &QEMUBackend{out: out}
}

func (b *QEMUBackend) Start(ctx context.Context, spec StartSpec) (StartResult, error) {
	if spec.CPUs <= 0 {
		spec.CPUs = defaultCPUs
	}
	if spec.MemoryMiB <= 0 {
		spec.MemoryMiB = defaultMemoryMiB
	}
	if spec.GatewayGuestPort <= 0 {
		spec.GatewayGuestPort = spec.GatewayHostPort
	}
	if spec.OpenClawPackage == "" {
		spec.OpenClawPackage = "openclaw@latest"
	}
	if strings.ContainsAny(spec.OpenClawPackage, "\n\r") {
		return StartResult{}, errors.New("invalid OpenClaw package: contains newline")
	}
	if err := validatePort(spec.GatewayHostPort); err != nil {
		return StartResult{}, fmt.Errorf("gateway host port: %w", err)
	}
	if err := validatePort(spec.GatewayGuestPort); err != nil {
		return StartResult{}, fmt.Errorf("gateway guest port: %w", err)
	}

	if err := os.MkdirAll(spec.InstanceDir, 0o755); err != nil {
		return StartResult{}, err
	}

	diskPath, diskFormat, err := prepareInstanceDisk(spec.SourceDiskPath, spec.InstanceDir, b.out)
	if err != nil {
		return StartResult{}, err
	}

	seedISO := filepath.Join(spec.InstanceDir, "seed.iso")
	if err := createNoCloudSeedISO(spec, seedISO); err != nil {
		return StartResult{}, err
	}

	platform, err := resolveQEMUPlatform(spec.ImageArch)
	if err != nil {
		return StartResult{}, err
	}

	serialLogPath := filepath.Join(spec.InstanceDir, "serial.log")
	qemuLogPath := filepath.Join(spec.InstanceDir, "qemu.log")
	pidFilePath := filepath.Join(spec.InstanceDir, "qemu.pid")
	monitorPath := filepath.Join(spec.InstanceDir, "qemu-monitor.sock")

	args, err := buildQEMUArgs(spec, platform, diskPath, diskFormat, seedISO, serialLogPath, qemuLogPath, pidFilePath, monitorPath)
	if err != nil {
		return StartResult{}, err
	}

	if err := os.Remove(pidFilePath); err != nil && !os.IsNotExist(err) {
		return StartResult{}, err
	}

	command := exec.CommandContext(ctx, platform.Binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return StartResult{}, fmt.Errorf("start qemu failed: %s", message)
	}

	pid, err := waitForPIDFile(pidFilePath, 10*time.Second)
	if err != nil {
		return StartResult{}, err
	}

	writeLine(b.out, "qemu started: pid=%d accel=%s", pid, platform.Accel)

	return StartResult{
		PID:           pid,
		DiskPath:      diskPath,
		DiskFormat:    diskFormat,
		SeedISOPath:   seedISO,
		SerialLogPath: serialLogPath,
		QEMULogPath:   qemuLogPath,
		PIDFilePath:   pidFilePath,
		MonitorPath:   monitorPath,
		Accel:         platform.Accel,
		Command:       append([]string{platform.Binary}, args...),
	}, nil
}

func (b *QEMUBackend) Stop(ctx context.Context, pid int) error {
	if pid <= 0 || !processExists(pid) {
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}

	return fmt.Errorf("process %d did not exit after kill", pid)
}

func (b *QEMUBackend) Suspend(pid int) error {
	if pid <= 0 {
		return errors.New("invalid process id")
	}
	if !processExists(pid) {
		return fmt.Errorf("process %d is not running", pid)
	}
	return syscall.Kill(pid, syscall.SIGSTOP)
}

func (b *QEMUBackend) Resume(pid int) error {
	if pid <= 0 {
		return errors.New("invalid process id")
	}
	if !processExists(pid) {
		return fmt.Errorf("process %d is not running", pid)
	}
	return syscall.Kill(pid, syscall.SIGCONT)
}

func (b *QEMUBackend) IsRunning(pid int) bool {
	return processExists(pid)
}

func resolveQEMUPlatform(imageArch string) (qemuPlatform, error) {
	platform := qemuPlatform{}
	hostArch := detectHostArch()
	if hostArch == imageArch {
		platform.Accel = "hvf"
		platform.CPU = "host"
	} else {
		platform.Accel = "tcg"
		platform.CPU = "max"
	}

	switch imageArch {
	case "amd64":
		binary, err := exec.LookPath("qemu-system-x86_64")
		if err != nil {
			return qemuPlatform{}, errors.New("qemu-system-x86_64 is required")
		}
		platform.Binary = binary
		platform.Machine = "q35"
		platform.Console = "ttyS0"
		platform.NetDevice = "virtio-net-pci"
	case "arm64":
		binary, err := exec.LookPath("qemu-system-aarch64")
		if err != nil {
			return qemuPlatform{}, errors.New("qemu-system-aarch64 is required")
		}
		platform.Binary = binary
		platform.Machine = "virt"
		platform.Console = "ttyAMA0"
		platform.NetDevice = "virtio-net-device"
	default:
		return qemuPlatform{}, fmt.Errorf("unsupported image architecture %q", imageArch)
	}

	return platform, nil
}

func buildQEMUArgs(
	spec StartSpec,
	platform qemuPlatform,
	diskPath string,
	diskFormat string,
	seedISO string,
	serialLogPath string,
	qemuLogPath string,
	pidFilePath string,
	monitorPath string,
) ([]string, error) {
	for _, path := range []string{diskPath, seedISO, spec.WorkspacePath, spec.StatePath, serialLogPath, qemuLogPath, pidFilePath, monitorPath} {
		if strings.Contains(path, ",") {
			return nil, fmt.Errorf("path contains unsupported comma: %s", path)
		}
	}

	portForwards, err := normalizePortForwards(spec.GatewayHostPort, spec.GatewayGuestPort, spec.PublishedPorts)
	if err != nil {
		return nil, err
	}

	netdev := "user,id=net0"
	for _, mapping := range portForwards {
		netdev += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:%d", mapping.HostPort, mapping.GuestPort)
	}

	args := []string{
		"-machine", fmt.Sprintf("%s,accel=%s", platform.Machine, platform.Accel),
		"-cpu", platform.CPU,
		"-smp", strconv.Itoa(spec.CPUs),
		"-m", strconv.Itoa(spec.MemoryMiB),
		"-kernel", spec.KernelPath,
		"-initrd", spec.InitrdPath,
		"-append", fmt.Sprintf("root=/dev/vda1 rw console=%s", platform.Console),
		"-drive", fmt.Sprintf("if=virtio,format=%s,file=%s", diskFormat, diskPath),
		"-drive", fmt.Sprintf("if=virtio,format=raw,readonly=on,file=%s", seedISO),
		"-virtfs", fmt.Sprintf("local,path=%s,mount_tag=workspace,security_model=none,id=workspace", spec.WorkspacePath),
		"-virtfs", fmt.Sprintf("local,path=%s,mount_tag=state,security_model=none,id=state", spec.StatePath),
		"-netdev", netdev,
		"-device", fmt.Sprintf("%s,netdev=net0", platform.NetDevice),
		"-display", "none",
		"-serial", "file:" + serialLogPath,
		"-monitor", "unix:" + monitorPath + ",server,nowait",
		"-D", qemuLogPath,
		"-daemonize",
		"-pidfile", pidFilePath,
	}

	return args, nil
}

func normalizePortForwards(gatewayHostPort int, gatewayGuestPort int, published []PortMapping) ([]PortMapping, error) {
	if err := validatePort(gatewayHostPort); err != nil {
		return nil, err
	}
	if err := validatePort(gatewayGuestPort); err != nil {
		return nil, err
	}

	used := map[int]int{gatewayHostPort: gatewayGuestPort}
	result := []PortMapping{{HostPort: gatewayHostPort, GuestPort: gatewayGuestPort}}
	for _, mapping := range published {
		if err := validatePort(mapping.HostPort); err != nil {
			return nil, fmt.Errorf("publish %d:%d invalid host port: %w", mapping.HostPort, mapping.GuestPort, err)
		}
		if err := validatePort(mapping.GuestPort); err != nil {
			return nil, fmt.Errorf("publish %d:%d invalid guest port: %w", mapping.HostPort, mapping.GuestPort, err)
		}

		existingGuest, exists := used[mapping.HostPort]
		if exists {
			if existingGuest == mapping.GuestPort {
				continue
			}
			return nil, fmt.Errorf("duplicate host port %d with different guests (%d and %d)", mapping.HostPort, existingGuest, mapping.GuestPort)
		}

		used[mapping.HostPort] = mapping.GuestPort
		result = append(result, mapping)
	}
	return result, nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("expected 1-65535")
	}
	return nil
}

func prepareInstanceDisk(sourceDiskPath string, instanceDir string, out io.Writer) (string, string, error) {
	absoluteSourceDiskPath, err := filepath.Abs(sourceDiskPath)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(absoluteSourceDiskPath); err != nil {
		return "", "", fmt.Errorf("source disk not found: %w", err)
	}

	overlayPath := filepath.Join(instanceDir, "rootfs.qcow2")
	if qemuImgPath, err := exec.LookPath("qemu-img"); err == nil {
		_ = os.Remove(overlayPath)
		command := exec.Command(qemuImgPath, "create", "-f", "qcow2", "-F", "raw", "-b", absoluteSourceDiskPath, overlayPath)
		output, err := command.CombinedOutput()
		if err == nil {
			writeLine(out, "instance disk prepared: %s (qcow2 overlay)", overlayPath)
			return overlayPath, "qcow2", nil
		}
		writeLine(out, "qemu-img overlay failed, falling back to raw copy: %s", strings.TrimSpace(string(output)))
	}

	rawPath := filepath.Join(instanceDir, "rootfs.raw")
	if err := copyFile(absoluteSourceDiskPath, rawPath); err != nil {
		return "", "", err
	}
	writeLine(out, "instance disk prepared: %s (raw copy)", rawPath)
	return rawPath, "raw", nil
}

func createNoCloudSeedISO(spec StartSpec, outputPath string) error {
	seedDir := filepath.Join(spec.InstanceDir, "seed")
	if err := os.RemoveAll(seedDir); err != nil {
		return err
	}
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		return err
	}

	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", spec.InstanceID, spec.InstanceID)
	userData := buildCloudInitUserData(spec)

	if err := os.WriteFile(filepath.Join(seedDir, "meta-data"), []byte(metaData), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(seedDir, "user-data"), []byte(userData), 0o644); err != nil {
		return err
	}

	if _, err := exec.LookPath("hdiutil"); err != nil {
		return errors.New("hdiutil is required to build cloud-init seed ISO")
	}
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	command := exec.Command(
		"hdiutil", "makehybrid", "-quiet",
		"-o", outputPath,
		seedDir,
		"-iso",
		"-joliet",
		"-default-volume-name", "cidata",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build seed iso: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func buildCloudInitUserData(spec StartSpec) string {
	bootstrapScript := buildBootstrapScript(spec)
	return fmt.Sprintf(`#cloud-config
package_update: false
write_files:
  - path: /usr/local/bin/vclaw-bootstrap.sh
    permissions: "0755"
    owner: root:root
    content: |
%s
runcmd:
  - [ bash, -lc, "/usr/local/bin/vclaw-bootstrap.sh > /var/log/vclaw-bootstrap.log 2>&1" ]
`, indentForCloudConfig(bootstrapScript, 6))
}

func buildBootstrapScript(spec StartSpec) string {
	packageName := spec.OpenClawPackage
	if packageName == "" {
		packageName = "openclaw@latest"
	}

	return fmt.Sprintf(`#!/usr/bin/env bash
set -euxo pipefail

modprobe 9p 2>/dev/null || true
modprobe 9pnet 2>/dev/null || true
modprobe 9pnet_virtio 2>/dev/null || true

mkdir -p /workspace /root/.openclaw /etc/vclaw

if ! mountpoint -q /workspace; then
  mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 workspace /workspace || true
fi
if ! mountpoint -q /root/.openclaw; then
  mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 state /root/.openclaw || true
fi

cat >/etc/vclaw/openclaw.json <<'JSON'
{
  "agents": {
    "defaults": {
      "workspace": "/workspace"
    }
  },
  "gateway": {
    "port": %d
  }
}
JSON

cat >/usr/local/bin/vclaw-gateway.sh <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

export HOME=/root
export OPENCLAW_CONFIG_PATH=/etc/vclaw/openclaw.json

if command -v openclaw >/dev/null 2>&1; then
  exec openclaw gateway --allow-unconfigured --port %d
fi

exec /usr/bin/python3 -m http.server %d --directory /workspace
SCRIPT
chmod +x /usr/local/bin/vclaw-gateway.sh

cat >/etc/systemd/system/vclaw-gateway.service <<'UNIT'
[Unit]
Description=vclaw Gateway Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/vclaw-gateway.sh
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now vclaw-gateway.service

if ! command -v openclaw >/dev/null 2>&1; then
  (
    set +e
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends ca-certificates curl gnupg bash python3
    if ! command -v node >/dev/null 2>&1; then
      curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
      apt-get install -y --no-install-recommends nodejs
    fi
    npm install -g %s
    systemctl restart vclaw-gateway.service
  ) >/var/log/vclaw-openclaw-install.log 2>&1 &
fi
`, spec.GatewayGuestPort, spec.GatewayGuestPort, spec.GatewayGuestPort, packageName)
}

func indentForCloudConfig(content string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	var builder strings.Builder
	for _, line := range lines {
		builder.WriteString(prefix)
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return strings.TrimSuffix(builder.String(), "\n")
}

func waitForPIDFile(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		contents, err := os.ReadFile(path)
		if err == nil {
			value := strings.TrimSpace(string(contents))
			pid, parseErr := strconv.Atoi(value)
			if parseErr == nil && pid > 0 {
				return pid, nil
			}
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timed out waiting for qemu pid file at %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func copyFile(sourcePath string, destinationPath string) error {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}

	temporaryPath := destinationPath + ".tmp"
	targetFile, err := os.Create(temporaryPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		targetFile.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := targetFile.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}

	if err := os.Rename(temporaryPath, destinationPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func detectHostArch() string {
	if runtime.GOOS == "darwin" {
		if output, err := exec.Command("sysctl", "-n", "hw.optional.arm64").Output(); err == nil {
			if strings.TrimSpace(string(output)) == "1" {
				return "arm64"
			}
		}
	}

	output, err := exec.Command("uname", "-m").Output()
	if err == nil {
		switch strings.TrimSpace(string(output)) {
		case "x86_64":
			return "amd64"
		case "arm64", "aarch64":
			return "arm64"
		}
	}
	return runtime.GOARCH
}
