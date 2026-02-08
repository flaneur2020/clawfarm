use std::path::PathBuf;

use anyhow::{Context, Result};
use clap::{Args, Parser, Subcommand};
use krunclaw_runtime::doctor::run_doctor;
use krunclaw_runtime::image::{
    FetchConfig, ImageConfig, ensure_image, fetch_ubuntu_image, image_disk_path, inspect_image,
};
use krunclaw_runtime::run::{
    RunConfig, default_state_dir, parse_disk_format, parse_publish, run_openclaw,
};

#[derive(Parser, Debug)]
#[command(
    name = "krunclaw",
    version,
    about = "Run full OpenClaw in a libkrun lightweight VM"
)]
struct Cli {
    #[arg(long, global = true)]
    verbose: bool,

    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand, Debug)]
enum Command {
    Run(RunCommand),
    Image(ImageCommand),
    Doctor(DoctorCommand),
}

#[derive(Args, Debug)]
struct RunCommand {
    #[arg(long, default_value_t = 2)]
    cpus: u8,

    #[arg(long = "memory-mib", default_value_t = 2048)]
    memory_mib: u32,

    #[arg(long, default_value_t = 18789)]
    port: u16,

    #[arg(long = "publish")]
    publish: Vec<String>,

    #[arg(long, default_value = "default")]
    image: String,

    #[arg(long)]
    disk: Option<PathBuf>,

    #[arg(long, default_value = "auto")]
    disk_format: String,

    #[arg(long, default_value = "/dev/vda1")]
    root_device: String,

    #[arg(long, default_value = "auto")]
    root_fstype: String,

    #[arg(long)]
    root_options: Option<String>,

    #[arg(long)]
    state_dir: Option<PathBuf>,

    #[arg(long)]
    auto_fetch_image: bool,

    #[arg(long)]
    image_url: Option<String>,

    #[arg(long)]
    ubuntu_date: Option<String>,

    #[arg(long)]
    arch: Option<String>,
}

#[derive(Args, Debug)]
struct DoctorCommand {
    #[arg(long, default_value = "default")]
    image: String,

    #[arg(long)]
    disk: Option<PathBuf>,
}

#[derive(Args, Debug)]
struct ImageCommand {
    #[command(subcommand)]
    action: ImageAction,
}

#[derive(Subcommand, Debug)]
enum ImageAction {
    Inspect(ImageInspect),
    Fetch(ImageFetch),
}

#[derive(Args, Debug)]
struct ImageInspect {
    #[arg(long, default_value = "default")]
    image: String,

    #[arg(long)]
    disk: Option<PathBuf>,
}

#[derive(Args, Debug)]
struct ImageFetch {
    #[arg(long, default_value = "default")]
    image: String,

    #[arg(long)]
    disk: Option<PathBuf>,

    #[arg(long)]
    url: Option<String>,

    #[arg(long)]
    ubuntu_date: Option<String>,

    #[arg(long)]
    arch: Option<String>,

    #[arg(long)]
    force: bool,
}

fn main() -> Result<()> {
    let cli = Cli::parse();
    init_tracing(cli.verbose);

    match cli.command {
        Command::Run(args) => cmd_run(args),
        Command::Doctor(args) => cmd_doctor(args),
        Command::Image(args) => cmd_image(args),
    }
}

fn init_tracing(verbose: bool) {
    let env_filter = if verbose { "info" } else { "warn" };
    let _ = tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::new(env_filter))
        .try_init();
}

fn cmd_run(args: RunCommand) -> Result<()> {
    let cwd = std::env::current_dir().context("failed to resolve current dir")?;
    let state_dir = args.state_dir.unwrap_or(default_state_dir()?);

    let image_cfg = ImageConfig {
        image: args.image.clone(),
        disk: args.disk.clone(),
    };

    let image = match ensure_image(&image_cfg) {
        Ok(status) => status,
        Err(err) if args.auto_fetch_image => {
            eprintln!("image missing, fetching ubuntu community image: {err}");
            fetch_ubuntu_image(&FetchConfig {
                image: args.image.clone(),
                disk: args.disk.clone(),
                url: args.image_url.clone(),
                ubuntu_date: args.ubuntu_date.clone(),
                arch: args.arch.clone(),
                force: false,
            })?
        }
        Err(err) => return Err(err),
    };

    let mut publish = Vec::new();
    for item in args.publish {
        publish.push(parse_publish(&item)?);
    }

    let root_fstype = if args.root_fstype.eq_ignore_ascii_case("auto") {
        Some("auto".to_string())
    } else {
        Some(args.root_fstype)
    };

    let run_cfg = RunConfig {
        cpus: args.cpus,
        memory_mib: args.memory_mib,
        disk_image: image.disk_path,
        disk_format: parse_disk_format(&args.disk_format)?,
        root_device: args.root_device,
        root_fstype,
        root_options: args.root_options,
        workspace_dir: cwd,
        state_dir,
        gateway_port: args.port,
        additional_publish: publish,
    };

    run_openclaw(&run_cfg)
}

fn cmd_doctor(args: DoctorCommand) -> Result<()> {
    let disk_path = match args.disk {
        Some(path) => path,
        None => image_disk_path(&args.image)?,
    };
    let report = run_doctor(&disk_path);

    println!("krunclaw doctor");
    println!("  libkrun.loadable: {}", report.libkrun_loadable);
    println!(
        "  libkrun.version: {}",
        report.libkrun_version.as_deref().unwrap_or("(unavailable)")
    );
    println!("  disk.exists: {}", report.disk_exists);
    println!("  disk.path: {}", report.disk_path);

    if report.issues.is_empty() {
        println!("  issues: none");
        return Ok(());
    }

    println!("  issues:");
    for issue in report.issues {
        println!("    - {issue}");
    }
    Ok(())
}

fn cmd_image(args: ImageCommand) -> Result<()> {
    match args.action {
        ImageAction::Inspect(inspect) => {
            let config = ImageConfig {
                image: inspect.image,
                disk: inspect.disk,
            };
            let status = inspect_image(&config)?;
            println!("image: {}", status.image);
            println!("disk: {}", status.disk_path.display());
            println!("exists: {}", status.exists);
            Ok(())
        }
        ImageAction::Fetch(fetch) => {
            let status = fetch_ubuntu_image(&FetchConfig {
                image: fetch.image,
                disk: fetch.disk,
                url: fetch.url,
                ubuntu_date: fetch.ubuntu_date,
                arch: fetch.arch,
                force: fetch.force,
            })?;
            println!("image fetch complete");
            println!("image: {}", status.image);
            println!("disk: {}", status.disk_path.display());
            println!("exists: {}", status.exists);
            Ok(())
        }
    }
}
