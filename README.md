# vclaw

Run full OpenClaw inside a lightweight VM.

`vclaw` currently uses a QEMU-based backend for real VM bring-up and lifecycle, while the RFC target backend remains `Code-Hex/vz`.

## Current status

This repository now includes:

- Go-based CLI commands: `run`, `image`, `ps`, `suspend`, `resume`, `rm`, `export`, `checkpoint`, `restore`
- Ubuntu image reference parsing (`ubuntu:24.04`, `ubuntu:24.04@YYYYMMDD`)
- Image cache + per-instance disk copy (`~/.vclaw/images`, `~/.vclaw/instances`)
- Lock-protected instance lifecycle (`instance.flock`) for `run/rm`
- OpenClaw preflight before VM creation:
  - validates required runtime parameters
  - interactive prompt when stdin is TTY
  - secret input masked with `*`
- `ps` health reconciliation (`ready`, `unhealthy`, `exited`) with `LAST_ERROR`

### Clawbox run modes

`vclaw run` now supports two `.clawbox` input modes:

1. **Header JSON clawbox**
   - `run <file.clawbox>` and `run .` (if current dir has exactly one `.clawbox`)
   - computes deterministic `CLAWID`
   - applies clawbox OpenClaw defaults (`model_primary`, `gateway_auth_mode`, `required_env`)

2. **Spec JSON clawbox (early simplified mode)**
   - if file starts with `{`, it is treated as JSON spec mode
   - does **not** mount clawbox payload
   - downloads `base_image` and `layers`
   - verifies SHA256
   - reuses cached artifacts (no redownload if already cached)
   - runs declared `provision` commands before VM start

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
vclaw run demo.clawbox --workspace=. --no-wait
vclaw run . --workspace=. --no-wait

vclaw ps
vclaw suspend <CLAWID>
vclaw resume <CLAWID>
vclaw rm <CLAWID>
```

`image ls` shows available images and whether each image is already downloaded (`yes`/`no`).

## OpenClaw run flags

Useful run flags:

```bash
vclaw run ubuntu:24.04 \
  --workspace=. \
  --port=18789 \
  --publish 8080:80 \
  --cpus=2 \
  --memory-mib=4096 \
  --ready-timeout-secs=900
```

Expanded OpenClaw config flags (equivalent to editing `--openclaw-config`):

```bash
vclaw run ubuntu:24.04 \
  --openclaw-agent-workspace /workspace \
  --openclaw-model-primary openai/gpt-5 \
  --openclaw-gateway-mode local \
  --openclaw-gateway-auth-mode token \
  --openclaw-gateway-token "$OPENCLAW_GATEWAY_TOKEN" \
  --openclaw-env OPENAI_API_KEY="$OPENAI_API_KEY"
```

You can also provide file-based inputs:

```bash
vclaw run ubuntu:24.04 \
  --openclaw-config ./openclaw.json \
  --openclaw-env-file ./.env.openclaw
```

Complete explicit provider/channel flags:

```bash
# AI provider keys
--openclaw-openai-api-key
--openclaw-anthropic-api-key
--openclaw-google-generative-ai-api-key
--openclaw-xai-api-key
--openclaw-openrouter-api-key
--openclaw-zai-api-key

# Gateway auth
--openclaw-gateway-token
--openclaw-gateway-password

# Channel tokens
--openclaw-discord-token
--openclaw-telegram-token
--openclaw-whatsapp-phone-number-id
--openclaw-whatsapp-access-token
--openclaw-whatsapp-verify-token
--openclaw-whatsapp-app-secret
```

Env precedence is:

1. explicit provider/channel flags
2. `--openclaw-env`
3. `--openclaw-env-file`

## Make targets

```bash
make help
make build
make test
make integration
make integration-001
make integration-001-run
make integration-002
make clean
```

Use `INTEGRATION_IMAGE_REF` to override integration image ref:

```bash
make integration-002 INTEGRATION_IMAGE_REF=ubuntu:24.04@20250115
```

## Cache and download notes

- `vclaw image fetch` downloads one runtime image file per ref and shows a dynamic progress bar.
- If an image is already present in cache, fetch reuses cache and does not redownload.
- Each `vclaw run` copies the source image into instance-local disk:
  - `~/.vclaw/instances/<CLAWID>/instance.img`
- Spec JSON clawbox artifacts are cached under:
  - `~/.clawfarm/blobs/<sha256>`
- Spec artifact download flow is:
  - download to a temporary file
  - verify SHA256
  - rename atomically to `<sha256>`
- Cached spec artifacts are checksum-verified; invalid cache is deleted and re-downloaded.
