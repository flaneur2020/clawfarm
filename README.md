# krunclaw

Run full OpenClaw inside a lightweight VM powered by libkrun.

## Current milestone status

This repository is at an early implementation milestone:

- RFC drafted in `rfc/001-initial-design.md`
- Rust workspace scaffolded
- `krunclaw` CLI commands present: `run`, `image`, `doctor`
- integration smoke script added: `integration/001-basic.sh`
- image build/import flow is still a placeholder

## Build

```bash
cargo build --bin krunclaw
```

## CLI examples

Inspect image rootfs location:

```bash
cargo run --bin krunclaw -- image inspect --image default
```

Doctor check:

```bash
cargo run --bin krunclaw -- doctor --image default
```

Run (requires existing rootfs and libkrun installed):

```bash
cargo run --bin krunclaw -- run --image default --port 18789 --publish 18793:18793
```

## Integration smoke script

```bash
integration/001-basic.sh
```

To enable the runtime probe stage:

```bash
INTEGRATION_ENABLE_RUN=1 integration/001-basic.sh
```

