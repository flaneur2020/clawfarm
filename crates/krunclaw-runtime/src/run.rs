use std::path::{Path, PathBuf};

use anyhow::{Context, Result, bail};
use libkrun_sys::{DiskImageFormat, ExecSpec, LibKrun, RootSpec, RunSpec};

#[derive(Debug, Clone)]
pub struct PublishSpec {
    pub host_port: u16,
    pub guest_port: u16,
}

#[derive(Debug, Clone, Copy)]
pub enum DiskFormatArg {
    Auto,
    Raw,
    Qcow2,
    Vmdk,
}

#[derive(Debug, Clone)]
pub struct RunConfig {
    pub cpus: u8,
    pub memory_mib: u32,
    pub disk_image: PathBuf,
    pub disk_format: DiskFormatArg,
    pub root_device: String,
    pub root_fstype: Option<String>,
    pub root_options: Option<String>,
    pub workspace_dir: PathBuf,
    pub state_dir: PathBuf,
    pub gateway_port: u16,
    pub additional_publish: Vec<PublishSpec>,
}

pub fn default_state_dir() -> Result<PathBuf> {
    let state_base = dirs::data_dir().context("cannot resolve data dir")?;
    Ok(state_base.join("krunclaw").join("state").join("openclaw"))
}

pub fn parse_publish(publish: &str) -> Result<PublishSpec> {
    let parts = publish.split(':').collect::<Vec<_>>();
    if parts.len() != 2 {
        bail!("invalid publish format '{publish}', expected host:guest");
    }
    let host_port: u16 = parts[0]
        .parse()
        .with_context(|| format!("invalid host port in '{publish}'"))?;
    let guest_port: u16 = parts[1]
        .parse()
        .with_context(|| format!("invalid guest port in '{publish}'"))?;
    Ok(PublishSpec {
        host_port,
        guest_port,
    })
}

pub fn parse_disk_format(value: &str) -> Result<DiskFormatArg> {
    match value.to_ascii_lowercase().as_str() {
        "auto" => Ok(DiskFormatArg::Auto),
        "raw" => Ok(DiskFormatArg::Raw),
        "qcow2" | "qcow" => Ok(DiskFormatArg::Qcow2),
        "vmdk" => Ok(DiskFormatArg::Vmdk),
        other => bail!("unsupported disk format '{other}', expected auto|raw|qcow2|vmdk"),
    }
}

pub fn build_run_spec(config: &RunConfig) -> Result<RunSpec> {
    ensure_path_exists(&config.disk_image, "disk image")?;
    ensure_path_exists(&config.workspace_dir, "workspace")?;

    std::fs::create_dir_all(&config.state_dir).with_context(|| {
        format!(
            "failed to create state dir at {}",
            config.state_dir.display()
        )
    })?;

    let disk_format = resolve_disk_format(config.disk_format, &config.disk_image)?;

    let mut port_map = vec![(config.gateway_port, config.gateway_port)];
    for publish in &config.additional_publish {
        if (publish.host_port, publish.guest_port) != (config.gateway_port, config.gateway_port) {
            port_map.push((publish.host_port, publish.guest_port));
        }
    }

    let guest_config_path = "/tmp/krunclaw-openclaw.json";
    let entrypoint_script = format!(
        r#"set -eu
mkdir -p /workspace /root/.openclaw
mount -t virtiofs workspace /workspace
mount -t virtiofs state /root/.openclaw

if ! command -v openclaw >/dev/null 2>&1; then
  if ! command -v node >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends ca-certificates curl gnupg bash
      curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
      apt-get install -y --no-install-recommends nodejs
    else
      echo "error: node is missing and apt-get unavailable; cannot auto-install openclaw" >&2
      exit 1
    fi
  fi

  npm install -g openclaw@latest
fi

cat > {guest_config_path} <<'JSON'
{{
  "agents": {{
    "defaults": {{
      "workspace": "/workspace"
    }}
  }},
  "gateway": {{
    "port": {gateway_port}
  }}
}}
JSON

export HOME=/root
export OPENCLAW_CONFIG_PATH={guest_config_path}
exec openclaw gateway --allow-unconfigured --port {gateway_port}
"#,
        gateway_port = config.gateway_port,
    );

    let exec = ExecSpec {
        exec_path: "/bin/sh".to_string(),
        argv: vec!["-c".to_string(), entrypoint_script],
        env: vec![
            "HOME=/root".to_string(),
            "USER=root".to_string(),
            "SHELL=/bin/sh".to_string(),
            "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin".to_string(),
        ],
        workdir: "/".to_string(),
    };

    Ok(RunSpec {
        cpus: config.cpus,
        memory_mib: config.memory_mib,
        root: RootSpec::DiskImage {
            disk_path: config.disk_image.display().to_string(),
            disk_format,
            read_only: false,
            root_device: config.root_device.clone(),
            root_fstype: config.root_fstype.clone(),
            root_options: config.root_options.clone(),
        },
        virtiofs_mounts: vec![
            (
                "workspace".to_string(),
                config.workspace_dir.display().to_string(),
            ),
            ("state".to_string(), config.state_dir.display().to_string()),
        ],
        port_map,
        exec,
    })
}

pub fn run_openclaw(config: &RunConfig) -> Result<()> {
    let spec = build_run_spec(config)?;
    let krun = LibKrun::load().context("failed to load libkrun")?;
    krun.run(&spec)
}

fn resolve_disk_format(format: DiskFormatArg, disk_path: &Path) -> Result<DiskImageFormat> {
    match format {
        DiskFormatArg::Raw => Ok(DiskImageFormat::Raw),
        DiskFormatArg::Qcow2 => Ok(DiskImageFormat::Qcow2),
        DiskFormatArg::Vmdk => Ok(DiskImageFormat::Vmdk),
        DiskFormatArg::Auto => guess_disk_format(disk_path),
    }
}

fn guess_disk_format(disk_path: &Path) -> Result<DiskImageFormat> {
    let name = disk_path
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or_default()
        .to_ascii_lowercase();

    if name.ends_with(".qcow2") || name.ends_with(".img") {
        return Ok(DiskImageFormat::Qcow2);
    }
    if name.ends_with(".vmdk") {
        return Ok(DiskImageFormat::Vmdk);
    }
    if name.ends_with(".raw") {
        return Ok(DiskImageFormat::Raw);
    }

    bail!(
        "cannot auto-detect disk format from '{}'; pass --disk-format explicitly",
        disk_path.display()
    )
}

fn ensure_path_exists(path: &Path, label: &str) -> Result<()> {
    if path.exists() {
        Ok(())
    } else {
        bail!("{} path does not exist: {}", label, path.display())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn publish_parser_accepts_valid() {
        let parsed = parse_publish("18793:18793").expect("publish should parse");
        assert_eq!(parsed.host_port, 18793);
        assert_eq!(parsed.guest_port, 18793);
    }

    #[test]
    fn publish_parser_rejects_invalid() {
        let error = parse_publish("18793").expect_err("publish should fail");
        assert!(error.to_string().contains("invalid publish format"));
    }

    #[test]
    fn disk_format_parser_accepts_known_values() {
        assert!(matches!(parse_disk_format("auto"), Ok(DiskFormatArg::Auto)));
        assert!(matches!(parse_disk_format("raw"), Ok(DiskFormatArg::Raw)));
        assert!(matches!(
            parse_disk_format("qcow2"),
            Ok(DiskFormatArg::Qcow2)
        ));
        assert!(matches!(parse_disk_format("vmdk"), Ok(DiskFormatArg::Vmdk)));
    }
}
