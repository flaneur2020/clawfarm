package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/yazhou/krunclaw/internal/clawbox"
	"github.com/yazhou/krunclaw/internal/config"
	"github.com/yazhou/krunclaw/internal/images"
	"github.com/yazhou/krunclaw/internal/mount"
	"github.com/yazhou/krunclaw/internal/state"
	"github.com/yazhou/krunclaw/internal/vm"
)

const (
	defaultGatewayPort      = 18789
	defaultCPUs             = 2
	defaultMemoryMiB        = 4096
	defaultReadyTimeoutSecs = 900
	unhealthyGracePeriod    = 30 * time.Second
)

type App struct {
	out     io.Writer
	errOut  io.Writer
	in      io.Reader
	backend vm.Backend
}

func New(out io.Writer, errOut io.Writer) *App {
	return NewWithIOAndBackend(out, errOut, os.Stdin, vm.NewQEMUBackend(out))
}

func NewWithBackend(out io.Writer, errOut io.Writer, backend vm.Backend) *App {
	return NewWithIOAndBackend(out, errOut, nil, backend)
}

func NewWithIOAndBackend(out io.Writer, errOut io.Writer, in io.Reader, backend vm.Backend) *App {
	return &App{out: out, errOut: errOut, in: in, backend: backend}
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
	case "export":
		return a.runExport(args[1:])
	case "checkpoint":
		return a.runCheckpoint(args[1:])
	case "restore":
		return a.runRestore(args[1:])
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

type runTarget struct {
	Input                   string
	ImageRef                string
	ClawID                  string
	MountSource             string
	OpenClawModelPrimary    string
	OpenClawGatewayAuthMode string
	IsClawbox               bool
}

func (a *App) resolveRunTarget(input string) (runTarget, error) {
	if !isClawboxRunInput(input) {
		return runTarget{Input: input, ImageRef: input}, nil
	}

	clawboxPath, err := resolveClawboxPath(input)
	if err != nil {
		return runTarget{}, err
	}

	header, err := clawbox.LoadHeaderJSON(clawboxPath)
	if err != nil {
		return runTarget{}, fmt.Errorf("load clawbox %s: %w", clawboxPath, err)
	}
	if strings.TrimSpace(header.Spec.BaseImage.Ref) == "" {
		return runTarget{}, fmt.Errorf("clawbox %s missing spec.base_image.ref", clawboxPath)
	}

	clawID, err := header.ClawID(clawboxPath)
	if err != nil {
		return runTarget{}, fmt.Errorf("compute CLAWID for %s: %w", clawboxPath, err)
	}

	return runTarget{
		Input:                   input,
		ImageRef:                strings.TrimSpace(header.Spec.BaseImage.Ref),
		ClawID:                  clawID,
		MountSource:             clawboxPath,
		OpenClawModelPrimary:    strings.TrimSpace(header.Spec.OpenClaw.ModelPrimary),
		OpenClawGatewayAuthMode: strings.TrimSpace(header.Spec.OpenClaw.GatewayAuthMode),
		IsClawbox:               true,
	}, nil
}

func isClawboxRunInput(input string) bool {
	trimmed := strings.TrimSpace(input)
	return trimmed == "." || strings.HasSuffix(trimmed, ".clawbox")
}

func resolveClawboxPath(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "." {
		entries, err := os.ReadDir(".")
		if err != nil {
			return "", err
		}
		matches := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasSuffix(name, ".clawbox") {
				matches = append(matches, name)
			}
		}
		switch len(matches) {
		case 0:
			return "", errors.New("current directory does not contain a .clawbox file")
		case 1:
			absolutePath, err := filepath.Abs(matches[0])
			if err != nil {
				return "", err
			}
			return absolutePath, nil
		default:
			return "", fmt.Errorf("current directory has multiple .clawbox files, choose one explicitly: %s", strings.Join(matches, ", "))
		}
	}

	absolutePath, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory: expected .clawbox file", absolutePath)
	}
	return absolutePath, nil
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
	openClawConfigPath := ""
	openClawEnvFile := ""
	openClawAgentWorkspace := "/workspace"
	openClawModelPrimary := ""
	openClawGatewayMode := ""
	openClawGatewayAuthMode := ""
	openClawGatewayToken := ""
	openClawGatewayPassword := ""
	openClawOpenAIAPIKey := ""
	openClawAnthropicAPIKey := ""
	openClawGoogleGenerativeAIAPIKey := ""
	openClawXAIAPIKey := ""
	openClawOpenRouterAPIKey := ""
	openClawZAIAPIKey := ""
	openClawDiscordToken := ""
	openClawTelegramToken := ""
	openClawWhatsAppPhoneNumberID := ""
	openClawWhatsAppAccessToken := ""
	openClawWhatsAppVerifyToken := ""
	openClawWhatsAppAppSecret := ""
	var published portList
	var openClawEnvironment envVarList

	flags.StringVar(&workspace, "workspace", ".", "workspace path to mount")
	flags.IntVar(&gatewayPort, "port", defaultGatewayPort, "host gateway port")
	flags.IntVar(&cpus, "cpus", defaultCPUs, "vCPU count")
	flags.IntVar(&memoryMiB, "memory-mib", defaultMemoryMiB, "memory size in MiB")
	flags.IntVar(&readyTimeoutSecs, "ready-timeout-secs", defaultReadyTimeoutSecs, "gateway readiness timeout in seconds")
	flags.BoolVar(&noWait, "no-wait", false, "start and return without waiting for readiness")
	flags.StringVar(&openClawPackage, "openclaw-package", "openclaw@latest", "OpenClaw package spec")
	flags.StringVar(&openClawConfigPath, "openclaw-config", "", "host path to OpenClaw JSON config")
	flags.StringVar(&openClawEnvFile, "openclaw-env-file", "", "host path to OpenClaw .env file")
	flags.StringVar(&openClawAgentWorkspace, "openclaw-agent-workspace", "/workspace", "OpenClaw agents.defaults.workspace")
	flags.StringVar(&openClawModelPrimary, "openclaw-model-primary", "", "OpenClaw agents.defaults.model.primary")
	flags.StringVar(&openClawGatewayMode, "openclaw-gateway-mode", "", "OpenClaw gateway.mode (example: local)")
	flags.StringVar(&openClawGatewayAuthMode, "openclaw-gateway-auth-mode", "", "OpenClaw gateway.auth.mode (token|password|none)")
	flags.StringVar(&openClawGatewayToken, "openclaw-gateway-token", "", "OpenClaw gateway token (maps to OPENCLAW_GATEWAY_TOKEN)")
	flags.StringVar(&openClawGatewayPassword, "openclaw-gateway-password", "", "OpenClaw gateway password (maps to OPENCLAW_GATEWAY_PASSWORD)")
	flags.StringVar(&openClawOpenAIAPIKey, "openclaw-openai-api-key", "", "OpenAI API key (maps to OPENAI_API_KEY)")
	flags.StringVar(&openClawAnthropicAPIKey, "openclaw-anthropic-api-key", "", "Anthropic API key (maps to ANTHROPIC_API_KEY)")
	flags.StringVar(&openClawGoogleGenerativeAIAPIKey, "openclaw-google-generative-ai-api-key", "", "Google Generative AI API key (maps to GOOGLE_GENERATIVE_AI_API_KEY)")
	flags.StringVar(&openClawXAIAPIKey, "openclaw-xai-api-key", "", "xAI API key (maps to XAI_API_KEY)")
	flags.StringVar(&openClawOpenRouterAPIKey, "openclaw-openrouter-api-key", "", "OpenRouter API key (maps to OPENROUTER_API_KEY)")
	flags.StringVar(&openClawZAIAPIKey, "openclaw-zai-api-key", "", "Z.AI API key (maps to ZAI_API_KEY)")
	flags.StringVar(&openClawDiscordToken, "openclaw-discord-token", "", "Discord token (maps to DISCORD_TOKEN)")
	flags.StringVar(&openClawTelegramToken, "openclaw-telegram-token", "", "Telegram token (maps to TELEGRAM_TOKEN)")
	flags.StringVar(&openClawWhatsAppPhoneNumberID, "openclaw-whatsapp-phone-number-id", "", "WhatsApp phone number id (maps to WHATSAPP_PHONE_NUMBER_ID)")
	flags.StringVar(&openClawWhatsAppAccessToken, "openclaw-whatsapp-access-token", "", "WhatsApp access token (maps to WHATSAPP_ACCESS_TOKEN)")
	flags.StringVar(&openClawWhatsAppVerifyToken, "openclaw-whatsapp-verify-token", "", "WhatsApp verify token (maps to WHATSAPP_VERIFY_TOKEN)")
	flags.StringVar(&openClawWhatsAppAppSecret, "openclaw-whatsapp-app-secret", "", "WhatsApp app secret (maps to WHATSAPP_APP_SECRET)")
	flags.Var(&openClawEnvironment, "openclaw-env", "OpenClaw env override KEY=VALUE (repeatable)")
	flags.Var(&published, "publish", "host:guest mapping (repeatable)")
	flags.Var(&published, "port-forward", "alias of --publish (repeatable)")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: vclaw run <ref|file.clawbox|.> [--workspace=. --port=18789 --publish host:guest] [--openclaw-config path --openclaw-env-file path --openclaw-env KEY=VALUE] [--openclaw-openai-api-key ... --openclaw-discord-token ...]")
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
	if openClawGatewayAuthMode != "" && openClawGatewayAuthMode != "token" && openClawGatewayAuthMode != "password" && openClawGatewayAuthMode != "none" {
		return fmt.Errorf("invalid --openclaw-gateway-auth-mode %q: expected token, password, or none", openClawGatewayAuthMode)
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

	rawOpenClawConfig, err := loadOpenClawConfig(openClawConfigPath)
	if err != nil {
		return err
	}

	openClawConfig, err := buildOpenClawConfig(rawOpenClawConfig, openClawConfigOptions{
		AgentWorkspace:  openClawAgentWorkspace,
		ModelPrimary:    openClawModelPrimary,
		GatewayMode:     openClawGatewayMode,
		GatewayPort:     gatewayPort,
		GatewayAuthMode: openClawGatewayAuthMode,
	})
	if err != nil {
		return err
	}

	openClawEnv, err := parseOpenClawEnvFile(openClawEnvFile)
	if err != nil {
		return err
	}
	for key, value := range openClawEnvironment.Values {
		openClawEnv[key] = value
	}
	explicitEnv := map[string]string{
		"OPENCLAW_GATEWAY_TOKEN":       openClawGatewayToken,
		"OPENCLAW_GATEWAY_PASSWORD":    openClawGatewayPassword,
		"OPENAI_API_KEY":               openClawOpenAIAPIKey,
		"ANTHROPIC_API_KEY":            openClawAnthropicAPIKey,
		"GOOGLE_GENERATIVE_AI_API_KEY": openClawGoogleGenerativeAIAPIKey,
		"XAI_API_KEY":                  openClawXAIAPIKey,
		"OPENROUTER_API_KEY":           openClawOpenRouterAPIKey,
		"ZAI_API_KEY":                  openClawZAIAPIKey,
		"DISCORD_TOKEN":                openClawDiscordToken,
		"TELEGRAM_TOKEN":               openClawTelegramToken,
		"WHATSAPP_PHONE_NUMBER_ID":     openClawWhatsAppPhoneNumberID,
		"WHATSAPP_ACCESS_TOKEN":        openClawWhatsAppAccessToken,
		"WHATSAPP_VERIFY_TOKEN":        openClawWhatsAppVerifyToken,
		"WHATSAPP_APP_SECRET":          openClawWhatsAppAppSecret,
	}
	for key, value := range explicitEnv {
		if value != "" {
			openClawEnv[key] = value
		}
	}

	manager, err := a.imageManager()
	if err != nil {
		return err
	}

	runTarget, err := a.resolveRunTarget(flags.Arg(0))
	if err != nil {
		return err
	}
	if openClawModelPrimary == "" && runTarget.OpenClawModelPrimary != "" {
		openClawConfig, err = setOpenClawModelPrimary(openClawConfig, runTarget.OpenClawModelPrimary)
		if err != nil {
			return err
		}
	}
	if openClawGatewayAuthMode == "" && runTarget.OpenClawGatewayAuthMode != "" {
		openClawConfig, err = setOpenClawGatewayAuthMode(openClawConfig, runTarget.OpenClawGatewayAuthMode)
		if err != nil {
			return err
		}
	}

	ref := runTarget.ImageRef
	imageMeta, err := manager.Resolve(ref)
	if err != nil {
		if errors.Is(err, images.ErrImageNotFetched) {
			return fmt.Errorf("image %s is not ready, run `vclaw image fetch %s` first", ref, ref)
		}
		return err
	}

	openClawConfig, err = a.preflightOpenClawInputs(openClawConfig, openClawEnv)
	if err != nil {
		return err
	}

	store, instancesRoot, err := a.instanceStore()
	if err != nil {
		return err
	}
	mountManager, err := a.mountManager()
	if err != nil {
		return err
	}

	vmPublished := make([]vm.PortMapping, 0, len(published.Mappings))
	for _, mapping := range published.Mappings {
		vmPublished = append(vmPublished, vm.PortMapping{HostPort: mapping.HostPort, GuestPort: mapping.GuestPort})
	}

	id := runTarget.ClawID
	if id == "" {
		id, err = newClawID()
		if err != nil {
			return err
		}
	}
	instanceDir := filepath.Join(instancesRoot, id)
	statePath := filepath.Join(instanceDir, "state")
	instanceImagePath := filepath.Join(instanceDir, "instance.img")
	mountSource := imageMeta.RuntimeDisk
	if runTarget.MountSource != "" {
		mountSource = runTarget.MountSource
	}

	var startResult vm.StartResult
	var instance state.Instance
	err = mountManager.WithInstanceLock(id, func() error {
		existing, loadErr := store.Load(id)
		if loadErr != nil && !errors.Is(loadErr, state.ErrNotFound) {
			return loadErr
		}
		if loadErr == nil && existing.PID > 0 && a.backend.IsRunning(existing.PID) {
			return mount.ErrBusy
		}

		if err := ensureDir(statePath); err != nil {
			return err
		}
		if err := copyFile(imageMeta.RuntimeDisk, instanceImagePath); err != nil {
			return err
		}
		if err := mountManager.AcquireWhileLocked(context.Background(), mount.AcquireRequest{
			ClawID:     id,
			SourcePath: mountSource,
			InstanceID: id,
		}); err != nil {
			return err
		}

		startResult, err = a.backend.Start(context.Background(), vm.StartSpec{
			InstanceID:          id,
			InstanceDir:         instanceDir,
			ImageArch:           imageMeta.Arch,
			SourceDiskPath:      instanceImagePath,
			WorkspacePath:       workspacePath,
			StatePath:           statePath,
			GatewayHostPort:     gatewayPort,
			GatewayGuestPort:    gatewayPort,
			PublishedPorts:      vmPublished,
			CPUs:                cpus,
			MemoryMiB:           memoryMiB,
			OpenClawPackage:     openClawPackage,
			OpenClawConfig:      openClawConfig,
			OpenClawEnvironment: openClawEnv,
		})
		if err != nil {
			_ = mountManager.ReleaseWhileLocked(context.Background(), mount.ReleaseRequest{ClawID: id, Unmount: true})
			return err
		}
		if err := mountManager.AcquireWhileLocked(context.Background(), mount.AcquireRequest{
			ClawID:     id,
			InstanceID: id,
			PID:        startResult.PID,
		}); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			_ = a.backend.Stop(stopCtx, startResult.PID)
			_ = mountManager.ReleaseWhileLocked(context.Background(), mount.ReleaseRequest{ClawID: id, Unmount: true})
			return err
		}

		now := time.Now().UTC()
		instance = state.Instance{
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
			stopCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			_ = a.backend.Stop(stopCtx, startResult.PID)
			_ = mountManager.ReleaseWhileLocked(context.Background(), mount.ReleaseRequest{ClawID: id, Unmount: true})
			return err
		}
		return nil
	})
	if err != nil {
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
		instance.Status = "unhealthy"
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
	fmt.Fprintln(tw, "CLAWID\tIMAGE\tSTATUS\tGATEWAY\tPID\tUPDATED(UTC)\tLAST_ERROR")
	for _, instance := range instances {
		lastError := instance.LastError
		if lastError == "" {
			lastError = "-"
		} else {
			lastError = strings.ReplaceAll(lastError, "\n", " ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t127.0.0.1:%d\t%d\t%s\t%s\n", instance.ID, instance.ImageRef, instance.Status, instance.GatewayPort, instance.PID, instance.UpdatedAtUTC.Format(time.RFC3339), lastError)
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

	url := fmt.Sprintf("http://127.0.0.1:%d/", instance.GatewayPort)
	isHealthy, healthError := probeGatewayHealth(url, 300*time.Millisecond)
	if isHealthy {
		if instance.Status != "ready" || instance.LastError != "" {
			instance.Status = "ready"
			instance.LastError = ""
			changed = true
		}
		return instance, changed
	}

	shouldMarkUnhealthy := false
	if instance.Status == "ready" {
		shouldMarkUnhealthy = true
	}
	if (instance.Status == "booting" || instance.Status == "running") && (instance.LastError != "" || time.Since(instance.CreatedAtUTC) >= unhealthyGracePeriod) {
		shouldMarkUnhealthy = true
	}
	if instance.Status == "unhealthy" {
		shouldMarkUnhealthy = true
	}

	if shouldMarkUnhealthy {
		if instance.Status != "unhealthy" {
			instance.Status = "unhealthy"
			changed = true
		}
		if healthError == "" {
			healthError = "gateway is unreachable"
		}
		if instance.LastError != healthError {
			instance.LastError = healthError
			changed = true
		}
	}
	return instance, changed
}

func probeGatewayHealth(url string, timeout time.Duration) (bool, string) {
	client := &http.Client{Timeout: timeout}
	response, err := client.Get(url)
	if err != nil {
		return false, err.Error()
	}
	_ = response.Body.Close()

	if response.StatusCode >= 200 && response.StatusCode < 500 {
		return true, ""
	}
	return false, fmt.Sprintf("gateway returned HTTP %d", response.StatusCode)
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
	mountManager, err := a.mountManager()
	if err != nil {
		return err
	}

	id := args[0]
	err = mountManager.WithInstanceLock(id, func() error {
		instance, loadErr := store.Load(id)
		if loadErr != nil {
			if errors.Is(loadErr, state.ErrNotFound) {
				return fmt.Errorf("instance %s not found", id)
			}
			return loadErr
		}

		if instance.PID > 0 && a.backend.IsRunning(instance.PID) {
			stopCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			if err := a.backend.Stop(stopCtx, instance.PID); err != nil {
				return err
			}
		}
		if err := mountManager.ReleaseWhileLocked(context.Background(), mount.ReleaseRequest{ClawID: instance.ID, Unmount: true}); err != nil {
			return err
		}

		if err := store.Delete(id); err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return fmt.Errorf("instance %s not found", id)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(a.out, "removed %s\n", id)
	return nil
}

func (a *App) runExport(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: vclaw export <clawid> <output.clawbox>")
	}
	id := strings.TrimSpace(args[0])
	outputPath := strings.TrimSpace(args[1])
	if outputPath == "" {
		return errors.New("output path is required")
	}
	if !strings.HasSuffix(strings.ToLower(outputPath), ".clawbox") {
		return fmt.Errorf("output path %s must end with .clawbox", outputPath)
	}
	absOutputPath, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}

	store, _, err := a.instanceStore()
	if err != nil {
		return err
	}
	mountManager, err := a.mountManager()
	if err != nil {
		return err
	}

	err = mountManager.WithInstanceLock(id, func() error {
		if _, loadErr := store.Load(id); loadErr != nil {
			if errors.Is(loadErr, state.ErrNotFound) {
				return fmt.Errorf("instance %s not found", id)
			}
			return loadErr
		}

		mountState, inspectErr := mountManager.Inspect(id)
		if inspectErr != nil {
			return inspectErr
		}
		sourcePath := strings.TrimSpace(mountState.SourcePath)
		if sourcePath == "" {
			return fmt.Errorf("instance %s has no exportable clawbox source", id)
		}
		if !strings.HasSuffix(strings.ToLower(sourcePath), ".clawbox") {
			return fmt.Errorf("instance %s is not clawbox-backed (source: %s)", id, sourcePath)
		}

		absSourcePath, absErr := filepath.Abs(sourcePath)
		if absErr != nil {
			return absErr
		}
		if absSourcePath == absOutputPath {
			return errors.New("output path must be different from source clawbox path")
		}

		return copyFile(absSourcePath, absOutputPath)
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(a.out, "exported %s -> %s\n", id, absOutputPath)
	return nil
}

func (a *App) runCheckpoint(args []string) error {
	args = normalizeRunArgs(args)

	flags := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	flags.SetOutput(a.errOut)

	checkpointName := ""
	flags.StringVar(&checkpointName, "name", "", "checkpoint name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: vclaw checkpoint <clawid> --name <name>")
	}
	id := strings.TrimSpace(flags.Arg(0))
	checkpointName = strings.TrimSpace(checkpointName)
	if err := validateCheckpointName(checkpointName); err != nil {
		return err
	}

	store, instancesRoot, err := a.instanceStore()
	if err != nil {
		return err
	}
	mountManager, err := a.mountManager()
	if err != nil {
		return err
	}
	checkpointPath := checkpointPathForName(instancesRoot, id, checkpointName)

	err = mountManager.WithInstanceLock(id, func() error {
		instance, loadErr := store.Load(id)
		if loadErr != nil {
			if errors.Is(loadErr, state.ErrNotFound) {
				return fmt.Errorf("instance %s not found", id)
			}
			return loadErr
		}
		if strings.TrimSpace(instance.DiskPath) == "" {
			return fmt.Errorf("instance %s has no disk path", id)
		}

		suspended := false
		if instance.PID > 0 && a.backend.IsRunning(instance.PID) {
			if err := a.backend.Suspend(instance.PID); err != nil {
				return err
			}
			suspended = true
		}

		if err := copyFile(instance.DiskPath, checkpointPath); err != nil {
			if suspended {
				if resumeErr := a.backend.Resume(instance.PID); resumeErr != nil {
					return fmt.Errorf("%w (and failed to resume VM: %v)", err, resumeErr)
				}
			}
			return err
		}

		if suspended {
			if err := a.backend.Resume(instance.PID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(a.out, "checkpointed %s -> %s\n", id, checkpointPath)
	return nil
}

func (a *App) runRestore(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: vclaw restore <clawid> <checkpoint>")
	}
	id := strings.TrimSpace(args[0])
	checkpointName := strings.TrimSpace(args[1])
	if err := validateCheckpointName(checkpointName); err != nil {
		return err
	}

	store, instancesRoot, err := a.instanceStore()
	if err != nil {
		return err
	}
	mountManager, err := a.mountManager()
	if err != nil {
		return err
	}
	checkpointPath := checkpointPathForName(instancesRoot, id, checkpointName)

	err = mountManager.WithInstanceLock(id, func() error {
		instance, loadErr := store.Load(id)
		if loadErr != nil {
			if errors.Is(loadErr, state.ErrNotFound) {
				return fmt.Errorf("instance %s not found", id)
			}
			return loadErr
		}
		if strings.TrimSpace(instance.DiskPath) == "" {
			return fmt.Errorf("instance %s has no disk path", id)
		}
		if _, statErr := os.Stat(checkpointPath); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return fmt.Errorf("checkpoint %s not found for %s", checkpointName, id)
			}
			return statErr
		}

		suspended := false
		if instance.PID > 0 && a.backend.IsRunning(instance.PID) {
			if err := a.backend.Suspend(instance.PID); err != nil {
				return err
			}
			suspended = true
		}

		if err := copyFile(checkpointPath, instance.DiskPath); err != nil {
			if suspended {
				if resumeErr := a.backend.Resume(instance.PID); resumeErr != nil {
					return fmt.Errorf("%w (and failed to resume VM: %v)", err, resumeErr)
				}
			}
			return err
		}

		if suspended {
			if err := a.backend.Resume(instance.PID); err != nil {
				return err
			}
		}

		instance.UpdatedAtUTC = time.Now().UTC()
		return store.Save(instance)
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(a.out, "restored %s from %s\n", id, checkpointPath)
	return nil
}

func validateCheckpointName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("checkpoint name is required")
	}
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return fmt.Errorf("invalid checkpoint name %q", name)
	}
	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("invalid checkpoint name %q", name)
	}
	return nil
}

func checkpointPathForName(instancesRoot string, id string, checkpointName string) string {
	fileName := checkpointName
	if !strings.HasSuffix(strings.ToLower(fileName), ".qcow2") {
		fileName += ".qcow2"
	}
	return filepath.Join(instancesRoot, id, "checkpoints", fileName)
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

func (a *App) mountManager() (*mount.Manager, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	clawsRoot := filepath.Join(dataDir, "claws")
	if err := ensureDir(clawsRoot); err != nil {
		return nil, err
	}
	return mount.NewManager(clawsRoot, nil, nil), nil
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
	fmt.Fprintln(a.out, "  vclaw run <ref|file.clawbox|.> [--workspace=. --port=18789 --publish host:guest]")
	fmt.Fprintln(a.out, "             [--openclaw-config path --openclaw-agent-workspace /workspace --openclaw-model-primary openai/gpt-5]")
	fmt.Fprintln(a.out, "             [--openclaw-gateway-mode local --openclaw-gateway-auth-mode token --openclaw-gateway-token xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-openai-api-key xxx --openclaw-anthropic-api-key xxx --openclaw-openrouter-api-key xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-google-generative-ai-api-key xxx --openclaw-xai-api-key xxx --openclaw-zai-api-key xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-discord-token xxx --openclaw-telegram-token xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-whatsapp-phone-number-id xxx --openclaw-whatsapp-access-token xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-whatsapp-verify-token xxx --openclaw-whatsapp-app-secret xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-env-file path --openclaw-env KEY=VALUE]")
	fmt.Fprintln(a.out, "  vclaw ps")
	fmt.Fprintln(a.out, "  vclaw suspend <clawid>")
	fmt.Fprintln(a.out, "  vclaw resume <clawid>")
	fmt.Fprintln(a.out, "  vclaw rm <clawid>")
	fmt.Fprintln(a.out, "  vclaw export <clawid> <output.clawbox>")
	fmt.Fprintln(a.out, "  vclaw checkpoint <clawid> --name <name>")
	fmt.Fprintln(a.out, "  vclaw restore <clawid> <checkpoint>")
	fmt.Fprintln(a.out, "")
	fmt.Fprintln(a.out, "Examples:")
	fmt.Fprintln(a.out, "  vclaw image fetch ubuntu:24.04")
	fmt.Fprintln(a.out, "  vclaw run ubuntu:24.04 --workspace=. --publish 8080:80")
	fmt.Fprintln(a.out, "  vclaw run ubuntu:24.04 --openclaw-openai-api-key $OPENAI_API_KEY --openclaw-discord-token $DISCORD_TOKEN")
	fmt.Fprintln(a.out, "  vclaw checkpoint claw-1234 --name before-upgrade")
	fmt.Fprintln(a.out, "  vclaw restore claw-1234 before-upgrade")
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

type envVarList struct {
	Values map[string]string
}

func (l *envVarList) String() string {
	return ""
}

func (l *envVarList) Set(value string) error {
	key, parsedValue, err := parseEnvAssignment(value)
	if err != nil {
		return err
	}
	if l.Values == nil {
		l.Values = map[string]string{}
	}
	l.Values[key] = parsedValue
	return nil
}

type openClawConfigOptions struct {
	AgentWorkspace  string
	ModelPrimary    string
	GatewayMode     string
	GatewayPort     int
	GatewayAuthMode string
}

func loadOpenClawConfig(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read --openclaw-config %s: %w", path, err)
	}
	if strings.TrimSpace(string(contents)) == "" {
		return "", fmt.Errorf("--openclaw-config %s is empty", path)
	}
	return string(contents), nil
}

func buildOpenClawConfig(baseConfig string, options openClawConfigOptions) (string, error) {
	config := map[string]interface{}{}
	if strings.TrimSpace(baseConfig) != "" {
		if err := json.Unmarshal([]byte(baseConfig), &config); err != nil {
			return "", fmt.Errorf("parse --openclaw-config JSON: %w", err)
		}
	}

	agents := ensureMapValue(config, "agents")
	defaults := ensureMapValue(agents, "defaults")
	workspace := options.AgentWorkspace
	if strings.TrimSpace(workspace) == "" {
		workspace = "/workspace"
	}
	defaults["workspace"] = workspace
	if strings.TrimSpace(options.ModelPrimary) != "" {
		model := ensureMapValue(defaults, "model")
		model["primary"] = options.ModelPrimary
	}

	gateway := ensureMapValue(config, "gateway")
	if options.GatewayPort > 0 {
		gateway["port"] = options.GatewayPort
	}
	if _, exists := gateway["mode"]; !exists {
		gateway["mode"] = "local"
	}
	if strings.TrimSpace(options.GatewayMode) != "" {
		gateway["mode"] = options.GatewayMode
	}
	if strings.TrimSpace(options.GatewayAuthMode) != "" {
		auth := ensureMapValue(gateway, "auth")
		auth["mode"] = options.GatewayAuthMode
	}

	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func ensureMapValue(root map[string]interface{}, key string) map[string]interface{} {
	value, exists := root[key]
	if !exists {
		next := map[string]interface{}{}
		root[key] = next
		return next
	}
	mapValue, ok := value.(map[string]interface{})
	if ok {
		return mapValue
	}
	next := map[string]interface{}{}
	root[key] = next
	return next
}

type openClawRuntimeRequirements struct {
	ModelPrimary    string
	GatewayAuthMode string
}

func (a *App) preflightOpenClawInputs(openClawConfig string, openClawEnv map[string]string) (string, error) {
	requirements, err := parseOpenClawRuntimeRequirements(openClawConfig)
	if err != nil {
		return "", err
	}

	canPrompt := a.canPromptForInput()
	promptFile := a.promptInputFile()
	var reader *bufio.Reader
	if canPrompt {
		reader = bufio.NewReader(a.in)
	}

	modelPrimary := strings.TrimSpace(requirements.ModelPrimary)
	if modelPrimary == "" {
		modelPrimary, err = a.resolveRequiredInput(reader, canPrompt, promptFile,
			"OpenClaw primary model (provider/model, e.g. openai/gpt-5)",
			"--openclaw-model-primary",
			"",
			false)
		if err != nil {
			return "", err
		}
		openClawConfig, err = setOpenClawModelPrimary(openClawConfig, modelPrimary)
		if err != nil {
			return "", err
		}
	}

	providerEnvKey, providerLabel, err := providerEnvRequirementForModel(modelPrimary)
	if err != nil {
		return "", err
	}
	if providerEnvKey != "" && strings.TrimSpace(openClawEnv[providerEnvKey]) == "" {
		flagHint := requiredFlagForEnvKey(providerEnvKey)
		value, resolveErr := a.resolveRequiredInput(reader, canPrompt, promptFile,
			fmt.Sprintf("%s for model %s", providerLabel, modelPrimary),
			flagHint,
			providerEnvKey,
			true)
		if resolveErr != nil {
			return "", resolveErr
		}
		openClawEnv[providerEnvKey] = value
	}

	switch strings.ToLower(strings.TrimSpace(requirements.GatewayAuthMode)) {
	case "", "none":
	case "token":
		if strings.TrimSpace(openClawEnv["OPENCLAW_GATEWAY_TOKEN"]) == "" {
			value, resolveErr := a.resolveRequiredInput(reader, canPrompt, promptFile,
				"OpenClaw gateway token",
				"--openclaw-gateway-token",
				"OPENCLAW_GATEWAY_TOKEN",
				true)
			if resolveErr != nil {
				return "", resolveErr
			}
			openClawEnv["OPENCLAW_GATEWAY_TOKEN"] = value
		}
	case "password":
		if strings.TrimSpace(openClawEnv["OPENCLAW_GATEWAY_PASSWORD"]) == "" {
			value, resolveErr := a.resolveRequiredInput(reader, canPrompt, promptFile,
				"OpenClaw gateway password",
				"--openclaw-gateway-password",
				"OPENCLAW_GATEWAY_PASSWORD",
				true)
			if resolveErr != nil {
				return "", resolveErr
			}
			openClawEnv["OPENCLAW_GATEWAY_PASSWORD"] = value
		}
	default:
		return "", fmt.Errorf("invalid gateway.auth.mode %q in OpenClaw config: expected token, password, or none", requirements.GatewayAuthMode)
	}

	whatsAppRequired := []struct {
		envKey   string
		flagName string
		label    string
	}{
		{envKey: "WHATSAPP_PHONE_NUMBER_ID", flagName: "--openclaw-whatsapp-phone-number-id", label: "WhatsApp phone number id"},
		{envKey: "WHATSAPP_ACCESS_TOKEN", flagName: "--openclaw-whatsapp-access-token", label: "WhatsApp access token"},
		{envKey: "WHATSAPP_VERIFY_TOKEN", flagName: "--openclaw-whatsapp-verify-token", label: "WhatsApp verify token"},
		{envKey: "WHATSAPP_APP_SECRET", flagName: "--openclaw-whatsapp-app-secret", label: "WhatsApp app secret"},
	}

	presentCount := 0
	for _, item := range whatsAppRequired {
		if strings.TrimSpace(openClawEnv[item.envKey]) != "" {
			presentCount++
		}
	}
	if presentCount > 0 && presentCount < len(whatsAppRequired) {
		for _, item := range whatsAppRequired {
			if strings.TrimSpace(openClawEnv[item.envKey]) != "" {
				continue
			}
			value, resolveErr := a.resolveRequiredInput(reader, canPrompt, promptFile, item.label, item.flagName, item.envKey, isSecretOpenClawEnvKey(item.envKey))
			if resolveErr != nil {
				return "", resolveErr
			}
			openClawEnv[item.envKey] = value
		}
	}

	return openClawConfig, nil
}

func (a *App) resolveRequiredInput(reader *bufio.Reader, canPrompt bool, promptFile *os.File, label string, flagName string, envKey string, secret bool) (string, error) {
	if !canPrompt || reader == nil {
		if envKey != "" {
			return "", fmt.Errorf("missing required OpenClaw parameter: %s (set %s or --openclaw-env %s=...)", label, flagName, envKey)
		}
		return "", fmt.Errorf("missing required OpenClaw parameter: %s (set %s)", label, flagName)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		fmt.Fprintf(a.out, "openclaw> %s: ", label)
		value, err := a.readPromptValue(reader, promptFile, secret)
		if err != nil {
			return "", err
		}
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed, nil
		}
		fmt.Fprintf(a.errOut, "invalid value: %s cannot be empty\n", label)
	}

	if envKey != "" {
		return "", fmt.Errorf("missing required OpenClaw parameter after 3 attempts: %s (set %s or --openclaw-env %s=...)", label, flagName, envKey)
	}
	return "", fmt.Errorf("missing required OpenClaw parameter after 3 attempts: %s (set %s)", label, flagName)
}

func (a *App) readPromptValue(reader *bufio.Reader, promptFile *os.File, secret bool) (string, error) {
	if secret && promptFile != nil {
		return readMaskedTTYInput(promptFile, a.out)
	}

	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read interactive input: %w", err)
	}
	value := strings.TrimSpace(line)
	if secret && value != "" {
		fmt.Fprintln(a.out, strings.Repeat("*", len(value)))
	}
	return value, nil
}

func readMaskedTTYInput(file *os.File, out io.Writer) (string, error) {
	state, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return "", fmt.Errorf("prepare masked input: %w", err)
	}
	defer term.Restore(int(file.Fd()), state)

	buffer := make([]byte, 0, 64)
	oneByte := make([]byte, 1)
	for {
		count, readErr := file.Read(oneByte)
		if readErr != nil {
			return "", fmt.Errorf("read interactive input: %w", readErr)
		}
		if count == 0 {
			continue
		}

		char := oneByte[0]
		switch char {
		case '\r', '\n':
			fmt.Fprintln(out)
			return string(buffer), nil
		case 3:
			fmt.Fprintln(out)
			return "", errors.New("input canceled")
		case 8, 127:
			if len(buffer) > 0 {
				buffer = buffer[:len(buffer)-1]
				fmt.Fprint(out, "\b \b")
			}
		default:
			if char < 32 {
				continue
			}
			buffer = append(buffer, char)
			fmt.Fprint(out, "*")
		}
	}
}

func (a *App) canPromptForInput() bool {
	if a.in == nil {
		return false
	}
	if file, ok := a.in.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return false
		}
		return info.Mode()&os.ModeCharDevice != 0
	}
	return true
}

