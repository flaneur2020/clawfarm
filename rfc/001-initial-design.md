# RFC 001 — Initial Design for `vclaw` (formerly `krunclaw`)

- **Status:** Draft
- **Date:** 2026-02-08
- **Project:** `vclaw`

## 1. Summary

`vclaw` runs the **full OpenClaw components** inside a macOS lightweight VM using the Apple Virtualization framework via `github.com/Code-Hex/vz`.

This RFC updates earlier `libkrun` assumptions and aligns with the current README direction:

```bash
vclaw image ls
vclaw image fetch ubuntu:24.04
vclaw run ubuntu:24.04 --workspace=.
vclaw ps
vclaw suspend <CLAWID>
vclaw rm <CLAWID>
```

## 2. Goals

- **G1:** Boot full OpenClaw runtime in VM (not gateway-only).
- **G2:** Mount host current folder into guest on each `vclaw run`.
- **G3:** Expose OpenClaw service ports on host loopback (gateway + optional extras).
- **G4:** Reuse Ubuntu community cloud images instead of building a custom base image first.
- **G5:** Keep implementation in Go, directly against `Code-Hex/vz`.

## 3. Non-goals (RFC-001)

- Cross-platform hypervisor support beyond macOS Virtualization.framework.
- Full orchestration (clusters, schedulers, multi-tenant control-plane).
- Production hardening beyond single-user local development.
- Cloud-init abstraction layer for multiple distros (can be expanded later).

## 4. Key constraints and implications

### 4.1 Virtualization API / `Code-Hex/vz`

- Requires macOS virtualization entitlement (`com.apple.security.virtualization`) for built binaries.
- `Code-Hex/vz` major version is tied to Go toolchain level (for example, `vz/v2` requires Go >= 1.17; newer majors can require newer Go).
- Linux boot path uses explicit kernel + initrd + block storage attachment.
- Virtio-fs directory sharing is required for workspace/state mounts.
- NAT networking is available, but **direct host port mapping API is not equivalent to libkrun port-map**.

### 4.2 Disk format reality

- `vz` block attachment expects raw disk images for guest writable root disk usage.
- Ubuntu cloud images are often qcow2-formatted `.img`; conversion to raw may be required.
- We standardize cached runtime disks as `raw` to avoid ambiguity.

### 4.3 OpenClaw runtime model

- OpenClaw depends on Node.js 22+.
- Full behavior requires gateway + UI surfaces + plugins/channels/hooks/state.
- Per user requirement, OpenClaw process runs as **root inside guest**.

## 5. Architecture overview

`vclaw` is split into five modules:

1. **CLI layer**: command parsing and UX (`run`, `image`, `ps`, `suspend`, `rm`).
2. **Image manager**: fetch/cache Ubuntu kernel+initrd+base image, convert/prepare raw disk.
3. **VM runtime (vz backend)**: build VM config/devices and lifecycle control.
4. **Guest bootstrap**: cloud-init + startup script to install/start full OpenClaw stack.
5. **Forwarding + state manager**: workspace/state mounts, host port exposure, metadata persistence.

## 6. Image strategy (community Ubuntu first)

### 6.1 Source

Primary image source is Ubuntu cloud images (Lima-style source policy), e.g. `ubuntu:24.04` (Noble), including date-pinned variants when specified.

Examples:

- Release:
  - `https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-<arch>.img`
  - `https://cloud-images.ubuntu.com/releases/noble/release/unpacked/ubuntu-24.04-server-cloudimg-<arch>-vmlinuz-generic`
  - `https://cloud-images.ubuntu.com/releases/noble/release/unpacked/ubuntu-24.04-server-cloudimg-<arch>-initrd-generic`
- Date-pinned:
  - `https://cloud-images.ubuntu.com/noble/<date>/noble-server-cloudimg-<arch>.img`
  - `https://cloud-images.ubuntu.com/noble/<date>/unpacked/noble-server-cloudimg-<arch>-vmlinuz-generic`
  - `https://cloud-images.ubuntu.com/noble/<date>/unpacked/noble-server-cloudimg-<arch>-initrd-generic`

### 6.2 Cache layout (proposal)

- `~/.cache/vclaw/images/<name>/kernel`
- `~/.cache/vclaw/images/<name>/initrd`
- `~/.cache/vclaw/images/<name>/base.img` (downloaded)
- `~/.cache/vclaw/images/<name>/disk.raw` (runtime-ready)

### 6.3 Conversion

If source image is qcow2, convert once:

```bash
qemu-img convert -p -f qcow2 -O raw base.img disk.raw
```

## 7. VM runtime flow (`vclaw run`)

1. Resolve image ref (`ubuntu:24.04`) and host `--workspace` path.
2. Ensure kernel/initrd/disk files exist and are architecture-compatible.
3. Materialize instance directory: `~/.local/share/vclaw/instances/<clawid>/`.
4. Generate cloud-init seed (user-data + meta-data) ISO.
5. Configure VM with `Code-Hex/vz`:
   - Linux boot loader (`kernel`, `initrd`, cmdline)
   - Virtio block device (`disk.raw`)
   - NAT network device
   - Virtio-fs mounts (`workspace`, `state`)
   - serial console
   - optional vsock device (for port-forward strategy)
