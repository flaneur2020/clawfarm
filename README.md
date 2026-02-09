# vclaw

Run full OpenClaw inside a lightweight VM.

`vclaw` currently uses a QEMU-based backend for real VM bring-up and lifecycle, while the RFC target backend remains `Code-Hex/vz`.

## Current status (RFC-001 progress)

This repository now includes:

- Go-based `vclaw` CLI: `run`, `image`, `ps`, `suspend`, `resume`, `rm`
- Ubuntu image reference parsing (`ubuntu:24.04`, `ubuntu:24.04@YYYYMMDD`)
- Image artifact caching under `~/.cache/vclaw/images` (override with `VCLAW_CACHE_DIR`)
- Real VM run path via QEMU:
  - cloud-init seed generation
  - workspace/state host mounts
  - OpenClaw bootstrap in guest
  - host loopback port forwarding for gateway and `--publish`
- Instance metadata + process lifecycle under `~/.local/share/vclaw/instances` (override with `VCLAW_DATA_DIR`)

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

Useful `run` flags:

```bash
vclaw run ubuntu:24.04 \
  --workspace=. \
  --port=18789 \
  --publish 8080:80 \
  --cpus=2 \
  --memory-mib=4096 \
  --ready-timeout-secs=900
```

## Integration smoke script

```bash
go build -o vclaw ./cmd/vclaw
integration/001-basic.sh
```

To execute full VM run + readiness probe:

```bash
INTEGRATION_ENABLE_RUN=1 integration/001-basic.sh
```

## Notes on image conversion

`vclaw image fetch` normalizes runtime disks to `disk.raw`.

- If `qemu-img` is available, `vclaw` detects source format and converts to raw when needed.
- If `qemu-img` is missing and the source image appears to be qcow2, fetch fails with an explicit install hint.
