# RFC 001 — Initial Design for `krunclaw`

- **Status:** Draft
- **Date:** 2026-02-08
- **Project:** `krunclaw`

## 1. Summary

`krunclaw` is a Rust CLI that launches **full OpenClaw runtime components** inside a lightweight VM powered by `libkrun`.

This design targets more than only `openclaw gateway`. It includes:

1. Gateway daemon and WS/HTTP control plane.
2. Control UI + web surfaces served by the gateway.
3. Canvas host surface used by node/web features.
4. Channels/plugin runtime loaded by OpenClaw config.
5. Skills/hooks/plugin loading from workspace + state.
6. Mounting the caller's current folder into the guest at every run.

## 2. Goals

- **G1:** `krunclaw run` starts OpenClaw in VM with full component parity (not gateway-only mode).
- **G2:** Host current directory (`$PWD`) is mounted inside guest and used as OpenClaw workspace.
- **G3:** Host can reach OpenClaw service ports through `libkrun` port mapping.
- **G4:** Reproducible image build path for Ubuntu or another Linux distro.
- **G5:** Keep host installation lightweight: primary dependency is `libkrun` + `libkrunfw`.

## 3. Non-goals (RFC-001 scope)

- Full multi-VM orchestration.
- Defaulting to `virtio-net + passt/gvproxy` (TSI first).
- Production-grade multi-tenant hard isolation model.
- Automatically provisioning third-party channel credentials/secrets.
- Shipping desktop wrapper apps (e.g., platform-native GUI packaging).

## 4. Key upstream constraints (driving decisions)

### 4.1 libkrun networking

- If no explicit network interface is added, `libkrun` uses TSI (`virtio-vsock + TSI`) networking.
- `krun_set_port_map()` accepts `"host_port:guest_port"` strings.
- Port mapping is not supported when passt mode is used.

### 4.2 libkrun filesystem

- Root filesystem can be set from a host directory with `krun_set_root()`.
- Additional host directories can be exposed with `krun_add_virtiofs(tag, path)`.
- `krun_set_mapped_volumes()` is no longer supported.

### 4.3 OpenClaw runtime model

- OpenClaw requires Node.js >= 22.
- Gateway port defaults to `18789`, multiplexing WebSocket + HTTP surfaces.
- Canvas host is a separate HTTP listener (commonly `gateway.port + 4`).
- Full behavior depends on config + state (`~/.openclaw`-style data) and workspace files.

### 4.4 Component realism

- Some channel features require extra OS packages/binaries (example: optional channel-specific tools).
- RFC-001 defines a **full components baseline** and a path for optional feature package layers.

## 5. High-level architecture

`krunclaw` has five modules:

1. **CLI layer** (`clap`): parse commands/flags.
2. **Image manager:** build/pull/export rootfs containing full OpenClaw runtime.
3. **VM launcher (libkrun FFI):** configure VM, mounts, ports, and guest entrypoint.
4. **Guest bootstrap/orchestrator:** mount virtio-fs tags, materialize config/env, start OpenClaw.
5. **Runtime state manager:** host paths for image cache, persistent state, logs, and workspace mount.

## 6. Base image strategy

### 6.1 Distro choice

- **Default proposal:** Debian Bookworm (aligns with current OpenClaw Docker base).
- **Supported alternatives:** Ubuntu (or other distro) via configurable recipe.

### 6.2 Build method (v1)

Build an OCI image, then export to a rootfs directory used by `krun_set_root()`.

Pipeline:

1. Build image with Node 22 + OpenClaw full package/install.
2. `docker create` + `docker export` to tarball.
3. Unpack tarball into local cache:
   - `~/.cache/krunclaw/images/<image-id>/rootfs`
4. Launch VM from cached rootfs.

### 6.3 Guest image contents (full-components baseline)

