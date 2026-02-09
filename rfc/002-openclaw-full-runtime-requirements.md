# RFC 002 — Full OpenClaw Runtime Requirements Study and Plan

- **Status:** Draft
- **Date:** 2026-02-09
- **Project:** `vclaw`
- **Related:** `rfc/001-initial-design.md`

## 1. Summary

This RFC documents the full set of requirements to run OpenClaw reliably inside `vclaw`, with emphasis on **runtime parameters**, especially **agent/model authentication inputs** (API keys/tokens), gateway auth, and channel credentials.

It also defines an implementation plan so `vclaw` can support a repeatable “full OpenClaw” deployment rather than a minimal gateway bootstrap.

## 2. Scope

### 2.1 In scope

- Runtime prerequisites for OpenClaw (host + guest expectations).
- Required vs optional configuration parameters.
- Model provider authentication requirements (API keys/tokens).
- Channel credential requirements (Discord/Telegram/WhatsApp).
- A concrete implementation plan for `vclaw`.

### 2.2 Out of scope

- Replacing OpenClaw onboarding UX.
- OpenClaw internal feature design.
- Non-VM deployments unrelated to `vclaw`.

## 3. Study method and source policy

This study uses OpenClaw’s official documentation pages only (captured on 2026-02-09). See references in Section 12.

## 4. Requirement model

We classify requirements into three levels:

- **L0: Boot requirement** — needed to start OpenClaw process at all.
- **L1: Usable local runtime** — needed for local agent chat (Control UI / local gateway).
- **L2: Full runtime** — needed for production-like behavior: authenticated models + channels + secure gateway access.

## 5. Requirements study results

### 5.1 Base runtime prerequisites

- Node.js `>=22` is required.
- Supported host environments are macOS, Linux, and Windows via WSL2.
- For standard install, global CLI install path is expected (`openclaw` command available).

**Classification:** L0

### 5.2 Gateway process requirements

- `gateway.mode` must be explicitly set to `local` for local gateway startup (unless override flags are used).
- `gateway.port` defaults to `18789` if not overridden.
- Gateway auth is expected by default (`token`/`password` mode behavior); onboarding generates token by default and docs map env auth inputs to `OPENCLAW_GATEWAY_TOKEN` / `OPENCLAW_GATEWAY_PASSWORD`.
- Non-loopback binds require shared token/password controls.

**Classification:** L1 (mode/port), L2 (secure auth/bind exposure)

### 5.3 Agent/workspace baseline

- A workspace path must be set for practical agent operation (`agents.defaults.workspace` in config patterns).
- A model must resolve to a valid provider/model id and usable credentials.

**Classification:** L1

### 5.4 Model authentication requirements (API keys / tokens)

At least one authenticated provider is required for normal AI responses.

#### Provider credential matrix

| Provider | Typical model id prefix | Credential input | Required when selected |
|---|---|---|---|
| OpenAI | `openai/...` | `OPENAI_API_KEY` | Yes |
| Anthropic | `anthropic/...` | `ANTHROPIC_API_KEY` (recommended) or setup-token flow | Yes |
| Gemini | `gemini/...` | `GOOGLE_GENERATIVE_AI_API_KEY` | Yes |
| Grok (xAI) | `grok/...` | `XAI_API_KEY` | Yes |
| OpenRouter | `openrouter/...` | `OPENROUTER_API_KEY` | Yes |
| Z.AI | `zai/...` | `ZAI_API_KEY` | Yes |
| Ollama / LM Studio | `ollama/...`, `lmstudio/...` | No API key; local model service required | Yes (service availability instead of key) |

**Classification:** L1 minimum = at least one provider path must pass auth; L2 = explicit provider policy and health checks.

### 5.5 Channel requirements (for “full OpenClaw” behavior)

No messaging channel is required for first local chat (Control UI can work without channel setup), but channels are required for multi-channel runtime.

#### Channel credential matrix

| Channel | Required parameters |
|---|---|
| Discord | `discordToken` |
| Telegram | `telegramToken` |
| WhatsApp Cloud API | `whatsappPhoneNumberId`, `whatsappAccessToken`, `whatsappVerifyToken`, `whatsappAppSecret`, webhook reachability; allow-list strongly recommended |

**Classification:** L2

### 5.6 HTTP/API client auth requirements

For HTTP API surfaces (such as OpenResponses-compatible endpoint), bearer authentication follows gateway auth configuration:

- token mode uses configured gateway token (`OPENCLAW_GATEWAY_TOKEN`),
- password mode uses configured gateway password (`OPENCLAW_GATEWAY_PASSWORD`).

