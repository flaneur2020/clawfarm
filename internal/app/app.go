package app

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yazhou/krunclaw/internal/config"
	"github.com/yazhou/krunclaw/internal/images"
	"github.com/yazhou/krunclaw/internal/state"
	"github.com/yazhou/krunclaw/internal/vm"
)

const (
	defaultGatewayPort      = 18789
	defaultCPUs             = 2
	defaultMemoryMiB        = 4096
	defaultReadyTimeoutSecs = 900
)

type App struct {
	out     io.Writer
	errOut  io.Writer
	backend vm.Backend
}

func New(out io.Writer, errOut io.Writer) *App {
	return NewWithBackend(out, errOut, vm.NewQEMUBackend(out))
}

func NewWithBackend(out io.Writer, errOut io.Writer, backend vm.Backend) *App {
	return &App{out: out, errOut: errOut, backend: backend}
}

func (a *App) Run(args []string) error {
	if len(args) == 0 {
		a.printUsage()
		return nil
	}

	switch args[0] {
	case "image":
		return a.runImage(args[1:])
	case "run":
		return a.runRun(args[1:])
	case "ps":
		return a.runPS(args[1:])
	case "suspend":
		return a.runSuspend(args[1:])
	case "resume":
		return a.runResume(args[1:])
	case "rm":
		return a.runRemove(args[1:])
	case "help", "-h", "--help":
		a.printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a *App) runImage(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: vclaw image <ls|fetch>")
	}

	manager, err := a.imageManager()
	if err != nil {
		return err
	}

	switch args[0] {
	case "ls":
		if len(args) != 1 {
			return errors.New("usage: vclaw image ls")
		}
		items, err := manager.ListAvailable()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(a.out, "no images available")
			return nil
		}
		tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "REF\tARCH\tDOWNLOADED\tUPDATED(UTC)")
		for _, item := range items {
			downloaded := "no"
			updated := "-"
			if item.Ready {
				downloaded = "yes"
				if !item.UpdatedAtUTC.IsZero() {
					updated = item.UpdatedAtUTC.Format(time.RFC3339)
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", item.Ref, item.Arch, downloaded, updated)
		}
		return tw.Flush()
	case "fetch":
		if len(args) != 2 {
			return errors.New("usage: vclaw image fetch <ref>")
		}
		ref := args[1]
		fmt.Fprintf(a.out, "fetching image %s\n", ref)
		meta, err := manager.Fetch(context.Background(), ref)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "cached image %s\n", meta.Ref)
		fmt.Fprintf(a.out, "  file:   %s\n", meta.RuntimeDisk)
		fmt.Fprintf(a.out, "  format: %s\n", meta.DiskFormat)
		return nil
	default:
		return fmt.Errorf("unknown image subcommand %q", args[0])
	}
}

