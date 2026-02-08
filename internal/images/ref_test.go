package images

import (
	"runtime"
	"testing"
)

func TestParseUbuntuRefRelease(t *testing.T) {
	ref, err := ParseUbuntuRef("ubuntu:24.04")
	if err != nil {
		t.Fatalf("ParseUbuntuRef failed: %v", err)
	}
	if ref.Version != "24.04" {
		t.Fatalf("unexpected version: %s", ref.Version)
	}
	if ref.Codename != "noble" {
		t.Fatalf("unexpected codename: %s", ref.Codename)
	}
	if ref.Arch != runtime.GOARCH {
		t.Fatalf("unexpected arch: %s", ref.Arch)
	}
	if ref.Date != "" {
		t.Fatalf("expected empty date, got %q", ref.Date)
	}
}

func TestParseUbuntuRefPinnedDate(t *testing.T) {
	ref, err := ParseUbuntuRef("ubuntu:24.04@20260131")
	if err != nil {
		t.Fatalf("ParseUbuntuRef failed: %v", err)
	}
	if ref.Date != "20260131" {
		t.Fatalf("unexpected date: %q", ref.Date)
	}
	want := "https://cloud-images.ubuntu.com/noble/20260131/noble-server-cloudimg-" + runtime.GOARCH + ".img"
	if got := ref.BaseImageURL(); got != want {
		t.Fatalf("unexpected base image url: %s", got)
	}
}

func TestParseUbuntuRefErrors(t *testing.T) {
	cases := []string{
		"debian:12",
		"ubuntu:22.04",
		"ubuntu:24.04@2026-01-31",
	}

	for _, value := range cases {
		if _, err := ParseUbuntuRef(value); err == nil {
			t.Fatalf("expected parse error for %q", value)
		}
	}
}
