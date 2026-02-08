#!/usr/bin/env bash
set -euo pipefail

# Basic integration smoke test for krunclaw.
#
# This script verifies:
# 1) krunclaw binary is callable
# 2) core CLI entrypoints exist
# 3) (optional) VM run path can be exercised and HTTP surface becomes reachable
#
# Environment overrides:
# - KRUNCLAW_BIN: path to krunclaw binary
# - INTEGRATION_WORKDIR: host directory mounted as workspace
# - INTEGRATION_TIMEOUT_SECS: readiness timeout for run mode
# - INTEGRATION_GATEWAY_PORT: host gateway port
# - INTEGRATION_CANVAS_PORT: host canvas port
# - INTEGRATION_ENABLE_RUN: set to 1 to execute `krunclaw run`
# - INTEGRATION_FETCH_IMAGE: set to 1 to run `krunclaw image fetch` before run
# - INTEGRATION_IMAGE: logical image name (default: default)
# - INTEGRATION_IMAGE_URL: explicit disk URL (optional)
# - INTEGRATION_UBUNTU_DATE: release date for Ubuntu cloud image (e.g. 20260108)
# - INTEGRATION_ARCH: arch override (e.g. x86_64, aarch64)
# - INTEGRATION_DISK: custom disk image path
# - INTEGRATION_DISK_FORMAT: auto|raw|qcow2|vmdk (default: auto)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KRUNCLAW_BIN="${KRUNCLAW_BIN:-${ROOT_DIR}/target/debug/krunclaw}"
INTEGRATION_TIMEOUT_SECS="${INTEGRATION_TIMEOUT_SECS:-60}"
INTEGRATION_GATEWAY_PORT="${INTEGRATION_GATEWAY_PORT:-18789}"
INTEGRATION_CANVAS_PORT="${INTEGRATION_CANVAS_PORT:-18793}"
INTEGRATION_ENABLE_RUN="${INTEGRATION_ENABLE_RUN:-0}"
INTEGRATION_FETCH_IMAGE="${INTEGRATION_FETCH_IMAGE:-0}"
INTEGRATION_IMAGE="${INTEGRATION_IMAGE:-default}"
INTEGRATION_IMAGE_URL="${INTEGRATION_IMAGE_URL:-}"
INTEGRATION_UBUNTU_DATE="${INTEGRATION_UBUNTU_DATE:-}"
INTEGRATION_ARCH="${INTEGRATION_ARCH:-}"
INTEGRATION_DISK="${INTEGRATION_DISK:-}"
INTEGRATION_DISK_FORMAT="${INTEGRATION_DISK_FORMAT:-auto}"

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

DISK_ARGS=()
if [[ -n "${INTEGRATION_DISK}" ]]; then
  DISK_ARGS+=(--disk "${INTEGRATION_DISK}")
fi

if [[ "${INTEGRATION_FETCH_IMAGE}" == "1" ]]; then
  echo "[001-basic] fetching ubuntu community image '${INTEGRATION_IMAGE}'"
  FETCH_ARGS=(image fetch --image "${INTEGRATION_IMAGE}" "${DISK_ARGS[@]}" --force)

  if [[ -n "${INTEGRATION_IMAGE_URL}" ]]; then
    FETCH_ARGS+=(--url "${INTEGRATION_IMAGE_URL}")
  fi
  if [[ -n "${INTEGRATION_UBUNTU_DATE}" ]]; then
    FETCH_ARGS+=(--ubuntu-date "${INTEGRATION_UBUNTU_DATE}")
  fi
  if [[ -n "${INTEGRATION_ARCH}" ]]; then
    FETCH_ARGS+=(--arch "${INTEGRATION_ARCH}")
  fi

  "${KRUNCLAW_BIN}" "${FETCH_ARGS[@]}"
fi

if [[ "${INTEGRATION_ENABLE_RUN}" != "1" ]]; then
  echo "[001-basic] run stage skipped (set INTEGRATION_ENABLE_RUN=1 to enable)"
  exit 0
fi

echo "[001-basic] starting krunclaw run"
RUN_ARGS=(
  run
  --image "${INTEGRATION_IMAGE}"
  --disk-format "${INTEGRATION_DISK_FORMAT}"
  --port "${INTEGRATION_GATEWAY_PORT}"
  --publish "${INTEGRATION_CANVAS_PORT}:${INTEGRATION_CANVAS_PORT}"
  "${DISK_ARGS[@]}"
)

if [[ -n "${INTEGRATION_IMAGE_URL}" ]]; then
  RUN_ARGS+=(--image-url "${INTEGRATION_IMAGE_URL}")
fi
if [[ -n "${INTEGRATION_UBUNTU_DATE}" ]]; then
  RUN_ARGS+=(--ubuntu-date "${INTEGRATION_UBUNTU_DATE}")
fi
if [[ -n "${INTEGRATION_ARCH}" ]]; then
  RUN_ARGS+=(--arch "${INTEGRATION_ARCH}")
fi

"${KRUNCLAW_BIN}" "${RUN_ARGS[@]}" >"${LOG_FILE}" 2>&1 &
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
  tail -n 120 "${LOG_FILE}" >&2 || true
fi
exit 1
