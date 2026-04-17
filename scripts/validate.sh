#!/usr/bin/env bash
set -euo pipefail

PASS=0
FAIL=0

check() {
  local desc="$1"
  local expected="$2"
  local actual="$3"

  if [ "$actual" = "$expected" ]; then
    echo "  [PASS] $desc"
    PASS=$((PASS + 1))
  else
    echo "  [FAIL] $desc (expected '$expected', got '$actual')"
    FAIL=$((FAIL + 1))
  fi
}

echo ""
echo "=== vault-ui-proxy-test validation ==="
echo ""

# 1. Vault health via proxy
echo "--- Vault API ---"
HEALTH_STATUS=$(curl -sf http://localhost:8080/v1/sys/health | python3 -c "import sys,json; d=json.load(sys.stdin); print('ok' if not d['sealed'] and d['initialized'] else 'bad')" 2>/dev/null || echo "unreachable")
check "Vault health via proxy (unsealed + initialized)" "ok" "$HEALTH_STATUS"

HEALTH_DIRECT=$(curl -sf http://localhost:8200/v1/sys/health | python3 -c "import sys,json; d=json.load(sys.stdin); print('ok' if not d['sealed'] and d['initialized'] else 'bad')" 2>/dev/null || echo "unreachable")
check "Vault health direct (unsealed + initialized)" "ok" "$HEALTH_DIRECT"

echo ""
echo "--- CSS injection ---"

INJECTED=$(curl -sf http://localhost:8080/ui/ | grep -c 'override.css' || true)
check "override.css link injected in proxied /ui/" "1" "$INJECTED"

NOT_INJECTED=$(curl -sf http://localhost:8200/ui/ | grep -c 'override.css' || true)
check "override.css NOT present in direct /ui/" "0" "$NOT_INJECTED"

echo ""
echo "--- CSS file reachable ---"

CSS_STATUS=$(curl -so /dev/null -w "%{http_code}" http://localhost:8080/_env/override.css)
check "/_env/override.css returns 200" "200" "$CSS_STATUS"

echo ""
echo "--- API not rewritten ---"

API_CLEAN=$(curl -sf http://localhost:8080/v1/sys/health | grep -c 'override.css' || true)
check "API response does not contain override.css" "0" "$API_CLEAN"

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
echo ""

[ "$FAIL" -eq 0 ]
