package vm

import (
	"strings"
	"testing"
)

func TestNormalizePortForwards(t *testing.T) {
	forwards, err := normalizePortForwards(18789, 18789, []PortMapping{{HostPort: 8080, GuestPort: 80}, {HostPort: 18789, GuestPort: 18789}})
	if err != nil {
		t.Fatalf("normalizePortForwards failed: %v", err)
	}
	if len(forwards) != 2 {
		t.Fatalf("unexpected forward count: %d", len(forwards))
	}
	if forwards[0].HostPort != 18789 || forwards[0].GuestPort != 18789 {
		t.Fatalf("unexpected gateway mapping: %+v", forwards[0])
	}
	if forwards[1].HostPort != 8080 || forwards[1].GuestPort != 80 {
		t.Fatalf("unexpected publish mapping: %+v", forwards[1])
	}
}

func TestNormalizePortForwardsRejectsConflict(t *testing.T) {
	_, err := normalizePortForwards(18789, 18789, []PortMapping{{HostPort: 8080, GuestPort: 80}, {HostPort: 8080, GuestPort: 81}})
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	if !strings.Contains(err.Error(), "duplicate host port") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildCloudInitUserData(t *testing.T) {
	spec := StartSpec{GatewayGuestPort: 18789, OpenClawPackage: "openclaw@latest"}
	userData := buildCloudInitUserData(spec)

	for _, expected := range []string{
		"#cloud-config",
		"/usr/local/bin/vclaw-bootstrap.sh",
		"npm install -g openclaw@latest",
		"openclaw gateway --allow-unconfigured --port 18789",
	} {
		if !strings.Contains(userData, expected) {
			t.Fatalf("cloud-init user-data missing %q", expected)
		}
	}
}

func TestIndentForCloudConfig(t *testing.T) {
	content := "line1\nline2\n"
	indented := indentForCloudConfig(content, 4)
	if indented != "    line1\n    line2" {
		t.Fatalf("unexpected indent result: %q", indented)
	}
}
