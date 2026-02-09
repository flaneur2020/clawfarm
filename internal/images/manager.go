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
	imageFileName    = "image.img"
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
		meta = normalizeMetadata(imageDir, meta)
		meta.Ready = fileExistsAndNonEmpty(meta.RuntimeDisk)
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

func (m *Manager) ListAvailable() ([]Metadata, error) {
	cached, err := m.List()
	if err != nil {
		return nil, err
	}

	cachedByRef := map[string]Metadata{}
	for _, item := range cached {
		cachedByRef[item.Ref] = item
	}

	items := make([]Metadata, 0, len(SupportedRefs())+len(cachedByRef))
	for _, ref := range SupportedRefs() {
		if existing, ok := cachedByRef[ref]; ok {
			items = append(items, existing)
			delete(cachedByRef, ref)
			continue
		}

		parsed, err := ParseUbuntuRef(ref)
		if err != nil {
			continue
		}
		imageDir := filepath.Join(m.imagesRoot(), parsed.ImageDirName())
		items = append(items, Metadata{
			Ref:         parsed.Original,
			Version:     parsed.Version,
			Codename:    parsed.Codename,
			Date:        parsed.Date,
			Arch:        parsed.Arch,
			ImageDir:    imageDir,
			RuntimeDisk: filepath.Join(imageDir, imageFileName),
			Ready:       false,
		})
	}

	for _, item := range cachedByRef {
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Ready != items[j].Ready {
			return items[i].Ready
		}
		return items[i].Ref < items[j].Ref
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

	meta = normalizeMetadata(imageDir, meta)
	if !fileExistsAndNonEmpty(meta.RuntimeDisk) {
		return Metadata{}, ErrImageNotFetched
	}
	meta.Ready = true
	if meta.DiskFormat == "" {
		meta.DiskFormat = detectDownloadedDiskFormat(meta.RuntimeDisk)
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

	diskPath := filepath.Join(imageDir, imageFileName)
	metaPath := filepath.Join(imageDir, metadataFileName)

	if fileExistsAndNonEmpty(diskPath) {
		cachedMeta, err := readMetadata(metaPath)
		if err == nil {
			cachedMeta = normalizeMetadata(imageDir, cachedMeta)
			cachedMeta.RuntimeDisk = diskPath
			cachedMeta.Ready = true
			if cachedMeta.DiskFormat == "" {
				cachedMeta.DiskFormat = detectDownloadedDiskFormat(diskPath)
			}
			if m.stdout != nil {
				fmt.Fprintf(m.stdout, "using cached image %s\n", cachedMeta.Ref)
			}
			if writeErr := writeMetadata(metaPath, cachedMeta); writeErr != nil {
				return Metadata{}, writeErr
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
			RuntimeDisk:  diskPath,
			Ready:        true,
			DiskFormat:   detectDownloadedDiskFormat(diskPath),
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

	if err := ensureDownloadedFile(ctx, parsed.BaseImageURL(), diskPath, m.stdout, "image"); err != nil {
		return Metadata{}, fmt.Errorf("download image: %w", err)
	}

	now := time.Now().UTC()
	meta := Metadata{
		Ref:          parsed.Original,
		Version:      parsed.Version,
		Codename:     parsed.Codename,
		Date:         parsed.Date,
		Arch:         parsed.Arch,
		ImageDir:     imageDir,
		RuntimeDisk:  diskPath,
		Ready:        true,
		DiskFormat:   detectDownloadedDiskFormat(diskPath),
		FetchedAtUTC: now,
		UpdatedAtUTC: now,
	}

	if err := writeMetadata(metaPath, meta); err != nil {
		return Metadata{}, err
	}

	return meta, nil
}

func (m *Manager) imagesRoot() string {
	return filepath.Join(m.root, "images")
}

func normalizeMetadata(imageDir string, meta Metadata) Metadata {
	if meta.ImageDir == "" {
		meta.ImageDir = imageDir
	}
	meta.RuntimeDisk = filepath.Join(imageDir, imageFileName)
	if meta.Arch == "" && meta.Ref != "" {
		if parsed, err := ParseUbuntuRef(meta.Ref); err == nil {
			meta.Arch = parsed.Arch
			if meta.Version == "" {
				meta.Version = parsed.Version
			}
			if meta.Codename == "" {
				meta.Codename = parsed.Codename
			}
			if meta.Date == "" {
				meta.Date = parsed.Date
			}
		}
	}
	return meta
}

func ensureDownloadedFile(ctx context.Context, url string, destination string, out io.Writer, label string) error {
	if fileExistsAndNonEmpty(destination) {
		return nil
	}
	return downloadFile(ctx, url, destination, out, label)
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

func detectDownloadedDiskFormat(imagePath string) string {
	if qemuImgPath, err := exec.LookPath("qemu-img"); err == nil {
		if format, detectErr := detectDiskFormat(qemuImgPath, imagePath); detectErr == nil {
			return format
		}
	}
	if format, err := detectDiskFormatByMagic(imagePath); err == nil {
		return format
	}
	return "unknown"
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

func fileExistsAndNonEmpty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0
}
