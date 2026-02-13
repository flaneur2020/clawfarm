package cloudinitbuilder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type CloudInitBuilder struct {
	InstanceID          string
	InstanceDir         string
	GatewayGuestPort    int
	OpenClawPackage     string
	OpenClawConfig      string
	OpenClawEnvironment map[string]string
	SSHAuthorizedKeys   []string
	VolumeMounts        []VolumeMount
	CloudInitProvision  []string
}

type VolumeMount struct {
	Tag       string
	GuestPath string
}

func NewCloudInitBuilder() *CloudInitBuilder {
	return &CloudInitBuilder{}
}

func (builder *CloudInitBuilder) WithInstance(instanceID string, instanceDir string) *CloudInitBuilder {
	builder.InstanceID = instanceID
	builder.InstanceDir = instanceDir
	return builder
}

func (builder *CloudInitBuilder) WithGatewayGuestPort(gatewayGuestPort int) *CloudInitBuilder {
	builder.GatewayGuestPort = gatewayGuestPort
	return builder
}

func (builder *CloudInitBuilder) WithOpenClawPackage(openClawPackage string) *CloudInitBuilder {
	builder.OpenClawPackage = openClawPackage
	return builder
}

func (builder *CloudInitBuilder) WithOpenClawConfig(openClawConfig string) *CloudInitBuilder {
	builder.OpenClawConfig = openClawConfig
	return builder
}

func (builder *CloudInitBuilder) WithOpenClawEnvironment(openClawEnvironment map[string]string) *CloudInitBuilder {
	if len(openClawEnvironment) == 0 {
		builder.OpenClawEnvironment = nil
		return builder
	}
	copied := make(map[string]string, len(openClawEnvironment))
	for key, value := range openClawEnvironment {
		copied[key] = value
	}
	builder.OpenClawEnvironment = copied
	return builder
}

func (builder *CloudInitBuilder) WithSSHAuthorizedKeys(sshAuthorizedKeys []string) *CloudInitBuilder {
	builder.SSHAuthorizedKeys = append([]string(nil), sshAuthorizedKeys...)
	return builder
}

func (builder *CloudInitBuilder) WithCloudInitProvision(cloudInitProvision []string) *CloudInitBuilder {
	builder.CloudInitProvision = append([]string(nil), cloudInitProvision...)
	return builder
}

func (builder *CloudInitBuilder) WithVolumeMounts(volumeMounts []VolumeMount) *CloudInitBuilder {
	builder.VolumeMounts = append([]VolumeMount(nil), volumeMounts...)
	return builder
}

