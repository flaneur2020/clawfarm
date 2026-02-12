# RFC 007 — Clawfarm Consolidated Design (Current Baseline)

- **Status:** Draft (working baseline)
- **Date:** 2026-02-12
- **Project:** `clawfarm`
- **Supersedes (conceptually):** RFC 001–006 (now archived)

---

## 0. Key Ideas (Top-Level Summary)

1. `clawfarm` is an **agent-first VM sandbox runtime**, prioritizing guest systems with GUI capabilities.
2. `.clawbox` is a **distribution format only** (import/export), not a runtime-mounted control plane.
3. `run <file.clawbox>` means **import first, then run**.
4. Runtime identity is per-instance **CLAWID**; the same `.clawbox` can start multiple instances.
5. Runtime storage is centered on `~/.clawfarm`, with blob dedup via content address (`blobs/<sha256>`).
6. Initialization is generated from `clawspec` through **cloud-init**.
7. Guest bootstrap must create `claw` user with sudo `NOPASSWD:ALL`.
8. The product is **not** a general-purpose VM manager; it is optimized for AI agent workloads.

---

## 1. Design Target

Provide a practical, sharable, reproducible VM runtime for OpenClaw/agent workflows:

- Easy to package and share as `.clawbox`.
- Easy to run repeatedly with different names/instances.
- Stable runtime semantics around `CLAWID`.
- Ready for GUI-first product evolution without coupling runtime to legacy mount semantics.

---

## 2. Goals

- **G1:** Keep `.clawbox` focused on import/export distribution.
- **G2:** Keep runtime model instance-centric (`CLAWID`) and concurrency-friendly.
- **G3:** Support agent runtime bootstrap from `clawspec` + cloud-init.
- **G4:** Keep cache and artifact handling deterministic via SHA256.
- **G5:** Keep UX agent-centric (credentials/config preflight, health visibility, repeatable run flow).

---

## 3. Non-Goals

- Become a full-featured, general-purpose VM lifecycle platform.
- Optimize for non-agent infrastructure workflows first.
- Preserve legacy RFC semantics when they conflict with this baseline.

---

## 4. Runtime Model

## 4.1 Instance Identity and Concurrency

- Every run yields one `CLAWID`.
- Locking is instance-scoped (per `CLAWID`).
- Running from the same source `.clawbox` multiple times is valid.

## 4.2 Run Semantics

`clawfarm run demo.clawbox --name demo-a` is interpreted as:

1. Parse and validate the clawbox payload.
2. Import required artifacts into `~/.clawfarm` (dedup by SHA256).
3. Materialize one instance directory for a new `CLAWID`.
4. Generate cloud-init and start VM.

## 4.3 Health Semantics

`ps` must surface unhealthy/exited states and last error, not only healthy/running instances.

---

## 5. Clawbox Format (Distribution)

`.clawbox` baseline format is `tar.gz` with at least:

```text
clawspec.json
run.qcow2
```

Optional payload:

```text
claw/
  ...
```

Rules:

- `clawspec.json` is required.
- `run.qcow2` and any embedded artifacts are verified by SHA256 from spec.
- No plaintext secrets should be bundled in `.clawbox`.

---

## 6. Clawspec Baseline (v2 Direction)

Expected logical fields:

- `schema_version`
- `name`
- `image[]` (must include `base`; may include `run`)
- `openclaw` (model/auth/env requirements)
- `provision[]` (to be transformed into cloud-init actions)

If artifacts are remote:

- Download to temp file first.
- Verify SHA256.
- Atomically rename to `~/.clawfarm/blobs/<sha256>`.
- If hash file already exists and verifies, reuse directly.

---

## 7. Directory Layout

```text
~/.clawfarm/
  blobs/
    <sha256>

  claws/
    <CLAWID>/
      clawspec.json
      run.qcow2
      env
      init.iso
      claw/
      state/
      logs/
      ...
```

Notes:

- `blobs/` is shared content-addressed storage.
- `claws/<CLAWID>/` is the instance root, including imported payload and runtime metadata.

---

## 8. Cloud-init and Guest Bootstrap Requirements

Minimum required behavior:

1. Create user `claw` in guest.
2. Ensure `claw` has sudo with `NOPASSWD:ALL`.
3. Mount runtime paths (`/workspace`, state path, and `/claw` when provided).
4. Materialize OpenClaw config/env from host-provided values.
5. Install/start OpenClaw service in guest.
6. Execute provision steps generated from `clawspec`.

---

## 9. CLI Product Direction

User-centric command surface target:

- `clawfarm new`
- `clawfarm run <box> --name <name>`
- `clawfarm ps`
- `clawfarm stop <CLAWID>`
- `clawfarm export <CLAWID> <output.clawbox>`

Implementation may temporarily keep compatibility aliases while converging on this surface.

---

## 10. Security and Secrets

- Required runtime env keys must be validated before VM creation.
- Missing required credentials should fail fast (or use guided input in interactive mode).
- Export should continue secret scanning and require explicit override when risky payload is detected.

---

## 11. Migration / Documentation Cleanup

- RFC 001–006 are archived under `rfc/archived/` for historical context.
- This RFC is the primary design baseline moving forward.
- When conflicts exist, this RFC takes precedence unless replaced by a newer consolidated RFC.
