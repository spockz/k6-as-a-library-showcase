#!/usr/bin/env bash
# Run the source-mounted benchmark and persist reports and logs on the writable output volume.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
wait_for_http="$script_dir/wait-for-http.sh"

provider_url="${E2E_PROVIDER_URL:-http://provider:8080}"
test_id="${E2E_TEST_ID:-k6-e2e}"
iterations="${E2E_ITERATIONS:-45}"
vus="${E2E_VUS:-1}"
min_iteration_duration="${E2E_MIN_ITERATION_DURATION:-25ms}"
request_timeout="${E2E_REQUEST_TIMEOUT:-5s}"
max_duration="${E2E_MAX_DURATION:-30s}"
readiness_timeout="${E2E_READINESS_TIMEOUT_SECONDS:-60}"
trace_output="${K6_TRACES_OUTPUT:-otel=http://collector:4318,proto=http}"
log_file=/out/benchmark.log

if [[ ! "$iterations" =~ ^[1-9][0-9]*$ ]]; then
  printf 'E2E_ITERATIONS must be a positive integer: %s\n' "$iterations" >&2
  exit 2
fi
if [[ ! "$vus" =~ ^[1-9][0-9]*$ ]]; then
  printf 'E2E_VUS must be a positive integer: %s\n' "$vus" >&2
  exit 2
fi
if [[ ! "$test_id" =~ ^[A-Za-z0-9_.:/-]+$ ]]; then
  printf 'E2E_TEST_ID contains characters unsafe for telemetry queries: %s\n' "$test_id" >&2
  exit 2
fi

run_benchmark() {
  local help
  local status
  local -a args

  if ! help="$(go run . run --help 2>&1)"; then
    printf '%s\n' 'the benchmark CLI could not be built' >&2
    printf '%s\n' "$help" >&2
    return 78
  fi
  if ! grep --fixed-strings --quiet -- '--out' <<<"$help"; then
    printf '%s\n' 'the benchmark CLI has no --out opentelemetry option' >&2
    return 78
  fi
  if ! grep --fixed-strings --quiet -- '--traces-output' <<<"$help"; then
    printf '%s\n' 'the benchmark CLI has no --traces-output option' >&2
    return 78
  fi
  if ! grep --fixed-strings --quiet -- '--combined-output' <<<"$help"; then
    printf '%s\n' 'the benchmark CLI has no --combined-output option' >&2
    return 78
  fi
  if ! grep --fixed-strings --quiet -- '--pact-provider-url' <<<"$help"; then
    printf '%s\n' 'the benchmark CLI has no --pact-provider-url option' >&2
    return 78
  fi

  rm -f -- /out/metrics.json /out/report.html /out/combined.html
  printf 'benchmark_started testid=%s\n' "$test_id"
  if ! "$wait_for_http" "$provider_url/headers" "$readiness_timeout"; then
    return 1
  fi
  if ! "$wait_for_http" http://collector:13133/ "$readiness_timeout"; then
    return 1
  fi

  args=(
    run
    --pact-provider-url "$provider_url"
    --pacts-dir /workspace/testdata/pacts
    --vus "$vus"
    --iterations "$iterations"
    --min-iteration-duration "$min_iteration_duration"
    --request-timeout "$request_timeout"
    --max-duration "$max_duration"
    --json-output /out/metrics.json
    --html-output /out/report.html
    --combined-output /out/combined.html
    --out opentelemetry
    --traces-output "$trace_output"
  )

  if go run . "${args[@]}"; then
    status=0
  else
    status=$?
  fi
  printf 'benchmark_finished testid=%s status=%s\n' "$test_id" "$status"
  return "$status"
}

: > "$log_file"
set +e
run_benchmark >"$log_file" 2>&1
status=$?
set -e
cat -- "$log_file"
exit "$status"