func (builder *CloudInitBuilder) CreateNoCloudSeedISO(outputPath string) error {
	seedDir := filepath.Join(builder.InstanceDir, "seed")
	if err := os.RemoveAll(seedDir); err != nil {
		return err
	}
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		return err
	}

	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", builder.InstanceID, builder.InstanceID)
	userData := builder.BuildCloudInitUserData()

	if err := os.WriteFile(filepath.Join(seedDir, "meta-data"), []byte(metaData), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(seedDir, "user-data"), []byte(userData), 0o644); err != nil {
		return err
	}

	if _, err := exec.LookPath("hdiutil"); err != nil {
		return fmt.Errorf("hdiutil is required to build cloud-init seed ISO")
	}
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	command := exec.Command(
		"hdiutil", "makehybrid", "-quiet",
		"-o", outputPath,
		seedDir,
		"-iso",
		"-joliet",
		"-default-volume-name", "cidata",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build seed iso: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (builder *CloudInitBuilder) BuildCloudInitUserData() string {
	bootstrapScript := builder.BuildBootstrapScript()
	sshAuthorizedKeysSection := renderSSHAuthorizedKeysSection(builder.SSHAuthorizedKeys)
	return fmt.Sprintf(`#cloud-config
package_update: false
users:
  - default
  - name: claw
    gecos: Claw User
    shell: /bin/bash
    groups: [sudo]
    sudo: ["ALL=(ALL) NOPASSWD:ALL"]
    lock_passwd: true
%s
write_files:
  - path: /usr/local/bin/clawfarm-bootstrap.sh
    permissions: "0755"
    owner: root:root
    content: |
%s
runcmd:
  - [ bash, -lc, "/usr/local/bin/clawfarm-bootstrap.sh > /var/log/clawfarm-bootstrap.log 2>&1" ]
`, sshAuthorizedKeysSection, IndentForCloudConfig(bootstrapScript, 6))
}

func (builder *CloudInitBuilder) BuildBootstrapScript() string {
	packageName := builder.OpenClawPackage
	if packageName == "" {
		packageName = "openclaw@latest"
	}

	openClawConfig := strings.TrimSpace(builder.OpenClawConfig)
	if openClawConfig == "" {
		openClawConfig = fmt.Sprintf(`{
  "agents": {
    "defaults": {
      "workspace": "/workspace"
    }
  },
  "gateway": {
    "mode": "local",
    "port": %d
  }
}`, builder.GatewayGuestPort)
	}

	openClawEnv := renderOpenClawEnvironment(builder.OpenClawEnvironment)
	sshBootstrapScript := renderSSHBootstrapScript(builder.SSHAuthorizedKeys)
	volumeMountScript := renderVolumeMountScript(builder.VolumeMounts)
	provisionScript := renderProvisionScript(builder.CloudInitProvision)

	return fmt.Sprintf(`#!/usr/bin/env bash
set -euxo pipefail

modprobe 9p 2>/dev/null || true
modprobe 9pnet 2>/dev/null || true
modprobe 9pnet_virtio 2>/dev/null || true

mkdir -p /workspace /root/.openclaw /etc/clawfarm

if ! id -u claw >/dev/null 2>&1; then
  useradd -m -s /bin/bash claw
fi
usermod -aG sudo claw || true
install -d -m 0755 -o claw -g claw /claw

%s

if ! mountpoint -q /workspace; then
  mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 workspace /workspace || true
fi
if ! mountpoint -q /root/.openclaw; then
  mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 state /root/.openclaw || true
fi
if ! mountpoint -q /claw; then
  mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 claw /claw || true
fi

%s

chown -R claw:claw /claw || true

cat >/etc/clawfarm/openclaw.json <<'CLAWFARM_OPENCLAW_JSON'
%s
CLAWFARM_OPENCLAW_JSON

cat >/etc/clawfarm/openclaw.env <<'CLAWFARM_OPENCLAW_ENV'
%s
CLAWFARM_OPENCLAW_ENV
chmod 0600 /etc/clawfarm/openclaw.env

cat >/usr/local/bin/clawfarm-gateway.sh <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

export HOME=/root
export OPENCLAW_CONFIG_PATH=/etc/clawfarm/openclaw.json
if [[ -f /etc/clawfarm/openclaw.env ]]; then
  set -a
  source /etc/clawfarm/openclaw.env
  set +a
fi

if command -v openclaw >/dev/null 2>&1; then
  exec openclaw gateway --allow-unconfigured --port %d
fi

exec /usr/bin/python3 -m http.server %d --directory /workspace
SCRIPT
chmod +x /usr/local/bin/clawfarm-gateway.sh

%s

cat >/etc/systemd/system/clawfarm-gateway.service <<'UNIT'
[Unit]
Description=clawfarm Gateway Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/clawfarm-gateway.sh
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now clawfarm-gateway.service

if ! command -v openclaw >/dev/null 2>&1; then
  (
    set +e
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends ca-certificates curl gnupg bash python3
    if ! command -v node >/dev/null 2>&1; then
      curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
      apt-get install -y --no-install-recommends nodejs
    fi
    npm install -g %s
    systemctl restart clawfarm-gateway.service
  ) >/var/log/clawfarm-openclaw-install.log 2>&1 &
fi

if [[ -x /usr/local/bin/clawfarm-provision.sh ]]; then
  /usr/local/bin/clawfarm-provision.sh >/var/log/clawfarm-provision.log 2>&1
fi
`, sshBootstrapScript, volumeMountScript, openClawConfig, openClawEnv, builder.GatewayGuestPort, builder.GatewayGuestPort, provisionScript, packageName)
}

func renderSSHAuthorizedKeysSection(sshAuthorizedKeys []string) string {
	if len(sshAuthorizedKeys) == 0 {
		return ""
	}

	var sectionBuilder strings.Builder
	sectionBuilder.WriteString("    ssh_authorized_keys:\n")
	for _, key := range sshAuthorizedKeys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		sectionBuilder.WriteString("      - ")
		sectionBuilder.WriteString(yamlSingleQuote(trimmed))
		sectionBuilder.WriteString("\n")
	}
	return strings.TrimSuffix(sectionBuilder.String(), "\n")
}

func yamlSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func renderSSHBootstrapScript(sshAuthorizedKeys []string) string {
	if len(sshAuthorizedKeys) == 0 {
		return ""
	}

	return `if ! command -v sshd >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends openssh-server
fi

mkdir -p /run/sshd
if command -v systemctl >/dev/null 2>&1; then
  systemctl enable --now ssh || systemctl enable --now sshd || true
fi
service ssh start >/dev/null 2>&1 || service sshd start >/dev/null 2>&1 || true`
}

func renderVolumeMountScript(volumeMounts []VolumeMount) string {
	if len(volumeMounts) == 0 {
		return ""
	}

	var scriptBuilder strings.Builder
	for _, mount := range volumeMounts {
		tag := strings.TrimSpace(mount.Tag)
		guestPath := strings.TrimSpace(mount.GuestPath)
		if tag == "" || guestPath == "" {
			continue
		}
		quotedGuestPath := shellSingleQuote(guestPath)
		scriptBuilder.WriteString(fmt.Sprintf("install -d -m 0755 %s\n", quotedGuestPath))
		scriptBuilder.WriteString(fmt.Sprintf("if ! mountpoint -q %s; then\n", quotedGuestPath))
		scriptBuilder.WriteString(fmt.Sprintf("  mount -t 9p -o trans=virtio,version=9p2000.L,msize=262144 %s %s || true\n", tag, quotedGuestPath))
		scriptBuilder.WriteString("fi\n")
	}

	return strings.TrimSpace(scriptBuilder.String())
}

func renderProvisionScript(commands []string) string {
	if len(commands) == 0 {
		return ""
	}

	var scriptBuilder strings.Builder
	scriptBuilder.WriteString("cat >/usr/local/bin/clawfarm-provision.sh <<'PROVISION'\n")
	scriptBuilder.WriteString("#!/usr/bin/env bash\n")
	scriptBuilder.WriteString("set -euxo pipefail\n")
	scriptBuilder.WriteString("export HOME=/home/claw\n")
	scriptBuilder.WriteString("cd /claw\n")
	for _, command := range commands {
		trimmed := strings.TrimSpace(command)
		if trimmed == "" {
			continue
		}
		scriptBuilder.WriteString(trimmed)
		scriptBuilder.WriteString("\n")
	}
	scriptBuilder.WriteString("PROVISION\n")
	scriptBuilder.WriteString("chmod +x /usr/local/bin/clawfarm-provision.sh\n")
	scriptBuilder.WriteString("chown claw:claw /usr/local/bin/clawfarm-provision.sh\n")
	return scriptBuilder.String()
}

func renderOpenClawEnvironment(values map[string]string) string {
	if len(values) == 0 {
		return "# no extra environment overrides"
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("export %s=%s", key, shellSingleQuote(values[key])))
	}
	return strings.Join(lines, "\n")
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func IndentForCloudConfig(content string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	var result strings.Builder
	for _, line := range lines {
		result.WriteString(prefix)
		result.WriteString(line)
		result.WriteString("\n")
	}
	return strings.TrimSuffix(result.String(), "\n")
}