6. Boot VM and wait for readiness (cloud-init/bootstrap signal).
7. Start/attach host forwarding for requested ports.
8. Persist runtime metadata for `ps` / `suspend` / `rm`.

## 8. Guest bootstrap (full OpenClaw components)

Guest bootstrap responsibilities:

1. Mount shared paths:
   - `/workspace` from host `--workspace` (default current folder)
   - `/root/.openclaw` from host persistent state dir
2. Install runtime dependencies if missing:
   - Node.js 22
   - OpenClaw package (`openclaw@latest` or pinned)
3. Materialize OpenClaw config with workspace + gateway defaults.
4. Start full OpenClaw runtime (gateway/UI/canvas/plugins/channels as configured).

Root-mode behavior:

- OpenClaw runs as root in guest to satisfy permission expectations for full component behavior.

## 9. Networking and port exposure

### 9.1 Requirement

- Host must access OpenClaw gateway (default `18789`), plus optional mapped ports (example canvas `18793`).

### 9.2 Design direction

Because direct libkrun-style `host:guest` mapping is not available in VZ API, v1 uses a deterministic forwarding layer:

- Default: host loopback listeners (`127.0.0.1:<hostPort>`).
- Forwarding backend options:
  - vsock-based proxy bridge (preferred), or
  - helper tunnel process strategy.

CLI surface remains simple:

- `--port <gatewayHostPort>`
- repeated `--publish host:guest`

## 10. Command UX (aligned with README)

```bash
vclaw image ls
vclaw image fetch ubuntu:24.04
vclaw run ubuntu:24.04 --workspace=.
vclaw ps
vclaw suspend <CLAWID>
vclaw rm <CLAWID>
```

Minimum command behavior:

- `image ls`: show locally cached image refs and readiness.
- `image fetch <ref>`: fetch kernel/initrd/base and prepare raw runtime disk.
- `run <ref>`: start instance and print endpoints + CLAWID.
- `ps`: list instances and states.
- `suspend <id>`: stop or pause instance lifecycle safely.
- `rm <id>`: remove instance runtime metadata (and optional disk artifacts).

## 11. State, mounts, and directories

- Image cache: `~/.cache/vclaw/images`
- Instance metadata/logs: `~/.local/share/vclaw/instances`
- Host mounted workspace: from `--workspace` (default current directory)
- Host mounted OpenClaw state: per-instance persistent directory

## 12. Validation plan

### 12.1 Integration baseline

`integration/001-basic.sh` should execute a **real VM path** by default:

1. Verify binary and command entrypoints.
2. Ensure image available (fetch if needed).
3. Execute `vclaw run ...`.
4. Probe `http://127.0.0.1:<gatewayPort>/` readiness.
5. Fail with useful logs on timeout.

### 12.2 Acceptance checks

- Workspace mount visible inside guest/OpenClaw operations.
- Full OpenClaw surface available (not gateway-only shortcut).
- Port mapping reachable on host loopback.
- Restart preserves state in mounted guest root state path.

## 13. Risks and mitigations

- **R1: Missing entitlements/codesign for virtualization**  
  Mitigation: `doctor` checks and explicit troubleshooting output.
- **R2: Cloud image format mismatch (qcow2 vs raw)**  
  Mitigation: normalize to raw in cache; verify via tooling before boot.
- **R3: Port mapping complexity under VZ NAT**  
  Mitigation: stable forwarding layer with explicit tests.
- **R4: First-boot bootstrap latency**  
  Mitigation: cache runtime disk; avoid reinstall on every start.
- **R5: Root in guest + writable mounts**  
  Mitigation: document trust model, minimize mounted host paths.

## 14. Implementation milestones

1. **M1 — CLI skeleton in Go** (`run`, `image`, `ps`, `suspend`, `rm`).
2. **M2 — Image fetch/cache/convert** for Ubuntu cloud artifacts.
3. **M3 — VZ boot + mounts** (`workspace`, persistent state).
4. **M4 — Bootstrap full OpenClaw** in guest startup script.
5. **M5 — Host port forwarding** for gateway + `--publish`.
6. **M6 — Lifecycle + integration hardening** (`ps`, `suspend`, `rm`, script coverage).

## 15. Open questions

1. Should `suspend` map to pause or graceful stop in v1 semantics?
2. Should `image fetch` pin by date by default or follow latest release URL?
3. What is the exact default published port set beyond gateway?
4. Should first boot always install `openclaw@latest` or pin a tested version?

## 16. MVP definition

MVP is complete when:

