use std::path::PathBuf;

use anyhow::{Context, Result};
use clap::{Args, Parser, Subcommand};
use krunclaw_runtime::doctor::run_doctor;
use krunclaw_runtime::image::{ImageConfig, ensure_image, image_rootfs_path, inspect_image};
use krunclaw_runtime::run::{RunConfig, default_state_dir, parse_publish, run_openclaw};

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
    rootfs: Option<PathBuf>,

    #[arg(long)]
    state_dir: Option<PathBuf>,
}

#[derive(Args, Debug)]
struct DoctorCommand {
    #[arg(long, default_value = "default")]
    image: String,

    #[arg(long)]
    rootfs: Option<PathBuf>,
}

#[derive(Args, Debug)]
struct ImageCommand {
    #[command(subcommand)]
    action: ImageAction,
}

#[derive(Subcommand, Debug)]
enum ImageAction {
    Inspect(ImageInspect),
    Build(ImageBuild),
}

#[derive(Args, Debug)]
struct ImageInspect {
    #[arg(long, default_value = "default")]
    image: String,

    #[arg(long)]
    rootfs: Option<PathBuf>,
}

#[derive(Args, Debug)]
struct ImageBuild {
    #[arg(long, default_value = "default")]
    image: String,

    #[arg(long)]
    distro: Option<String>,

    #[arg(long)]
    rootfs: Option<PathBuf>,
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
        image: args.image,
        distro: None,
        rootfs: args.rootfs,
    };
    let image = ensure_image(&image_cfg)?;

    let mut publish = Vec::new();
    for item in args.publish {
        publish.push(parse_publish(&item)?);
    }

    let run_cfg = RunConfig {
        cpus: args.cpus,
        memory_mib: args.memory_mib,
        rootfs: image.rootfs_path,
        workspace_dir: cwd,
        state_dir,
        gateway_port: args.port,
        additional_publish: publish,
    };

    run_openclaw(&run_cfg)
}

fn cmd_doctor(args: DoctorCommand) -> Result<()> {
    let rootfs_path = match args.rootfs {
        Some(path) => path,
        None => image_rootfs_path(&args.image)?,
    };
    let report = run_doctor(&rootfs_path);

    println!("krunclaw doctor");
    println!("  libkrun.loadable: {}", report.libkrun_loadable);
    println!(
        "  libkrun.version: {}",
        report.libkrun_version.as_deref().unwrap_or("(unavailable)")
    );
    println!("  rootfs.exists: {}", report.rootfs_exists);
    println!("  rootfs.path: {}", report.rootfs_path);

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
                distro: None,
                rootfs: inspect.rootfs,
            };
            let status = inspect_image(&config)?;
            println!("image: {}", status.image);
            println!("rootfs: {}", status.rootfs_path.display());
            println!("exists: {}", status.exists);
            Ok(())
        }
        ImageAction::Build(build) => {
            let config = ImageConfig {
                image: build.image,
                distro: build.distro,
                rootfs: build.rootfs,
            };
            let status = inspect_image(&config)?;
            println!("image build (placeholder)");
            println!("image: {}", status.image);
            if let Some(distro) = config.distro {
                println!("distro: {distro}");
            }
            println!("target rootfs: {}", status.rootfs_path.display());
            println!("status: not implemented yet (prepare rootfs manually for now)");
            Ok(())
        }
    }
}
