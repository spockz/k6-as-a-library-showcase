#!/usr/bin/env bash
# Share runtime selection, project isolation, and portable Compose invocation across E2E checks.
set -euo pipefail

E2E_ITERATIONS="${E2E_ITERATIONS:-45}"
E2E_EXPECTED_ITERATIONS="${E2E_EXPECTED_ITERATIONS:-$E2E_ITERATIONS}"
E2E_VUS="${E2E_VUS:-1}"
E2E_MIN_ITERATION_DURATION="${E2E_MIN_ITERATION_DURATION:-25ms}"
E2E_REQUEST_TIMEOUT="${E2E_REQUEST_TIMEOUT:-5s}"
E2E_MAX_DURATION="${E2E_MAX_DURATION:-30s}"

e2e_repo_root() {
  local script_dir
  script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
  cd -- "$script_dir/../.." && pwd
}

e2e_select_container_runtime() {
  if command -v podman >/dev/null 2>&1; then
    container_runtime=podman
  elif command -v docker >/dev/null 2>&1; then
    container_runtime=docker
  else
    printf '%s\n' 'podman or docker is required' >&2
    return 2
  fi
  "$container_runtime" compose version
}

e2e_compose() {
  local project="$1"
  local grafana_port="$2"
  local test_id="$3"
  local repo_root="$4"
  shift 4

  env \
    "GRAFANA_HOST_PORT=$grafana_port" \
    "E2E_TEST_ID=$test_id" \
    "E2E_ITERATIONS=$E2E_ITERATIONS" \
    "E2E_EXPECTED_ITERATIONS=$E2E_EXPECTED_ITERATIONS" \
    "E2E_VUS=$E2E_VUS" \
    "E2E_MIN_ITERATION_DURATION=$E2E_MIN_ITERATION_DURATION" \
    "E2E_REQUEST_TIMEOUT=$E2E_REQUEST_TIMEOUT" \
    "E2E_MAX_DURATION=$E2E_MAX_DURATION" \
    "$container_runtime" compose \
    --file "$repo_root/compose.yaml" \
    --project-name "$project" \
    "$@"
}
