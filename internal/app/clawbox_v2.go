package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	clawboxSpecV2SchemaVersion = 2
	clawboxSpecV2Path          = "clawspec.json"
)

var sha256LowerHexPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type runClawboxSpecV2 struct {
	SchemaVersion int                   `json:"schema_version"`
	Name          string                `json:"name"`
	SHA256        string                `json:"sha256,omitempty"`
	Images        []runClawboxImageV2   `json:"image"`
	Provision     []runProvisionStepV2  `json:"provision,omitempty"`
	OpenClaw      runOpenClawConfigSpec `json:"openclaw"`
}

type runClawboxImageV2 struct {
	Name   string `json:"name"`
	Ref    string `json:"ref"`
	SHA256 string `json:"sha256"`
}

type runProvisionStepV2 struct {
	Name   string `json:"name,omitempty"`
	Shell  string `json:"shell,omitempty"`
	Script string `json:"script"`
}

type runOpenClawConfigSpec struct {
	ModelPrimary    string   `json:"model_primary,omitempty"`
	GatewayAuthMode string   `json:"gateway_auth_mode,omitempty"`
	RequiredEnv     []string `json:"required_env,omitempty"`
	OptionalEnv     []string `json:"optional_env,omitempty"`
}

func resolveRunTargetFromTarClawbox(input string, clawboxPath string) (runTarget, error) {
	spec, err := parseRunClawboxSpecV2(clawboxPath)
	if err != nil {
		return runTarget{}, err
	}

	baseImage, err := spec.baseImage()
	if err != nil {
		return runTarget{}, err
	}
	if strings.HasPrefix(baseImage.Ref, "clawbox:///") {
		return runTarget{}, errors.New("base image ref clawbox:///... is not supported yet")
	}

	return runTarget{
		Input:                   input,
		ImageRef:                strings.TrimSpace(baseImage.Ref),
		ClawboxV2Mode:           true,
		SkipMount:               true,
		ClawboxPath:             clawboxPath,
		ClawboxV2Spec:           &spec,
		OpenClawModelPrimary:    strings.TrimSpace(spec.OpenClaw.ModelPrimary),
		OpenClawGatewayAuthMode: strings.TrimSpace(spec.OpenClaw.GatewayAuthMode),
		OpenClawRequiredEnv:     append([]string(nil), spec.OpenClaw.RequiredEnv...),
		IsClawbox:               true,
	}, nil
}

func parseRunClawboxSpecV2(clawboxPath string) (runClawboxSpecV2, error) {
	file, err := os.Open(clawboxPath)
	if err != nil {
		return runClawboxSpecV2{}, err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return runClawboxSpecV2{}, fmt.Errorf("open .clawbox as gzip stream: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return runClawboxSpecV2{}, fmt.Errorf("read .clawbox tar stream: %w", err)
		}

		name := normalizedTarPath(header.Name)
		if name != clawboxSpecV2Path {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return runClawboxSpecV2{}, fmt.Errorf("%s must be a regular file", clawboxSpecV2Path)
		}

		payload, err := io.ReadAll(io.LimitReader(tarReader, 2*1024*1024))
		if err != nil {
			return runClawboxSpecV2{}, fmt.Errorf("read %s: %w", clawboxSpecV2Path, err)
		}

		spec := runClawboxSpecV2{}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&spec); err != nil {
			return runClawboxSpecV2{}, fmt.Errorf("parse %s: %w", clawboxSpecV2Path, err)
		}
		if err := spec.validate(); err != nil {
			return runClawboxSpecV2{}, fmt.Errorf("invalid %s: %w", clawboxSpecV2Path, err)
		}
		return spec, nil
	}

	return runClawboxSpecV2{}, fmt.Errorf("missing %s", clawboxSpecV2Path)
}

func (spec runClawboxSpecV2) validate() error {
	if spec.SchemaVersion != clawboxSpecV2SchemaVersion {
		return fmt.Errorf("schema_version must be %d", clawboxSpecV2SchemaVersion)
	}
	if strings.TrimSpace(spec.Name) == "" {
		return errors.New("name is required")
	}
	if len(spec.Images) == 0 {
		return errors.New("image is required")
	}

	seen := map[string]struct{}{}
	for index, image := range spec.Images {
		name := strings.ToLower(strings.TrimSpace(image.Name))
		if name == "" {
			return fmt.Errorf("image[%d].name is required", index)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate image name %q", image.Name)
		}
		seen[name] = struct{}{}

		if strings.TrimSpace(image.Ref) == "" {
			return fmt.Errorf("image[%d].ref is required", index)
		}
		sha := strings.ToLower(strings.TrimSpace(image.SHA256))
		if !sha256LowerHexPattern.MatchString(sha) {
			return fmt.Errorf("image[%d].sha256 must be lowercase 64-char hex", index)
		}
	}
	if _, ok := seen["base"]; !ok {
		return errors.New("image entry with name=base is required")
	}

	if strings.TrimSpace(spec.OpenClaw.GatewayAuthMode) != "" {
		mode := strings.ToLower(strings.TrimSpace(spec.OpenClaw.GatewayAuthMode))
		if mode != "token" && mode != "password" && mode != "none" {
			return fmt.Errorf("openclaw.gateway_auth_mode %q is invalid", spec.OpenClaw.GatewayAuthMode)
		}
	}
	return nil
}

