use std::ffi::{CStr, CString, c_char, c_int};
use std::path::Path;

use anyhow::{Context, Result, anyhow, bail};
use libloading::{Library, Symbol};

type KRunCreateCtxFn = unsafe extern "C" fn() -> i32;
type KRunFreeCtxFn = unsafe extern "C" fn(u32) -> i32;
type KRunSetVmConfigFn = unsafe extern "C" fn(u32, u8, u32) -> i32;
type KRunSetRootFn = unsafe extern "C" fn(u32, *const c_char) -> i32;
type KRunAddVirtiofsFn = unsafe extern "C" fn(u32, *const c_char, *const c_char) -> i32;
type KRunSetPortMapFn = unsafe extern "C" fn(u32, *const *const c_char) -> i32;
type KRunSetWorkdirFn = unsafe extern "C" fn(u32, *const c_char) -> i32;
type KRunSetExecFn =
    unsafe extern "C" fn(u32, *const c_char, *const *const c_char, *const *const c_char) -> i32;
type KRunStartEnterFn = unsafe extern "C" fn(u32) -> i32;
type KRunAddDisk2Fn = unsafe extern "C" fn(u32, *const c_char, *const c_char, u32, bool) -> i32;
type KRunSetRootDiskRemountFn =
    unsafe extern "C" fn(u32, *const c_char, *const c_char, *const c_char) -> i32;

struct LibKrunFns {
    create_ctx: KRunCreateCtxFn,
    free_ctx: KRunFreeCtxFn,
    set_vm_config: KRunSetVmConfigFn,
    set_root: KRunSetRootFn,
    add_virtiofs: KRunAddVirtiofsFn,
    set_port_map: KRunSetPortMapFn,
    set_workdir: KRunSetWorkdirFn,
    set_exec: KRunSetExecFn,
    start_enter: KRunStartEnterFn,
    add_disk2: Option<KRunAddDisk2Fn>,
    set_root_disk_remount: Option<KRunSetRootDiskRemountFn>,
}

pub struct LibKrun {
    _lib: Library,
    fns: LibKrunFns,
}

#[derive(Debug, Clone)]
pub struct ExecSpec {
    pub exec_path: String,
    pub argv: Vec<String>,
    pub env: Vec<String>,
    pub workdir: String,
}

#[derive(Debug, Clone, Copy)]
pub enum DiskImageFormat {
    Raw,
    Qcow2,
    Vmdk,
}

impl DiskImageFormat {
    fn to_krun_constant(self) -> u32 {
        match self {
            Self::Raw => 0,
            Self::Qcow2 => 1,
            Self::Vmdk => 2,
        }
    }
}

#[derive(Debug, Clone)]
pub enum RootSpec {
    VirtioFs {
        rootfs: String,
    },
    DiskImage {
        disk_path: String,
        disk_format: DiskImageFormat,
        read_only: bool,
        root_device: String,
        root_fstype: Option<String>,
        root_options: Option<String>,
    },
}

#[derive(Debug, Clone)]
pub struct RunSpec {
    pub cpus: u8,
    pub memory_mib: u32,
    pub root: RootSpec,
    pub virtiofs_mounts: Vec<(String, String)>,
    pub port_map: Vec<(u16, u16)>,
    pub exec: ExecSpec,
}

impl LibKrun {
    pub fn load() -> Result<Self> {
        let candidates = [
            std::env::var("LIBKRUN_PATH").ok(),
            Some("libkrun.so".to_string()),
            Some("libkrun.dylib".to_string()),
            Some("libkrun-efi.so".to_string()),
            Some("libkrun-efi.dylib".to_string()),
        ];

        let mut last_error = None;
        for candidate in candidates.into_iter().flatten() {
            let lib = unsafe { Library::new(&candidate) };
            match lib {
                Ok(lib) => {
                    let fns = unsafe { load_symbols(&lib) }
                        .with_context(|| format!("failed to load symbols from {candidate}"))?;
                    return Ok(Self { _lib: lib, fns });
                }
                Err(err) => {
                    last_error = Some(format!("{candidate}: {err}"));
                }
            }
        }

        bail!(
            "unable to load libkrun (set LIBKRUN_PATH if needed); last error: {}",
            last_error.unwrap_or_else(|| "unknown".to_string())
        );
    }

