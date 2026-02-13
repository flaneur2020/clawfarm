# SPEC — Clawspec (Bootstrap-Oriented)

- **Status:** Draft (active)
- **Date:** 2026-02-12
- **Project:** `clawfarm`

---

## 0. Key Decisions

1. Clawspec is the source of truth for runtime bootstrap behavior.
2. Agent runtime is **not preinstalled** in base images.
3. The old `preset` concept is renamed to **`bootstrap`**.
4. `clawfarm new` generates clawspec via TUI prompts.
5. `bootstrap` declares install/start scripts and parameter schema.

---

## 1. Scope

This document defines:

- Clawspec JSON schema.
- `bootstrap` structure and parameter definitions.
- Validation rules for `clawfarm new` and `clawfarm run`.

This document does **not** define general VM lifecycle features.

---

## 2. Schema Overview

Proposed schema version: `3`.

Top-level fields:

- `schema_version` (number, required)
- `name` (string, required)
- `image` (array, required)
- `bootstrap` (object, required)
- `provision` (array, optional)

---

## 3. `image[]` Definition

Each image entry:

- `name` (string, required)
- `ref` (string, required)
- `sha256` (string, required)

Rules:

- Must contain one `name=base` entry.
- May contain one `name=run` entry.
- `sha256` must be lowercase 64-hex.

---

## 4. `bootstrap` Definition

`bootstrap` describes how to initialize agent runtime in guest VM.

Fields:

- `id` (string, required): bootstrap type id, e.g. `openclaw`.
- `display_name` (string, optional): user-facing label.
- `install_script` (string, required): shell script to install runtime dependencies.
- `start_script` (string, required): shell script/command to start runtime service.
- `params` (object, required): runtime parameter schema.

`bootstrap.params` fields:

- `required` (array, required)
- `optional` (array, optional)

Parameter object fields:

- `key` (string, required)
- `label` (string, required)
- `description` (string, optional)
- `secret` (bool, required)
- `default` (string, optional, only for non-secret recommended)
- `validator` (object, optional)

`validator.kind` examples:

- `non_empty`
- `regex`
- `enum`

Current supported `bootstrap.id`:

- `openclaw`

---

## 5. `provision[]` Definition (Optional)

Each provision step:

- `name` (string, optional)
- `shell` (string, optional; default runtime shell)
- `script` (string, required)

Provision runs after bootstrap install/start preparation.

---

## 6. Example Clawspec

```json
{
  "schema_version": 3,
  "name": "demo-openclaw",
  "image": [
    {
      "name": "base",
      "ref": "ubuntu:24.04",
      "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    },
    {
      "name": "run",
      "ref": "clawbox:///run.qcow2",
      "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  ],
  "bootstrap": {
    "id": "openclaw",
    "display_name": "OpenClaw",
    "install_script": "#!/usr/bin/env bash\nset -euxo pipefail\n# install openclaw runtime",
    "start_script": "#!/usr/bin/env bash\nset -euxo pipefail\n# start openclaw gateway",
    "params": {
      "required": [
        {
          "key": "OPENAI_API_KEY",
          "label": "OpenAI API Key",
          "description": "Used by OpenClaw provider",
          "secret": true,
          "validator": { "kind": "non_empty" }
        }
      ],
      "optional": [
        {
          "key": "DISCORD_TOKEN",
          "label": "Discord Token",
          "description": "Enable Discord integration",
          "secret": true
        }
      ]
    }
  },
  "provision": [
    {
      "name": "project-setup",
      "shell": "bash",
      "script": "echo 'setup done'"
    }
  ]
}
```

---

## 7. Validation Rules

Hard validation:

- Missing required top-level fields => fail.
- Unsupported `schema_version` => fail.
- Unknown `bootstrap.id` => fail.
- Missing `bootstrap.install_script` or `bootstrap.start_script` => fail.
- Required params unresolved at runtime => fail before VM creation.

Security validation:

- Secret params should not be embedded as plain values in clawspec by default.
- Secret input in TUI must be masked.

Artifact validation:

- Downloaded artifacts must pass SHA256 verification.

---

## 8. `clawfarm new` Contract

`clawfarm new` must:

1. Let user choose bootstrap type (currently only `openclaw`).
2. Prompt required params first, optional params next.
3. Validate each input in TUI.
4. Mask secret inputs with `*`.
5. Generate clawspec with `bootstrap` section populated.

---

## 9. Compatibility Note

- `preset` is deprecated in spec terminology.
- New documents and code should use `bootstrap`.
