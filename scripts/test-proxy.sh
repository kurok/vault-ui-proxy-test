#!/usr/bin/env bash
# Integration tests for Vault API through the NGINX proxy.
# Requires: vault CLI, curl, python3
# Usage: ./scripts/test-proxy.sh [--proxy http://localhost:8080] [--direct http://localhost:8200]
set -euo pipefail

PROXY="${PROXY:-http://localhost:8080}"
DIRECT="${DIRECT:-http://localhost:8200}"
ROOT_TOKEN="${VAULT_TOKEN:-root}"

export VAULT_TOKEN="$ROOT_TOKEN"

PASS=0
FAIL=0
SKIP=0

# ── helpers ──────────────────────────────────────────────────────────────────

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
gray()  { printf '\033[90m%s\033[0m\n' "$*"; }

check() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    green "  [PASS] $desc"
    PASS=$((PASS+1))
  else
    red   "  [FAIL] $desc"
    red   "         expected: $expected"
    red   "         actual:   $actual"
    FAIL=$((FAIL+1))
  fi
}

check_contains() {
  local desc="$1" needle="$2" haystack="$3"
  if echo "$haystack" | grep -qF "$needle"; then
    green "  [PASS] $desc"
    PASS=$((PASS+1))
  else
    red   "  [FAIL] $desc (not found: $needle)"
    FAIL=$((FAIL+1))
  fi
}

check_header() {
  local desc="$1" header="$2" url="$3"
  local val
  val=$(curl -sf -H "X-Vault-Token: $ROOT_TOKEN" "$url" -I 2>/dev/null | grep -i "^$header:" || true)
  if [ -n "$val" ]; then
    green "  [PASS] $desc ($val)"
    PASS=$((PASS+1))
  else
    red   "  [FAIL] $desc (header '$header' missing from $url)"
    FAIL=$((FAIL+1))
  fi
}

vault_proxy() { VAULT_ADDR="$PROXY" vault "$@"; }
vault_direct() { VAULT_ADDR="$DIRECT" vault "$@"; }

# Cleanup: disable any test mounts/auth created during the run
cleanup() {
  gray "\nCleaning up..."
  VAULT_ADDR="$PROXY" vault secrets disable proxy-test/ 2>/dev/null || true
  VAULT_ADDR="$PROXY" vault auth disable userpass/ 2>/dev/null || true
  VAULT_ADDR="$PROXY" vault policy delete proxy-test-policy 2>/dev/null || true
}
trap cleanup EXIT

# ── pre-flight ────────────────────────────────────────────────────────────────

echo ""
echo "=== vault-ui-proxy-test :: API + Integration Tests ==="
echo ""
printf "  Proxy:  %s\n" "$PROXY"
printf "  Direct: %s\n" "$DIRECT"
printf "  Token:  %s\n" "$ROOT_TOKEN"
echo ""

echo "--- Pre-flight ---"

HAS_VAULT=$(command -v vault >/dev/null 2>&1 && echo yes || echo no)
check "vault CLI available" "yes" "$HAS_VAULT"

PROXY_UP=$(curl -sf "$PROXY/v1/sys/health" >/dev/null 2>&1 && echo yes || echo no)
check "proxy reachable" "yes" "$PROXY_UP"

DIRECT_UP=$(curl -sf "$DIRECT/v1/sys/health" >/dev/null 2>&1 && echo yes || echo no)
check "vault direct reachable" "yes" "$DIRECT_UP"

# ── system endpoints ──────────────────────────────────────────────────────────

echo ""
echo "--- System Endpoints ---"

INITIALIZED=$(vault_proxy read -field=initialized sys/health 2>/dev/null || echo "false")
check "sys/health: initialized=true" "true" "$INITIALIZED"

SEALED=$(vault_proxy read -field=sealed sys/health 2>/dev/null || echo "true")
check "sys/health: sealed=false" "false" "$SEALED"

SEAL_TYPE=$(vault_proxy read -field=type sys/seal-status 2>/dev/null || echo "")
check "sys/seal-status: type is shamir" "shamir" "$SEAL_TYPE"

