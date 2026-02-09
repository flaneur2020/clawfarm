package images

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

func SupportedRefs() []string {
	return []string{"ubuntu:24.04"}
}

type UbuntuRef struct {
	Original string
	Version  string
	Codename string
	Date     string
	Arch     string
}

func ParseUbuntuRef(ref string) (UbuntuRef, error) {
	if !strings.HasPrefix(ref, "ubuntu:") {
		return UbuntuRef{}, fmt.Errorf("unsupported image ref %q: only ubuntu:<version> is currently supported", ref)
	}

	body := strings.TrimPrefix(ref, "ubuntu:")
	parts := strings.SplitN(body, "@", 2)
	channel := parts[0]
	date := ""
	if len(parts) == 2 {
		date = parts[1]
		if !regexp.MustCompile(`^[0-9]{8}$`).MatchString(date) {
			return UbuntuRef{}, fmt.Errorf("invalid pinned date %q: expected YYYYMMDD", date)
		}
	}

	version, codename, err := normalizeUbuntuChannel(channel)
	if err != nil {
		return UbuntuRef{}, err
	}

	arch, err := hostArch()
	if err != nil {
		return UbuntuRef{}, err
	}

	return UbuntuRef{
		Original: ref,
		Version:  version,
		Codename: codename,
		Date:     date,
		Arch:     arch,
	}, nil
}

func (r UbuntuRef) ImageDirName() string {
	name := strings.ReplaceAll(r.Original, ":", "_")
	name = strings.ReplaceAll(name, "@", "_")
	name = strings.ReplaceAll(name, "/", "_")
	return name
}

func (r UbuntuRef) BaseImageURL() string {
	if r.Date == "" {
		return fmt.Sprintf("https://cloud-images.ubuntu.com/releases/%s/release/ubuntu-%s-server-cloudimg-%s.img", r.Codename, r.Version, r.Arch)
	}
	return fmt.Sprintf("https://cloud-images.ubuntu.com/%s/%s/%s-server-cloudimg-%s.img", r.Codename, r.Date, r.Codename, r.Arch)
}

func normalizeUbuntuChannel(channel string) (string, string, error) {
	channel = strings.TrimSpace(channel)
	switch channel {
	case "24.04", "noble":
		return "24.04", "noble", nil
	default:
		if regexp.MustCompile(`^[0-9]{2}\.[0-9]{2}$`).MatchString(channel) {
			return "", "", fmt.Errorf("unsupported ubuntu version %q", channel)
		}
		return "", "", fmt.Errorf("unsupported ubuntu channel %q", channel)
	}
}

func hostArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	default:
		return "", fmt.Errorf("unsupported host architecture %q", runtime.GOARCH)
	}
}
