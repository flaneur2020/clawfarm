package qemuargsbuilder

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type PortMapping struct {
	HostPort  int
	GuestPort int
}

type VolumeMount struct {
	HostPath string
	Tag      string
}

type QemuArgsBuilder struct {
	Machine          string
	CPU              string
	Accel            string
	NetDevice        string
	Firmware         string
	DiskPath         string
	DiskFormat       string
	SeedISOPath      string
	WorkspacePath    string
	StatePath        string
	ClawPath         string
	SerialLogPath    string
	QEMULogPath      string
	PIDFilePath      string
	MonitorPath      string
	GatewayHostPort  int
	GatewayGuestPort int
	PublishedPorts   []PortMapping
	VolumeMounts     []VolumeMount
	CPUs             int
	MemoryMiB        int
}

func NewQemuArgsBuilder() *QemuArgsBuilder {
	return &QemuArgsBuilder{}
}

func (builder *QemuArgsBuilder) WithPlatform(machine string, cpu string, accel string, netDevice string, firmware string) *QemuArgsBuilder {
	builder.Machine = machine
	builder.CPU = cpu
	builder.Accel = accel
	builder.NetDevice = netDevice
	builder.Firmware = firmware
	return builder
}

func (builder *QemuArgsBuilder) WithDisk(diskPath string, diskFormat string, seedISOPath string) *QemuArgsBuilder {
	builder.DiskPath = diskPath
	builder.DiskFormat = diskFormat
	builder.SeedISOPath = seedISOPath
	return builder
}

func (builder *QemuArgsBuilder) WithRuntimePaths(
	workspacePath string,
	statePath string,
	clawPath string,
	serialLogPath string,
	qemuLogPath string,
	pidFilePath string,
	monitorPath string,
) *QemuArgsBuilder {
	builder.WorkspacePath = workspacePath
	builder.StatePath = statePath
	builder.ClawPath = clawPath
	builder.SerialLogPath = serialLogPath
	builder.QEMULogPath = qemuLogPath
	builder.PIDFilePath = pidFilePath
	builder.MonitorPath = monitorPath
	return builder
}

func (builder *QemuArgsBuilder) WithPorts(gatewayHostPort int, gatewayGuestPort int, published []PortMapping) *QemuArgsBuilder {
	builder.GatewayHostPort = gatewayHostPort
	builder.GatewayGuestPort = gatewayGuestPort
	builder.PublishedPorts = append([]PortMapping(nil), published...)
	return builder
}

func (builder *QemuArgsBuilder) WithResources(cpus int, memoryMiB int) *QemuArgsBuilder {
	builder.CPUs = cpus
	builder.MemoryMiB = memoryMiB
	return builder
}

func (builder *QemuArgsBuilder) WithVolumeMounts(volumeMounts []VolumeMount) *QemuArgsBuilder {
	builder.VolumeMounts = append([]VolumeMount(nil), volumeMounts...)
	return builder
}

func (builder *QemuArgsBuilder) Build() ([]string, error) {
	paths := []string{
		builder.DiskPath,
		builder.SeedISOPath,
		builder.WorkspacePath,
		builder.StatePath,
		builder.SerialLogPath,
		builder.QEMULogPath,
		builder.PIDFilePath,
		builder.MonitorPath,
	}
	if builder.Firmware != "" {
		paths = append(paths, builder.Firmware)
	}
	for _, mount := range builder.VolumeMounts {
		paths = append(paths, mount.HostPath)
	}
	for _, path := range paths {
		if strings.Contains(path, ",") {
			return nil, fmt.Errorf("path contains unsupported comma: %s", path)
		}
	}

	for _, mount := range builder.VolumeMounts {
		if strings.TrimSpace(mount.Tag) == "" {
			return nil, errors.New("volume mount tag is required")
		}
		if strings.Contains(mount.Tag, ",") {
			return nil, fmt.Errorf("volume mount tag contains unsupported comma: %s", mount.Tag)
		}
	}

	portForwards, err := NormalizePortForwards(builder.GatewayHostPort, builder.GatewayGuestPort, builder.PublishedPorts)
	if err != nil {
		return nil, err
	}

	netdev := "user,id=net0"
	for _, mapping := range portForwards {
		netdev += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:%d", mapping.HostPort, mapping.GuestPort)
	}

	args := []string{
		"-machine", fmt.Sprintf("%s,accel=%s", builder.Machine, builder.Accel),
		"-cpu", builder.CPU,
		"-smp", strconv.Itoa(builder.CPUs),
		"-m", strconv.Itoa(builder.MemoryMiB),
	}

	if builder.Firmware != "" {
		args = append(args, "-bios", builder.Firmware)
	}

	args = append(args,
		"-boot", "order=c",
		"-drive", fmt.Sprintf("if=virtio,format=%s,file=%s", builder.DiskFormat, builder.DiskPath),
		"-drive", fmt.Sprintf("if=virtio,format=raw,readonly=on,file=%s", builder.SeedISOPath),
		"-virtfs", fmt.Sprintf("local,path=%s,mount_tag=workspace,security_model=none,id=workspace", builder.WorkspacePath),
		"-virtfs", fmt.Sprintf("local,path=%s,mount_tag=state,security_model=none,id=state", builder.StatePath),
		"-netdev", netdev,
		"-device", fmt.Sprintf("%s,netdev=net0", builder.NetDevice),
		"-display", "none",
		"-serial", "file:"+builder.SerialLogPath,
		"-monitor", "unix:"+builder.MonitorPath+",server,nowait",
		"-D", builder.QEMULogPath,
		"-daemonize",
		"-pidfile", builder.PIDFilePath,
	)

	if strings.TrimSpace(builder.ClawPath) != "" {
		args = append(args,
			"-virtfs",
			fmt.Sprintf("local,path=%s,mount_tag=claw,security_model=none,id=claw", builder.ClawPath),
		)
	}

	for index, mount := range builder.VolumeMounts {
		args = append(args,
			"-virtfs",
			fmt.Sprintf("local,path=%s,mount_tag=%s,security_model=none,id=volume%d", mount.HostPath, mount.Tag, index+1),
		)
	}

	return args, nil
}

func NormalizePortForwards(gatewayHostPort int, gatewayGuestPort int, published []PortMapping) ([]PortMapping, error) {
	if err := ValidatePort(gatewayHostPort); err != nil {
		return nil, err
	}
	if err := ValidatePort(gatewayGuestPort); err != nil {
		return nil, err
	}

	used := map[int]int{gatewayHostPort: gatewayGuestPort}
	result := []PortMapping{{HostPort: gatewayHostPort, GuestPort: gatewayGuestPort}}
	for _, mapping := range published {
		if err := ValidatePort(mapping.HostPort); err != nil {
			return nil, fmt.Errorf("publish %d:%d invalid host port: %w", mapping.HostPort, mapping.GuestPort, err)
		}
		if err := ValidatePort(mapping.GuestPort); err != nil {
			return nil, fmt.Errorf("publish %d:%d invalid guest port: %w", mapping.HostPort, mapping.GuestPort, err)
		}

		existingGuest, exists := used[mapping.HostPort]
		if exists {
			if existingGuest == mapping.GuestPort {
				continue
			}
			return nil, fmt.Errorf(
				"duplicate host port %d with different guests (%d and %d)",
				mapping.HostPort,
				existingGuest,
				mapping.GuestPort,
			)
		}

		used[mapping.HostPort] = mapping.GuestPort
		result = append(result, mapping)
	}
	return result, nil
}

func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("expected 1-65535")
	}
	return nil
}