func (spec runClawboxSpecV2) baseImage() (runClawboxImageV2, error) {
	for _, image := range spec.Images {
		if strings.EqualFold(strings.TrimSpace(image.Name), "base") {
			return runClawboxImageV2{
				Name:   strings.TrimSpace(image.Name),
				Ref:    strings.TrimSpace(image.Ref),
				SHA256: strings.ToLower(strings.TrimSpace(image.SHA256)),
			}, nil
		}
	}
	return runClawboxImageV2{}, errors.New("image entry with name=base is required")
}

func (spec runClawboxSpecV2) runImage() (runClawboxImageV2, bool) {
	for _, image := range spec.Images {
		if strings.EqualFold(strings.TrimSpace(image.Name), "run") {
			return runClawboxImageV2{
				Name:   strings.TrimSpace(image.Name),
				Ref:    strings.TrimSpace(image.Ref),
				SHA256: strings.ToLower(strings.TrimSpace(image.SHA256)),
			}, true
		}
	}
	return runClawboxImageV2{}, false
}

func (spec runClawboxSpecV2) provisionScripts() []string {
	result := make([]string, 0, len(spec.Provision))
	for _, step := range spec.Provision {
		script := strings.TrimSpace(step.Script)
		if script == "" {
			continue
		}
		result = append(result, script)
	}
	return result
}

func importRunClawboxV2(target runTarget, clawID string, clawsRoot string, fallbackBaseDiskPath string) (string, error) {
	if !target.ClawboxV2Mode || target.ClawboxV2Spec == nil {
		return "", nil
	}

	spec := target.ClawboxV2Spec
	clawDir := filepath.Join(clawsRoot, clawID)
	if err := ensureDir(clawDir); err != nil {
		return "", err
	}

	if err := writeRunClawboxSpecV2(filepath.Join(clawDir, clawboxSpecV2Path), *spec); err != nil {
		return "", err
	}

	runImage, hasRunImage := spec.runImage()
	runArchivePath := ""
	if hasRunImage {
		if !strings.HasPrefix(runImage.Ref, "clawbox:///") {
			return "", fmt.Errorf("run image ref %q is unsupported: expected clawbox:///...", runImage.Ref)
		}
		runArchivePath = strings.TrimPrefix(runImage.Ref, "clawbox:///")
		runArchivePath = normalizedTarPath(runArchivePath)
		if runArchivePath == "" || runArchivePath == "." {
			return "", errors.New("run image ref clawbox:///... points to empty path")
		}
	}

	runDiskPath := filepath.Join(clawDir, "run.qcow2")
	foundRunDisk := false

	file, err := os.Open(target.ClawboxPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open .clawbox as gzip stream: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read .clawbox tar stream: %w", err)
		}

		name := normalizedTarPath(header.Name)
		if name == "" || name == "." {
			continue
		}

		if hasRunImage && name == runArchivePath {
			if header.Typeflag != tar.TypeReg {
				return "", fmt.Errorf("run image %s must be a regular file", name)
			}
			tempPath := runDiskPath + ".tmp.download"
			_ = os.Remove(tempPath)
			if err := writeTarRegularFileToPath(tarReader, tempPath, header.FileInfo().Mode().Perm()); err != nil {
				_ = os.Remove(tempPath)
				return "", err
			}
			if err := verifyFileSHA256(tempPath, runImage.SHA256); err != nil {
				_ = os.Remove(tempPath)
				return "", err
			}
			if err := os.Rename(tempPath, runDiskPath); err != nil {
				_ = os.Remove(tempPath)
				return "", err
			}
			foundRunDisk = true
			continue
		}

		if !strings.HasPrefix(name, "claw/") {
			continue
		}
		targetPath, err := safeJoinWithin(clawDir, name)
		if err != nil {
			return "", err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := writeTarRegularFileToPath(tarReader, targetPath, header.FileInfo().Mode().Perm()); err != nil {
				return "", err
			}
		}
	}

	if foundRunDisk {
		return runDiskPath, nil
	}
	if hasRunImage {
		return "", fmt.Errorf("missing run image entry %s in .clawbox", runArchivePath)
	}
	if fallbackBaseDiskPath == "" {
		return "", errors.New("cannot initialize run.qcow2: base disk path is empty")
	}
	if err := copyFile(fallbackBaseDiskPath, runDiskPath); err != nil {
		return "", err
	}
	return runDiskPath, nil
}

func writeRunClawboxSpecV2(path string, spec runClawboxSpecV2) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
}

func writeTarRegularFileToPath(reader io.Reader, path string, mode os.FileMode) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, reader); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func safeJoinWithin(root string, child string) (string, error) {
	cleanChild := filepath.FromSlash(normalizedTarPath(child))
	fullPath := filepath.Join(root, cleanChild)
	relativePath, err := filepath.Rel(root, fullPath)
	if err != nil {
		return "", err
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("tar entry escapes target root: %s", child)
	}
	return fullPath, nil
}

func normalizedTarPath(name string) string {
	cleaned := path.Clean(strings.TrimPrefix(strings.TrimSpace(name), "./"))
	if cleaned == "/" {
		return ""
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

func sortedImageNames(images []runClawboxImageV2) string {
	names := make([]string, 0, len(images))
	for _, image := range images {
		names = append(names, strings.TrimSpace(image.Name))
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