**Classification:** L2

### 5.7 Daemon/service env requirements

If OpenClaw runs as launchd/systemd service, provider credentials must be available to the service environment (e.g. `.env` path used by OpenClaw tooling/docs).

**Classification:** L2

## 6. Consolidated full requirement checklist

To run **full OpenClaw** in `vclaw`, the minimum complete parameter set should be:

1. Runtime:
   - Node `>=22`
   - OpenClaw CLI installed
2. Gateway:
   - `gateway.mode=local`
   - `gateway.port` (default `18789` if omitted)
   - `gateway.auth.mode` + token/password material
3. Agent defaults:
   - `agents.defaults.workspace`
   - `agents.defaults.model.primary`
4. Model auth:
   - At least one valid provider credential (or local model backend for keyless providers)
5. Optional channels (for full multi-channel usage):
   - Discord token and/or Telegram token and/or WhatsApp Cloud credentials
6. Service consistency:
   - Credentials available in runtime environment used by background daemon

## 7. Gap analysis against current `vclaw`

Current `vclaw` guest bootstrap installs/runs OpenClaw and starts gateway, but does not yet provide a structured contract for:

- Provider API key injection and validation,
- gateway auth policy configuration lifecycle,
- channel credential provisioning,
- full requirement preflight checks before run.

## 8. Implementation plan for `vclaw`

### M1 — Requirement contract (CLI + config)

- Add explicit `vclaw run` inputs for OpenClaw runtime requirements:
  - `--openclaw-config` (path),
  - `--openclaw-env-file` (path),
  - selective `--openclaw-env KEY=VALUE` passthrough.
- Define precedence rules (flag > env-file > default generated config).

### M2 — Secret injection and storage policy

- Inject provider/channel secrets into guest via cloud-init at boot.
- Keep secrets out of command-line process args and logs.
- Write guest-side `.env` with restrictive permissions.

### M3 — Gateway/auth and model validation

- Preflight validation in host CLI before VM boot:
  - missing required model auth,
  - invalid gateway auth combination,
  - channel selected but missing token set.
- In-guest validation after boot:
  - `openclaw models status`,
  - gateway health endpoint checks.

### M4 — Channel provisioning path

- Add optional channel profile blocks in `vclaw` config:
  - `discord`, `telegram`, `whatsapp`.
- Generate OpenClaw config sections from profile and mount safely.

### M5 — Integration test matrix

Add integration scenarios:

1. **L1 baseline:** one provider key, no channels, Control UI/gateway reachable.
2. **L2 secure gateway:** token auth enforced; unauth request rejected.
3. **L2 channel config:** startup fails fast when channel selected but credentials missing.
4. **L2 daemon restart persistence:** creds and config survive restart.

### M6 — Operator docs and runbook

- Add `docs/openclaw-runtime-requirements.md` with provider/channel examples.
- Add troubleshooting matrix for auth failures and token mismatches.

## 9. Acceptance criteria

This RFC is considered implemented when:

1. `vclaw` can start OpenClaw with explicit provider credentials and pass model health checks.
2. Missing required credentials fail with actionable errors before long boot waits.
3. Gateway auth policy is explicit and testable.
4. Channel requirements are validated by config contract and integration tests.
5. Operator documentation includes a complete parameter checklist.

## 10. Open questions

1. Should `vclaw` manage OpenClaw onboarding state, or require pre-generated config/env from host?
2. Should `vclaw` support multiple provider credentials simultaneously in first release?
3. For WhatsApp webhook flow, should `vclaw` include built-in reverse tunnel helpers or keep it external?
4. Should default mode be “fail if no model auth”, even when local keyless providers are configured?

## 11. Suggested next RFCs

- RFC 003: `vclaw` secret-injection architecture and threat model.
- RFC 004: OpenClaw channel profile schema for `vclaw`.
- RFC 005: `vclaw doctor` preflight checks for OpenClaw full runtime.

## 12. References (official OpenClaw docs)

- https://docs.openclaw.ai/
- https://docs.openclaw.ai/start/quickstart
- https://docs.openclaw.ai/install/index
- https://docs.openclaw.ai/gateway/configuration
- https://docs.openclaw.ai/gateway/authentication
- https://docs.openclaw.ai/models/providers
- https://docs.openclaw.ai/wizard/quickstart
- https://docs.openclaw.ai/channels/discord
- https://docs.openclaw.ai/channels/telegram
- https://docs.openclaw.ai/channels/whatsapp
- https://docs.openclaw.ai/gateway/openresponses-http-api
