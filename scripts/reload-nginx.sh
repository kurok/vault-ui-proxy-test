#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "Testing NGINX config..."
docker compose exec nginx nginx -t

echo "Reloading NGINX..."
docker compose exec nginx nginx -s reload
echo "Done. Changes to nginx/static/env-override.css are live."
