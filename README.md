# vclaw

Run full OpenClaw inside a lightweight VM.

`vclaw` currently uses a QEMU-based backend for real VM bring-up and lifecycle, while the RFC target backend remains `Code-Hex/vz`.

## Current status (RFC-001 progress)

This repository now includes:

- Go-based `vclaw` CLI: `run`, `image`, `ps`, `suspend`, `resume`, `rm`
- Ubuntu image reference parsing (`ubuntu:24.04`, `ubuntu:24.04@YYYYMMDD`)
- Image artifact caching under `~/.vclaw/images` (override with `VCLAW_HOME`/`VCLAW_CACHE_DIR`)
- Real VM run path via QEMU:
  - cloud-init seed generation
  - workspace/state host mounts
  - OpenClaw bootstrap in guest
  - host loopback port forwarding for gateway and `--publish`
- Instance metadata + process lifecycle under `~/.vclaw/instances` (override with `VCLAW_HOME`/`VCLAW_DATA_DIR`)

## Build

```bash
make build
```

(Equivalent manual command: `go build -o vclaw ./cmd/vclaw`.)

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

## Make targets

```bash
make help
make test
make integration-001
make integration-001-run
make integration-002
make clean
```

Use `INTEGRATION_IMAGE_REF` to override the integration image, for example:

```bash
make integration-002 INTEGRATION_IMAGE_REF=ubuntu:24.04@20250115
```

## Integration smoke script

```bash
make integration-001
```

To execute full VM run + readiness probe:

```bash
make integration-001-run
```

To verify image cache reuse and per-instance image copy:

```bash
make integration-002
```

## Notes on image conversion

`vclaw image fetch` normalizes runtime disks to `disk.raw`.

- If `qemu-img` is available, `vclaw` detects source format and converts to raw when needed.
- If `qemu-img` is missing and the source image appears to be qcow2, fetch fails with an explicit install hint.
- If artifacts are already ready in `~/.vclaw/images/<ref>/`, `vclaw image fetch` reuses cache and does not download again.
- Each `vclaw run` copies cached disk to `~/.vclaw/instances/<CLAWID>/instance.img` before VM start.
