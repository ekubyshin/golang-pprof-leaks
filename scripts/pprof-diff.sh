#!/usr/bin/env bash
# Открывает -diff_base для одного сервиса: goroutine и heap одновременно.
# Использование: ./scripts/pprof-diff.sh [сервис] [порт goroutine] [порт heap]
set -euo pipefail

cd "$(dirname "$0")/.."

name="${1:-http_leak}"
goroutine_port="${2:-9091}"
heap_port="${3:-9090}"

base_goroutine="profiles/${name}.goroutine1.pb.gz"
next_goroutine="profiles/${name}.goroutine2.pb.gz"
base_heap="profiles/${name}.heap1.pb.gz"
next_heap="profiles/${name}.heap2.pb.gz"

for f in "$base_goroutine" "$next_goroutine" "$base_heap" "$next_heap"; do
  if [[ ! -f "$f" ]]; then
    echo "нет файла ${f}: сначала ./scripts/capture-profiles.sh" >&2
    exit 1
  fi
done

pids=()
cleanup() {
  local pid
  if ((${#pids[@]})); then
    for pid in "${pids[@]}"; do
      kill "$pid" 2>/dev/null || true
    done
  fi
}
trap cleanup EXIT INT TERM

go tool pprof -http="127.0.0.1:${goroutine_port}" -diff_base \
  "$base_goroutine" "$next_goroutine" &
pids+=($!)

go tool pprof -http="127.0.0.1:${heap_port}" -diff_base \
  "$base_heap" "$next_heap" &
pids+=($!)

echo "goroutine http://127.0.0.1:${goroutine_port}"
echo "heap      http://127.0.0.1:${heap_port}"
wait
