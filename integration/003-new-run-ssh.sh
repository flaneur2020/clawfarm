#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLAWFARM_BIN="${CLAWFARM_BIN:-${ROOT_DIR}/clawfarm}"
INTEGRATION_IMAGE_REF="${INTEGRATION_IMAGE_REF:-ubuntu:24.04}"
INTEGRATION_GATEWAY_PORT="${INTEGRATION_GATEWAY_PORT:-auto}"
INTEGRATION_RUN_MARKER="${INTEGRATION_RUN_MARKER:-integration-003-ok}"

TEST_TMP="${ROOT_DIR}/.tmp/integration-003"
HOME_DIR="${TEST_TMP}/home"
WORKDIR="${TEST_TMP}/workspace"
FETCH_LOG="${TEST_TMP}/fetch.log"
RUN_LOG="${TEST_TMP}/new.log"
PS_LOG="${TEST_TMP}/ps.log"
CLAWID=""

pick_free_port() {
  python3 - <<'PY'
import socket

sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
}

cleanup() {
  if [[ -n "${CLAWID}" ]]; then
    HOME="${HOME_DIR}" "${CLAWFARM_BIN}" rm "${CLAWID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

rm -rf "${TEST_TMP}"
mkdir -p "${TEST_TMP}" "${HOME_DIR}" "${WORKDIR}"
printf 'integration-003 %s\n' "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" >"${WORKDIR}/integration-003.txt"

if [[ "${INTEGRATION_GATEWAY_PORT}" == "auto" ]]; then
  INTEGRATION_GATEWAY_PORT="$(pick_free_port)"
fi

echo "[003-new-run-ssh] using binary: ${CLAWFARM_BIN}"
if [[ ! -x "${CLAWFARM_BIN}" ]]; then
  echo "[003-new-run-ssh] error: binary not found or not executable: ${CLAWFARM_BIN}" >&2
  echo "[003-new-run-ssh] hint: go build -o clawfarm ./cmd/clawfarm" >&2
  exit 1
fi

echo "[003-new-run-ssh] fetching image"
set +e
HOME="${HOME_DIR}" "${CLAWFARM_BIN}" image fetch "${INTEGRATION_IMAGE_REF}" >"${FETCH_LOG}" 2>&1
FETCH_EXIT=$?
set -e
if [[ "${FETCH_EXIT}" -ne 0 ]]; then
  echo "[003-new-run-ssh] image fetch failed" >&2
  tail -n 120 "${FETCH_LOG}" >&2 || true
  exit 1
fi

RUN_COMMAND="echo ${INTEGRATION_RUN_MARKER} > /tmp/ssh-run-ok.txt"

echo "[003-new-run-ssh] starting instance via new + --run over SSH"
set +e
HOME="${HOME_DIR}" "${CLAWFARM_BIN}" new "${INTEGRATION_IMAGE_REF}" \
  --workspace="${WORKDIR}" \
  --port="${INTEGRATION_GATEWAY_PORT}" \
  --run "${RUN_COMMAND}" \
  --volume ".openclaw:/root/.openclaw" \
  >"${RUN_LOG}" 2>&1
RUN_EXIT=$?
set -e

if [[ "${RUN_EXIT}" -ne 0 ]]; then
  CLAWID="$(awk '/^CLAWID:/{print $2; exit}' "${RUN_LOG}" || true)"
  if [[ -z "${CLAWID}" ]]; then
    CLAWID="$(sed -nE 's#.*\/claws\/([^/]+)/instance\.img.*#\1#p' "${RUN_LOG}" | head -n 1 || true)"
  fi
  echo "[003-new-run-ssh] new command failed" >&2
  tail -n 200 "${RUN_LOG}" >&2 || true
  exit 1
fi

echo "[003-new-run-ssh] new output"
cat "${RUN_LOG}"

if ! grep -q '^ssh: claw@127\.0\.0\.1:' "${RUN_LOG}"; then
  echo "[003-new-run-ssh] expected ssh endpoint in output" >&2
  exit 1
fi

CLAWID="$(awk '/^CLAWID:/{print $2; exit}' "${RUN_LOG}" || true)"
if [[ -z "${CLAWID}" ]]; then
  echo "[003-new-run-ssh] failed to parse CLAWID from new output" >&2
  exit 1
fi

SSH_DIR="${HOME_DIR}/.clawfarm/claws/${CLAWID}/ssh"
if [[ ! -f "${SSH_DIR}/id_ed25519" || ! -f "${SSH_DIR}/id_ed25519.pub" ]]; then
  echo "[003-new-run-ssh] expected instance ssh keypair under ${SSH_DIR}" >&2
  ls -la "${SSH_DIR}" >&2 || true
  exit 1
fi

SSH_PORT="$(awk '/^ssh: claw@127\.0\.0\.1:/{split($2, parts, ":"); print parts[2]; exit}' "${RUN_LOG}" || true)"
if [[ -z "${SSH_PORT}" ]]; then
  echo "[003-new-run-ssh] failed to parse ssh port from new output" >&2
  exit 1
fi

RUN_RESULT="$(ssh \
  -o BatchMode=yes \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null \
  -o IdentitiesOnly=yes \
  -o ConnectTimeout=5 \
  -o LogLevel=ERROR \
  -i "${SSH_DIR}/id_ed25519" \
  -p "${SSH_PORT}" \
  claw@127.0.0.1 \
  "sudo -n cat /tmp/ssh-run-ok.txt" 2>/dev/null || true)"
if [[ "${RUN_RESULT}" != "${INTEGRATION_RUN_MARKER}" ]]; then
  echo "[003-new-run-ssh] run command result mismatch: got='${RUN_RESULT}' want='${INTEGRATION_RUN_MARKER}'" >&2
  exit 1
fi

VOLUME_DIR="${HOME_DIR}/.clawfarm/claws/${CLAWID}/volumes/.openclaw"
if [[ ! -d "${VOLUME_DIR}" ]]; then
  echo "[003-new-run-ssh] expected volume directory at ${VOLUME_DIR}" >&2
  ls -la "${VOLUME_DIR}" >&2 || true
  exit 1
fi

echo "[003-new-run-ssh] verifying instance appears in ps"
HOME="${HOME_DIR}" "${CLAWFARM_BIN}" ps >"${PS_LOG}"
cat "${PS_LOG}"
if ! grep -q "^${CLAWID}[[:space:]]" "${PS_LOG}"; then
  echo "[003-new-run-ssh] instance not found in ps output" >&2
  exit 1
fi
if grep -q "^${CLAWID}[[:space:]].*[[:space:]]unhealthy[[:space:]]" "${PS_LOG}"; then
  echo "[003-new-run-ssh] instance unexpectedly unhealthy" >&2
  exit 1
fi

echo "[003-new-run-ssh] success"
