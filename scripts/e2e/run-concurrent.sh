#!/usr/bin/env bash
# Prove separate Mimir-backed Compose projects can run concurrently without shared resources or host-port collisions.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=common.sh
# shellcheck disable=SC1091
source "$script_dir/common.sh"

repo_root="$(e2e_repo_root)"
e2e_select_container_runtime

project_a="${E2E_PROJECT_A:-k6-e2e-a-$$}"
project_b="${E2E_PROJECT_B:-k6-e2e-b-$$}"
port_a="${GRAFANA_HOST_PORT_A:-${GRAFANA_HOST_PORT:-3000}}"
port_b="${GRAFANA_HOST_PORT_B:-3001}"
test_id_a="${E2E_TEST_ID_A:-$project_a}"
test_id_b="${E2E_TEST_ID_B:-$project_b}"

if [[ "$project_a" == "$project_b" ]]; then
  printf 'concurrent projects must be different\n' >&2
  exit 2
fi
if [[ "$test_id_a" == "$test_id_b" ]]; then
  printf 'concurrent test IDs must be different\n' >&2
  exit 2
fi
for port in "$port_a" "$port_b"; do
  if [[ ! "$port" =~ ^[1-9][0-9]*$ ]] || (( 10#$port > 65535 )); then
    printf 'Grafana host ports must be between 1 and 65535: %s\n' "$port" >&2
    exit 2
  fi
done
if [[ "$port_a" == "$port_b" ]]; then
  printf 'concurrent projects must use different Grafana host ports\n' >&2
  exit 2
fi

tmp_dir="$(mktemp -d -t k6-e2e-concurrent.XXXXXX)"
cleanup() {
  local status="$?"
  if [[ "${KEEP_E2E_STACK:-0}" != 1 ]]; then
    if ! e2e_compose "$project_a" "$port_a" "$test_id_a" "$repo_root" down --volumes --remove-orphans; then
      printf 'failed to remove the E2E project %s\n' "$project_a" >&2
      status=1
    fi
    if ! e2e_compose "$project_b" "$port_b" "$test_id_b" "$repo_root" down --volumes --remove-orphans; then
      printf 'failed to remove the E2E project %s\n' "$project_b" >&2
      status=1
    fi
  fi
  rm -rf -- "$tmp_dir"
  trap - EXIT
  exit "$status"
}
trap cleanup EXIT

e2e_compose "$project_a" "$port_a" "$test_id_a" "$repo_root" config >/dev/null
e2e_compose "$project_b" "$port_b" "$test_id_b" "$repo_root" config >/dev/null

e2e_compose "$project_a" "$port_a" "$test_id_a" "$repo_root" \
  up --build --detach provider pact-stub collector mimir tempo loki grafana \
  >"$tmp_dir/up-a.log" 2>&1 &
up_a_pid=$!
e2e_compose "$project_b" "$port_b" "$test_id_b" "$repo_root" \
  up --build --detach provider pact-stub collector mimir tempo loki grafana \
  >"$tmp_dir/up-b.log" 2>&1 &
up_b_pid=$!

up_a_status=0
up_b_status=0
wait "$up_a_pid" || up_a_status=$?
wait "$up_b_pid" || up_b_status=$?
if (( up_a_status != 0 || up_b_status != 0 )); then
  cat -- "$tmp_dir/up-a.log" "$tmp_dir/up-b.log"
  exit 1
fi

e2e_compose "$project_a" "$port_a" "$test_id_a" "$repo_root" \
  run --build --rm --no-deps -T benchmark \
  >"$tmp_dir/benchmark-a.log" 2>&1 &
benchmark_a_pid=$!
e2e_compose "$project_b" "$port_b" "$test_id_b" "$repo_root" \
  run --build --rm --no-deps -T benchmark \
  >"$tmp_dir/benchmark-b.log" 2>&1 &
benchmark_b_pid=$!

benchmark_a_status=0
benchmark_b_status=0
wait "$benchmark_a_pid" || benchmark_a_status=$?
wait "$benchmark_b_pid" || benchmark_b_status=$?
cat -- "$tmp_dir/benchmark-a.log" "$tmp_dir/benchmark-b.log"
if (( benchmark_a_status != 0 || benchmark_b_status != 0 )); then
  exit 1
fi

e2e_compose "$project_a" "$port_a" "$test_id_a" "$repo_root" logs --no-color provider >"$tmp_dir/provider-a.log"
e2e_compose "$project_b" "$port_b" "$test_id_b" "$repo_root" logs --no-color provider >"$tmp_dir/provider-b.log"
"$script_dir/assert-provider-logs.sh" "$tmp_dir/provider-a.log"
"$script_dir/assert-provider-logs.sh" "$tmp_dir/provider-b.log"

e2e_compose "$project_a" "$port_a" "$test_id_a" "$repo_root" \
  run --rm --no-deps -T assertion \
  >"$tmp_dir/assertion-a.log" 2>&1 &
assertion_a_pid=$!
e2e_compose "$project_b" "$port_b" "$test_id_b" "$repo_root" \
  run --rm --no-deps -T assertion \
  >"$tmp_dir/assertion-b.log" 2>&1 &
assertion_b_pid=$!

assertion_a_status=0
assertion_b_status=0
wait "$assertion_a_pid" || assertion_a_status=$?
wait "$assertion_b_pid" || assertion_b_status=$?
cat -- "$tmp_dir/assertion-a.log" "$tmp_dir/assertion-b.log"
if (( assertion_a_status != 0 || assertion_b_status != 0 )); then
  exit 1
fi

printf 'two-project concurrent OTLP E2E check passed for projects=%s,%s\n' "$project_a" "$project_b"
