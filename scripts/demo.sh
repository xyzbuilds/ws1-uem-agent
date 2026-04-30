#!/usr/bin/env bash
#
# Alice-lock end-to-end demo. Runs the v0 acceptance scenario from spec
# section 11 against a local mock WS1 tenant.
#
# Usage:  make demo
#         WS1_DEMO_INTERACTIVE=1 make demo   (you click Approve yourself)
#         WS1_DEMO_KEEP_AUDIT=1 make demo    (don't wipe audit log between runs)
#
# Defaults to auto-approve so the demo runs unattended.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

BIN_DIR="$ROOT_DIR/bin"
WS1="$BIN_DIR/ws1"
MOCK="$BIN_DIR/mockws1"
DEMO_DIR="$ROOT_DIR/.build/demo"
PORT=9911
BASE_URL="http://127.0.0.1:$PORT"

# --- helpers ---------------------------------------------------------------

c_bold=$(printf '\033[1m')
c_dim=$(printf '\033[2m')
c_blue=$(printf '\033[34m')
c_green=$(printf '\033[32m')
c_red=$(printf '\033[31m')
c_off=$(printf '\033[0m')

step() {
  printf '\n%s== %s ==%s\n' "$c_bold$c_blue" "$1" "$c_off"
}
note() {
  printf '%s  %s%s\n' "$c_dim" "$1" "$c_off"
}
ok() {
  printf '%s  ✓ %s%s\n' "$c_green" "$1" "$c_off"
}
fail() {
  printf '%s  ✗ %s%s\n' "$c_red" "$1" "$c_off"
  exit 1
}

