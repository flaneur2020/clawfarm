package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	kernelFileName   = "kernel"
	initrdFileName   = "initrd"
	baseImageName    = "base.img"
	runtimeDiskName  = "disk.raw"
	metadataFileName = "image.json"
)

var ErrImageNotFetched = errors.New("image not fetched")

type Metadata struct {
	Ref          string    `json:"ref"`
	Version      string    `json:"version"`
	Codename     string    `json:"codename"`
	Date         string    `json:"date,omitempty"`
	Arch         string    `json:"arch"`
	ImageDir     string    `json:"image_dir"`
	KernelPath   string    `json:"kernel_path"`
	InitrdPath   string    `json:"initrd_path"`
	BaseImage    string    `json:"base_image"`
	RuntimeDisk  string    `json:"runtime_disk"`
	Ready        bool      `json:"ready"`
	DiskFormat   string    `json:"disk_format"`
	FetchedAtUTC time.Time `json:"fetched_at_utc"`
	UpdatedAtUTC time.Time `json:"updated_at_utc"`
}

type Manager struct {
	root   string
	stdout io.Writer
}

func NewManager(root string, stdout io.Writer) *Manager {
	return &Manager{root: root, stdout: stdout}
}

func (m *Manager) List() ([]Metadata, error) {
	imagesRoot := m.imagesRoot()
	if err := os.MkdirAll(imagesRoot, 0o755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(imagesRoot)
	if err != nil {
		return nil, err
	}

	items := make([]Metadata, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		imageDir := filepath.Join(imagesRoot, entry.Name())
		meta, err := readMetadata(filepath.Join(imageDir, metadataFileName))
		if err != nil {
			continue
		}
		meta.Ready = fileExists(meta.KernelPath) && fileExists(meta.InitrdPath) && fileExists(meta.RuntimeDisk)
		items = append(items, meta)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAtUTC.Equal(items[j].UpdatedAtUTC) {
			return items[i].Ref < items[j].Ref
		}
		return items[i].UpdatedAtUTC.After(items[j].UpdatedAtUTC)
	})

	return items, nil
}

func (m *Manager) Resolve(ref string) (Metadata, error) {
	parsed, err := ParseUbuntuRef(ref)
	if err != nil {
		return Metadata{}, err
	}
	imageDir := filepath.Join(m.imagesRoot(), parsed.ImageDirName())
	metaPath := filepath.Join(imageDir, metadataFileName)
	meta, err := readMetadata(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Metadata{}, ErrImageNotFetched
		}
		return Metadata{}, err
	}
	if !(fileExists(meta.KernelPath) && fileExists(meta.InitrdPath) && fileExists(meta.RuntimeDisk)) {
		return Metadata{}, ErrImageNotFetched
	}
	return meta, nil
}

func (m *Manager) Fetch(ctx context.Context, ref string) (Metadata, error) {
	parsed, err := ParseUbuntuRef(ref)
	if err != nil {
		return Metadata{}, err
	}

	imageDir := filepath.Join(m.imagesRoot(), parsed.ImageDirName())
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return Metadata{}, err
	}

	kernelPath := filepath.Join(imageDir, kernelFileName)
	initrdPath := filepath.Join(imageDir, initrdFileName)
	basePath := filepath.Join(imageDir, baseImageName)
	diskPath := filepath.Join(imageDir, runtimeDiskName)
	metaPath := filepath.Join(imageDir, metadataFileName)

	if artifactsReady(kernelPath, initrdPath, basePath, diskPath) {
		cachedMeta, err := readMetadata(metaPath)
		if err == nil {
			cachedMeta.Ready = true
			if m.stdout != nil {
				fmt.Fprintf(m.stdout, "using cached image %s\n", cachedMeta.Ref)
			}
			return cachedMeta, nil
		}

		now := time.Now().UTC()
		generatedMeta := Metadata{
			Ref:          parsed.Original,
			Version:      parsed.Version,
			Codename:     parsed.Codename,
			Date:         parsed.Date,
			Arch:         parsed.Arch,
			ImageDir:     imageDir,
			KernelPath:   kernelPath,
			InitrdPath:   initrdPath,
			BaseImage:    basePath,
			RuntimeDisk:  diskPath,
			Ready:        true,
			DiskFormat:   "raw",
			FetchedAtUTC: now,
			UpdatedAtUTC: now,
		}
		if writeErr := writeMetadata(metaPath, generatedMeta); writeErr != nil {
			return Metadata{}, writeErr
		}
		if m.stdout != nil {
			fmt.Fprintf(m.stdout, "using cached image %s\n", generatedMeta.Ref)
		}
		return generatedMeta, nil
	}

	if err := ensureDownloadedFile(ctx, parsed.KernelURL(), kernelPath, m.stdout, "kernel"); err != nil {
		return Metadata{}, fmt.Errorf("download kernel: %w", err)
	}
	if err := ensureDownloadedFile(ctx, parsed.InitrdURL(), initrdPath, m.stdout, "initrd"); err != nil {
		return Metadata{}, fmt.Errorf("download initrd: %w", err)
	}
	if err := ensureDownloadedFile(ctx, parsed.BaseImageURL(), basePath, m.stdout, "base"); err != nil {
		return Metadata{}, fmt.Errorf("download base image: %w", err)
	}

	format := "raw"
	if !fileExistsAndNonEmpty(diskPath) {
		preparedFormat, prepareErr := prepareRuntimeDisk(basePath, diskPath)
		if prepareErr != nil {
			return Metadata{}, fmt.Errorf("prepare runtime disk: %w", prepareErr)
		}
		format = preparedFormat
	}

	now := time.Now().UTC()
	meta := Metadata{
		Ref:          parsed.Original,
		Version:      parsed.Version,
		Codename:     parsed.Codename,
		Date:         parsed.Date,
		Arch:         parsed.Arch,
		ImageDir:     imageDir,
		KernelPath:   kernelPath,
		InitrdPath:   initrdPath,
		BaseImage:    basePath,
		RuntimeDisk:  diskPath,
		Ready:        true,
		DiskFormat:   format,
		FetchedAtUTC: now,
		UpdatedAtUTC: now,
	}

	if err := writeMetadata(metaPath, meta); err != nil {
		return Metadata{}, err
	}

	return meta, nil
}

