use std::path::{Path, PathBuf};

use anyhow::{Context, Result, bail};
use libkrun_sys::{ExecSpec, LibKrun, RunSpec};

#[derive(Debug, Clone)]
pub struct PublishSpec {
    pub host_port: u16,
    pub guest_port: u16,
}

#[derive(Debug, Clone)]
pub struct RunConfig {
    pub cpus: u8,
    pub memory_mib: u32,
    pub rootfs: PathBuf,
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

pub fn build_run_spec(config: &RunConfig) -> Result<RunSpec> {
    ensure_path_exists(&config.rootfs, "rootfs")?;
    ensure_path_exists(&config.workspace_dir, "workspace")?;

    std::fs::create_dir_all(&config.state_dir).with_context(|| {
        format!(
            "failed to create state dir at {}",
            config.state_dir.display()
        )
    })?;

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
        rootfs: config.rootfs.display().to_string(),
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
}
