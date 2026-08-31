#!/usr/bin/env bash
# Supply bounded provider-neutral readiness checks before telemetry assertions run.
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  printf 'usage: %s URL TIMEOUT_SECONDS\n' "$0" >&2
  exit 2
fi

url="$1"
timeout_seconds="$2"
if ! command -v wget >/dev/null 2>&1; then
  printf '%s\n' 'wget is required for HTTP readiness checks' >&2
  exit 2
fi
if [[ ! "$timeout_seconds" =~ ^[0-9]+$ ]]; then
  printf 'timeout must be a non-negative integer: %s\n' "$timeout_seconds" >&2
  exit 2
fi

started_at="$(date +%s)"
deadline="$((started_at + 10#$timeout_seconds))"
while :; do
  if wget --quiet --tries=1 --timeout=2 --output-document=/dev/null "$url"; then
    exit 0
  fi

  now="$(date +%s)"
  if (( now >= deadline )); then
    printf 'timed out waiting for HTTP 200 from %s after %s seconds\n' "$url" "$timeout_seconds" >&2
    exit 1
  fi
  sleep 1
done
