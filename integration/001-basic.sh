#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VCLAW_BIN="${VCLAW_BIN:-${ROOT_DIR}/vclaw}"
INTEGRATION_TIMEOUT_SECS="${INTEGRATION_TIMEOUT_SECS:-90}"
INTEGRATION_GATEWAY_PORT="${INTEGRATION_GATEWAY_PORT:-18789}"
INTEGRATION_CANVAS_PORT="${INTEGRATION_CANVAS_PORT:-18793}"
INTEGRATION_IMAGE_REF="${INTEGRATION_IMAGE_REF:-ubuntu:24.04}"
INTEGRATION_ENABLE_RUN="${INTEGRATION_ENABLE_RUN:-0}"

TEST_TMP="${ROOT_DIR}/.tmp/integration-001"
CACHE_DIR="${TEST_TMP}/cache"
DATA_DIR="${TEST_TMP}/data"
WORKDIR="${TEST_TMP}/workspace"
RUN_LOG="${TEST_TMP}/vclaw-run.log"

mkdir -p "${TEST_TMP}" "${CACHE_DIR}" "${DATA_DIR}" "${WORKDIR}"
printf 'integration-001 %s\n' "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" >"${WORKDIR}/integration-001.txt"

echo "[001-basic] using binary: ${VCLAW_BIN}"
if [[ ! -x "${VCLAW_BIN}" ]]; then
  echo "[001-basic] error: binary not found or not executable: ${VCLAW_BIN}" >&2
  echo "[001-basic] hint: go build -o vclaw ./cmd/vclaw" >&2
  exit 1
fi

echo "[001-basic] checking core CLI entrypoints"
"${VCLAW_BIN}" --help >/dev/null
"${VCLAW_BIN}" image ls >/dev/null || true
"${VCLAW_BIN}" ps >/dev/null || true

echo "[001-basic] fetching image metadata/artifacts"
VCLAW_CACHE_DIR="${CACHE_DIR}" VCLAW_DATA_DIR="${DATA_DIR}" \
  "${VCLAW_BIN}" image fetch "${INTEGRATION_IMAGE_REF}"

if [[ "${INTEGRATION_ENABLE_RUN}" != "1" ]]; then
  echo "[001-basic] run stage skipped (set INTEGRATION_ENABLE_RUN=1 to enable)"
  exit 0
fi

echo "[001-basic] starting vclaw run"
VCLAW_CACHE_DIR="${CACHE_DIR}" VCLAW_DATA_DIR="${DATA_DIR}" \
  "${VCLAW_BIN}" run "${INTEGRATION_IMAGE_REF}" \
    --workspace="${WORKDIR}" \
    --port="${INTEGRATION_GATEWAY_PORT}" \
    --publish "${INTEGRATION_CANVAS_PORT}:80" \
    >"${RUN_LOG}" 2>&1 || {
      echo "[001-basic] run command failed" >&2
      tail -n 80 "${RUN_LOG}" >&2 || true
      exit 1
    }

echo "[001-basic] run output"
cat "${RUN_LOG}"

echo "[001-basic] currently run command is a metadata-only milestone checkpoint"
echo "[001-basic] full HTTP readiness validation will activate at RFC milestone M3-M5"
