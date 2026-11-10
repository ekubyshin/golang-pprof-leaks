#!/usr/bin/env bash
# Один веб-UI в переднем плане — тот же вызов, что и руками:
#   go tool pprof -http=127.0.0.1:PORT -diff_base BASE CURRENT
# Использование: ./scripts/pprof-diff.sh [сервис] [heap|goroutine] [порт]
set -euo pipefail

cd "$(dirname "$0")/.."

name="${1:-http_leak}"
kind="${2:-heap}"
port="${3:-}"

case "$kind" in
  heap)
    base="profiles/${name}.heap1.pb.gz"
    next="profiles/${name}.heap2.pb.gz"
    sample=(-inuse_space)
    port="${port:-9999}"
    ;;
  goroutine)
    base="profiles/${name}.goroutine1.pb.gz"
    next="profiles/${name}.goroutine2.pb.gz"
    sample=()
    port="${port:-9998}"
    ;;
  *)
    echo "второй аргумент: heap или goroutine" >&2
    exit 1
    ;;
esac

for f in "$base" "$next"; do
  if [[ ! -f "$f" ]]; then
    echo "нет файла ${f}: сначала ./scripts/capture-profiles.sh" >&2
    exit 1
  fi
done

report=$(go tool pprof -top -nodecount=5 "${sample[@]}" -diff_base "$base" "$next")
printf '%s\n\n' "$report"
if [[ "$report" == *"accounting for 0,"* ]]; then
  echo "Дельта нулевая: ${base} и ${next} совпали, flame graph будет пустым." >&2
  echo "Снимайте пару, пока счётчик в логе ещё растёт." >&2
  echo >&2
fi

echo "http://127.0.0.1:${port}"
exec go tool pprof -http="127.0.0.1:${port}" -diff_base "$base" "$next"
