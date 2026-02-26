#!/usr/bin/env bash
# Functional smoke test for the devd CLI.
# Run from repo root: bash test-functional.sh
#
# Tests the full basic-usage lifecycle:
#   build → run → ps → ssh → stop → ps → start → ssh → rm → ps

set -euo pipefail

DEVD="./bin/devd"
WORKSPACE="test-functional"
SSH_KEY="${HOME}/.devd/ssh/devd_ed25519"
SSH_USER="root"
SSH_HOST="127.0.0.1"

# ── Helpers ───────────────────────────────────────────────────────────────────

step() {
    echo ""
    echo "=== STEP $1: $2 ==="
}

fail() {
    echo ""
    echo "FAIL: $1"
    if [[ -n "${2:-}" ]]; then echo "  expected: $2"; fi
    if [[ -n "${3:-}" ]]; then echo "  actual:   $3"; fi
    exit 1
}

# ── Cleanup trap ──────────────────────────────────────────────────────────────

cleanup() {
    echo ""
    echo "--- cleanup: removing $WORKSPACE (if it exists) ---"
    "${DEVD}" rm -f "${WORKSPACE}" 2>/dev/null || true
}
trap cleanup EXIT

# ── STEP 1: Build ─────────────────────────────────────────────────────────────

step 1 "Build devd"
go build -o bin/devd ./cmd/devd
echo "OK built ./bin/devd"

# ── STEP 2: devd run ──────────────────────────────────────────────────────────

step 2 "devd run nicolaka/netshoot --name ${WORKSPACE}"
run_output=$("${DEVD}" run nicolaka/netshoot --name "${WORKSPACE}" 2>&1)
echo "${run_output}"

# Parse the SSH port from the summary block: "     Port:    2222"
ssh_port=$(echo "${run_output}" | grep -E '^\s+Port:\s+[0-9]+' | grep -oE '[0-9]+' | head -1)
if [[ -z "${ssh_port}" ]]; then
    fail "could not parse SSH port from 'devd run' output" \
         "a line matching '     Port:    <N>'" \
         "(no match found)"
fi
echo "OK devd run exited 0, SSH port: ${ssh_port}"

# ── STEP 3: devd ps — should show state=running ───────────────────────────────

step 3 "devd ps — verify workspace is running"
ps_output=$("${DEVD}" ps 2>&1)
echo "${ps_output}"

if ! echo "${ps_output}" | grep -qE "^${WORKSPACE}\s"; then
    fail "'devd ps' does not list workspace '${WORKSPACE}'" \
         "a row starting with '${WORKSPACE}'" \
         "(not found)"
fi
if ! echo "${ps_output}" | grep -E "^${WORKSPACE}\s" | grep -q "running"; then
    actual_state=$(echo "${ps_output}" | grep -E "^${WORKSPACE}\s" | awk '{print $3}')
    fail "workspace state is not 'running'" "running" "${actual_state}"
fi
echo "OK workspace '${WORKSPACE}' is running"

# ── STEP 4: SSH connectivity ──────────────────────────────────────────────────

step 4 "SSH into workspace — echo hello"
ssh_result=$(ssh \
    -i "${SSH_KEY}" \
    -p "${ssh_port}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=5 \
    -o BatchMode=yes \
    -o LogLevel=ERROR \
    "${SSH_USER}@${SSH_HOST}" \
    echo hello 2>/dev/stderr)
if [[ "${ssh_result}" != "hello" ]]; then
    fail "SSH echo did not return 'hello'" "hello" "${ssh_result}"
fi
echo "OK SSH works, got: ${ssh_result}"

# ── STEP 5: devd stop ─────────────────────────────────────────────────────────

step 5 "devd stop ${WORKSPACE}"
"${DEVD}" stop "${WORKSPACE}"
echo "OK devd stop exited 0"

# ── STEP 6: devd ps — should show state=stopped ───────────────────────────────

step 6 "devd ps --all — verify workspace is stopped"
ps_output=$("${DEVD}" ps --all 2>&1)
echo "${ps_output}"

if ! echo "${ps_output}" | grep -qE "^${WORKSPACE}\s"; then
    fail "'devd ps --all' does not list workspace '${WORKSPACE}'" \
         "a row starting with '${WORKSPACE}'" \
         "(not found)"
fi
if ! echo "${ps_output}" | grep -E "^${WORKSPACE}\s" | grep -q "stopped"; then
    actual_state=$(echo "${ps_output}" | grep -E "^${WORKSPACE}\s" | awk '{print $3}')
    fail "workspace state is not 'stopped'" "stopped" "${actual_state}"
fi
echo "OK workspace '${WORKSPACE}' is stopped"

# ── STEP 7: devd start ────────────────────────────────────────────────────────

step 7 "devd start ${WORKSPACE}"
"${DEVD}" start "${WORKSPACE}"
echo "OK devd start exited 0"

# ── STEP 8: SSH again after restart ───────────────────────────────────────────

step 8 "SSH into workspace after restart — echo hello"
ssh_result=$(ssh \
    -i "${SSH_KEY}" \
    -p "${ssh_port}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=5 \
    -o BatchMode=yes \
    -o LogLevel=ERROR \
    "${SSH_USER}@${SSH_HOST}" \
    echo hello 2>/dev/stderr)
if [[ "${ssh_result}" != "hello" ]]; then
    fail "SSH echo after restart did not return 'hello'" "hello" "${ssh_result}"
fi
echo "OK SSH works after restart, got: ${ssh_result}"

# ── STEP 9: devd rm -f ────────────────────────────────────────────────────────

step 9 "devd rm -f ${WORKSPACE}"
"${DEVD}" rm -f "${WORKSPACE}"
echo "OK devd rm exited 0"

# ── STEP 10: devd ps — workspace must be gone ────────────────────────────────

step 10 "devd ps --all — verify workspace is removed"
ps_output=$("${DEVD}" ps --all 2>&1)
echo "${ps_output}"

if echo "${ps_output}" | grep -qE "^${WORKSPACE}\s"; then
    fail "workspace '${WORKSPACE}' still appears in 'devd ps --all' after rm" \
         "(no row for '${WORKSPACE}')" \
         "(row found)"
fi
echo "OK workspace '${WORKSPACE}' is gone"

# ── Done ──────────────────────────────────────────────────────────────────────

echo ""
echo "=== ALL TESTS PASSED ==="
