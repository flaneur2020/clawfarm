#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLAWFARM_BIN="${CLAWFARM_BIN:-${ROOT_DIR}/vclaw}"
INTEGRATION_IMAGE_REF="${INTEGRATION_IMAGE_REF:-ubuntu:24.04}"
INTEGRATION_GATEWAY_PORT="${INTEGRATION_GATEWAY_PORT:-19289}"
INTEGRATION_READY_TIMEOUT_SECS="${INTEGRATION_READY_TIMEOUT_SECS:-300}"

TEST_TMP="${ROOT_DIR}/.tmp/integration-002"
HOME_DIR="${TEST_TMP}/home"
WORKDIR="${TEST_TMP}/workspace"
FETCH_LOG="${TEST_TMP}/image-fetch.log"
RUN_LOG="${TEST_TMP}/run.log"
CLAWID=""

sanitize_ref() {
  local value="$1"
  value="${value//:/_}"
  value="${value//@/_}"
  value="${value//\//_}"
  echo "${value}"
}

cleanup() {
  if [[ -n "${CLAWID}" ]]; then
    HOME="${HOME_DIR}" "${CLAWFARM_BIN}" rm "${CLAWID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

rm -rf "${TEST_TMP}"
mkdir -p "${TEST_TMP}" "${HOME_DIR}" "${WORKDIR}"
printf 'integration-002 %s\n' "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" >"${WORKDIR}/integration-002.txt"

echo "[002-cache-copy] using binary: ${CLAWFARM_BIN}"
if [[ ! -x "${CLAWFARM_BIN}" ]]; then
  echo "[002-cache-copy] error: binary not found or not executable: ${CLAWFARM_BIN}" >&2
  echo "[002-cache-copy] hint: go build -o vclaw ./cmd/vclaw" >&2
  exit 1
fi

echo "[002-cache-copy] fetching image with isolated HOME"
set +e
HOME="${HOME_DIR}" "${CLAWFARM_BIN}" image fetch "${INTEGRATION_IMAGE_REF}" >"${FETCH_LOG}" 2>&1
FETCH_EXIT=$?
set -e
if [[ "${FETCH_EXIT}" -ne 0 ]]; then
  echo "[002-cache-copy] image fetch failed" >&2
  tail -n 120 "${FETCH_LOG}" >&2 || true
  exit 1
fi

if ! grep -Eq '100\.0%|using cached image' "${FETCH_LOG}"; then
  echo "[002-cache-copy] expected progress or cached message in first fetch log" >&2
  tail -n 80 "${FETCH_LOG}" >&2 || true
  exit 1
fi

echo "[002-cache-copy] fetching image again should be cached (no download)"
set +e
HOME="${HOME_DIR}" "${CLAWFARM_BIN}" image fetch "${INTEGRATION_IMAGE_REF}" >"${TEST_TMP}/second-fetch.log" 2>&1
SECOND_FETCH_EXIT=$?
set -e
if [[ "${SECOND_FETCH_EXIT}" -ne 0 ]]; then
  echo "[002-cache-copy] second fetch failed" >&2
  tail -n 80 "${TEST_TMP}/second-fetch.log" >&2 || true
  exit 1
fi

if ! grep -q "using cached image" "${TEST_TMP}/second-fetch.log"; then
  echo "[002-cache-copy] expected cached-image message on second fetch" >&2
  cat "${TEST_TMP}/second-fetch.log" >&2
  exit 1
fi

if grep -Eq 'image  \[' "${TEST_TMP}/second-fetch.log"; then
  echo "[002-cache-copy] second fetch unexpectedly showed download progress" >&2
  cat "${TEST_TMP}/second-fetch.log" >&2
  exit 1
fi

IMAGE_NAME="$(sanitize_ref "${INTEGRATION_IMAGE_REF}")"
IMAGE_DIR="${HOME_DIR}/.vclaw/images/${IMAGE_NAME}"
SOURCE_DISK="${IMAGE_DIR}/image.img"

if [[ ! -f "${SOURCE_DISK}" ]]; then
  echo "[002-cache-copy] expected cached disk at ${SOURCE_DISK}" >&2
  exit 1
fi

echo "[002-cache-copy] creating instance"
set +e
HOME="${HOME_DIR}" "${CLAWFARM_BIN}" run "${INTEGRATION_IMAGE_REF}" \
  --workspace="${WORKDIR}" \
  --port="${INTEGRATION_GATEWAY_PORT}" \
  --ready-timeout-secs="${INTEGRATION_READY_TIMEOUT_SECS}" \
  >"${RUN_LOG}" 2>&1
RUN_EXIT=$?
set -e
if [[ "${RUN_EXIT}" -ne 0 ]]; then
  echo "[002-cache-copy] run failed" >&2
  tail -n 160 "${RUN_LOG}" >&2 || true
  exit 1
fi

CLAWID="$(awk '/^CLAWID:/{print $2; exit}' "${RUN_LOG}" || true)"
if [[ -z "${CLAWID}" ]]; then
  echo "[002-cache-copy] failed to parse CLAWID from run output" >&2
  cat "${RUN_LOG}" >&2
  exit 1
fi

INSTANCE_IMG="${HOME_DIR}/.vclaw/instances/${CLAWID}/instance.img"
if [[ ! -f "${INSTANCE_IMG}" ]]; then
  echo "[002-cache-copy] expected copied instance image at ${INSTANCE_IMG}" >&2
  ls -la "${HOME_DIR}/.vclaw/instances/${CLAWID}" >&2 || true
  exit 1
fi

SOURCE_SIZE="$(stat -f%z "${SOURCE_DISK}")"
INSTANCE_SIZE="$(stat -f%z "${INSTANCE_IMG}")"
if [[ "${INSTANCE_SIZE}" -lt "${SOURCE_SIZE}" ]]; then
  echo "[002-cache-copy] unexpected size shrink: source=${SOURCE_SIZE} instance=${INSTANCE_SIZE}" >&2
  exit 1
fi

echo "[002-cache-copy] verified ~/.vclaw image cache and per-instance copy"
echo "[002-cache-copy] success"