    pub fn run(&self, spec: &RunSpec) -> Result<()> {
        match &spec.root {
            RootSpec::VirtioFs { rootfs } => {
                if !Path::new(rootfs).exists() {
                    bail!("rootfs path does not exist: {rootfs}");
                }
            }
            RootSpec::DiskImage { disk_path, .. } => {
                if !Path::new(disk_path).exists() {
                    bail!("disk image path does not exist: {disk_path}");
                }
            }
        }

        let ctx_id = unsafe { (self.fns.create_ctx)() };
        if ctx_id < 0 {
            bail!("krun_create_ctx failed with {ctx_id}");
        }
        let ctx_id = ctx_id as u32;

        let result = (|| {
            call_krun(
                unsafe { (self.fns.set_vm_config)(ctx_id, spec.cpus, spec.memory_mib) },
                "krun_set_vm_config",
            )?;

            match &spec.root {
                RootSpec::VirtioFs { rootfs } => {
                    let c_root =
                        CString::new(rootfs.as_str()).context("rootfs contains null byte")?;
                    call_krun(
                        unsafe { (self.fns.set_root)(ctx_id, c_root.as_ptr()) },
                        "krun_set_root",
                    )?;
                }
                RootSpec::DiskImage {
                    disk_path,
                    disk_format,
                    read_only,
                    root_device,
                    root_fstype,
                    root_options,
                } => {
                    let add_disk2 = self.fns.add_disk2.ok_or_else(|| {
                        anyhow!("libkrun missing krun_add_disk2; BLK support may be disabled")
                    })?;
                    let set_root_disk_remount = self.fns.set_root_disk_remount.ok_or_else(|| {
                        anyhow!("libkrun missing krun_set_root_disk_remount; BLK support may be disabled")
                    })?;

                    let block_id =
                        CString::new("rootdisk").context("internal block id contains null")?;
                    let c_disk =
                        CString::new(disk_path.as_str()).context("disk path contains null byte")?;

                    call_krun(
                        unsafe {
                            add_disk2(
                                ctx_id,
                                block_id.as_ptr(),
                                c_disk.as_ptr(),
                                disk_format.to_krun_constant(),
                                *read_only,
                            )
                        },
                        "krun_add_disk2",
                    )?;

                    let c_device = CString::new(root_device.as_str())
                        .context("root device contains null byte")?;
                    let c_fstype = root_fstype
                        .as_ref()
                        .map(|value| CString::new(value.as_str()))
                        .transpose()
                        .context("root fstype contains null byte")?;
                    let c_options = root_options
                        .as_ref()
                        .map(|value| CString::new(value.as_str()))
                        .transpose()
                        .context("root options contains null byte")?;

                    let fstype_ptr = c_fstype
                        .as_ref()
                        .map_or(std::ptr::null(), |value| value.as_ptr());
                    let options_ptr = c_options
                        .as_ref()
                        .map_or(std::ptr::null(), |value| value.as_ptr());

                    call_krun(
                        unsafe {
                            set_root_disk_remount(
                                ctx_id,
                                c_device.as_ptr(),
                                fstype_ptr,
                                options_ptr,
                            )
                        },
                        "krun_set_root_disk_remount",
                    )?;
                }
            }

            for (tag, host_path) in &spec.virtiofs_mounts {
                let c_tag = CString::new(tag.as_str())
                    .with_context(|| format!("virtiofs tag contains null byte: {tag}"))?;
                let c_path = CString::new(host_path.as_str())
                    .with_context(|| format!("virtiofs path contains null byte: {host_path}"))?;
                call_krun(
                    unsafe { (self.fns.add_virtiofs)(ctx_id, c_tag.as_ptr(), c_path.as_ptr()) },
                    "krun_add_virtiofs",
                )?;
            }

            let port_map = make_port_map(&spec.port_map)?;
            call_krun(
                unsafe { (self.fns.set_port_map)(ctx_id, port_map.as_ptr()) },
                "krun_set_port_map",
            )?;

            let workdir =
                CString::new(spec.exec.workdir.as_str()).context("workdir contains null byte")?;
            call_krun(
                unsafe { (self.fns.set_workdir)(ctx_id, workdir.as_ptr()) },
                "krun_set_workdir",
            )?;

            let exec_path = CString::new(spec.exec.exec_path.as_str())
                .context("exec path contains null byte")?;
            let argv = make_c_array(&spec.exec.argv).context("building argv failed")?;
            let env = make_c_array(&spec.exec.env).context("building env failed")?;

            call_krun(
                unsafe {
                    (self.fns.set_exec)(ctx_id, exec_path.as_ptr(), argv.as_ptr(), env.as_ptr())
                },
                "krun_set_exec",
            )?;

            call_krun(
                unsafe { (self.fns.start_enter)(ctx_id) },
                "krun_start_enter",
            )?;

            Ok(())
        })();

        let _ = unsafe { (self.fns.free_ctx)(ctx_id) };
        result
    }
}