func (a *App) promptInputFile() *os.File {
	if a.in == nil {
		return nil
	}
	file, ok := a.in.(*os.File)
	if !ok {
		return nil
	}
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	return file
}

func isSecretOpenClawEnvKey(envKey string) bool {
	upper := strings.ToUpper(strings.TrimSpace(envKey))
	return strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "SECRET")
}
func parseOpenClawRuntimeRequirements(configPayload string) (openClawRuntimeRequirements, error) {
	requirements := openClawRuntimeRequirements{}
	if strings.TrimSpace(configPayload) == "" {
		return requirements, nil
	}

	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(configPayload), &config); err != nil {
		return requirements, fmt.Errorf("parse generated OpenClaw config JSON: %w", err)
	}
	requirements.ModelPrimary = lookupNestedString(config, "agents", "defaults", "model", "primary")
	requirements.GatewayAuthMode = lookupNestedString(config, "gateway", "auth", "mode")
	return requirements, nil
}

func setOpenClawModelPrimary(configPayload string, modelPrimary string) (string, error) {
	config := map[string]interface{}{}
	if strings.TrimSpace(configPayload) != "" {
		if err := json.Unmarshal([]byte(configPayload), &config); err != nil {
			return "", fmt.Errorf("parse generated OpenClaw config JSON: %w", err)
		}
	}

	agents := ensureMapValue(config, "agents")
	defaults := ensureMapValue(agents, "defaults")
	model := ensureMapValue(defaults, "model")
	model["primary"] = modelPrimary

	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func setOpenClawGatewayAuthMode(configPayload string, authMode string) (string, error) {
	config := map[string]interface{}{}
	if strings.TrimSpace(configPayload) != "" {
		if err := json.Unmarshal([]byte(configPayload), &config); err != nil {
			return "", fmt.Errorf("parse generated OpenClaw config JSON: %w", err)
		}
	}

	gateway := ensureMapValue(config, "gateway")
	auth := ensureMapValue(gateway, "auth")
	auth["mode"] = authMode

	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func providerEnvRequirementForModel(modelPrimary string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(modelPrimary), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid --openclaw-model-primary %q: expected provider/model", modelPrimary)
	}

	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "openai":
		return "OPENAI_API_KEY", "OpenAI API key", nil
	case "anthropic":
		return "ANTHROPIC_API_KEY", "Anthropic API key", nil
	case "gemini":
		return "GOOGLE_GENERATIVE_AI_API_KEY", "Google Generative AI API key", nil
	case "grok", "xai":
		return "XAI_API_KEY", "xAI API key", nil
	case "openrouter":
		return "OPENROUTER_API_KEY", "OpenRouter API key", nil
	case "zai":
		return "ZAI_API_KEY", "Z.AI API key", nil
	case "ollama", "lmstudio":
		return "", "", nil
	default:
		return "", "", fmt.Errorf("unsupported model provider %q in --openclaw-model-primary %q; supported providers: openai, anthropic, gemini, grok, xai, openrouter, zai, ollama, lmstudio", parts[0], modelPrimary)
	}
}

