#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "Stopping vault-ui-proxy-test stack..."
docker compose down
echo "Done."
