package clawbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestHeaderValidateSuccess(t *testing.T) {
	header := validHeader()
	if err := header.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestHeaderValidateRejectsUnsupportedSchema(t *testing.T) {
	header := validHeader()
	header.SchemaVersion = 2

	err := header.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "unsupported schema_version") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHeaderValidateRejectsInvalidEnvKey(t *testing.T) {
	header := validHeader()
	header.Spec.OpenClaw.RequiredEnv = []string{"OPENAI_API_KEY", "bad-key"}

	err := header.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "required_env") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHeaderValidateRejectsOptionalDuplicateOfRequired(t *testing.T) {
	header := validHeader()
	header.Spec.OpenClaw.RequiredEnv = []string{"OPENAI_API_KEY"}
	header.Spec.OpenClaw.OptionalEnv = []string{"OPENAI_API_KEY"}

	err := header.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "duplicates required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseHeaderJSONRejectsUnknownField(t *testing.T) {
	header := validHeader()
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	invalidJSON := strings.TrimSuffix(string(raw), "}") + `,"extra_field":1}`

	_, err = ParseHeaderJSON([]byte(invalidJSON))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveLoadHeaderJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo", "header.json")
	input := validHeader()

	if err := SaveHeaderJSON(path, input); err != nil {
		t.Fatalf("SaveHeaderJSON failed: %v", err)
	}

	output, err := LoadHeaderJSON(path)
	if err != nil {
		t.Fatalf("LoadHeaderJSON failed: %v", err)
	}

	if output.SchemaVersion != input.SchemaVersion {
		t.Fatalf("schema mismatch: got %d want %d", output.SchemaVersion, input.SchemaVersion)
	}
	if output.Name != input.Name {
		t.Fatalf("name mismatch: got %q want %q", output.Name, input.Name)
	}
	if !output.CreatedAtUTC.Equal(input.CreatedAtUTC) {
		t.Fatalf("created_at mismatch: got %s want %s", output.CreatedAtUTC, input.CreatedAtUTC)
	}
	if output.Payload != input.Payload {
		t.Fatalf("payload mismatch: got %+v want %+v", output.Payload, input.Payload)
	}
	if output.Spec.BaseImage != input.Spec.BaseImage {
		t.Fatalf("base image mismatch: got %+v want %+v", output.Spec.BaseImage, input.Spec.BaseImage)
	}
	if len(output.Spec.Layers) != len(input.Spec.Layers) {
		t.Fatalf("layers length mismatch: got %d want %d", len(output.Spec.Layers), len(input.Spec.Layers))
	}
}

func TestComputeClawIDStableForSameFileAndName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.clawbox")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	idA, err := ComputeClawID(path, "demo-openclaw")
	if err != nil {
		t.Fatalf("ComputeClawID failed: %v", err)
	}
	idB, err := ComputeClawID(path, "demo-openclaw")
	if err != nil {
		t.Fatalf("ComputeClawID failed: %v", err)
	}

	if idA != idB {
		t.Fatalf("expected stable id for same file: %q vs %q", idA, idB)
	}
	if !strings.HasPrefix(idA, "demo-openclaw-") {
		t.Fatalf("expected id prefix demo-openclaw-, got %q", idA)
	}
}

func TestComputeClawIDDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.clawbox")
	pathB := filepath.Join(dir, "b.clawbox")
	if err := os.WriteFile(pathA, []byte("a"), 0o644); err != nil {
		t.Fatalf("write file A: %v", err)
	}
	if err := os.WriteFile(pathB, []byte("b"), 0o644); err != nil {
		t.Fatalf("write file B: %v", err)
	}

	idA, err := ComputeClawID(pathA, "demo-openclaw")
	if err != nil {
		t.Fatalf("ComputeClawID A failed: %v", err)
	}
	idB, err := ComputeClawID(pathB, "demo-openclaw")
	if err != nil {
		t.Fatalf("ComputeClawID B failed: %v", err)
	}

	if idA == idB {
		t.Fatalf("expected different IDs for different files, got %q", idA)
	}
}

func validHeader() Header {
	return Header{
		SchemaVersion: SchemaVersionV1,
		Name:          "demo-openclaw",
		CreatedAtUTC:  time.Date(2026, time.February, 10, 0, 0, 0, 0, time.UTC),
		Payload: Payload{
			FSType: payloadFSTypeSquashFS,
			Offset: 4096,
			Size:   123456789,
			SHA256: testSHA256,
		},
		Spec: RuntimeSpec{
			BaseImage: BaseImage{
				Ref:    "ubuntu:24.04",
				URL:    "https://example.com/base.img",
				SHA256: testSHA256,
			},
			Layers: []Layer{
				{
					Ref:    "xfce",
					URL:    "https://example.com/xfce.qcow2",
					SHA256: testSHA256,
				},
			},
			OpenClaw: OpenClawSpec{
				InstallRoot:     "/claw",
				ModelPrimary:    "openai/gpt-5",
				GatewayAuthMode: "token",
				RequiredEnv:     []string{"OPENAI_API_KEY", "OPENCLAW_GATEWAY_TOKEN"},
				OptionalEnv:     []string{"DISCORD_TOKEN", "TELEGRAM_TOKEN"},
			},
		},
	}
}