func requiredFlagForEnvKey(envKey string) string {
	switch envKey {
	case "OPENAI_API_KEY":
		return "--openclaw-openai-api-key"
	case "ANTHROPIC_API_KEY":
		return "--openclaw-anthropic-api-key"
	case "GOOGLE_GENERATIVE_AI_API_KEY":
		return "--openclaw-google-generative-ai-api-key"
	case "XAI_API_KEY":
		return "--openclaw-xai-api-key"
	case "OPENROUTER_API_KEY":
		return "--openclaw-openrouter-api-key"
	case "ZAI_API_KEY":
		return "--openclaw-zai-api-key"
	case "OPENCLAW_GATEWAY_TOKEN":
		return "--openclaw-gateway-token"
	case "OPENCLAW_GATEWAY_PASSWORD":
		return "--openclaw-gateway-password"
	case "DISCORD_TOKEN":
		return "--openclaw-discord-token"
	case "TELEGRAM_TOKEN":
		return "--openclaw-telegram-token"
	case "WHATSAPP_PHONE_NUMBER_ID":
		return "--openclaw-whatsapp-phone-number-id"
	case "WHATSAPP_ACCESS_TOKEN":
		return "--openclaw-whatsapp-access-token"
	case "WHATSAPP_VERIFY_TOKEN":
		return "--openclaw-whatsapp-verify-token"
	case "WHATSAPP_APP_SECRET":
		return "--openclaw-whatsapp-app-secret"
	default:
		return "--openclaw-env"
	}
}

