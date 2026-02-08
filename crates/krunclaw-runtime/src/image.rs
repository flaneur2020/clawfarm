use std::fs;
use std::path::PathBuf;
use std::process::Command;

use anyhow::{Context, Result, bail};

#[derive(Debug, Clone)]
pub struct ImageConfig {
    pub image: String,
    pub disk: Option<PathBuf>,
}

#[derive(Debug, Clone)]
pub struct ImageStatus {
    pub image: String,
    pub disk_path: PathBuf,
    pub exists: bool,
}

#[derive(Debug, Clone)]
pub struct FetchConfig {
    pub image: String,
    pub disk: Option<PathBuf>,
    pub url: Option<String>,
    pub ubuntu_date: Option<String>,
    pub arch: Option<String>,
    pub force: bool,
}

#[derive(Debug, Clone)]
struct ImageSource {
    url: String,
    sha256: Option<String>,
}

pub fn default_cache_dir() -> Result<PathBuf> {
    let cache = dirs::cache_dir().context("cannot resolve cache dir")?;
    Ok(cache.join("krunclaw").join("images"))
}

pub fn image_disk_path(image: &str) -> Result<PathBuf> {
    if image.trim().is_empty() {
        bail!("image name cannot be empty");
    }

    let mut safe = String::with_capacity(image.len());
    for character in image.chars() {
        if character.is_ascii_alphanumeric()
            || character == '-'
            || character == '_'
            || character == '.'
        {
            safe.push(character);
        } else {
            safe.push('_');
        }
    }
    Ok(default_cache_dir()?.join(safe).join("disk.img"))
}

pub fn inspect_image(config: &ImageConfig) -> Result<ImageStatus> {
    let disk_path = match &config.disk {
        Some(custom) => custom.clone(),
        None => image_disk_path(&config.image)?,
    };

    Ok(ImageStatus {
        image: config.image.clone(),
        exists: disk_path.exists(),
        disk_path,
    })
}

pub fn ensure_image(config: &ImageConfig) -> Result<ImageStatus> {
    let status = inspect_image(config)?;
    if status.exists {
        return Ok(status);
    }

    bail!(
        "disk image missing for '{}' at {}. Fetch one using: krunclaw image fetch --image {}",
        status.image,
        status.disk_path.display(),
        status.image
    );
}

pub fn fetch_ubuntu_image(config: &FetchConfig) -> Result<ImageStatus> {
    let status = inspect_image(&ImageConfig {
        image: config.image.clone(),
        disk: config.disk.clone(),
    })?;

    if status.exists && !config.force {
        bail!(
            "disk image already exists at {} (use --force to replace)",
            status.disk_path.display()
        );
    }

    if let Some(parent) = status.disk_path.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("failed to create {}", parent.display()))?;
    }

    let sources = resolve_sources(config)?;
    let temp_path = status.disk_path.with_extension("img.tmp");
    if temp_path.exists() {
        fs::remove_file(&temp_path)
            .with_context(|| format!("failed to remove {}", temp_path.display()))?;
    }

    let mut last_error = None;
    for source in &sources {
        match download_to_path(&source.url, &temp_path) {
            Ok(()) => {
                if let Some(digest) = &source.sha256 {
                    verify_sha256(&temp_path, digest).with_context(|| {
                        format!(
                            "sha256 verification failed for {} from {}",
                            temp_path.display(),
                            source.url
                        )
                    })?;
                }

                if status.disk_path.exists() {
                    fs::remove_file(&status.disk_path).with_context(|| {
                        format!(
                            "failed to remove existing disk at {}",
                            status.disk_path.display()
                        )
                    })?;
                }

                fs::rename(&temp_path, &status.disk_path).with_context(|| {
                    format!("failed to move image to {}", status.disk_path.display())
                })?;

                return inspect_image(&ImageConfig {
                    image: config.image.clone(),
                    disk: Some(status.disk_path.clone()),
                });
            }
            Err(err) => {
                last_error = Some(format!("{}: {err}", source.url));
            }
        }
    }

    let _ = fs::remove_file(&temp_path);
    bail!(
        "failed to fetch ubuntu image. attempted sources: {}. last error: {}",
        sources
            .iter()
            .map(|source| source.url.as_str())
            .collect::<Vec<_>>()
            .join(", "),
        last_error.unwrap_or_else(|| "unknown".to_string())
    );
}