cleanup() {
  if [[ -n "${MOCK_PID:-}" ]] && kill -0 "$MOCK_PID" 2>/dev/null; then
    note "stopping mock (pid $MOCK_PID)"
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# --- build -----------------------------------------------------------------

step "Building binaries"
mkdir -p "$BIN_DIR" "$DEMO_DIR"
go build -o "$WS1" ./cmd/ws1
go build -o "$MOCK" ./cmd/mockws1
ok "ws1 + mockws1 built"

# --- start mock ------------------------------------------------------------

step "Starting mock WS1 tenant on $BASE_URL"
"$MOCK" -addr "127.0.0.1:$PORT" >"$DEMO_DIR/mock.log" 2>&1 &
MOCK_PID=$!
note "mock pid=$MOCK_PID, log=$DEMO_DIR/mock.log"

# wait for /healthz-like readiness (the mock returns text on /).
for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
  if curl -fsS "$BASE_URL/" >/dev/null 2>&1; then
    ok "mock is ready"
    break
  fi
  sleep 0.2
done

# --- env setup -------------------------------------------------------------

step "Configuring CLI for mock tenant"
export WS1_BASE_URL="$BASE_URL"
export WS1_MOCK_TOKEN="demo-mock-token"
export WS1_CONFIG_DIR="$DEMO_DIR/cfg"
export WS1_NO_BROWSER=1
mkdir -p "$WS1_CONFIG_DIR"

if [[ "${WS1_DEMO_KEEP_AUDIT:-}" != "1" ]]; then
  rm -f "$WS1_CONFIG_DIR/audit.log"
fi

note "WS1_BASE_URL=$WS1_BASE_URL"
note "WS1_CONFIG_DIR=$WS1_CONFIG_DIR (clean for this run)"

"$WS1" og use 12345 >/dev/null
note "OG context: 12345"

# --- doctor ----------------------------------------------------------------

step "ws1 doctor"
"$WS1" doctor | jq '{ok: .ok, summary: .data.summary}'

# --- 1. Find Alice ---------------------------------------------------------

step "1. Find user alice@example.com"
USER_OUT=$("$WS1" systemv2 users search --email alice@example.com)
echo "$USER_OUT" | jq '{ok: .ok, count: .meta.count, users: [.data[] | {UserID, Email, FirstName, LastName}]}'
ALICE_UID=$(echo "$USER_OUT" | jq '.data[0].UserID')
[[ "$ALICE_UID" == "10001" ]] && ok "alice = UserID $ALICE_UID" || fail "wrong alice id: $ALICE_UID"

# --- 2. List Alice's devices -----------------------------------------------

step "2. List Alice's devices"
DEV_OUT=$("$WS1" mdmv4 devices search --user alice@example.com)
echo "$DEV_OUT" | jq '{ok: .ok, count: .meta.count, devices: [.data[] | {DeviceID, FriendlyName, EnrollmentStatus, OrganizationGroupName}]}'
DEV_COUNT=$(echo "$DEV_OUT" | jq '.meta.count')
[[ "$DEV_COUNT" == "2" ]] && ok "alice owns $DEV_COUNT devices" || fail "expected 2 devices, got $DEV_COUNT"

DEVICE_IDS=$(echo "$DEV_OUT" | jq -r '[.data[].DeviceID] | join(",")')
note "device IDs: $DEVICE_IDS"

# --- 3. Lock Alice's devices (bulk under threshold; no approval) -----------

step "3. Lock Alice's devices (bulk, under threshold -> no browser approval)"
LOCK_OUT=$("$WS1" --profile operator mdmv4 devices lock --ids "$DEVICE_IDS")
echo "$LOCK_OUT" | jq '{ok, op: .operation, success_count: .meta.success_count, failure_count: .meta.failure_count, audit: .meta.audit_log_entry}'
SUCC=$(echo "$LOCK_OUT" | jq '.meta.success_count')
[[ "$SUCC" == "2" ]] && ok "locked 2 devices" || fail "lock success count = $SUCC"

# --- 4. Wipe one device (destructive -> approval required) -----------------

step "4. Wipe Alice's iPhone (DESTRUCTIVE -> browser approval flow)"

if [[ "${WS1_DEMO_INTERACTIVE:-}" == "1" ]]; then
  note "interactive mode: open the URL the CLI prints and click Approve"
  "$WS1" --profile operator mdmv4 devices wipe --id 12345 \
    | jq '{ok, op: .operation, approval_request_id: .meta.approval_request_id}'
else
  note "auto-approving via curl on the CLI's stderr-printed URL"
  TMPSTDOUT=$(mktemp)
  TMPSTDERR=$(mktemp)
  trap "rm -f $TMPSTDOUT $TMPSTDERR" RETURN
  ("$WS1" --profile operator mdmv4 devices wipe --id 12345 \
    >"$TMPSTDOUT" 2>"$TMPSTDERR") &
  WIPE_PID=$!

  # Read stderr until we find the approval URL.
  APPROVAL_URL=""
  for _ in $(seq 1 60); do
    if grep -o 'http://127.0.0.1:[0-9]*/r/req_[a-z0-9]*' "$TMPSTDERR" 2>/dev/null | head -1; then
      APPROVAL_URL=$(grep -o 'http://127.0.0.1:[0-9]*/r/req_[a-z0-9]*' "$TMPSTDERR" | head -1)
      break
    fi
    sleep 0.1
  done >/dev/null

  if [[ -z "$APPROVAL_URL" ]]; then
    cat "$TMPSTDERR"
    fail "did not see approval URL on stderr"
  fi
  note "approval URL: $APPROVAL_URL"
  curl -fsS -X POST "$APPROVAL_URL/approve" >/dev/null
  ok "POST /approve sent"

  wait "$WIPE_PID" || true
  jq '{ok, op: .operation, approval_request_id: .meta.approval_request_id, audit: .meta.audit_log_entry}' < "$TMPSTDOUT"
fi

# --- 5. Verify the audit chain ---------------------------------------------

step "5. ws1 audit verify (chain integrity)"
"$WS1" audit verify | jq '{ok, total: .data.total, failures: .data.failures}'

step "6. ws1 audit tail (recent entries)"
"$WS1" audit tail --last 5 | jq '.data.entries[] | {seq, operation, class, result, profile, approval_request_id}'

# --- done ------------------------------------------------------------------

step "Demo complete"
note "mock log: $DEMO_DIR/mock.log"
note "audit log: $WS1_CONFIG_DIR/audit.log"
ok "alice-lock scenario executed end-to-end"
