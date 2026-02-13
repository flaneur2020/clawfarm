package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/yazhou/krunclaw/internal/clawbox"
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
	unhealthyGracePeriod    = 30 * time.Second
)

var exportSecretScanPatterns = []struct {
	label string
	re    *regexp.Regexp
}{
	{label: "openai_sk_token", re: regexp.MustCompile(`(?i)\bsk-[a-z0-9_-]{16,}\b`)},
	{label: "github_pat", re: regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`)},
	{label: "slack_token", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{label: "api_key_assignment", re: regexp.MustCompile(`(?i)["']?(api[_-]?key|access[_-]?token|refresh[_-]?token|secret|password)["']?\s*[:=]\s*["'][^"'\s]{8,}["']?`)},
}

var runNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

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
	case "new":
		return a.runNew(args[1:])
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
		return errors.New("usage: clawfarm image <ls|fetch>")
	}

	manager, err := a.imageManager()
	if err != nil {
		return err
	}

	switch args[0] {
	case "ls":
		if len(args) != 1 {
			return errors.New("usage: clawfarm image ls")
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
			return errors.New("usage: clawfarm image fetch <ref>")
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
	ClawboxV2Mode           bool
	ClawboxPath             string
	ClawboxV2Spec           *runClawboxSpecV2
	SpecJSONMode            bool
	SkipMount               bool
	SpecBaseImageURL        string
	SpecBaseImageSHA256     string
	SpecLayerArtifacts      []runArtifact
	SpecProvisionCommands   []string
	OpenClawModelPrimary    string
	OpenClawGatewayAuthMode string
	OpenClawRequiredEnv     []string
	IsClawbox               bool
}

type runArtifact struct {
	Label  string
	URL    string
	SHA256 string
}

type runSpecJSONEnvelope struct {
	Name      string          `json:"name,omitempty"`
	Spec      runSpecJSONBody `json:"spec"`
	Provision []string        `json:"provision,omitempty"`
}

type runSpecJSONBody struct {
	Name      string               `json:"name,omitempty"`
	BaseImage clawbox.BaseImage    `json:"base_image"`
	Layers    []clawbox.Layer      `json:"layers,omitempty"`
	OpenClaw  clawbox.OpenClawSpec `json:"openclaw"`
	Provision []string             `json:"provision,omitempty"`
}

type preparedRunTarget struct {
	ImageMeta         images.Metadata
	MountSource       string
	LayerPaths        []string
	ProvisionCommands []string
}

func (a *App) resolveRunTarget(input string) (runTarget, error) {
	if !isClawboxRunInput(input) {
		return runTarget{Input: input, ImageRef: input}, nil
	}

	clawboxPath, err := resolveClawboxPath(input)
	if err != nil {
		return runTarget{}, err
	}

	startsJSON, err := fileStartsWithJSONObject(clawboxPath)
	if err != nil {
		return runTarget{}, err
	}

	if startsJSON {
		body, err := os.ReadFile(clawboxPath)
		if err != nil {
			return runTarget{}, err
		}

		header, headerErr := clawbox.ParseHeaderJSON(body)
		if headerErr == nil {
			clawID, clawIDErr := header.ClawID(clawboxPath)
			if clawIDErr != nil {
				return runTarget{}, fmt.Errorf("compute CLAWID for %s: %w", clawboxPath, clawIDErr)
			}

			return runTarget{
				Input:                   input,
				ImageRef:                strings.TrimSpace(header.Spec.BaseImage.Ref),
				ClawID:                  clawID,
				MountSource:             clawboxPath,
				OpenClawModelPrimary:    strings.TrimSpace(header.Spec.OpenClaw.ModelPrimary),
				OpenClawGatewayAuthMode: strings.TrimSpace(header.Spec.OpenClaw.GatewayAuthMode),
				OpenClawRequiredEnv:     append([]string(nil), header.Spec.OpenClaw.RequiredEnv...),
				IsClawbox:               true,
			}, nil
		}

		target, specErr := resolveRunTargetFromSpecJSON(input, clawboxPath, body)
		if specErr == nil {
			return target, nil
		}

		return runTarget{}, fmt.Errorf("parse clawbox %s: %v; spec-json parse: %w", clawboxPath, headerErr, specErr)
	}

	target, tarErr := resolveRunTargetFromTarClawbox(input, clawboxPath)
	if tarErr == nil {
		return target, nil
	}

	return runTarget{}, fmt.Errorf("parse clawbox %s as tar.gz: %w", clawboxPath, tarErr)
}

func fileStartsWithJSONObject(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	buffer := make([]byte, 1)
	for {
		count, readErr := file.Read(buffer)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return false, nil
			}
			return false, readErr
		}
		if count == 0 {
			continue
		}
		value := buffer[0]
		if value == ' ' || value == '\n' || value == '\r' || value == '\t' {
			continue
		}
		return value == '{', nil
	}
}

func resolveRunTargetFromSpecJSON(input string, clawboxPath string, body []byte) (runTarget, error) {
	var envelope runSpecJSONEnvelope
	if decodeErr := decodeJSONStrict(body, &envelope); decodeErr == nil && strings.TrimSpace(envelope.Spec.BaseImage.Ref) != "" {
		provision := append([]string(nil), envelope.Provision...)
		provision = append(provision, envelope.Spec.Provision...)
		return buildRunTargetFromSpecJSON(input, clawboxPath, envelope.Name, envelope.Spec, provision)
	}

	var direct runSpecJSONBody
	if decodeErr := decodeJSONStrict(body, &direct); decodeErr == nil {
		if strings.TrimSpace(direct.BaseImage.Ref) == "" {
			return runTarget{}, errors.New("spec-json missing base_image.ref")
		}
		return buildRunTargetFromSpecJSON(input, clawboxPath, direct.Name, direct, direct.Provision)
	}

	return runTarget{}, errors.New("expected JSON clawbox header or JSON clawbox spec")
}