- Node.js 22 runtime.
- OpenClaw CLI/runtime package (includes gateway + web assets expected by standard install).
- `util-linux`, `bash`, `coreutils`, `ca-certificates`, `curl`, `git`.
- Entrypoint helper (`/usr/local/bin/krunclaw-entrypoint`).
- Runtime user is `root` (requirement for full OpenClaw permissions inside guest).

### 6.4 Optional image flavors (future)

- `core`: baseline full OpenClaw components.
- `core+extras`: additional OS dependencies for channel/tool-heavy environments.

## 7. Command UX (initial)

```bash
krunclaw run [--port 18789] [--publish 18793:18793] [--cpus 2] [--memory-mib 2048] [--image default]
```

Planned utility commands:

```bash
krunclaw image build [--distro debian|ubuntu|other]
krunclaw image list
krunclaw openclaw <args...>   # run openclaw CLI inside VM context
krunclaw doctor
```

## 8. Runtime flow for `krunclaw run`

1. Resolve host `cwd = std::env::current_dir()`.
2. Ensure chosen image/rootfs exists (build or pull policy).
3. Prepare host runtime dirs:
   - state: `~/.local/share/krunclaw/state/openclaw`
   - logs: `~/.local/share/krunclaw/logs`
4. Create libkrun context and set VM resources.
5. `krun_set_root(ctx, <rootfs_dir>)`.
6. `krun_add_virtiofs(ctx, "workspace", <cwd>)`.
7. `krun_add_virtiofs(ctx, "state", <state_dir>)`.
8. Configure TSI port mappings (gateway + explicit publishes).
9. Set guest executable to `/usr/local/bin/krunclaw-entrypoint`.
10. Start VM via `krun_start_enter(ctx)`.

## 9. Full OpenClaw component coverage (required)

RFC-001 runtime target is to launch OpenClaw so these components are usable:

1. **Gateway daemon** (`openclaw gateway`) with WS + HTTP API.
2. **Control UI/web surfaces** served on gateway HTTP port.
3. **Canvas host** listener (when enabled by config).
4. **Channels runtime** (WhatsApp/Telegram/Slack/Discord/etc.) as configured.
5. **Hooks/skills/plugins** loaded from mounted workspace/state.
6. **Node connectivity** (`role: node`) via gateway WS.
7. **CLI operations** via helper command (`krunclaw openclaw ...`) in same VM/state context.

## 10. Mounting current folder (required behavior)

On every `krunclaw run`:

- Host current folder is exposed as virtio-fs tag `workspace`.
- Guest mounts it to `/workspace`.
- OpenClaw workspace resolves to `/workspace`.

State handling for full components:

- Host persistent state dir is exposed as virtio-fs tag `state`.
- Guest mounts it to `/root/.openclaw` (OpenClaw runs as root in guest).
- This preserves config, credentials, pairing state, sessions, and channel runtime data across runs.

Guest entrypoint responsibilities:

1. `mount -t virtiofs workspace /workspace`
2. `mount -t virtiofs state /root/.openclaw`
3. Materialize/update config defaults:
   - `agents.defaults.workspace = "/workspace"`
   - `gateway.port = <GATEWAY_PORT>`
4. Start OpenClaw with full runtime defaults (not stripped gateway-only profile).

## 11. Networking and port exposure

### 11.1 Default network mode

- Use TSI default (do **not** add explicit net devices in v1).
- Keeps runtime simple and compatible with `krun_set_port_map`.

### 11.2 Port mapping policy

- Default published port: gateway `18789:18789`.
- Additional ports can be declared with repeated `--publish host:guest`.
- Recommended default extra mapping when canvas host enabled: `18793:18793`.

### 11.3 Full-component implications

- Gateway HTTP/WS remains primary control-plane port.
- Channels requiring additional webhook/listener ports can be supported via explicit `--publish` entries.
- v1 keeps passt disabled to retain native libkrun port-map semantics.

### 11.4 Security default

