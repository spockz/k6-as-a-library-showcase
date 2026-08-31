#!/usr/bin/env bash
# Confirm the containerized provider received every Pact endpoint used by the E2E workload.
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  printf 'usage: %s PROVIDER_LOG\n' "$0" >&2
  exit 2
fi

log_file="$1"
if [[ ! -s "$log_file" ]]; then
  printf 'provider log is missing or empty: %s\n' "$log_file" >&2
  exit 1
fi

for path in \
  /get \
  /post \
  /json \
  /base64/UGFjdCBleGFtcGxl \
  /response-headers \
  /cookies/set \
  /status/204 \
  /status/418 \
  /status/200; do
  if ! grep --fixed-strings --quiet -- "$path" "$log_file"; then
    printf 'provider log has no request for %s\n' "$path" >&2
    exit 1
  fi
done

printf 'provider request log contains all Pact endpoint paths\n'
