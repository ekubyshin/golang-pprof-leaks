#!/usr/bin/env bash
# Второй снимок heap и goroutine с шести сервисов: profiles/<сервис>.heap2.pb.gz
set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p profiles

services=(
  http_leak:8080
  goroutine_leak:8091
  client_leak:8092
  ctx_leak:8093
  db_leak:8094
  tx_leak:8095
)

for spec in "${services[@]}"; do
  name="${spec%%:*}"
  port="${spec##*:}"
  curl -fsS "http://127.0.0.1:${port}/debug/pprof/heap" \
    -o "profiles/${name}.heap2.pb.gz"
  curl -fsS "http://127.0.0.1:${port}/debug/pprof/goroutine" \
    -o "profiles/${name}.goroutine2.pb.gz"
  echo "saved ${name} #2"
done