unsafe fn load_symbols(lib: &Library) -> Result<LibKrunFns> {
    let create_ctx: Symbol<'_, KRunCreateCtxFn> = unsafe { lib.get(b"krun_create_ctx") }?;
    let free_ctx: Symbol<'_, KRunFreeCtxFn> = unsafe { lib.get(b"krun_free_ctx") }?;
    let set_vm_config: Symbol<'_, KRunSetVmConfigFn> = unsafe { lib.get(b"krun_set_vm_config") }?;
    let set_root: Symbol<'_, KRunSetRootFn> = unsafe { lib.get(b"krun_set_root") }?;
    let add_virtiofs: Symbol<'_, KRunAddVirtiofsFn> = unsafe { lib.get(b"krun_add_virtiofs") }?;
    let set_port_map: Symbol<'_, KRunSetPortMapFn> = unsafe { lib.get(b"krun_set_port_map") }?;
    let set_workdir: Symbol<'_, KRunSetWorkdirFn> = unsafe { lib.get(b"krun_set_workdir") }?;
    let set_exec: Symbol<'_, KRunSetExecFn> = unsafe { lib.get(b"krun_set_exec") }?;
    let start_enter: Symbol<'_, KRunStartEnterFn> = unsafe { lib.get(b"krun_start_enter") }?;

    let add_disk2 = unsafe { lib.get::<KRunAddDisk2Fn>(b"krun_add_disk2") }
        .map(|symbol| *symbol)
        .ok();
    let set_root_disk_remount =
        unsafe { lib.get::<KRunSetRootDiskRemountFn>(b"krun_set_root_disk_remount") }
            .map(|symbol| *symbol)
            .ok();

    Ok(LibKrunFns {
        create_ctx: *create_ctx,
        free_ctx: *free_ctx,
        set_vm_config: *set_vm_config,
        set_root: *set_root,
        add_virtiofs: *add_virtiofs,
        set_port_map: *set_port_map,
        set_workdir: *set_workdir,
        set_exec: *set_exec,
        start_enter: *start_enter,
        add_disk2,
        set_root_disk_remount,
    })
}

fn make_c_array(items: &[String]) -> Result<CStringArray> {
    let mut owned = Vec::with_capacity(items.len());
    let mut pointers = Vec::with_capacity(items.len() + 1);
    for item in items {
        let c = CString::new(item.as_str())
            .with_context(|| format!("string contains null byte: {item}"))?;
        pointers.push(c.as_ptr());
        owned.push(c);
    }
    pointers.push(std::ptr::null());

    Ok(CStringArray {
        _owned: owned,
        pointers,
    })
}

fn make_port_map(pairs: &[(u16, u16)]) -> Result<CStringArray> {
    let strings = pairs
        .iter()
        .map(|(host, guest)| format!("{host}:{guest}"))
        .collect::<Vec<_>>();
    make_c_array(&strings)
}

struct CStringArray {
    _owned: Vec<CString>,
    pointers: Vec<*const c_char>,
}

impl CStringArray {
    fn as_ptr(&self) -> *const *const c_char {
        self.pointers.as_ptr()
    }
}

fn call_krun(code: c_int, name: &str) -> Result<()> {
    if code == 0 {
        Ok(())
    } else {
        Err(anyhow!("{name} failed with {code}"))
    }
}

pub fn detect_libkrun_version(path: Option<&str>) -> Result<String> {
    let lib = match path {
        Some(custom) => unsafe { Library::new(custom) }
            .with_context(|| format!("failed to open libkrun at {custom}"))?,
        None => {
            for candidate in [
                "libkrun.so",
                "libkrun.dylib",
                "libkrun-efi.so",
                "libkrun-efi.dylib",
            ] {
                if let Ok(lib) = unsafe { Library::new(candidate) } {
                    let version = unsafe {
                        let symbol: Symbol<'_, unsafe extern "C" fn() -> *const c_char> = lib
                            .get(b"krun_get_version")
                            .context("krun_get_version symbol missing")?;
                        symbol
                    };
                    let ptr = unsafe { version() };
                    if ptr.is_null() {
                        return Ok("unknown".to_string());
                    }
                    let c_str = unsafe { CStr::from_ptr(ptr) };
                    return Ok(c_str.to_string_lossy().to_string());
                }
            }
            bail!("unable to load libkrun to detect version")
        }
    };

    let version = unsafe {
        let symbol: Symbol<'_, unsafe extern "C" fn() -> *const c_char> = lib
            .get(b"krun_get_version")
            .context("krun_get_version symbol missing")?;
        symbol
    };

    let ptr = unsafe { version() };
    if ptr.is_null() {
        return Ok("unknown".to_string());
    }
    let c_str = unsafe { CStr::from_ptr(ptr) };
    Ok(c_str.to_string_lossy().to_string())
}