func (a *App) runRun(args []string) error {
	args = normalizeRunArgs(args)

	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(a.errOut)

	workspace := "."
	gatewayPort := defaultGatewayPort
	cpus := defaultCPUs
	memoryMiB := defaultMemoryMiB
	readyTimeoutSecs := defaultReadyTimeoutSecs
	noWait := false
	openClawPackage := "openclaw@latest"
	var published portList

	flags.StringVar(&workspace, "workspace", ".", "workspace path to mount")
	flags.IntVar(&gatewayPort, "port", defaultGatewayPort, "host gateway port")
	flags.IntVar(&cpus, "cpus", defaultCPUs, "vCPU count")
	flags.IntVar(&memoryMiB, "memory-mib", defaultMemoryMiB, "memory size in MiB")
	flags.IntVar(&readyTimeoutSecs, "ready-timeout-secs", defaultReadyTimeoutSecs, "gateway readiness timeout in seconds")
	flags.BoolVar(&noWait, "no-wait", false, "start and return without waiting for readiness")
	flags.StringVar(&openClawPackage, "openclaw-package", "openclaw@latest", "OpenClaw package spec")
	flags.Var(&published, "publish", "host:guest mapping (repeatable)")
	flags.Var(&published, "port-forward", "alias of --publish (repeatable)")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: vclaw run <ref> [--workspace=. --port=18789 --publish host:guest]")
	}
	if gatewayPort < 1 || gatewayPort > 65535 {
		return fmt.Errorf("invalid gateway port %d: expected 1-65535", gatewayPort)
	}
	if cpus < 1 {
		return errors.New("cpus must be >= 1")
	}
	if memoryMiB < 512 {
		return errors.New("memory-mib must be >= 512")
	}
	if readyTimeoutSecs < 1 {
		return errors.New("ready-timeout-secs must be >= 1")
	}

	workspacePath, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if info, err := os.Stat(workspacePath); err != nil {
		return fmt.Errorf("workspace %s: %w", workspacePath, err)
	} else if !info.IsDir() {
		return fmt.Errorf("workspace %s is not a directory", workspacePath)
	}

	manager, err := a.imageManager()
	if err != nil {
		return err
	}

	ref := flags.Arg(0)
	imageMeta, err := manager.Resolve(ref)
	if err != nil {
		if errors.Is(err, images.ErrImageNotFetched) {
			return fmt.Errorf("image %s is not ready, run `vclaw image fetch %s` first", ref, ref)
		}
		return err
	}

	store, instancesRoot, err := a.instanceStore()
	if err != nil {
		return err
	}

	id, err := newClawID()
	if err != nil {
		return err
	}
	instanceDir := filepath.Join(instancesRoot, id)
	statePath := filepath.Join(instanceDir, "state")
	if err := ensureDir(statePath); err != nil {
		return err
	}
	instanceImagePath := filepath.Join(instanceDir, "instance.img")
	if err := copyFile(imageMeta.RuntimeDisk, instanceImagePath); err != nil {
		return err
	}

	vmPublished := make([]vm.PortMapping, 0, len(published.Mappings))
	for _, mapping := range published.Mappings {
		vmPublished = append(vmPublished, vm.PortMapping{HostPort: mapping.HostPort, GuestPort: mapping.GuestPort})
	}

	startResult, err := a.backend.Start(context.Background(), vm.StartSpec{
		InstanceID:       id,
		InstanceDir:      instanceDir,
		ImageArch:        imageMeta.Arch,
		SourceDiskPath:   instanceImagePath,
		WorkspacePath:    workspacePath,
		StatePath:        statePath,
		GatewayHostPort:  gatewayPort,
		GatewayGuestPort: gatewayPort,
		PublishedPorts:   vmPublished,
		CPUs:             cpus,
		MemoryMiB:        memoryMiB,
		OpenClawPackage:  openClawPackage,
	})
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	instance := state.Instance{
		ID:             id,
		ImageRef:       ref,
		WorkspacePath:  workspacePath,
		StatePath:      statePath,
		GatewayPort:    gatewayPort,
		PublishedPorts: published.Mappings,
		Status:         "booting",
		Backend:        "qemu",
		PID:            startResult.PID,
		DiskPath:       startResult.DiskPath,
		SeedISOPath:    startResult.SeedISOPath,
		SerialLogPath:  startResult.SerialLogPath,
		QEMULogPath:    startResult.QEMULogPath,
		MonitorPath:    startResult.MonitorPath,
		QEMUAccel:      startResult.Accel,
		CreatedAtUTC:   now,
		UpdatedAtUTC:   now,
	}
	if noWait {
		instance.Status = "running"
	}
	if err := store.Save(instance); err != nil {
		return err
	}

	fmt.Fprintf(a.out, "CLAWID: %s\n", id)
	fmt.Fprintf(a.out, "image: %s (%s)\n", ref, imageMeta.Arch)
	fmt.Fprintf(a.out, "workspace: %s\n", workspacePath)
	fmt.Fprintf(a.out, "state: %s\n", statePath)
	fmt.Fprintf(a.out, "gateway: http://127.0.0.1:%d/\n", gatewayPort)
	fmt.Fprintf(a.out, "vm pid: %d\n", startResult.PID)
	fmt.Fprintf(a.out, "serial log: %s\n", startResult.SerialLogPath)
	if len(instance.PublishedPorts) > 0 {
		for _, mapping := range instance.PublishedPorts {
			fmt.Fprintf(a.out, "publish: 127.0.0.1:%d -> %d\n", mapping.HostPort, mapping.GuestPort)
		}
	}

	if noWait {
		fmt.Fprintln(a.out, "status: running (not waiting for gateway readiness)")
		return nil
	}

	address := fmt.Sprintf("127.0.0.1:%d", gatewayPort)
	httpURL := fmt.Sprintf("http://%s/", address)
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Duration(readyTimeoutSecs)*time.Second)
	defer cancel()
	if err := vm.WaitForHTTP(waitCtx, httpURL); err != nil {
		instance.LastError = err.Error()
		instance.UpdatedAtUTC = time.Now().UTC()
		if saveErr := store.Save(instance); saveErr != nil {
			return fmt.Errorf("%w (also failed to save instance state: %v)", err, saveErr)
		}
		return fmt.Errorf("gateway is not reachable yet at %s (%v); check %s", httpURL, err, instance.SerialLogPath)
	}

	instance.Status = "ready"
	instance.LastError = ""
	instance.UpdatedAtUTC = time.Now().UTC()
	if err := store.Save(instance); err != nil {
		return err
	}

	fmt.Fprintf(a.out, "status: ready (%s)\n", httpURL)
	return nil
}

