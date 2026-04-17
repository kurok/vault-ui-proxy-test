#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "Starting vault-ui-proxy-test stack..."
docker compose up -d

echo ""
echo "Waiting for Vault to become healthy..."
for i in $(seq 1 20); do
  if curl -sf http://localhost:8200/v1/sys/health > /dev/null 2>&1; then
    break
  fi
  printf "."
  sleep 1
done
echo ""

echo ""
echo "Stack is ready."
echo ""
echo "  Vault UI via NGINX (with overrides): http://localhost:8080/ui/"
echo "  Vault UI direct (no overrides):      http://localhost:8200/ui/"
echo "  Vault root token: root"
echo ""
echo "Run ./scripts/validate.sh to verify behavior."