func ensureDownloadedFile(ctx context.Context, url string, destination string, out io.Writer, label string) error {
	if fileExistsAndNonEmpty(destination) {
		return nil
	}
	return downloadFile(ctx, url, destination, out, label)
}

func artifactsReady(kernelPath string, initrdPath string, basePath string, diskPath string) bool {
	return fileExistsAndNonEmpty(kernelPath) && fileExistsAndNonEmpty(initrdPath) && fileExistsAndNonEmpty(basePath) && fileExistsAndNonEmpty(diskPath)
}

func (m *Manager) imagesRoot() string {
	return filepath.Join(m.root, "images")
}

func downloadFile(ctx context.Context, url string, destination string, out io.Writer, label string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

	tempFile := destination + ".tmp"
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}

	file, err := os.Create(tempFile)
	if err != nil {
		return err
	}

	if out == nil {
		if _, err := io.Copy(file, response.Body); err != nil {
			file.Close()
			_ = os.Remove(tempFile)
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
					file.Close()
					_ = os.Remove(tempFile)
					return writeErr
				}
				if writtenBytes != readBytes {
					file.Close()
					_ = os.Remove(tempFile)
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
				file.Close()
				_ = os.Remove(tempFile)
				return readErr
			}
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tempFile)
		return err
	}

	if err := os.Rename(tempFile, destination); err != nil {
		_ = os.Remove(tempFile)
		return err
	}

	return nil
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
		fmt.Fprintf(out, "\r%-6s [%s] %5.1f%% %s/%s", label, bar, percent, humanBytes(downloaded), humanBytes(total))
		return
	}
	fmt.Fprintf(out, "\r%-6s downloaded %s", label, humanBytes(downloaded))
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

func prepareRuntimeDisk(basePath, diskPath string) (string, error) {
	binary, err := exec.LookPath("qemu-img")
	if err != nil {
		fallbackFormat, detectErr := detectDiskFormatByMagic(basePath)
		if detectErr == nil {
			switch fallbackFormat {
			case "raw":
				if copyErr := copyFile(basePath, diskPath); copyErr != nil {
					return "", copyErr
				}
				return "raw", nil
			case "qcow2":
				return "", errors.New("qemu-img is required to convert qcow2 images; install qemu-img and retry")
			}
		}
		if copyErr := copyFile(basePath, diskPath); copyErr != nil {
			return "", copyErr
		}
		return "unknown", nil
	}

	format, err := detectDiskFormat(binary, basePath)
	if err != nil {
		format = ""
	}

	if format == "raw" {
		if err := copyFile(basePath, diskPath); err != nil {
			return "", err
		}
		return "raw", nil
	}

	convertArgs := []string{"convert"}
	if format != "" {
		convertArgs = append(convertArgs, "-f", format)
	}
	convertArgs = append(convertArgs, "-O", "raw", basePath, diskPath)
	cmd := exec.Command(binary, convertArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("qemu-img convert failed: %s", strings.TrimSpace(string(output)))
	}

	if format == "" {
		return "raw", nil
	}
	return format, nil
}

func detectDiskFormat(qemuBinary, imagePath string) (string, error) {
	cmd := exec.Command(qemuBinary, "info", "--output=json", imagePath)
	output, err := cmd.Output()
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

func copyFile(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}

	tmpPath := destinationPath + ".tmp"
	target, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := target.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, destinationPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return nil
}

func writeMetadata(path string, metadata Metadata) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(metadata)
}

func readMetadata(path string) (Metadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()

	var metadata Metadata
	if err := json.NewDecoder(file).Decode(&metadata); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileExistsAndNonEmpty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0
}
