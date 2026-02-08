use std::path::Path;

use libkrun_sys::detect_libkrun_version;

#[derive(Debug, Clone)]
pub struct DoctorReport {
    pub libkrun_loadable: bool,
    pub libkrun_version: Option<String>,
    pub rootfs_exists: bool,
    pub rootfs_path: String,
    pub issues: Vec<String>,
}

pub fn run_doctor(rootfs_path: &Path) -> DoctorReport {
    let mut issues = Vec::new();
    let rootfs_exists = rootfs_path.exists();
    if !rootfs_exists {
        issues.push(format!(
            "rootfs not found: {} (run image build/import first)",
            rootfs_path.display()
        ));
    }

    let libkrun_version =
        match detect_libkrun_version(std::env::var("LIBKRUN_PATH").ok().as_deref()) {
            Ok(version) => Some(version),
            Err(err) => {
                issues.push(format!("unable to load libkrun: {err}"));
                None
            }
        };

    DoctorReport {
        libkrun_loadable: libkrun_version.is_some(),
        libkrun_version,
        rootfs_exists,
        rootfs_path: rootfs_path.display().to_string(),
        issues,
    }
}