MOUNT_COUNT=$(vault_proxy secrets list -format=json 2>/dev/null | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
[ "$MOUNT_COUNT" -gt 0 ] && R="yes" || R="no"
check "sys/mounts: at least one mount present" "yes" "$R"

# ── KV operations ─────────────────────────────────────────────────────────────

echo ""
echo "--- KV Operations (mount: proxy-test/) ---"

vault_proxy secrets enable -path=proxy-test kv-v2 >/dev/null 2>&1
check "enable KV v2 at proxy-test/" "0" "$?"

vault_proxy kv put proxy-test/hello greeting=world >/dev/null 2>&1
check "kv put proxy-test/hello" "0" "$?"

VAL=$(vault_proxy kv get -field=greeting proxy-test/hello 2>/dev/null)
check "kv get proxy-test/hello (value matches)" "world" "$VAL"

vault_proxy kv put proxy-test/special "key with spaces=value-123" "emoji=ok" >/dev/null 2>&1
check "kv put with unusual key name" "0" "$?"

EMOJI=$(vault_proxy kv get -field=emoji proxy-test/special 2>/dev/null)
check "kv get special key" "ok" "$EMOJI"

vault_proxy kv put proxy-test/hello greeting=updated >/dev/null 2>&1
VAL2=$(vault_proxy kv get -field=greeting proxy-test/hello 2>/dev/null)
check "kv update creates new version" "updated" "$VAL2"

VERSION=$(vault_proxy kv metadata get -format=json proxy-test/hello 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['current_version'])" 2>/dev/null || echo "0")
check "kv metadata shows version 2" "2" "$VERSION"

LISTED=$(vault_proxy kv list -format=json proxy-test/ 2>/dev/null | python3 -c "import sys,json; keys=json.load(sys.stdin); print('yes' if 'hello' in keys else 'no')" 2>/dev/null || echo "no")
check "kv list includes proxy-test/hello" "yes" "$LISTED"

vault_proxy kv delete proxy-test/hello >/dev/null 2>&1
check "kv delete version" "0" "$?"

vault_proxy kv destroy -versions=1,2 proxy-test/hello >/dev/null 2>&1
check "kv destroy all versions" "0" "$?"

# ── large payload ─────────────────────────────────────────────────────────────

echo ""
echo "--- Large Payload (64 KB) ---"

BIG=$(python3 -c "print('x' * 65536)")
vault_proxy kv put proxy-test/bigval data="$BIG" >/dev/null 2>&1
check "write 64 KB secret" "0" "$?"

GOT=$(vault_proxy kv get -field=data proxy-test/bigval 2>/dev/null)
LEN=${#GOT}
check "read 64 KB secret back (length matches)" "65536" "$LEN"

# ── token operations ──────────────────────────────────────────────────────────

echo ""
echo "--- Token Operations ---"

CHILD_TOKEN=$(vault_proxy token create -ttl=5m -format=json 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin)['auth']['client_token'])" 2>/dev/null || echo "")
[ -n "$CHILD_TOKEN" ] && R="yes" || R="no"
check "token create (child token returned)" "yes" "$R"

SELF_ID=$(vault_proxy token lookup -format=json 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['id'])" 2>/dev/null || echo "")
check "token lookup-self (id is root)" "root" "$SELF_ID"

VAULT_ADDR="$PROXY" VAULT_TOKEN="$CHILD_TOKEN" vault token renew >/dev/null 2>&1
check "token renew child token" "0" "$?"

VAULT_ADDR="$PROXY" vault token revoke "$CHILD_TOKEN" >/dev/null 2>&1
check "token revoke child" "0" "$?"

# ── policy operations ─────────────────────────────────────────────────────────

echo ""
echo "--- Policy Operations ---"

vault_proxy policy write proxy-test-policy - >/dev/null 2>&1 <<'POLICY'
path "proxy-test/*" {
  capabilities = ["read", "create", "update", "delete", "list"]
}
POLICY
check "policy write proxy-test-policy" "0" "$?"

POLICY_COUNT=$(vault_proxy policy list -format=json 2>/dev/null | python3 -c "import sys,json; p=json.load(sys.stdin); print('yes' if 'proxy-test-policy' in p else 'no')" 2>/dev/null || echo "no")
check "policy list includes proxy-test-policy" "yes" "$POLICY_COUNT"

POLICY_BODY=$(vault_proxy policy read proxy-test-policy 2>/dev/null)
check_contains "policy read returns path rule" "proxy-test/*" "$POLICY_BODY"

vault_proxy policy delete proxy-test-policy >/dev/null 2>&1
check "policy delete" "0" "$?"

# ── auth methods ──────────────────────────────────────────────────────────────

echo ""
echo "--- Auth Methods (userpass) ---"

vault_proxy auth enable userpass >/dev/null 2>&1
check "auth enable userpass" "0" "$?"

vault_proxy write auth/userpass/users/testuser password=testpass123 >/dev/null 2>&1
check "create userpass user" "0" "$?"

LOGIN_TOKEN=$(VAULT_ADDR="$PROXY" vault login -method=userpass -format=json username=testuser password=testpass123 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin)['auth']['client_token'])" 2>/dev/null || echo "")
[ -n "$LOGIN_TOKEN" ] && R="yes" || R="no"
check "login with userpass returns token" "yes" "$R"

