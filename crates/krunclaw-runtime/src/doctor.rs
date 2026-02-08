use std::path::Path;

use libkrun_sys::detect_libkrun_version;

#[derive(Debug, Clone)]
pub struct DoctorReport {
    pub libkrun_loadable: bool,
    pub libkrun_version: Option<String>,
    pub disk_exists: bool,
    pub disk_path: String,
    pub issues: Vec<String>,
}

pub fn run_doctor(disk_path: &Path) -> DoctorReport {
    let mut issues = Vec::new();
    let disk_exists = disk_path.exists();
    if !disk_exists {
        issues.push(format!(
            "disk image not found: {} (run `krunclaw image fetch` first)",
            disk_path.display()
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
        disk_exists,
        disk_path: disk_path.display().to_string(),
        issues,
    }
}