fn resolve_sources(config: &FetchConfig) -> Result<Vec<ImageSource>> {
    if let Some(custom_url) = &config.url {
        return Ok(vec![ImageSource {
            url: custom_url.clone(),
            sha256: None,
        }]);
    }

    let arch = config
        .arch
        .clone()
        .unwrap_or_else(|| std::env::consts::ARCH.to_string());
    let suffix = ubuntu_suffix_for_arch(&arch)
        .ok_or_else(|| anyhow::anyhow!("unsupported arch '{}' for ubuntu cloud image", arch))?;

    if let Some(date) = &config.ubuntu_date {
        return Ok(vec![ImageSource {
            url: format!(
                "https://cloud-images.ubuntu.com/releases/noble/release-{date}/ubuntu-24.04-server-cloudimg-{suffix}.img"
            ),
            sha256: None,
        }]);
    }

    Ok(default_lima_ubuntu_sources_for_suffix(suffix))
}

fn default_lima_ubuntu_sources_for_suffix(suffix: &str) -> Vec<ImageSource> {
    let primary = match suffix {
        "amd64" => Some(ImageSource {
            url: "https://cloud-images.ubuntu.com/releases/noble/release-20251213/ubuntu-24.04-server-cloudimg-amd64.img"
                .to_string(),
            sha256: Some(
                "2b5f90ffe8180def601c021c874e55d8303e8bcbfc66fee2b94414f43ac5eb1f".to_string(),
            ),
        }),
        "arm64" => Some(ImageSource {
            url: "https://cloud-images.ubuntu.com/releases/noble/release-20251213/ubuntu-24.04-server-cloudimg-arm64.img"
                .to_string(),
            sha256: Some(
                "a40713938d74aaec811f74cb1fa8bfcb535d22e26b2a0ca1cc90ad9db898feb9".to_string(),
            ),
        }),
        _ => None,
    };

    let fallback = ImageSource {
        url: format!(
            "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-{suffix}.img"
        ),
        sha256: None,
    };

    match primary {
        Some(primary) => vec![primary, fallback],
        None => vec![fallback],
    }
}

fn ubuntu_suffix_for_arch(arch: &str) -> Option<&'static str> {
    match arch {
        "x86_64" | "amd64" => Some("amd64"),
        "aarch64" | "arm64" => Some("arm64"),
        "riscv64" => Some("riscv64"),
        "arm" | "armv7l" => Some("armhf"),
        "s390x" => Some("s390x"),
        "powerpc64le" | "ppc64le" => Some("ppc64el"),
        _ => None,
    }
}

fn download_to_path(url: &str, path: &PathBuf) -> Result<()> {
    let output = Command::new("curl")
        .arg("-fL")
        .arg("--retry")
        .arg("3")
        .arg("--retry-delay")
        .arg("2")
        .arg("-o")
        .arg(path)
        .arg(url)
        .output()
        .with_context(|| format!("failed to spawn curl for {url}"))?;

    if output.status.success() {
        Ok(())
    } else {
        let stderr = String::from_utf8_lossy(&output.stderr);
        bail!("curl failed for {url}: {}", stderr.trim())
    }
}

fn verify_sha256(path: &PathBuf, expected: &str) -> Result<()> {
    let output = Command::new("shasum")
        .arg("-a")
        .arg("256")
        .arg(path)
        .output()
        .with_context(|| format!("failed to spawn shasum for {}", path.display()))?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        bail!("shasum failed: {}", stderr.trim());
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    let actual = stdout.split_whitespace().next().unwrap_or_default();
    if actual.eq_ignore_ascii_case(expected) {
        Ok(())
    } else {
        bail!("expected sha256 {expected}, got {actual}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn image_name_is_sanitized() {
        let path = image_disk_path("default:ubuntu/24.04").expect("path");
        let rendered = path.to_string_lossy();
        assert!(rendered.contains("default_ubuntu_24.04"));
        assert!(rendered.ends_with("disk.img"));
    }

    #[test]
    fn ubuntu_arch_mapping_works() {
        assert_eq!(ubuntu_suffix_for_arch("x86_64"), Some("amd64"));
        assert_eq!(ubuntu_suffix_for_arch("aarch64"), Some("arm64"));
        assert_eq!(ubuntu_suffix_for_arch("ppc64le"), Some("ppc64el"));
    }

    #[test]
    fn ubuntu_sources_include_fallback() {
        let sources = default_lima_ubuntu_sources_for_suffix("amd64");
        assert_eq!(sources.len(), 2);
        assert!(sources[0].url.contains("release-20251213"));
        assert!(sources[1].url.contains("/release/"));
    }
}
