#!/usr/bin/env bash
set -euo pipefail

# Basic integration smoke test for krunclaw.
#
# This script is intended to be stable while implementation evolves.
# It verifies:
# 1) krunclaw binary is callable
# 2) basic CLI entrypoints exist
# 3) (optional) VM run path can be exercised and HTTP surface becomes reachable
#
# Environment overrides:
# - KRUNCLAW_BIN: path to krunclaw binary
# - INTEGRATION_WORKDIR: host directory mounted as workspace
# - INTEGRATION_TIMEOUT_SECS: readiness timeout for run mode
# - INTEGRATION_GATEWAY_PORT: host gateway port
# - INTEGRATION_CANVAS_PORT: host canvas port
# - INTEGRATION_ENABLE_RUN: set to 1 to execute `krunclaw run`

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KRUNCLAW_BIN="${KRUNCLAW_BIN:-${ROOT_DIR}/target/debug/krunclaw}"
INTEGRATION_TIMEOUT_SECS="${INTEGRATION_TIMEOUT_SECS:-60}"
INTEGRATION_GATEWAY_PORT="${INTEGRATION_GATEWAY_PORT:-18789}"
INTEGRATION_CANVAS_PORT="${INTEGRATION_CANVAS_PORT:-18793}"
INTEGRATION_ENABLE_RUN="${INTEGRATION_ENABLE_RUN:-0}"

TEST_TMP="${ROOT_DIR}/.tmp/integration-001"
WORKDIR="${INTEGRATION_WORKDIR:-${TEST_TMP}/workspace}"
LOG_FILE="${TEST_TMP}/krunclaw-run.log"
RUN_PID=""

cleanup() {
  if [[ -n "${RUN_PID}" ]] && kill -0 "${RUN_PID}" 2>/dev/null; then
    kill "${RUN_PID}" || true
    wait "${RUN_PID}" || true
  fi
}
trap cleanup EXIT

mkdir -p "${TEST_TMP}" "${WORKDIR}"
printf 'integration-001 %s\n' "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" > "${WORKDIR}/integration-001.txt"

echo "[001-basic] using binary: ${KRUNCLAW_BIN}"
if [[ ! -x "${KRUNCLAW_BIN}" ]]; then
  echo "[001-basic] error: binary not found or not executable: ${KRUNCLAW_BIN}" >&2
  echo "[001-basic] hint: cargo build --bin krunclaw" >&2
  exit 1
fi

echo "[001-basic] checking core CLI entrypoints"
"${KRUNCLAW_BIN}" --help >/dev/null
"${KRUNCLAW_BIN}" doctor --help >/dev/null
"${KRUNCLAW_BIN}" image --help >/dev/null
"${KRUNCLAW_BIN}" run --help >/dev/null

echo "[001-basic] smoke doctor invocation"
"${KRUNCLAW_BIN}" doctor || true

if [[ "${INTEGRATION_ENABLE_RUN}" != "1" ]]; then
  echo "[001-basic] run stage skipped (set INTEGRATION_ENABLE_RUN=1 to enable)"
  exit 0
fi

echo "[001-basic] starting krunclaw run"
"${KRUNCLAW_BIN}" run \
  --port "${INTEGRATION_GATEWAY_PORT}" \
  --publish "${INTEGRATION_CANVAS_PORT}:${INTEGRATION_CANVAS_PORT}" \
  >"${LOG_FILE}" 2>&1 &
RUN_PID="$!"

echo "[001-basic] waiting for gateway readiness on http://127.0.0.1:${INTEGRATION_GATEWAY_PORT}/"
for ((i=1; i<=INTEGRATION_TIMEOUT_SECS; i++)); do
  if curl -fsS "http://127.0.0.1:${INTEGRATION_GATEWAY_PORT}/" >/dev/null 2>&1; then
    echo "[001-basic] gateway became reachable"
    exit 0
  fi
  sleep 1
done

echo "[001-basic] error: gateway did not become reachable within ${INTEGRATION_TIMEOUT_SECS}s" >&2
if [[ -f "${LOG_FILE}" ]]; then
  echo "[001-basic] --- run log tail ---" >&2
  tail -n 80 "${LOG_FILE}" >&2 || true
fi
exit 1