func (a *App) runPS(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: vclaw ps")
	}
	store, _, err := a.instanceStore()
	if err != nil {
		return err
	}
	instances, err := store.List()
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		fmt.Fprintln(a.out, "no instances")
		return nil
	}

	for index := range instances {
		updated, changed := a.reconcileInstanceStatus(instances[index])
		if changed {
			updated.UpdatedAtUTC = time.Now().UTC()
			if err := store.Save(updated); err != nil {
				return err
			}
			instances[index] = updated
		}
	}

	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CLAWID\tIMAGE\tSTATUS\tGATEWAY\tPID\tUPDATED(UTC)")
	for _, instance := range instances {
		fmt.Fprintf(tw, "%s\t%s\t%s\t127.0.0.1:%d\t%d\t%s\n", instance.ID, instance.ImageRef, instance.Status, instance.GatewayPort, instance.PID, instance.UpdatedAtUTC.Format(time.RFC3339))
	}
	return tw.Flush()
}

func (a *App) reconcileInstanceStatus(instance state.Instance) (state.Instance, bool) {
	if instance.PID <= 0 {
		return instance, false
	}

	changed := false
	isRunning := a.backend.IsRunning(instance.PID)
	if !isRunning && instance.Status != "exited" {
		instance.Status = "exited"
		changed = true
		return instance, changed
	}
	if !isRunning {
		return instance, false
	}

	if instance.Status == "suspended" {
		return instance, false
	}

	if (instance.Status == "booting" || instance.Status == "running") && vm.IsHTTPReachable(fmt.Sprintf("http://127.0.0.1:%d/", instance.GatewayPort), 300*time.Millisecond) {
		instance.Status = "ready"
		instance.LastError = ""
		changed = true
	}
	return instance, changed
}

func (a *App) runSuspend(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: vclaw suspend <clawid>")
	}
	return a.updateInstanceStateWithSignal(args[0], "suspended")
}

func (a *App) runResume(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: vclaw resume <clawid>")
	}
	return a.updateInstanceStateWithSignal(args[0], "running")
}

func (a *App) updateInstanceStateWithSignal(id string, status string) error {
	store, _, err := a.instanceStore()
	if err != nil {
		return err
	}

	instance, err := store.Load(id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return fmt.Errorf("instance %s not found", id)
		}
		return err
	}

	if instance.PID <= 0 {
		return fmt.Errorf("instance %s has no running process", id)
	}

	if status == "suspended" {
		if err := a.backend.Suspend(instance.PID); err != nil {
			return err
		}
	} else {
		if err := a.backend.Resume(instance.PID); err != nil {
			return err
		}
	}

	instance.Status = status
	instance.UpdatedAtUTC = time.Now().UTC()
	if err := store.Save(instance); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s -> %s\n", id, status)
	return nil
}