func lookupNestedString(root map[string]interface{}, keys ...string) string {
	current := interface{}(root)
	for _, key := range keys {
		nextMap, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		next, exists := nextMap[key]
		if !exists {
			return ""
		}
		current = next
	}
	stringValue, ok := current.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(stringValue)
}

func parseOpenClawEnvFile(path string) (map[string]string, error) {
	result := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return result, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read --openclaw-env-file %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, parseErr := parseEnvAssignment(line)
		if parseErr != nil {
			return nil, fmt.Errorf("parse --openclaw-env-file %s line %d: %w", path, lineNumber, parseErr)
		}
		result[key] = stripMatchingQuotes(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read --openclaw-env-file %s: %w", path, err)
	}
	return result, nil
}

func parseEnvAssignment(input string) (string, string, error) {
	parts := strings.SplitN(input, "=", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid env assignment %q: expected KEY=VALUE", input)
	}

	key := strings.TrimSpace(parts[0])
	if !isValidEnvKey(key) {
		return "", "", fmt.Errorf("invalid env key %q", key)
	}
	return key, parts[1], nil
}

func isValidEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for index, char := range key {
		if index == 0 {
			if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_') {
				return false
			}
			continue
		}
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_') {
			return false
		}
	}
	return true
}

func stripMatchingQuotes(value string) string {
	if len(value) < 2 {
		return value
	}
	if (value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"') {
		return value[1 : len(value)-1]
	}
	return value
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
