package clawbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const SchemaVersionV1 = 1

const (
	payloadFSTypeSquashFS = "squashfs"
	payloadFSTypeEROFS    = "erofs"
)

var (
	clawboxNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)
	envNamePattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	sha256Pattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Header struct {
	SchemaVersion int         `json:"schema_version"`
	Name          string      `json:"name"`
	CreatedAtUTC  time.Time   `json:"created_at_utc"`
	Payload       Payload     `json:"payload"`
	Spec          RuntimeSpec `json:"spec"`
}

type Payload struct {
	FSType string `json:"fs_type"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type RuntimeSpec struct {
	BaseImage BaseImage    `json:"base_image"`
	Layers    []Layer      `json:"layers,omitempty"`
	OpenClaw  OpenClawSpec `json:"openclaw"`
}

type BaseImage struct {
	Ref    string `json:"ref"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type Layer struct {
	Ref    string `json:"ref"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type OpenClawSpec struct {
	InstallRoot     string   `json:"install_root"`
	ModelPrimary    string   `json:"model_primary"`
	GatewayAuthMode string   `json:"gateway_auth_mode"`
	RequiredEnv     []string `json:"required_env,omitempty"`
	OptionalEnv     []string `json:"optional_env,omitempty"`
}

func LoadHeaderJSON(path string) (Header, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Header{}, err
	}
	return ParseHeaderJSON(data)
}

func ParseHeaderJSON(data []byte) (Header, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var header Header
	if err := decoder.Decode(&header); err != nil {
		return Header{}, err
	}
	if err := header.Validate(); err != nil {
		return Header{}, err
	}
	return header, nil
}

func SaveHeaderJSON(path string, header Header) error {
	if err := header.Validate(); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(header)
}

func (header Header) Validate() error {
	if header.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("unsupported schema_version %d: expected %d", header.SchemaVersion, SchemaVersionV1)
	}
	if err := validateClawboxName(header.Name); err != nil {
		return fmt.Errorf("invalid name: %w", err)
	}
	if header.CreatedAtUTC.IsZero() {
		return errors.New("created_at_utc is required")
	}
	if err := validatePayload(header.Payload); err != nil {
		return err
	}
	if err := validateRuntimeSpec(header.Spec); err != nil {
		return err
	}
	return nil
}

func (header Header) ClawID(clawboxPath string) (string, error) {
	if err := validateClawboxName(header.Name); err != nil {
		return "", fmt.Errorf("invalid name: %w", err)
	}
	return ComputeClawID(clawboxPath, header.Name)
}

func ComputeClawID(clawboxPath string, name string) (string, error) {
	if err := validateClawboxName(name); err != nil {
		return "", err
	}

	info, err := os.Stat(clawboxPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory: expected .clawbox file", clawboxPath)
	}

	inode, err := inodeNumber(info)
	if err != nil {
		return "", err
	}
	inodeHash := hashInode(inode)
	return fmt.Sprintf("%s-%s", strings.ToLower(name), inodeHash), nil
}

func validateClawboxName(name string) error {
	if !clawboxNamePattern.MatchString(name) {
		return fmt.Errorf("expected %q, got %q", clawboxNamePattern.String(), name)
	}
	return nil
}

func validatePayload(payload Payload) error {
	switch payload.FSType {
	case payloadFSTypeSquashFS, payloadFSTypeEROFS:
		// valid
	default:
		return fmt.Errorf("unsupported payload.fs_type %q", payload.FSType)
	}

	if payload.Offset < 0 {
		return fmt.Errorf("payload.offset must be >= 0")
	}
	if payload.Size <= 0 {
		return fmt.Errorf("payload.size must be > 0")
	}
	if !sha256Pattern.MatchString(strings.ToLower(payload.SHA256)) {
		return fmt.Errorf("payload.sha256 must be lowercase hex sha256")
	}

	return nil
}

func validateRuntimeSpec(spec RuntimeSpec) error {
	if err := validateBlobRef("spec.base_image", spec.BaseImage.Ref, spec.BaseImage.URL, spec.BaseImage.SHA256); err != nil {
		return err
	}
	for i, layer := range spec.Layers {
		field := fmt.Sprintf("spec.layers[%d]", i)
		if err := validateBlobRef(field, layer.Ref, layer.URL, layer.SHA256); err != nil {
			return err
		}
	}
	if err := validateOpenClawSpec(spec.OpenClaw); err != nil {
		return err
	}
	return nil
}

func validateBlobRef(prefix string, ref string, url string, sha string) error {
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("%s.ref is required", prefix)
	}
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("%s.url is required", prefix)
	}
	if !sha256Pattern.MatchString(strings.ToLower(sha)) {
		return fmt.Errorf("%s.sha256 must be lowercase hex sha256", prefix)
	}
	return nil
}

func validateOpenClawSpec(openClaw OpenClawSpec) error {
	if strings.TrimSpace(openClaw.InstallRoot) == "" {
		return errors.New("spec.openclaw.install_root is required")
	}
	if !strings.HasPrefix(openClaw.InstallRoot, "/") {
		return errors.New("spec.openclaw.install_root must be an absolute path")
	}

	if openClaw.GatewayAuthMode != "" && openClaw.GatewayAuthMode != "token" && openClaw.GatewayAuthMode != "password" && openClaw.GatewayAuthMode != "none" {
		return fmt.Errorf("spec.openclaw.gateway_auth_mode %q is invalid", openClaw.GatewayAuthMode)
	}

	seenRequired := make(map[string]struct{}, len(openClaw.RequiredEnv))
	for _, key := range openClaw.RequiredEnv {
		if !envNamePattern.MatchString(key) {
			return fmt.Errorf("spec.openclaw.required_env contains invalid key %q", key)
		}
		if _, exists := seenRequired[key]; exists {
			return fmt.Errorf("spec.openclaw.required_env contains duplicate key %q", key)
		}
		seenRequired[key] = struct{}{}
	}

	seenOptional := make(map[string]struct{}, len(openClaw.OptionalEnv))
	for _, key := range openClaw.OptionalEnv {
		if !envNamePattern.MatchString(key) {
			return fmt.Errorf("spec.openclaw.optional_env contains invalid key %q", key)
		}
		if _, exists := seenOptional[key]; exists {
			return fmt.Errorf("spec.openclaw.optional_env contains duplicate key %q", key)
		}
		if _, exists := seenRequired[key]; exists {
			return fmt.Errorf("spec.openclaw.optional_env duplicates required key %q", key)
		}
		seenOptional[key] = struct{}{}
	}

	return nil
}

func inodeNumber(info os.FileInfo) (uint64, error) {
	sys := info.Sys()
	if sys == nil {
		return 0, errors.New("file metadata does not include inode")
	}

	value := reflect.ValueOf(sys)
	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return 0, errors.New("file metadata does not include inode")
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return 0, errors.New("file metadata does not include inode")
	}

	field := value.FieldByName("Ino")
	if !field.IsValid() {
		return 0, errors.New("file metadata does not expose inode")
	}

	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint(), nil
	default:
		return 0, errors.New("file metadata has unexpected inode type")
	}
}

func hashInode(inode uint64) string {
	sum := sha256.Sum256([]byte(strconv.FormatUint(inode, 10)))
	return hex.EncodeToString(sum[:6])
}
