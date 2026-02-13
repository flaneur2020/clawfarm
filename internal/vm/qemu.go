package vm

import (
	"context"
	"encoding/json"
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

	"github.com/yazhou/krunclaw/internal/vm/cloudinitbuilder"
	"github.com/yazhou/krunclaw/internal/vm/qemuargsbuilder"
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
	NetDevice string
	Accel     string
	Firmware  string
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
		platform.NetDevice = "virtio-net-pci"
	case "arm64":
		binary, err := exec.LookPath("qemu-system-aarch64")
		if err != nil {
			return qemuPlatform{}, errors.New("qemu-system-aarch64 is required")
		}
		firmwarePath, err := findAArch64Firmware()
		if err != nil {
			return qemuPlatform{}, err
		}
		platform.Binary = binary
		platform.Machine = "virt"
		platform.NetDevice = "virtio-net-device"
		platform.Firmware = firmwarePath
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
	published := make([]qemuargsbuilder.PortMapping, 0, len(spec.PublishedPorts))
	for _, mapping := range spec.PublishedPorts {
		published = append(published, qemuargsbuilder.PortMapping{HostPort: mapping.HostPort, GuestPort: mapping.GuestPort})
	}

	builder := qemuargsbuilder.NewQemuArgsBuilder().
		WithPlatform(platform.Machine, platform.CPU, platform.Accel, platform.NetDevice, platform.Firmware).
		WithDisk(diskPath, diskFormat, seedISO).
		WithRuntimePaths(spec.WorkspacePath, spec.StatePath, spec.ClawPath, serialLogPath, qemuLogPath, pidFilePath, monitorPath).
		WithPorts(spec.GatewayHostPort, spec.GatewayGuestPort, published).
		WithResources(spec.CPUs, spec.MemoryMiB)
	return builder.Build()
}

func normalizePortForwards(gatewayHostPort int, gatewayGuestPort int, published []PortMapping) ([]PortMapping, error) {
	mappings := make([]qemuargsbuilder.PortMapping, 0, len(published))
	for _, mapping := range published {
		mappings = append(mappings, qemuargsbuilder.PortMapping{HostPort: mapping.HostPort, GuestPort: mapping.GuestPort})
	}

	resolved, err := qemuargsbuilder.NormalizePortForwards(gatewayHostPort, gatewayGuestPort, mappings)
	if err != nil {
		return nil, err
	}

	result := make([]PortMapping, 0, len(resolved))
	for _, mapping := range resolved {
		result = append(result, PortMapping{HostPort: mapping.HostPort, GuestPort: mapping.GuestPort})
	}
	return result, nil
}

func validatePort(port int) error {
	return qemuargsbuilder.ValidatePort(port)
}

func prepareInstanceDisk(sourceDiskPath string, instanceDir string, out io.Writer) (string, string, error) {
	_ = instanceDir

	absoluteSourceDiskPath, err := filepath.Abs(sourceDiskPath)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(absoluteSourceDiskPath); err != nil {
		return "", "", fmt.Errorf("source disk not found: %w", err)
	}

	format := "raw"
	if qemuImgPath, err := exec.LookPath("qemu-img"); err == nil {
		if detectedFormat, detectErr := detectSourceDiskFormat(qemuImgPath, absoluteSourceDiskPath); detectErr == nil {
			format = detectedFormat
		}
	} else if detectedFormat, detectErr := detectDiskFormatByMagic(absoluteSourceDiskPath); detectErr == nil {
		format = detectedFormat
	}

	if format != "raw" && format != "qcow2" {
		format = "raw"
	}

	writeLine(out, "instance disk prepared: %s (%s)", absoluteSourceDiskPath, format)
	return absoluteSourceDiskPath, format, nil
}

func detectSourceDiskFormat(qemuImgPath string, imagePath string) (string, error) {
	command := exec.Command(qemuImgPath, "info", "--output=json", imagePath)
	output, err := command.Output()
	if err != nil {
		return "", err
	}

	var payload struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return "", err
	}
	if payload.Format == "" {
		return "", errors.New("empty format")
	}
	return payload.Format, nil
}

func detectDiskFormatByMagic(imagePath string) (string, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	header := make([]byte, 4)
	if _, err := io.ReadFull(file, header); err != nil {
		return "", err
	}

	if string(header) == "QFI\xfb" {
		return "qcow2", nil
	}
	return "raw", nil
}

func findAArch64Firmware() (string, error) {
	candidates := []string{
		"/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
		"/usr/local/share/qemu/edk2-aarch64-code.fd",
		"/usr/share/qemu/edk2-aarch64-code.fd",
		"/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",
		"/usr/share/edk2/aarch64/QEMU_EFI.fd",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("aarch64 firmware is required (missing edk2-aarch64-code.fd / QEMU_EFI.fd)")
}

func createNoCloudSeedISO(spec StartSpec, outputPath string) error {
	builder := newCloudInitBuilder(spec)
	return builder.CreateNoCloudSeedISO(outputPath)
}

func buildCloudInitUserData(spec StartSpec) string {
	builder := newCloudInitBuilder(spec)
	return builder.BuildCloudInitUserData()
}

func buildBootstrapScript(spec StartSpec) string {
	builder := newCloudInitBuilder(spec)
	return builder.BuildBootstrapScript()
}

func indentForCloudConfig(content string, spaces int) string {
	return cloudinitbuilder.IndentForCloudConfig(content, spaces)
}

func newCloudInitBuilder(spec StartSpec) *cloudinitbuilder.CloudInitBuilder {
	return cloudinitbuilder.NewCloudInitBuilder().
		WithInstance(spec.InstanceID, spec.InstanceDir).
		WithGatewayGuestPort(spec.GatewayGuestPort).
		WithOpenClawPackage(spec.OpenClawPackage).
		WithOpenClawConfig(spec.OpenClawConfig).
		WithOpenClawEnvironment(spec.OpenClawEnvironment).
		WithCloudInitProvision(spec.CloudInitProvision)
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
