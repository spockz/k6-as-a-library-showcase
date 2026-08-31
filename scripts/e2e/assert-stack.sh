#!/usr/bin/env bash
# Verify benchmark artifacts and every telemetry path, including Mimir queries, before the stack is healthy.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
wait_for_http="$script_dir/wait-for-http.sh"

test_id="${E2E_TEST_ID:-k6-e2e}"
expected_iterations="${E2E_EXPECTED_ITERATIONS:-45}"
readiness_timeout="${E2E_READINESS_TIMEOUT_SECONDS:-60}"
query_timeout="${E2E_QUERY_TIMEOUT_SECONDS:-30}"
settle_seconds="${E2E_SETTLE_SECONDS:-5}"
metrics_file=/out/metrics.json
report_file=/out/report.html
combined_report_file=/out/combined.html
log_file=/out/benchmark.log

fail() {
  printf 'E2E assertion failed: %s\n' "$1" >&2
  exit 1
}

if [[ ! "$expected_iterations" =~ ^[1-9][0-9]*$ ]]; then
  fail "E2E_EXPECTED_ITERATIONS must be a positive integer"
fi
if [[ ! "$readiness_timeout" =~ ^[0-9]+$ ]] || (( 10#$readiness_timeout > 300 )); then
  fail "E2E_READINESS_TIMEOUT_SECONDS must be between 0 and 300"
fi
if [[ ! "$query_timeout" =~ ^[1-9][0-9]*$ ]] || (( 10#$query_timeout > 300 )); then
  fail "E2E_QUERY_TIMEOUT_SECONDS must be between 1 and 300"
fi
if [[ ! "$settle_seconds" =~ ^[0-9]+$ ]] || (( 10#$settle_seconds > 60 )); then
  fail "E2E_SETTLE_SECONDS must be between 0 and 60"
fi
if [[ ! "$test_id" =~ ^[A-Za-z0-9_.:/-]+$ ]]; then
  fail "E2E_TEST_ID contains characters unsafe for telemetry queries"
fi

expected_iterations_number=$((10#$expected_iterations))
expected_failure_minimum=$((expected_iterations_number / 9))
if (( expected_failure_minimum < 1 )); then
  expected_failure_minimum=1
fi

"$wait_for_http" http://provider:8080/headers "$readiness_timeout"
"$wait_for_http" http://collector:13133/ "$readiness_timeout"
"$wait_for_http" http://mimir:9009/ready "$readiness_timeout"
"$wait_for_http" http://tempo:3200/ready "$readiness_timeout"
"$wait_for_http" http://loki:3100/ready "$readiness_timeout"
"$wait_for_http" http://grafana:3000/api/health "$readiness_timeout"

if (( settle_seconds > 0 )); then
  sleep "$settle_seconds"
fi

for artifact in "$metrics_file" "$report_file" "$combined_report_file" "$log_file"; do
  if [[ ! -s "$artifact" ]]; then
    fail "artifact is missing or empty: $artifact"
  fi
done

for fragment in \
  'K6 Reporter v3.0.4' \
  'Detailed Metrics' \
  'id="combined-graphs"' \
  'id="combined-graphs-frame"' \
  'id="combined-tables"' \
  'id="combined-tagged-series-table"' \
  'pact response matches' \
  'rate==1' \
  'AGPL-3.0'; do
  if ! grep --fixed-strings --quiet -- "$fragment" "$combined_report_file"; then
    fail "combined report is missing $fragment"
  fi
done

if ! jq -e -s 'length > 0 and all(.[]; type == "object")' "$metrics_file" >/dev/null; then
  fail "metrics.json is not a non-empty JSON-lines object stream"
fi
if ! jq -e -s --argjson expected "$expected_iterations_number" '
  ([.[] | select(.type == "Point" and .metric == "http_reqs")] | length) == $expected
  and
  (([.[] | select(.type == "Point" and .metric == "http_reqs") | .data.value] | add) == $expected)
' "$metrics_file" >/dev/null; then
  fail "http_reqs JSON points do not equal the requested iteration count"
fi
if ! jq -e -s --argjson minimum "$expected_failure_minimum" '
  (([.[] | select(.type == "Point" and .metric == "http_req_failed") | .data.value] | add) >= $minimum)
' "$metrics_file" >/dev/null; then
  fail "JSON metrics contain fewer failed requests than the deliberate Pact mismatch minimum"
fi
if ! jq -e -s '
  any(.[]; .type == "Point" and .metric == "http_reqs" and .data.tags.provider_service == "httpbin")
' "$metrics_file" >/dev/null; then
  fail "JSON metrics contain no request tagged with the Pact provider"
fi

for endpoint in \
  'GET /get' \
  'POST /post' \
  'GET /json' \
  'GET /base64/UGFjdCBleGFtcGxl' \
  'GET /response-headers' \
  'GET /cookies/set' \
  'GET /status/204' \
  'GET /status/418' \
  'GET /status/200'; do
  if ! jq -e -s --arg endpoint "$endpoint" '
    any(.[]; .type == "Point" and .metric == "http_reqs" and .data.tags.endpoint == $endpoint)
  ' "$metrics_file" >/dev/null; then
    fail "JSON metrics contain no request for Pact endpoint $endpoint"
  fi
done

for fragment in \
  '<!DOCTYPE html>' \
  'K6 Reporter v3.0.4' \
  'Detailed Metrics' \
  'pact response matches' \
  'expect status 300 from the status 200 endpoint' \
  'checks{check:pact response matches}' \
  'Breached Thresholds'; do
  if ! grep --fixed-strings --quiet -- "$fragment" "$report_file"; then
    fail "HTML report is missing $fragment"
  fi
done
if ! grep --fixed-strings --quiet -- "benchmark_started testid=$test_id" "$log_file"; then
  fail "benchmark log has no start marker for $test_id"
fi
if ! grep --fixed-strings --quiet -- "benchmark_finished testid=$test_id status=0" "$log_file"; then
  fail "benchmark log has no successful finish marker for $test_id"
fi
if ! grep --fixed-strings --quiet -- "checks_total.......: $expected_iterations" "$log_file"; then
  fail "benchmark console output has no checks total for $expected_iterations iterations"
fi

api_get() {
  local url="$1"
  wget --quiet --tries=1 --timeout=5 --output-document=- "$url"
}

grafana_get() {
  local url="$1"
  wget \
    --quiet \
    --tries=1 \
    --timeout=5 \
    --user="${GF_SECURITY_ADMIN_USER:-admin}" \
    --password="${GF_SECURITY_ADMIN_PASSWORD:-admin}" \
    --output-document=- \
    "$url"
}

urlencode() {
  local value="$1"
  jq -rn --arg value "$value" '$value | @uri'
}

wait_for_json_condition() {
  local name="$1"
  local url="$2"
  local filter="$3"
  local request_kind="$4"
  local started_at
  local deadline
  local now
  local payload

  started_at="$(date +%s)"
  deadline="$((started_at + 10#$query_timeout))"
  while :; do
    payload=''
    if [[ "$request_kind" == grafana ]]; then
      if payload="$(grafana_get "$url")" && jq -e "$filter" <<<"$payload" >/dev/null; then
        return 0
      fi
    else
      if payload="$(api_get "$url")" && jq -e "$filter" <<<"$payload" >/dev/null; then
        return 0
      fi
    fi

    now="$(date +%s)"
    if (( now >= deadline )); then
      printf 'timed out waiting for %s\n' "$name" >&2
      if [[ -n "$payload" ]]; then
        printf '%s\n' "$payload" >&2
      fi
      return 1
    fi
    sleep 1
  done
}

mimir_query_url() {
  local query="$1"
  local encoded
  encoded="$(urlencode "$query")"
  printf 'http://mimir:9009/prometheus/api/v1/query?query=%s\n' "$encoded"
}

query_testid="$(jq -rn --arg id "$test_id" '$id | @json')"
request_query="sum(k6_http_reqs_total{testid=$query_testid})"
request_url="$(mimir_query_url "$request_query")"
wait_for_json_condition \
  'Mimir k6_http_reqs_total' \
  "$request_url" \
  ".status == \"success\" and (.data.result | length > 0) and ((.data.result | map(.value[1] | tonumber) | add) >= $expected_iterations_number)" \
  plain

failed_query="sum(k6_http_req_failed_total{testid=$query_testid,condition=\"nonzero\"})"
failed_url="$(mimir_query_url "$failed_query")"
wait_for_json_condition \
  'Mimir failed-request counter' \
  "$failed_url" \
  ".status == \"success\" and (.data.result | length > 0) and ((.data.result | map(.value[1] | tonumber) | add) >= $expected_failure_minimum)" \
  plain

checks_query="sum(k6_checks_total{testid=$query_testid,condition=\"zero\"})"
checks_url="$(mimir_query_url "$checks_query")"
wait_for_json_condition \
  'Mimir failed-check counter' \
  "$checks_url" \
  ".status == \"success\" and (.data.result | length > 0) and ((.data.result | map(.value[1] | tonumber) | add) >= $expected_failure_minimum)" \
  plain

histogram_query="sum(k6_http_req_duration_milliseconds_count{testid=$query_testid})"
histogram_url="$(mimir_query_url "$histogram_query")"
wait_for_json_condition \
  'Mimir OTLP histogram translation' \
  "$histogram_url" \
  ".status == \"success\" and (.data.result | length > 0) and ((.data.result | map(.value[1] | tonumber) | add) >= $expected_iterations_number)" \
  plain

p95_query="histogram_quantile(0.95, sum by (le) (last_over_time(k6_http_req_duration_milliseconds_bucket{testid=$query_testid}[15m])))"
p95_url="$(mimir_query_url "$p95_query")"
wait_for_json_condition \
  'Mimir aggregate p95 histogram query' \
  "$p95_url" \
  '.status == "success" and (.data.result | length > 0) and ([.data.result[].value[1] | tonumber] | all(. > 0))' \
  plain

tempo_traceql="$(jq -rn --arg id "$test_id" '"{ resource.testid = \($id | @json) }"')"
tempo_encoded="$(urlencode "$tempo_traceql")"
tempo_url="http://tempo:3200/api/search?q=$tempo_encoded"
wait_for_json_condition \
  'Tempo trace search' \
  "$tempo_url" \
  '.traces != null and (.traces | length > 0)' \
  plain

loki_logql="$(jq -rn --arg id "$test_id" '"{service_name=\"k6-as-a-library\"} |= \"benchmark_finished testid=\($id)\""')"
loki_encoded="$(urlencode "$loki_logql")"
loki_url="http://loki:3100/loki/api/v1/query_range?query=$loki_encoded"
wait_for_json_condition \
  'Loki benchmark log query' \
  "$loki_url" \
  '.data.result != null and (.data.result | length > 0)' \
  plain

grafana_base=http://grafana:3000
wait_for_json_condition \
  'Grafana Mimir datasource' \
  "$grafana_base/api/datasources/uid/mimir" \
  '.name == "Mimir" and .uid == "mimir" and .type == "prometheus" and .url == "http://mimir:9009/prometheus"' \
  grafana
wait_for_json_condition \
  'Grafana Tempo datasource' \
  "$grafana_base/api/datasources/uid/tempo" \
  '.uid == "tempo" and .type == "tempo" and .url == "http://tempo:3200"' \
  grafana
wait_for_json_condition \
  'Grafana Loki datasource' \
  "$grafana_base/api/datasources/uid/loki" \
  '.uid == "loki" and .type == "loki" and .url == "http://loki:3100"' \
  grafana
wait_for_json_condition \
  'Grafana original dashboard 19665' \
  "$grafana_base/api/dashboards/uid/ccbb2351-2ae2-462f-ae0e-f2c893ad1028" \
  '.dashboard.uid == "ccbb2351-2ae2-462f-ae0e-f2c893ad1028" and .dashboard.gnetId == 19665 and .dashboard.title == "k6 Prometheus"' \
  grafana

adapted_dashboard_filter='(
  .dashboard.uid == "k6-otlp-adapted-19665"
  and .dashboard.title == "k6 OTLP (adapted from dashboard 19665)"
  and any(.dashboard.panels[]?.targets[]?; ((.expr // "") | contains("k6_http_reqs_total")))
  and any(.dashboard.panels[]?.targets[]?; ((.expr // "") | contains("k6_http_req_duration_milliseconds_bucket")))
)'
wait_for_json_condition \
  'Grafana adapted OTLP dashboard' \
  "$grafana_base/api/dashboards/uid/k6-otlp-adapted-19665" \
  "$adapted_dashboard_filter" \
  grafana

printf 'all OTLP stack assertions passed for testid=%s\n' "$test_id"