func buildRunTargetFromSpecJSON(input string, clawboxPath string, name string, spec runSpecJSONBody, provision []string) (runTarget, error) {
	runtimeSpec := clawbox.RuntimeSpec{
		BaseImage: spec.BaseImage,
		Layers:    append([]clawbox.Layer(nil), spec.Layers...),
		OpenClaw:  spec.OpenClaw,
	}
	resolvedName := resolveSpecJSONName(name, clawboxPath)
	if err := validateRunSpecJSON(resolvedName, runtimeSpec); err != nil {
		return runTarget{}, fmt.Errorf("invalid JSON clawbox spec: %w", err)
	}

	clawID, err := clawbox.ComputeClawID(clawboxPath, resolvedName)
	if err != nil {
		return runTarget{}, fmt.Errorf("compute CLAWID for %s: %w", clawboxPath, err)
	}

	layerArtifacts := make([]runArtifact, 0, len(spec.Layers))
	for index, layer := range spec.Layers {
		layerArtifacts = append(layerArtifacts, runArtifact{
			Label:  fmt.Sprintf("layer-%d", index+1),
			URL:    strings.TrimSpace(layer.URL),
			SHA256: strings.TrimSpace(layer.SHA256),
		})
	}

	return runTarget{
		Input:                   input,
		ImageRef:                strings.TrimSpace(spec.BaseImage.Ref),
		ClawID:                  clawID,
		SpecJSONMode:            true,
		SkipMount:               true,
		SpecBaseImageURL:        strings.TrimSpace(spec.BaseImage.URL),
		SpecBaseImageSHA256:     strings.TrimSpace(spec.BaseImage.SHA256),
		SpecLayerArtifacts:      layerArtifacts,
		SpecProvisionCommands:   normalizeProvisionCommands(provision),
		OpenClawModelPrimary:    strings.TrimSpace(spec.OpenClaw.ModelPrimary),
		OpenClawGatewayAuthMode: strings.TrimSpace(spec.OpenClaw.GatewayAuthMode),
		OpenClawRequiredEnv:     append([]string(nil), spec.OpenClaw.RequiredEnv...),
		IsClawbox:               false,
	}, nil
}

func decodeJSONStrict(data []byte, target interface{}) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func resolveSpecJSONName(name string, path string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed != "" {
		return strings.ToLower(trimmed)
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.ToLower(base)
	base = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if len(base) < 3 {
		return "clawbox-spec"
	}
	if len(base) > 63 {
		base = base[:63]
	}
	if base[0] < 'a' || base[0] > 'z' {
		base = "claw" + base
		if len(base) > 63 {
			base = base[:63]
		}
	}
	return base
}

func validateRunSpecJSON(name string, spec clawbox.RuntimeSpec) error {
	header := clawbox.Header{
		SchemaVersion: clawbox.SchemaVersionV1,
		Name:          name,
		CreatedAtUTC:  time.Now().UTC(),
		Payload: clawbox.Payload{
			FSType: "squashfs",
			Offset: 4096,
			Size:   1,
			SHA256: strings.Repeat("0", 64),
		},
		Spec: spec,
	}
	return header.Validate()
}

func normalizeProvisionCommands(commands []string) []string {
	result := make([]string, 0, len(commands))
	for _, command := range commands {
		trimmed := strings.TrimSpace(command)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

func (a *App) prepareRunTarget(ctx context.Context, manager *images.Manager, target runTarget) (preparedRunTarget, error) {
	if target.ClawboxV2Mode && target.ClawboxV2Spec != nil {
		if _, hasRunImage := target.ClawboxV2Spec.runImage(); hasRunImage {
			now := time.Now().UTC()
			return preparedRunTarget{
				ImageMeta: images.Metadata{
					Ref:          target.ImageRef,
					Arch:         detectImageArch(target.ImageRef),
					Ready:        true,
					DiskFormat:   "qcow2",
					FetchedAtUTC: now,
					UpdatedAtUTC: now,
				},
			}, nil
		}
	}

	if !target.SpecJSONMode {
		imageMeta, err := manager.Resolve(target.ImageRef)
		if err != nil {
			return preparedRunTarget{}, err
		}

		mountSource := imageMeta.RuntimeDisk
		if target.MountSource != "" {
			mountSource = target.MountSource
		}

		return preparedRunTarget{
			ImageMeta:   imageMeta,
			MountSource: mountSource,
		}, nil
	}

	blobsRoot, err := clawfarmBlobsRoot()
	if err != nil {
		return preparedRunTarget{}, err
	}
	if err := ensureDir(blobsRoot); err != nil {
		return preparedRunTarget{}, err
	}

	baseArtifact := runArtifact{
		Label:  "base",
		URL:    strings.TrimSpace(target.SpecBaseImageURL),
		SHA256: strings.TrimSpace(target.SpecBaseImageSHA256),
	}
	basePath, err := ensureSpecArtifact(ctx, blobsRoot, baseArtifact, a.out)
	if err != nil {
		return preparedRunTarget{}, err
	}

	layerPaths := make([]string, 0, len(target.SpecLayerArtifacts))
	for _, layer := range target.SpecLayerArtifacts {
		layerPath, layerErr := ensureSpecArtifact(ctx, blobsRoot, layer, a.out)
		if layerErr != nil {
			return preparedRunTarget{}, layerErr
		}
		layerPaths = append(layerPaths, layerPath)
	}

	imageMeta := images.Metadata{
		Ref:         target.ImageRef,
		Arch:        detectImageArch(target.ImageRef),
		RuntimeDisk: basePath,
		Ready:       true,
		DiskFormat:  detectDiskFormatForPath(basePath),
	}

	now := time.Now().UTC()
	imageMeta.FetchedAtUTC = now
	imageMeta.UpdatedAtUTC = now

	return preparedRunTarget{
		ImageMeta:         imageMeta,
		LayerPaths:        layerPaths,
		ProvisionCommands: append([]string(nil), target.SpecProvisionCommands...),
	}, nil
}

func ensureSpecArtifact(ctx context.Context, root string, artifact runArtifact, out io.Writer) (string, error) {
	label := strings.TrimSpace(artifact.Label)
	if label == "" {
		label = "artifact"
	}

	rawURL := strings.TrimSpace(artifact.URL)
	if rawURL == "" {
		return "", fmt.Errorf("%s.url is required", label)
	}
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return "", fmt.Errorf("invalid %s.url %q: %w", label, rawURL, err)
	}

	expectedSHA := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if matched, _ := regexp.MatchString(`^[a-f0-9]{64}$`, expectedSHA); !matched {
		return "", fmt.Errorf("invalid %s.sha256 %q: expected lowercase 64-char hex", label, artifact.SHA256)
	}

	artifactPath := filepath.Join(root, expectedSHA)
	tempPath := artifactPath + ".tmp.download"
	_ = os.Remove(tempPath)
	if fileExistsAndNonEmpty(artifactPath) {
		if err := verifyFileSHA256(artifactPath, expectedSHA); err == nil {
			if out != nil {
				fmt.Fprintf(out, "using cached %s %s\n", label, artifactPath)
			}
			return artifactPath, nil
		}
		_ = os.Remove(artifactPath)
	}

	if err := downloadFileWithProgress(ctx, rawURL, tempPath, out, label); err != nil {
		return "", fmt.Errorf("download %s: %w", label, err)
	}
	if err := verifyFileSHA256(tempPath, expectedSHA); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := os.Rename(tempPath, artifactPath); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}

	return artifactPath, nil
}

func downloadFileWithProgress(ctx context.Context, rawURL string, destination string, out io.Writer, label string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("request failed with status %s", response.Status)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}

	file, err := os.Create(destination)
	if err != nil {
		return err
	}

	cleanup := func() {
		file.Close()
		_ = os.Remove(destination)
	}

	if out == nil {
		if _, err := io.Copy(file, response.Body); err != nil {
			cleanup()
			return err
		}
	} else {
		buffer := make([]byte, 1024*1024)
		total := response.ContentLength
		var downloaded int64
		lastRender := time.Time{}
		render := func(force bool) {
			if !force && !lastRender.IsZero() && time.Since(lastRender) < 120*time.Millisecond {
				return
			}
			lastRender = time.Now()
			renderDownloadProgress(out, label, downloaded, total)
		}

		for {
			readBytes, readErr := response.Body.Read(buffer)
			if readBytes > 0 {
				writtenBytes, writeErr := file.Write(buffer[:readBytes])
				if writeErr != nil {
					cleanup()
					return writeErr
				}
				if writtenBytes != readBytes {
					cleanup()
					return io.ErrShortWrite
				}
				downloaded += int64(readBytes)
				render(false)
			}

			if readErr == io.EOF {
				render(true)
				fmt.Fprintln(out)
				break
			}
			if readErr != nil {
				cleanup()
				return readErr
			}
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(destination)
		return err
	}

	return nil
}

func clawfarmBlobsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clawfarm", "blobs"), nil
}

