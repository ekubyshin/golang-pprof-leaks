#!/usr/bin/env bash
# Первый снимок heap и goroutine с пяти сервисов: profiles/<сервис>.heap1.pb.gz
set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p profiles

services=(
  http_leak:8080
  goroutine_leak:8091
  client_leak:8092
  ctx_leak:8093
  db_leak:8094
)

for spec in "${services[@]}"; do
  name="${spec%%:*}"
  port="${spec##*:}"
  curl -fsS "http://127.0.0.1:${port}/debug/pprof/heap" \
    -o "profiles/${name}.heap1.pb.gz"
  curl -fsS "http://127.0.0.1:${port}/debug/pprof/goroutine" \
    -o "profiles/${name}.goroutine1.pb.gz"
  echo "saved ${name} #1"
done
