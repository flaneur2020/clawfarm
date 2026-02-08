# krunclaw

Run full OpenClaw inside a lightweight VM powered by libkrun.

## Current milestone status

This repository is in active development:

- RFC drafted in `rfc/001-initial-design.md`
- Rust workspace scaffolded
- `krunclaw` CLI commands present: `run`, `image`, `doctor`
- integration smoke script added: `integration/001-basic.sh`
- disk-image flow supports Ubuntu community cloud images (Lima-template style)

## Build

```bash
cargo build --bin krunclaw
```

## Image workflow (community Ubuntu images)

Inspect where disk image is expected:

```bash
cargo run --bin krunclaw -- image inspect --image default
```

Fetch Ubuntu 24.04 cloud image (auto-arch, Lima-template style source):

```bash
cargo run --bin krunclaw -- image fetch --image default
```

Fetch by specific Ubuntu release date (example follows your style):

```bash
cargo run --bin krunclaw -- image fetch --image default --ubuntu-date 20260108
```

Fetch from explicit URL:

```bash
cargo run --bin krunclaw -- image fetch --image default \
  --url https://cloud-images.ubuntu.com/noble/20260108/noble-server-cloudimg-amd64.img
```

## Doctor

```bash
cargo run --bin krunclaw -- doctor --image default
```

## Run

Run using fetched disk image:

```bash
cargo run --bin krunclaw -- run --image default --port 18789 --publish 18793:18793
```

Run with explicit disk and format:

```bash
cargo run --bin krunclaw -- run \
  --disk ~/.cache/krunclaw/images/default/disk.img \
  --disk-format qcow2 \
  --root-device /dev/vda1
```

For first boot convenience, `run` can auto-fetch if missing:

```bash
cargo run --bin krunclaw -- run --image default --auto-fetch-image
```

## Integration smoke script

Basic CLI/doctor smoke:

```bash
integration/001-basic.sh
```

Fetch image then run:

```bash
INTEGRATION_FETCH_IMAGE=1 INTEGRATION_ENABLE_RUN=1 integration/001-basic.sh
```

Use a specific Ubuntu daily/release date path pattern:

```bash
INTEGRATION_FETCH_IMAGE=1 INTEGRATION_UBUNTU_DATE=20260108 integration/001-basic.sh
```
