#!/usr/bin/env bash
# Снимает heap и goroutine с пяти сервисов, ждёт и снимает ещё раз, затем печатает -base.
# Пауза в секундах — первый аргумент, по умолчанию 30.
set -euo pipefail

cd "$(dirname "$0")/.."

pause="${1:-30}"

mkdir -p profiles

services=(
  http_leak:8080
  goroutine_leak:8091
  client_leak:8092
  ctx_leak:8093
  db_leak:8094
)

snap() {
  local pass="$1"
  local name port
  for spec in "${services[@]}"; do
    name="${spec%%:*}"
    port="${spec##*:}"
    curl -fsS "http://127.0.0.1:${port}/debug/pprof/heap" \
      -o "profiles/${name}.heap${pass}.pb.gz"
    curl -fsS "http://127.0.0.1:${port}/debug/pprof/goroutine" \
      -o "profiles/${name}.goroutine${pass}.pb.gz"
    echo "saved ${name} #${pass}"
  done
}

snap 1
sleep "$pause"
snap 2

for spec in "${services[@]}"; do
  name="${spec%%:*}"
  echo
  echo "===== ${name}: goroutine -base ====="
  go tool pprof -top -cum -base \
    "profiles/${name}.goroutine1.pb.gz" \
    "profiles/${name}.goroutine2.pb.gz"

  echo
  echo "===== ${name}: heap inuse_space -base ====="
  go tool pprof -top -cum -inuse_space -base \
    "profiles/${name}.heap1.pb.gz" \
    "profiles/${name}.heap2.pb.gz"
done
