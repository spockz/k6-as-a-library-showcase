#!/usr/bin/env bash
# Run the source-mounted benchmark and persist reports and logs on the writable output volume.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
wait_for_http="$script_dir/wait-for-http.sh"

test_id="${E2E_TEST_ID:-k6-e2e}"
iterations="${E2E_ITERATIONS:-45}"
vus="${E2E_VUS:-1}"
request_timeout="${E2E_REQUEST_TIMEOUT:-5s}"
max_duration="${E2E_MAX_DURATION:-30s}"
readiness_timeout="${E2E_READINESS_TIMEOUT_SECONDS:-60}"
trace_output="${K6_TRACES_OUTPUT:-otel=http://collector:4318,proto=http}"

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
  local backend="$1"
  local provider_url="$2"
  local help
  local status
  local -a args
  local test_id_for_backend="${test_id}-${backend}"
  local suffix="-${backend}"
  local log_file="/out/benchmark${suffix}.log"
  local expected_status

  case "$backend" in
    httpbin) expected_status=1 ;;
    pact-stub) expected_status=0 ;;
    *)
      printf 'unsupported E2E backend: %s\n' "$backend" >&2
      return 64
      ;;
  esac

  : > "$log_file"
  {
    printf 'benchmark_started testid=%s backend=%s\n' "$test_id_for_backend" "$backend"

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

  rm -f -- "/out/metrics${suffix}.json" "/out/report${suffix}.html" "/out/combined${suffix}.html"
  if ! "$wait_for_http" "$provider_url/base64/UGFjdCBleGFtcGxl" "$readiness_timeout"; then
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
    --request-timeout "$request_timeout"
    --max-duration "$max_duration"
    --json-output "/out/metrics${suffix}.json"
    --html-output "/out/report${suffix}.html"
    --combined-output "/out/combined${suffix}.html"
    --out opentelemetry
    --traces-output "$trace_output"
  )

  if OTEL_RESOURCE_ATTRIBUTES="${OTEL_RESOURCE_ATTRIBUTES:+${OTEL_RESOURCE_ATTRIBUTES},}testid=${test_id_for_backend}" go run . "${args[@]}"; then
    status=0
  else
    status=$?
  fi
  printf 'benchmark_finished testid=%s backend=%s status=%s\n' "$test_id_for_backend" "$backend" "$status"
  } >>"$log_file" 2>&1

  if (( status != expected_status )); then
    printf 'unexpected benchmark status for %s: got %s, want %s\n' "$backend" "$status" "$expected_status" >&2
    return 65
  fi
  if [[ "$backend" == httpbin ]] && ! grep --fixed-strings --quiet -- 'checks failed: check "pact response matches" failed' "$log_file"; then
    printf 'go-httpbin did not report the deliberate Pact mismatch\n' >&2
    return 65
  fi
  if [[ "$backend" == pact-stub ]] && grep --fixed-strings --quiet -- 'checks failed: check "pact response matches" failed' "$log_file"; then
    printf 'Pact stub-server unexpectedly failed Pact response checks\n' >&2
    return 65
  fi
  return "$status"
}

set +e
run_benchmark httpbin "${E2E_PROVIDER_URL:-http://provider:8080}"
first_status=$?
run_benchmark pact-stub http://pact-stub:8080
second_status=$?
set -e
cat -- /out/benchmark-*.log
if (( first_status != 1 || second_status != 0 )); then
  printf 'expected go-httpbin to expose the deliberate mismatch and Pact stub-server to satisfy the contract: httpbin=%s pact-stub=%s\n' "$first_status" "$second_status" >&2
  exit 1
fi
exit 0
