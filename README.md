# vclaw

Run full OpenClaw inside a lightweight VM.

`vclaw` currently uses a QEMU-based backend for real VM bring-up and lifecycle, while the RFC target backend remains `Code-Hex/vz`.

## Current status (RFC-001 progress)

This repository now includes:

- Go-based `vclaw` CLI: `run`, `image`, `ps`, `suspend`, `resume`, `rm`
- Ubuntu image reference parsing (`ubuntu:24.04`, `ubuntu:24.04@YYYYMMDD`)
- Single-file image caching under `~/.vclaw/images` (override with `VCLAW_HOME`/`VCLAW_CACHE_DIR`)
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

The `image ls` table lists supported images and marks whether each image is already downloaded (`yes`/`no`).

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

## Notes on image download/cache

`vclaw image fetch` downloads one image file per ref (for example `image.img`; underlying format may be raw or qcow2).

- If the image is already present in `~/.vclaw/images/<ref>/`, `vclaw image fetch` reuses cache and does not download again.
- `vclaw image fetch` shows a dynamic progress bar while downloading.
- Each `vclaw run` copies the cached image to `~/.vclaw/instances/<CLAWID>/instance.img` before VM start.