func (a *App) runRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: vclaw rm <clawid>")
	}
	store, _, err := a.instanceStore()
	if err != nil {
		return err
	}

	instance, err := store.Load(args[0])
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return fmt.Errorf("instance %s not found", args[0])
		}
		return err
	}

	if instance.PID > 0 && a.backend.IsRunning(instance.PID) {
		stopCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		if err := a.backend.Stop(stopCtx, instance.PID); err != nil {
			return err
		}
	}

	if err := store.Delete(args[0]); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return fmt.Errorf("instance %s not found", args[0])
		}
		return err
	}
	fmt.Fprintf(a.out, "removed %s\n", args[0])
	return nil
}

func (a *App) imageManager() (*images.Manager, error) {
	cacheDir, err := config.CacheDir()
	if err != nil {
		return nil, err
	}
	if err := ensureDir(cacheDir); err != nil {
		return nil, err
	}
	return images.NewManager(cacheDir, a.out), nil
}

func (a *App) instanceStore() (*state.Store, string, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, "", err
	}
	instancesRoot := filepath.Join(dataDir, "instances")
	if err := ensureDir(instancesRoot); err != nil {
		return nil, "", err
	}
	return state.NewStore(instancesRoot), instancesRoot, nil
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func newClawID() (string, error) {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return fmt.Sprintf("claw-%x", buffer), nil
}

func (a *App) printUsage() {
	fmt.Fprintln(a.out, "vclaw - run full OpenClaw inside a lightweight VM")
	fmt.Fprintln(a.out, "")
	fmt.Fprintln(a.out, "Usage:")
	fmt.Fprintln(a.out, "  vclaw image ls")
	fmt.Fprintln(a.out, "  vclaw image fetch <ref>")
	fmt.Fprintln(a.out, "  vclaw run <ref> [--workspace=. --port=18789 --publish host:guest]")
	fmt.Fprintln(a.out, "  vclaw ps")
	fmt.Fprintln(a.out, "  vclaw suspend <clawid>")
	fmt.Fprintln(a.out, "  vclaw resume <clawid>")
	fmt.Fprintln(a.out, "  vclaw rm <clawid>")
	fmt.Fprintln(a.out, "")
	fmt.Fprintln(a.out, "Examples:")
	fmt.Fprintln(a.out, "  vclaw image fetch ubuntu:24.04")
	fmt.Fprintln(a.out, "  vclaw run ubuntu:24.04 --workspace=. --publish 8080:80")
}

type portList struct {
	Values   []string
	Mappings []state.PortMapping
}

func (l *portList) String() string {
	return strings.Join(l.Values, ",")
}

func (l *portList) Set(value string) error {
	mapping, err := parsePortMapping(value)
	if err != nil {
		return err
	}
	l.Values = append(l.Values, value)
	l.Mappings = append(l.Mappings, mapping)
	return nil
}

func parsePortMapping(input string) (state.PortMapping, error) {
	parts := strings.Split(input, ":")
	if len(parts) != 2 {
		return state.PortMapping{}, fmt.Errorf("invalid publish value %q: expected host:guest", input)
	}
	host, err := strconv.Atoi(parts[0])
	if err != nil {
		return state.PortMapping{}, fmt.Errorf("invalid host port %q", parts[0])
	}
	guest, err := strconv.Atoi(parts[1])
	if err != nil {
		return state.PortMapping{}, fmt.Errorf("invalid guest port %q", parts[1])
	}
	if host < 1 || host > 65535 || guest < 1 || guest > 65535 {
		return state.PortMapping{}, fmt.Errorf("ports must be within 1-65535")
	}
	return state.PortMapping{HostPort: host, GuestPort: guest}, nil
}

func normalizeRunArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	if strings.HasPrefix(args[0], "-") {
		return args
	}
	reordered := make([]string, 0, len(args))
	reordered = append(reordered, args[1:]...)
	reordered = append(reordered, args[0])
	return reordered
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