1. `vclaw run ubuntu:24.04 --workspace=.` boots VM via `Code-Hex/vz`.
2. Full OpenClaw components start (not gateway-only profile).
3. Host current folder is mounted and used as workspace.
4. Gateway is reachable from host on mapped loopback port.
5. `vclaw ps`, `vclaw suspend`, and `vclaw rm` operate on created instances.

## 17. Appendix — VZ knowledge dump (`Code-Hex/vz`)

This appendix captures implementation-relevant knowledge for the `vclaw` VZ backend.

### 17.1 OS / toolchain / entitlement matrix

- Virtualization.framework requires macOS support and a signed binary entitlement:
  - `com.apple.security.virtualization`
- If bridged networking is used, add:
  - `com.apple.vm.networking`
- `Code-Hex/vz` major versions track Go/runtime expectations:
  - `vz/v2` is practical with older Go toolchains (Go 1.17+)
  - newer majors can require newer Go versions and newer SDK assumptions

### 17.2 Linux VM boot model in VZ

- Linux boot is configured explicitly with:
  - kernel (`vmlinuz`)
  - initrd
  - kernel cmdline
- Typical bootloader setup uses `NewLinuxBootLoader(...)` + `WithInitrd(...)` + `WithCommandLine(...)`.
- Kernel/initrd/image architecture must match host-supported virtualization path:
  - `arm64` hosts should use `arm64` Ubuntu artifacts
  - Intel hosts should use `amd64` artifacts

### 17.3 Storage model

- VZ block device attachment (`DiskImageStorageDeviceAttachment`) is documented/implemented with raw disk image expectations.
- Community Ubuntu images can be qcow2 despite `.img` naming; converting to raw in cache avoids runtime ambiguity.
- Recommended cache policy:
  - keep original downloaded artifact (`base.img`)
  - maintain derived runtime disk (`disk.raw`)

### 17.4 Filesystem sharing (virtio-fs)

- Host folder sharing is done with `VirtioFileSystemDeviceConfiguration` + shared directory objects.
- Guest mounts by tag, e.g.:
  - `mount -t virtiofs workspace /workspace`
- This is the required mechanism for:
  - workspace mount
  - persistent OpenClaw state mount
- Security implication: writable shared dirs effectively grant guest process write access to host paths.

### 17.5 Networking reality in VZ

- NAT attachment is straightforward (`NewNATNetworkDeviceAttachment`), but this does **not** provide libkrun-style direct host-port mapping API.
- Bridged networking requires extra entitlement and is not default for local-dev UX.
- Therefore `vclaw` needs its own host exposure layer for `--port`/`--publish`, such as:
  - vsock-based proxy, or
  - helper tunnel/forwarder process.

### 17.6 Virtio socket capability (important)

- VZ can expose a virtio socket device.
- Host side can open listener/connect flows through the VM’s socket device APIs.
- This provides a solid foundation for deterministic port forwarding without relying on external VM managers.

### 17.7 Console and observability

- Serial console can be attached to stdio or log files via file-handle serial attachments.
- For early bring-up and cloud-init diagnostics, serial output should be captured into per-instance logs.
- Keep a concise health timeline in instance metadata:
  - created → booting → running → ready → stopping/stopped.

### 17.8 VM lifecycle semantics

- Always run `config.Validate()` before VM creation/start to fail fast with actionable errors.
- Lifecycle methods include:
  - `Start`
  - `RequestStop` (graceful guest-triggered)
  - `Stop` (forceful)
- State-change notifications should drive:
  - `ps` output
  - readiness waiting logic
  - cleanup behavior on failure.

### 17.9 Cloud-init seed strategy

- For Ubuntu cloud images, NoCloud seed (`user-data`, `meta-data`) can be generated as an ISO (label `cidata`).
- On macOS hosts this can be created with `hdiutil makehybrid`.
- Seed should contain:
  - one-time provisioning script
  - bootstrap script path/unit for OpenClaw startup
  - instance identifier for reproducibility

### 17.10 OpenClaw-specific practical notes

- First boot should be idempotent:
  - install Node/OpenClaw only if missing
  - write config only if absent or explicitly regenerate
- Run OpenClaw as root inside guest (as requested), but keep host mounts minimal.
- Default endpoint contract:
  - gateway: `127.0.0.1:18789`
  - optional canvas mapping can be exposed via `--publish`.

### 17.11 Failure patterns to handle explicitly

- Missing entitlements/codesign → virtualization init/validation failure.
- Mismatched architecture artifacts → boot failure or invalid config.
- Non-raw disk fed to VZ block attachment → attach/start failure.
- Forwarding process crash while VM is alive → service appears down though VM is running.
- Guest bootstrap succeeded partially → gateway unavailable despite VM `Running` state.

### 17.12 Recommended `doctor` checks (future)

- Host OS version and architecture.
- Presence of virtualization entitlement on built binary.
- Availability of required image artifacts (`kernel`, `initrd`, `disk.raw`).
- Ability to create/start minimal VZ config (lightweight preflight).
- Port forwarding backend readiness.