- Keep OpenClaw bind mode conservative (loopback-oriented guest config) unless user opts in.
- Document that exposing mapped ports broadens host network surface.

## 12. Implementation plan (phased)

### Phase 0 — Bootstrap repository

- Create Rust workspace and crates:
  - `crates/krunclaw-cli`
  - `crates/libkrun-sys`
  - `crates/krunclaw-runtime`

### Phase 1 — libkrun bring-up

- Add minimal FFI bindings for required APIs.
- Boot simple VM command and validate lifecycle/error paths.

### Phase 2 — Image pipeline for full OpenClaw

- Add image recipe(s) and `krunclaw image build`.
- Cache keying: distro + OpenClaw version + image flavor.
- Export/unpack rootfs flow.

### Phase 3 — Guest bootstrap and mounts

- Add `krunclaw-entrypoint` in image.
- Wire `workspace` + persistent `state` virtio-fs mounts.
- Start OpenClaw runtime from entrypoint with generated config/env.

### Phase 4 — Component parity checks

- Validate gateway WS/HTTP and Control UI serving.
- Validate canvas host exposure.
- Validate hooks/skills/plugins load from mounted workspace/state.
- Validate channel runtime startup path from config.

### Phase 5 — Port publishing and UX

- Add `--port` and repeated `--publish` parsing.
- Validate mappings for gateway + optional component ports.
- Add `krunclaw openclaw <args...>` passthrough command.

### Phase 6 — hardening and diagnostics

- Add `krunclaw doctor` preflight checks.
- Improve logging, shutdown behavior, and recovery guidance.

## 13. Validation plan

### 13.1 Automated checks

- Unit tests:
  - CLI parsing (`--port`, `--publish`, image selection)
  - path/state resolution
  - image cache keying
- Integration tests (where environment allows):
  - boot VM and run OpenClaw
  - verify `/workspace` mount in guest
  - verify gateway HTTP/WS reachable through mapped port
  - verify control UI static assets served
  - verify state persistence across restart

### 13.2 Manual acceptance test (MVP)

1. In host folder `demo/`, run:
   - `krunclaw run --port 18789 --publish 18793:18793`
2. Open `http://127.0.0.1:18789/` and verify Control UI loads.
3. Verify workspace-backed behavior (files in `demo/` visible via OpenClaw workspace operations).
4. Restart `krunclaw run` and confirm config/session state persisted.

## 14. Risks and mitigations

- **R1: virtio-fs host exposure risk**
  - Mitigation: mount only explicit paths (`cwd`, state dir) and document trust model.
- **R2: full-component dependency gaps per channel**
  - Mitigation: baseline + optional image flavors; clear diagnostics for missing deps.
- **R3: multi-port complexity for channel webhooks/canvas**
  - Mitigation: explicit `--publish` model with sensible defaults.
- **R4: image size and cold-start latency**
  - Mitigation: cached rootfs and pinned image versions.
- **R5: host dependency mismatch (`libkrun`/`libkrunfw`)**
  - Mitigation: `doctor` with actionable checks.
- **R6: OpenClaw runs as root inside guest**
  - Mitigation: keep host mounts minimal, default to explicit mount allowlist only, and add a future hardening mode for read-only mounts and reduced guest privileges.

## 15. Open questions

1. Default distro for v1 image: Debian, Ubuntu, or user-selected at first run?
2. Auto-build image on first run vs fail-fast with explicit `image build`?
3. Which additional ports should be auto-published by default besides gateway?
4. Should v1 enforce gateway auth token/password by default?
5. Which optional channel dependencies belong in `core+extras` flavor?

## 16. MVP deliverable definition

MVP is complete when:

1. `krunclaw run` boots OpenClaw in libkrun VM with full runtime components enabled.
2. Current host folder is mounted and used as guest workspace.
3. Persistent state survives VM restarts.
4. Host reaches gateway/control UI via mapped port(s).
5. Build instructions exist for Ubuntu or another Linux distro image recipe.
