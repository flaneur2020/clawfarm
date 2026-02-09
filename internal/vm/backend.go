package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

type PortMapping struct {
	HostPort  int
	GuestPort int
}

type StartSpec struct {
	InstanceID        string
	InstanceDir       string
	ImageArch         string
	SourceDiskPath    string
	WorkspacePath     string
	StatePath         string
	GatewayHostPort   int
	GatewayGuestPort  int
	PublishedPorts    []PortMapping
	CPUs              int
	MemoryMiB         int
	OpenClawPackage   string
	OpenClawConfigArg string
}

type StartResult struct {
	PID           int
	DiskPath      string
	DiskFormat    string
	SeedISOPath   string
	SerialLogPath string
	QEMULogPath   string
	PIDFilePath   string
	MonitorPath   string
	Accel         string
	Command       []string
}

type Backend interface {
	Start(ctx context.Context, spec StartSpec) (StartResult, error)
	Stop(ctx context.Context, pid int) error
	Suspend(pid int) error
	Resume(pid int) error
	IsRunning(pid int) bool
}

func WaitForTCP(ctx context.Context, address string) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		if IsTCPReachable(address, 1*time.Second) {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("timeout waiting for %s", address)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func WaitForHTTP(ctx context.Context, url string) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		if IsHTTPReachable(url, 2*time.Second) {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("timeout waiting for %s", url)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func IsTCPReachable(address string, timeout time.Duration) bool {
	connection, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func IsHTTPReachable(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	response, err := client.Get(url)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode >= 100 && response.StatusCode <= 599
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

func writeLine(out io.Writer, format string, args ...interface{}) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, format+"\n", args...)
}
