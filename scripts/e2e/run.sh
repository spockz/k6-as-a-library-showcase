#!/usr/bin/env bash
# Exercise one complete isolated Mimir-backed stack and clean its resources unless inspection is requested.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=common.sh
# shellcheck disable=SC1091
source "$script_dir/common.sh"

repo_root="$(e2e_repo_root)"
e2e_select_container_runtime

project="${E2E_PROJECT:-k6-e2e}"
grafana_port="${GRAFANA_HOST_PORT:-3000}"
test_id="${E2E_TEST_ID:-$project}"
if [[ ! "$grafana_port" =~ ^[1-9][0-9]*$ ]] || (( 10#$grafana_port > 65535 )); then
  printf 'GRAFANA_HOST_PORT must be between 1 and 65535: %s\n' "$grafana_port" >&2
  exit 2
fi

provider_log="$(mktemp -t k6-e2e-provider.XXXXXX)"
cleanup() {
  local status="$?"
  if [[ "${KEEP_E2E_STACK:-0}" != 1 ]]; then
    if ! e2e_compose "$project" "$grafana_port" "$test_id" "$repo_root" down --volumes --remove-orphans; then
      printf 'failed to remove the E2E project %s\n' "$project" >&2
      status=1
    fi
  fi
  rm -f -- "$provider_log"
  trap - EXIT
  exit "$status"
}
trap cleanup EXIT

e2e_compose "$project" "$grafana_port" "$test_id" "$repo_root" config >/dev/null
e2e_compose \
  "$project" "$grafana_port" "$test_id" "$repo_root" \
  up --build --detach provider pact-stub collector mimir tempo loki grafana
e2e_compose \
  "$project" "$grafana_port" "$test_id" "$repo_root" \
  run --build --rm --no-deps -T benchmark

e2e_compose "$project" "$grafana_port" "$test_id" "$repo_root" logs --no-color provider >"$provider_log"
"$script_dir/assert-provider-logs.sh" "$provider_log"
e2e_compose \
  "$project" "$grafana_port" "$test_id" "$repo_root" \
  run --rm --no-deps -T assertion

printf 'single-project OTLP E2E check passed for project=%s testid=%s\n' "$project" "$test_id"