vault_proxy auth disable userpass/ >/dev/null 2>&1
check "auth disable userpass" "0" "$?"

# ── HTTP proxy header verification ────────────────────────────────────────────

echo ""
echo "--- HTTP / Proxy Headers ---"

check_header "Content-Type on API response" "content-type" "$PROXY/v1/sys/health"

CT_API=$(curl -sf -H "X-Vault-Token: $ROOT_TOKEN" "$PROXY/v1/sys/health" -I 2>/dev/null | grep -i "^content-type:" | tr -d '\r')
check_contains "API Content-Type is application/json" "application/json" "$CT_API"

CT_UI=$(curl -sf "$PROXY/ui/" -I 2>/dev/null | grep -i "^content-type:" | tr -d '\r')
check_contains "UI Content-Type is text/html" "text/html" "$CT_UI"

# Vault 2.x: request_id is in JSON body, not a response header
REQ_ID=$(curl -sf -H "X-Vault-Token: $ROOT_TOKEN" "$PROXY/v1/auth/token/lookup-self" | python3 -c "import sys,json; d=json.load(sys.stdin); print('present' if d.get('request_id') else 'missing')" 2>/dev/null || echo "missing")
check "request_id present in API JSON body" "present" "$REQ_ID"

# X-Forwarded-For is added by NGINX to the upstream request (not the browser response).
# Verify it is configured in the proxy by checking the NGINX config.
XFF_CFG=$(docker compose exec nginx grep -c "X-Forwarded-For" /etc/nginx/conf.d/default.conf 2>/dev/null || echo "0")
check "X-Forwarded-For configured in NGINX proxy_set_header" "2" "$XFF_CFG"

# ── injection isolation ───────────────────────────────────────────────────────

echo ""
echo "--- Injection Isolation ---"

API_INJECT=$(curl -sf -H "X-Vault-Token: $ROOT_TOKEN" "$PROXY/v1/sys/health" | grep -c 'override.css' || true)
check "API response NOT injected" "0" "$API_INJECT"

UI_INJECT=$(curl -sf "$PROXY/ui/" | grep -c 'override.css' || true)
check "UI response IS injected" "1" "$UI_INJECT"

DIRECT_INJECT=$(curl -sf "$DIRECT/ui/" | grep -c 'override.css' || true)
check "Direct Vault UI NOT injected" "0" "$DIRECT_INJECT"

JSON_VALID=$(curl -sf -H "X-Vault-Token: $ROOT_TOKEN" "$PROXY/v1/sys/health" | python3 -c "import sys,json; json.load(sys.stdin); print('valid')" 2>/dev/null || echo "invalid")
check "API JSON is valid (not corrupted by sub_filter)" "valid" "$JSON_VALID"

# ── results ───────────────────────────────────────────────────────────────────

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
echo ""

[ "$FAIL" -eq 0 ]