func renderDownloadProgress(out io.Writer, label string, downloaded int64, total int64) {
	if total > 0 {
		percent := float64(downloaded) / float64(total) * 100
		if percent > 100 {
			percent = 100
		}
		barWidth := 28
		filled := int(float64(downloaded) / float64(total) * float64(barWidth))
		if filled > barWidth {
			filled = barWidth
		}
		bar := strings.Repeat("=", filled) + strings.Repeat(" ", barWidth-filled)
		fmt.Fprintf(out, "\r%-8s [%s] %5.1f%% %s/%s", label, bar, percent, humanBytes(downloaded), humanBytes(total))
		return
	}
	fmt.Fprintf(out, "\r%-8s downloaded %s", label, humanBytes(downloaded))
}

func humanBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	size := float64(value)
	for _, unit := range units {
		size /= 1024
		if size < 1024 {
			return fmt.Sprintf("%.1f%s", size, unit)
		}
	}
	return fmt.Sprintf("%.1fPB", size/1024)
}

func verifyFileSHA256(path string, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("sha256 mismatch for %s: expected %s got %s", path, expected, actual)
	}
	return nil
}

func fileExistsAndNonEmpty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func detectImageArch(ref string) string {
	if parsed, err := images.ParseUbuntuRef(strings.TrimSpace(ref)); err == nil {
		if parsed.Arch != "" {
			return parsed.Arch
		}
	}

	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

func detectDiskFormatForPath(imagePath string) string {
	if qemuImgPath, err := exec.LookPath("qemu-img"); err == nil {
		if format, detectErr := detectDiskFormatWithQEMU(qemuImgPath, imagePath); detectErr == nil {
			return format
		}
	}
	if format, err := detectDiskFormatByMagic(imagePath); err == nil {
		return format
	}
	return "unknown"
}

func detectDiskFormatWithQEMU(qemuBinary string, imagePath string) (string, error) {
	command := exec.Command(qemuBinary, "info", "--output=json", imagePath)
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

func (a *App) runProvisionCommands(ctx context.Context, instanceDir string, baseImagePath string, instanceImagePath string, layerPaths []string, commands []string) error {
	if len(commands) == 0 {
		return nil
	}

	env := append([]string{}, os.Environ()...)
	env = append(env,
		"CLAWFARM_BASE_IMAGE="+baseImagePath,
		"CLAWFARM_INSTANCE_IMAGE="+instanceImagePath,
		"CLAWFARM_LAYER_COUNT="+strconv.Itoa(len(layerPaths)),
	)
	for index, path := range layerPaths {
		env = append(env, fmt.Sprintf("CLAWFARM_LAYER_%d=%s", index+1, path))
	}

	for index, command := range commands {
		trimmed := strings.TrimSpace(command)
		if trimmed == "" {
			continue
		}

		fmt.Fprintf(a.out, "provision[%d/%d]: %s\n", index+1, len(commands), trimmed)
		proc := exec.CommandContext(ctx, "sh", "-lc", trimmed)
		proc.Dir = instanceDir
		proc.Env = env
		output, err := proc.CombinedOutput()
		if err != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("provision command %d failed: %s", index+1, message)
		}
	}

	return nil
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

func (a *App) runNew(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: clawfarm new <image-ref> [--workspace=. --port=18789 --publish host:guest] [--run \"cmd\" --volume name:/guest/path]")
	}

	forwarded := append([]string(nil), args...)
	if !hasCLIFlag(forwarded, "--no-wait") {
		forwarded = append(forwarded, "--no-wait")
	}
	if !hasCLIFlag(forwarded, "--openclaw-model-primary") {
		forwarded = append(forwarded, "--openclaw-model-primary", "ollama/llama3")
	}
	if !hasCLIFlag(forwarded, "--openclaw-gateway-auth-mode") {
		forwarded = append(forwarded, "--openclaw-gateway-auth-mode", "none")
	}

	return a.runRun(forwarded)
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
	runName := ""
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
	var runCommands stringList
	var volumes volumeList
	var openClawEnvironment envVarList

	flags.StringVar(&workspace, "workspace", ".", "workspace path to mount")
	flags.IntVar(&gatewayPort, "port", defaultGatewayPort, "host gateway port")
	flags.IntVar(&cpus, "cpus", defaultCPUs, "vCPU count")
	flags.IntVar(&memoryMiB, "memory-mib", defaultMemoryMiB, "memory size in MiB")
	flags.IntVar(&readyTimeoutSecs, "ready-timeout-secs", defaultReadyTimeoutSecs, "gateway readiness timeout in seconds")
	flags.BoolVar(&noWait, "no-wait", false, "start and return without waiting for readiness")
	flags.StringVar(&runName, "name", "", "instance name (used in CLAWID prefix)")
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
	flags.Var(&runCommands, "run", "run command inside guest over SSH as root (repeatable)")
	flags.Var(&volumes, "volume", "volume mapping name:/guest/abs/path (repeatable)")
	flags.Var(&published, "publish", "host:guest mapping (repeatable)")
	flags.Var(&published, "port-forward", "alias of --publish (repeatable)")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: clawfarm run <ref|file.clawbox|.> [--workspace=. --port=18789 --publish host:guest] [--run \"cmd\" --volume name:/guest/abs/path] [--openclaw-config path --openclaw-env-file path --openclaw-env KEY=VALUE] [--openclaw-openai-api-key ... --openclaw-discord-token ...]")
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
	normalizedRunName, err := normalizeRunName(runName)
	if err != nil {
		return err
	}
	runName = normalizedRunName

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
	preparedTarget, err := a.prepareRunTarget(context.Background(), manager, runTarget)
	if err != nil {
		if !runTarget.SpecJSONMode && errors.Is(err, images.ErrImageNotFetched) {
			return fmt.Errorf("image %s is not ready, run `clawfarm image fetch %s` first", ref, ref)
		}
		return err
	}
	imageMeta := preparedTarget.ImageMeta
	if imageMeta.Arch == "" {
		imageMeta.Arch = detectImageArch(ref)
	}

	openClawConfig, err = a.preflightOpenClawInputs(openClawConfig, openClawEnv, runTarget.OpenClawRequiredEnv)
	if err != nil {
		return err
	}

	store, clawsRoot, err := a.instanceStore()
	if err != nil {
		return err
	}
	lockManager, err := a.lockManager()
	if err != nil {
		return err
	}

	vmPublished := make([]vm.PortMapping, 0, len(published.Mappings))
	for _, mapping := range published.Mappings {
		vmPublished = append(vmPublished, vm.PortMapping{HostPort: mapping.HostPort, GuestPort: mapping.GuestPort})
	}
	requestedRunCommands := normalizeProvisionCommands(runCommands.Values)
	runCommandsRequireSSH := len(requestedRunCommands) > 0
	requestedVolumeMappings := append([]volumeMapping(nil), volumes.Mappings...)

	id := runTarget.ClawID
	if id == "" {
		id, err = newClawID(runName)
		if err != nil {
			return err
		}
	}
	instanceDir := filepath.Join(clawsRoot, id)
	statePath := filepath.Join(instanceDir, "state")
	instanceImagePath := filepath.Join(instanceDir, "instance.img")
	mountSource := preparedTarget.MountSource
	if mountSource == "" {
		mountSource = imageMeta.RuntimeDisk
	}

	var startResult vm.StartResult
	var instance state.Instance
	sshHostPort := 0
	sshPrivateKeyPath := ""
	err = lockManager.WithInstanceLock(id, func() error {
		existing, loadErr := store.Load(id)
		if loadErr != nil && !errors.Is(loadErr, state.ErrNotFound) {
			return loadErr
		}
		if loadErr == nil && existing.PID > 0 && a.backend.IsRunning(existing.PID) {
			return state.ErrBusy
		}

		if err := ensureDir(statePath); err != nil {
			return err
		}

		acquireRequest := state.AcquireRequest{
			ClawID:     id,
			InstanceID: id,
		}
		if !runTarget.SkipMount {
			acquireRequest.SourcePath = mountSource
		}
		if err := lockManager.AcquireWhileLocked(context.Background(), acquireRequest); err != nil {
			return err
		}

		sourceDiskPath := instanceImagePath
		clawPath := ""
		cloudInitProvision := []string{}
		effectivePublished := append([]vm.PortMapping(nil), vmPublished...)
		vmVolumeMounts := make([]vm.VolumeMount, 0, len(requestedVolumeMappings))
		for _, volume := range requestedVolumeMappings {
			hostVolumePath := filepath.Join(instanceDir, "volumes", volume.Name)
			if err := ensureDir(hostVolumePath); err != nil {
				_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
				return err
			}
			vmVolumeMounts = append(vmVolumeMounts, vm.VolumeMount{
				Name:      volume.Name,
				HostPath:  hostVolumePath,
				GuestPath: volume.GuestPath,
			})
		}

		sshAuthorizedKeys := []string{}
		if runCommandsRequireSSH {
			selectedSSHHostPort, portErr := findAvailableLoopbackPort()
			if portErr != nil {
				_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
				return portErr
			}
			sshHostPort = selectedSSHHostPort
			effectivePublished = append(effectivePublished, vm.PortMapping{HostPort: sshHostPort, GuestPort: 22})

			generatedKeyPath, publicKey, keyErr := generateInstanceSSHKeyPair(instanceDir)
			if keyErr != nil {
				_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
				return keyErr
			}
			sshPrivateKeyPath = generatedKeyPath
			sshAuthorizedKeys = append(sshAuthorizedKeys, publicKey)
		}

		if runTarget.ClawboxV2Mode && runTarget.ClawboxV2Spec != nil {
			importedRunDiskPath, importErr := importRunClawboxV2(runTarget, id, clawsRoot, imageMeta.RuntimeDisk)
			if importErr != nil {
				_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
				return importErr
			}
			sourceDiskPath = importedRunDiskPath

			clawDir := filepath.Join(clawsRoot, id, "claw")
			if dirExists(clawDir) {
				clawPath = clawDir
			}

			cloudInitProvision = runTarget.ClawboxV2Spec.provisionScripts()
		} else {
			if err := copyFile(imageMeta.RuntimeDisk, instanceImagePath); err != nil {
				_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
				return err
			}
		}

		if err := a.runProvisionCommands(context.Background(), instanceDir, imageMeta.RuntimeDisk, instanceImagePath, preparedTarget.LayerPaths, preparedTarget.ProvisionCommands); err != nil {
			_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
			return err
		}

		startResult, err = a.backend.Start(context.Background(), vm.StartSpec{
			InstanceID:          id,
			InstanceDir:         instanceDir,
			ImageArch:           imageMeta.Arch,
			SourceDiskPath:      sourceDiskPath,
			ClawPath:            clawPath,
			WorkspacePath:       workspacePath,
			StatePath:           statePath,
			GatewayHostPort:     gatewayPort,
			GatewayGuestPort:    gatewayPort,
			PublishedPorts:      effectivePublished,
			VolumeMounts:        vmVolumeMounts,
			CPUs:                cpus,
			MemoryMiB:           memoryMiB,
			OpenClawPackage:     openClawPackage,
			OpenClawConfig:      openClawConfig,
			OpenClawEnvironment: openClawEnv,
			SSHAuthorizedKeys:   sshAuthorizedKeys,
			CloudInitProvision:  cloudInitProvision,
		})
		if err != nil {
			_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
			return err
		}
		if err := lockManager.AcquireWhileLocked(context.Background(), state.AcquireRequest{
			ClawID:     id,
			InstanceID: id,
			PID:        startResult.PID,
		}); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			_ = a.backend.Stop(stopCtx, startResult.PID)
			_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
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
			_ = lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: id})
			return err
		}

		if runCommandsRequireSSH {
			if err := a.runCommandsViaSSH(id, sshHostPort, sshPrivateKeyPath, requestedRunCommands); err != nil {
				instance.Status = "unhealthy"
				instance.LastError = err.Error()
				instance.UpdatedAtUTC = time.Now().UTC()
				if saveErr := store.Save(instance); saveErr != nil {
					return fmt.Errorf("%w (also failed to save instance state: %v)", err, saveErr)
				}
				return err
			}
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
	for _, volume := range requestedVolumeMappings {
		hostVolumePath := filepath.Join(instanceDir, "volumes", volume.Name)
		fmt.Fprintf(a.out, "volume: %s -> %s\n", hostVolumePath, volume.GuestPath)
	}
	if runCommandsRequireSSH {
		fmt.Fprintf(a.out, "ssh: claw@127.0.0.1:%d\n", sshHostPort)
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
		return errors.New("usage: clawfarm ps")
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
		return errors.New("usage: clawfarm suspend <clawid>")
	}
	return a.updateInstanceStateWithSignal(args[0], "suspended")
}

func (a *App) runResume(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: clawfarm resume <clawid>")
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
		return errors.New("usage: clawfarm rm <clawid>")
	}
	store, _, err := a.instanceStore()
	if err != nil {
		return err
	}
	lockManager, err := a.lockManager()
	if err != nil {
		return err
	}

	id := args[0]
	err = lockManager.WithInstanceLock(id, func() error {
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
		if err := lockManager.ReleaseWhileLocked(context.Background(), state.ReleaseRequest{ClawID: instance.ID}); err != nil {
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
	allowSecrets := false
	exportName := ""
	positionals := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		trimmed := strings.TrimSpace(args[index])
		switch {
		case trimmed == "":
			continue
		case trimmed == "--allow-secrets":
			allowSecrets = true
		case trimmed == "--name":
			if index+1 >= len(args) {
				return errors.New("missing value for --name")
			}
			index++
			exportName = strings.TrimSpace(args[index])
		case strings.HasPrefix(trimmed, "--name="):
			exportName = strings.TrimSpace(strings.TrimPrefix(trimmed, "--name="))
		case strings.HasPrefix(trimmed, "--"):
			return fmt.Errorf("unknown export flag %q", trimmed)
		default:
			positionals = append(positionals, trimmed)
		}
	}
	if len(positionals) != 2 {
		return errors.New("usage: clawfarm export <clawid> <output.clawbox> [--allow-secrets] [--name <name>]")
	}
	id := positionals[0]
	outputPath := positionals[1]
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
	lockManager, err := a.lockManager()
	if err != nil {
		return err
	}

	err = lockManager.WithInstanceLock(id, func() error {
		if _, loadErr := store.Load(id); loadErr != nil {
			if errors.Is(loadErr, state.ErrNotFound) {
				return fmt.Errorf("instance %s not found", id)
			}
			return loadErr
		}

		lockState, inspectErr := lockManager.Inspect(id)
		if inspectErr != nil {
			return inspectErr
		}
		sourcePath := strings.TrimSpace(lockState.SourcePath)
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

		findings, scanErr := scanPotentialSecretsFromFile(absSourcePath)
		if scanErr != nil {
			return scanErr
		}
		if len(findings) > 0 && !allowSecrets {
			return fmt.Errorf("export blocked: detected possible secrets (%s); use --allow-secrets to override", strings.Join(findings, ", "))
		}
		if len(findings) > 0 && allowSecrets {
			fmt.Fprintf(a.errOut, "warning: exporting with possible secrets due to --allow-secrets (%s)\n", strings.Join(findings, ", "))
		}

		if strings.TrimSpace(exportName) == "" {
			return copyFile(absSourcePath, absOutputPath)
		}
		if _, computeErr := clawbox.ComputeClawID(absSourcePath, exportName); computeErr != nil {
			return fmt.Errorf("invalid --name %q: %w", exportName, computeErr)
		}

		header, loadErr := clawbox.LoadHeaderJSON(absSourcePath)
		if loadErr != nil {
			return fmt.Errorf("load source clawbox for --name: %w", loadErr)
		}
		header.Name = exportName
		header.CreatedAtUTC = time.Now().UTC()
		return clawbox.SaveHeaderJSON(absOutputPath, header)
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
		return errors.New("usage: clawfarm checkpoint <clawid> --name <name>")
	}
	id := strings.TrimSpace(flags.Arg(0))
	checkpointName = strings.TrimSpace(checkpointName)
	if err := validateCheckpointName(checkpointName); err != nil {
		return err
	}

	store, clawsRoot, err := a.instanceStore()
	if err != nil {
		return err
	}
	lockManager, err := a.lockManager()
	if err != nil {
		return err
	}
	checkpointPath := checkpointPathForName(clawsRoot, id, checkpointName)

	err = lockManager.WithInstanceLock(id, func() error {
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
		return errors.New("usage: clawfarm restore <clawid> <checkpoint>")
	}
	id := strings.TrimSpace(args[0])
	checkpointName := strings.TrimSpace(args[1])
	if err := validateCheckpointName(checkpointName); err != nil {
		return err
	}

	store, clawsRoot, err := a.instanceStore()
	if err != nil {
		return err
	}
	lockManager, err := a.lockManager()
	if err != nil {
		return err
	}
	checkpointPath := checkpointPathForName(clawsRoot, id, checkpointName)

	err = lockManager.WithInstanceLock(id, func() error {
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

func scanPotentialSecretsFromFile(path string) ([]string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return scanPotentialSecrets(string(payload)), nil
}

func scanPotentialSecrets(payload string) []string {
	findingsSet := map[string]struct{}{}
	for _, pattern := range exportSecretScanPatterns {
		if pattern.re.FindStringIndex(payload) != nil {
			findingsSet[pattern.label] = struct{}{}
		}
	}
	findings := make([]string, 0, len(findingsSet))
	for label := range findingsSet {
		findings = append(findings, label)
	}
	sort.Strings(findings)
	return findings
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
	clawsRoot := filepath.Join(dataDir, "claws")
	if err := ensureDir(clawsRoot); err != nil {
		return nil, "", err
	}
	return state.NewStore(clawsRoot), clawsRoot, nil
}

func (a *App) lockManager() (*state.LockManager, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	clawsRoot := filepath.Join(dataDir, "claws")
	if err := ensureDir(clawsRoot); err != nil {
		return nil, err
	}
	return state.NewLockManager(clawsRoot, nil), nil
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func newClawID(prefix string) (string, error) {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	normalizedPrefix, err := normalizeRunName(prefix)
	if err != nil {
		return "", err
	}
	if normalizedPrefix == "" {
		return fmt.Sprintf("claw-%x", buffer), nil
	}
	return fmt.Sprintf("%s-%x", normalizedPrefix, buffer), nil
}

func normalizeRunName(raw string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return "", nil
	}
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	trimmed = strings.ReplaceAll(trimmed, " ", "-")
	trimmed = strings.Trim(trimmed, "-")
	if !runNamePattern.MatchString(trimmed) {
		return "", fmt.Errorf("invalid --name %q: expected [a-z0-9-], max length 48, and must start with letter/number", raw)
	}
	return trimmed, nil
}

func (a *App) printUsage() {
	fmt.Fprintln(a.out, "clawfarm - run full OpenClaw inside a lightweight VM")
	fmt.Fprintln(a.out, "")
	fmt.Fprintln(a.out, "Usage:")
	fmt.Fprintln(a.out, "  clawfarm image ls")
	fmt.Fprintln(a.out, "  clawfarm image fetch <ref>")
	fmt.Fprintln(a.out, "  clawfarm new <image-ref> [--workspace=. --port=18789 --publish host:guest]")
	fmt.Fprintln(a.out, "              [--run \"cmd\" --run \"cmd\" --volume name:/guest/abs/path]")
	fmt.Fprintln(a.out, "  clawfarm run <ref|file.clawbox|.> [--workspace=. --port=18789 --publish host:guest]")
	fmt.Fprintln(a.out, "             [--openclaw-config path --openclaw-agent-workspace /workspace --openclaw-model-primary openai/gpt-5]")
	fmt.Fprintln(a.out, "             [--openclaw-gateway-mode local --openclaw-gateway-auth-mode token --openclaw-gateway-token xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-openai-api-key xxx --openclaw-anthropic-api-key xxx --openclaw-openrouter-api-key xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-google-generative-ai-api-key xxx --openclaw-xai-api-key xxx --openclaw-zai-api-key xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-discord-token xxx --openclaw-telegram-token xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-whatsapp-phone-number-id xxx --openclaw-whatsapp-access-token xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-whatsapp-verify-token xxx --openclaw-whatsapp-app-secret xxx]")
	fmt.Fprintln(a.out, "             [--openclaw-env-file path --openclaw-env KEY=VALUE]")
	fmt.Fprintln(a.out, "  clawfarm ps")
	fmt.Fprintln(a.out, "  clawfarm suspend <clawid>")
	fmt.Fprintln(a.out, "  clawfarm resume <clawid>")
	fmt.Fprintln(a.out, "  clawfarm rm <clawid>")
	fmt.Fprintln(a.out, "  clawfarm export <clawid> <output.clawbox> [--allow-secrets] [--name <name>]")
	fmt.Fprintln(a.out, "  clawfarm checkpoint <clawid> --name <name>")
	fmt.Fprintln(a.out, "  clawfarm restore <clawid> <checkpoint>")
	fmt.Fprintln(a.out, "")
	fmt.Fprintln(a.out, "Examples:")
	fmt.Fprintln(a.out, "  clawfarm image fetch ubuntu:24.04")
	fmt.Fprintln(a.out, "  clawfarm new ubuntu:24.04 --run \"echo hello\" --volume .openclaw:/root/.openclaw")
	fmt.Fprintln(a.out, "  clawfarm run ubuntu:24.04 --workspace=. --publish 8080:80")
	fmt.Fprintln(a.out, "  clawfarm run ubuntu:24.04 --openclaw-openai-api-key $OPENAI_API_KEY --openclaw-discord-token $DISCORD_TOKEN")
	fmt.Fprintln(a.out, "  clawfarm checkpoint claw-1234 --name before-upgrade")
	fmt.Fprintln(a.out, "  clawfarm restore claw-1234 before-upgrade")
}

type stringList struct {
	Values []string
}

func (l *stringList) String() string {
	return strings.Join(l.Values, ",")
}

func (l *stringList) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errors.New("value must not be empty")
	}
	l.Values = append(l.Values, trimmed)
	return nil
}

type volumeMapping struct {
	Name      string
	GuestPath string
}

type volumeList struct {
	Values   []string
	Mappings []volumeMapping
}

func (l *volumeList) String() string {
	return strings.Join(l.Values, ",")
}

func (l *volumeList) Set(value string) error {
	mapping, err := parseVolumeMapping(value)
	if err != nil {
		return err
	}
	l.Values = append(l.Values, value)
	l.Mappings = append(l.Mappings, mapping)
	return nil
}

func parseVolumeMapping(input string) (volumeMapping, error) {
	parts := strings.SplitN(strings.TrimSpace(input), ":", 2)
	if len(parts) != 2 {
		return volumeMapping{}, fmt.Errorf("invalid volume value %q: expected name:/guest/abs/path", input)
	}

	name := strings.TrimSpace(parts[0])
	if name == "" {
		return volumeMapping{}, fmt.Errorf("invalid volume value %q: volume name is required", input)
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return volumeMapping{}, fmt.Errorf("invalid volume name %q", name)
	}
	if matched, _ := regexp.MatchString(`^[A-Za-z0-9._-]+$`, name); !matched {
		return volumeMapping{}, fmt.Errorf("invalid volume name %q", name)
	}

	guestPath := strings.TrimSpace(parts[1])
	if guestPath == "" {
		return volumeMapping{}, fmt.Errorf("invalid volume value %q: guest path is required", input)
	}
	if !filepath.IsAbs(guestPath) {
		return volumeMapping{}, fmt.Errorf("invalid volume value %q: guest path must be absolute", input)
	}

	return volumeMapping{Name: name, GuestPath: guestPath}, nil
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

func (a *App) preflightOpenClawInputs(openClawConfig string, openClawEnv map[string]string, requiredEnvKeys []string) (string, error) {
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

	requiredEnvKeys = normalizeRequiredEnvKeys(requiredEnvKeys)
	for _, envKey := range requiredEnvKeys {
		if strings.TrimSpace(openClawEnv[envKey]) != "" {
			continue
		}
		flagHint := requiredFlagForEnvKey(envKey)
		value, resolveErr := a.resolveRequiredInput(reader, canPrompt, promptFile,
			requiredOpenClawEnvLabel(envKey),
			flagHint,
			envKey,
			isSecretOpenClawEnvKey(envKey))
		if resolveErr != nil {
			return "", resolveErr
		}
		openClawEnv[envKey] = value
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

func normalizeRequiredEnvKeys(keys []string) []string {
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		trimmed := strings.ToUpper(strings.TrimSpace(key))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func requiredOpenClawEnvLabel(envKey string) string {
	switch strings.ToUpper(strings.TrimSpace(envKey)) {
	case "OPENCLAW_GATEWAY_TOKEN":
		return "OpenClaw gateway token"
	case "OPENCLAW_GATEWAY_PASSWORD":
		return "OpenClaw gateway password"
	case "OPENAI_API_KEY":
		return "OpenAI API key"
	case "ANTHROPIC_API_KEY":
		return "Anthropic API key"
	case "GOOGLE_GENERATIVE_AI_API_KEY":
		return "Google Generative AI API key"
	case "XAI_API_KEY":
		return "xAI API key"
	case "OPENROUTER_API_KEY":
		return "OpenRouter API key"
	case "ZAI_API_KEY":
		return "Z.AI API key"
	default:
		return fmt.Sprintf("OpenClaw env %s", envKey)
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

type runFailureAction string

const (
	runFailureActionExit     runFailureAction = "exit"
	runFailureActionRescue   runFailureAction = "rescue"
	runFailureActionContinue runFailureAction = "continue"
)

func findAvailableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve local ssh port: %w", err)
	}
	defer listener.Close()

	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress.Port <= 0 {
		return 0, errors.New("reserve local ssh port: invalid address")
	}
	return tcpAddress.Port, nil
}

func generateInstanceSSHKeyPair(instanceDir string) (string, string, error) {
	sshKeygenPath, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return "", "", errors.New("ssh-keygen is required to use --run")
	}

	sshDir := filepath.Join(instanceDir, "ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return "", "", err
	}

	privateKeyPath := filepath.Join(sshDir, "id_ed25519")
	publicKeyPath := privateKeyPath + ".pub"

	privateInfo, privateErr := os.Stat(privateKeyPath)
	publicPayload, publicErr := os.ReadFile(publicKeyPath)
	if privateErr == nil && publicErr == nil && privateInfo.Mode().IsRegular() {
		trimmedPublicKey := strings.TrimSpace(string(publicPayload))
		if trimmedPublicKey != "" {
			return privateKeyPath, trimmedPublicKey, nil
		}
	}

	if removeErr := os.Remove(privateKeyPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return "", "", removeErr
	}
	if removeErr := os.Remove(publicKeyPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return "", "", removeErr
	}

	command := exec.Command(sshKeygenPath, "-q", "-t", "ed25519", "-N", "", "-f", privateKeyPath, "-C", "clawfarm-run")
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", "", fmt.Errorf("generate ssh key pair: %s", message)
	}

	publicPayload, err = os.ReadFile(publicKeyPath)
	if err != nil {
		return "", "", err
	}
	trimmedPublicKey := strings.TrimSpace(string(publicPayload))
	if trimmedPublicKey == "" {
		return "", "", errors.New("generate ssh key pair: empty public key")
	}

	return privateKeyPath, trimmedPublicKey, nil
}

func (a *App) runCommandsViaSSH(clawID string, sshHostPort int, sshPrivateKeyPath string, commands []string) error {
	if len(commands) == 0 {
		return nil
	}
	if sshHostPort <= 0 {
		return errors.New("invalid ssh port for --run")
	}
	if strings.TrimSpace(sshPrivateKeyPath) == "" {
		return errors.New("missing ssh private key for --run")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("ssh client is required to use --run")
	}

	fmt.Fprintf(a.out, "run: waiting for ssh on 127.0.0.1:%d\n", sshHostPort)
	sshReadyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := waitForSSHReady(sshReadyCtx, sshHostPort, sshPrivateKeyPath); err != nil {
		return fmt.Errorf("%s: wait for ssh readiness: %w", clawID, err)
	}

commandLoop:
	for index, command := range commands {
		trimmedCommand := strings.TrimSpace(command)
		if trimmedCommand == "" {
			continue
		}

		fmt.Fprintf(a.out, "run[%d/%d]: %s\n", index+1, len(commands), trimmedCommand)
		if err := a.runSSHCommand(sshHostPort, sshPrivateKeyPath, trimmedCommand, true); err == nil {
			continue
		} else {
			commandErr := fmt.Errorf("run command %d failed: %w", index+1, err)
			if !a.canPromptForInput() {
				return commandErr
			}

			for {
				action, promptErr := a.promptRunFailureAction(index+1, trimmedCommand)
				if promptErr != nil {
					return commandErr
				}

				switch action {
				case runFailureActionContinue:
					continue commandLoop
				case runFailureActionRescue:
					if rescueErr := a.openRescueShellViaSSH(sshHostPort, sshPrivateKeyPath); rescueErr != nil {
						fmt.Fprintf(a.errOut, "run rescue shell failed: %v\n", rescueErr)
					}
				case runFailureActionExit:
					fallthrough
				default:
					return commandErr
				}
			}
		}
	}

	return nil
}

func waitForSSHReady(ctx context.Context, sshHostPort int, sshPrivateKeyPath string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		if err := runSSHProbe(sshHostPort, sshPrivateKeyPath); err == nil {
			return nil
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			if lastErr == nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w (last error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func runSSHProbe(sshHostPort int, sshPrivateKeyPath string) error {
	args := append(sshBaseArgs(sshHostPort, sshPrivateKeyPath), "-T", "claw@127.0.0.1", "true")
	command := exec.Command("ssh", args...)
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}

	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return errors.New(message)
}

func (a *App) runSSHCommand(sshHostPort int, sshPrivateKeyPath string, command string, allocateTTY bool) error {
	remoteCommand := fmt.Sprintf("sudo -n bash -lc %s", shellSingleQuote(command))
	args := sshBaseArgs(sshHostPort, sshPrivateKeyPath)
	if allocateTTY {
		args = append(args, "-tt")
	} else {
		args = append(args, "-T")
	}
	args = append(args, "claw@127.0.0.1", remoteCommand)

	sshCommand := exec.Command("ssh", args...)
	sshCommand.Stdin = a.in
	sshCommand.Stdout = a.out
	sshCommand.Stderr = a.errOut

	if err := sshCommand.Run(); err != nil {
		return fmt.Errorf("ssh command failed: %w", err)
	}
	return nil
}

func (a *App) openRescueShellViaSSH(sshHostPort int, sshPrivateKeyPath string) error {
	args := sshBaseArgs(sshHostPort, sshPrivateKeyPath)
	args = append(args, "-tt", "claw@127.0.0.1", "sudo -n -i")

	fmt.Fprintln(a.out, "run: opening rescue shell as root (exit shell to continue)")
	command := exec.Command("ssh", args...)
	command.Stdin = a.in
	command.Stdout = a.out
	command.Stderr = a.errOut
	return command.Run()
}

func (a *App) promptRunFailureAction(index int, command string) (runFailureAction, error) {
	if !a.canPromptForInput() || a.in == nil {
		return runFailureActionExit, nil
	}

	reader := bufio.NewReader(a.in)
	for {
		fmt.Fprintf(a.out, "run[%d] failed: %s\n", index, command)
		fmt.Fprint(a.out, "choose action [exit/rescue/continue] (default: exit): ")

		line, err := reader.ReadString('\n')
		if err != nil {
			return runFailureActionExit, fmt.Errorf("read run failure action: %w", err)
		}

		action := normalizeRunFailureAction(line)
		if action != "" {
			return action, nil
		}
		fmt.Fprintln(a.errOut, "invalid action, expected exit, rescue, or continue")
	}
}

func normalizeRunFailureAction(input string) runFailureAction {
	value := strings.ToLower(strings.TrimSpace(input))
	switch value {
	case "", "e", "exit":
		return runFailureActionExit
	case "r", "rescue":
		return runFailureActionRescue
	case "c", "continue":
		return runFailureActionContinue
	default:
		return ""
	}
}

func sshBaseArgs(sshHostPort int, sshPrivateKeyPath string) []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		"-i", sshPrivateKeyPath,
		"-p", strconv.Itoa(sshHostPort),
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
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

func hasCLIFlag(args []string, flagName string) bool {
	for index := 0; index < len(args); index++ {
		value := strings.TrimSpace(args[index])
		if value == "" {
			continue
		}
		if value == flagName || strings.HasPrefix(value, flagName+"=") {
			return true
		}
	}
	return false
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
