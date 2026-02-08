# vclaw

Run full OpenClaw inside a lightweight VM powered by `Code-Hex/vz`.

You can manage claws in lightweight virtual machines, and let claws work together.

## Current status (RFC-001 checkpoint)

This repository is in active development and currently implements milestone M1 and part of M2 from `rfc/001-initial-design.md`:

- Go-based `vclaw` CLI with commands: `run`, `image`, `ps`, `suspend`, `resume`, `rm`
- Ubuntu image reference parsing (`ubuntu:24.04` and date-pinned `ubuntu:24.04@YYYYMMDD`)
- Image artifact caching under `~/.cache/vclaw/images` (override with `VCLAW_CACHE_DIR`)
- Instance metadata persistence under `~/.local/share/vclaw/instances` (override with `VCLAW_DATA_DIR`)

VM boot (`Code-Hex/vz`), cloud-init bootstrap, mounts, and forwarding runtime are tracked for next milestones (M3-M5).

## Build

```bash
go build -o vclaw ./cmd/vclaw
```

## Command examples

```bash
vclaw image ls
vclaw image fetch ubuntu:24.04
vclaw run ubuntu:24.04 --workspace=. --publish 8080:80
vclaw ps
vclaw suspend <CLAWID>
vclaw resume <CLAWID>
vclaw rm <CLAWID>
```

## Integration smoke script

```bash
go build -o vclaw ./cmd/vclaw
integration/001-basic.sh
```

To include the `run` stage:

```bash
INTEGRATION_ENABLE_RUN=1 integration/001-basic.sh
```

## Notes on image conversion

`vclaw image fetch` normalizes runtime disks to `disk.raw`.

- If `qemu-img` is available, `vclaw` detects the source format and converts to raw when needed.
- If `qemu-img` is missing and the source image appears to be qcow2, fetch fails with an explicit install hint.
