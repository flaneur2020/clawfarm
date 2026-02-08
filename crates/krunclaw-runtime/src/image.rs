use std::path::PathBuf;

use anyhow::{Context, Result, bail};

#[derive(Debug, Clone)]
pub struct ImageConfig {
    pub image: String,
    pub distro: Option<String>,
    pub rootfs: Option<PathBuf>,
}

#[derive(Debug, Clone)]
pub struct ImageStatus {
    pub image: String,
    pub rootfs_path: PathBuf,
    pub exists: bool,
}

pub fn default_cache_dir() -> Result<PathBuf> {
    let cache = dirs::cache_dir().context("cannot resolve cache dir")?;
    Ok(cache.join("krunclaw").join("images"))
}

pub fn image_rootfs_path(image: &str) -> Result<PathBuf> {
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
    Ok(default_cache_dir()?.join(safe).join("rootfs"))
}

pub fn inspect_image(config: &ImageConfig) -> Result<ImageStatus> {
    let rootfs_path = match &config.rootfs {
        Some(custom) => custom.clone(),
        None => image_rootfs_path(&config.image)?,
    };

    Ok(ImageStatus {
        image: config.image.clone(),
        exists: rootfs_path.exists(),
        rootfs_path,
    })
}

pub fn ensure_image(config: &ImageConfig) -> Result<ImageStatus> {
    let status = inspect_image(config)?;
    if status.exists {
        return Ok(status);
    }

    bail!(
        "rootfs missing for image '{}' at {}. Build/import flow is not implemented yet; create the rootfs path manually or pass --rootfs",
        status.image,
        status.rootfs_path.display()
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn image_name_is_sanitized() {
        let path = image_rootfs_path("default:ubuntu/24.04").expect("path");
        let rendered = path.to_string_lossy();
        assert!(rendered.contains("default_ubuntu_24.04"));
    }
}
