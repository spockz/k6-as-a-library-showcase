<!-- Document the versioned deployment assets that keep the portable Podman-compatible E2E stack reproducible. -->
# OTLP end-to-end stack

The stack uses an OpenTelemetry Collector contrib instance as its only
telemetry ingress. Metrics are exported over OTLP/HTTP directly to one
single-process Grafana Mimir instance, which stores them on a project-scoped
filesystem volume and exposes the Prometheus-compatible query API to Grafana.
Traces are forwarded to Tempo, and logs are sent to Loki's native OTLP
endpoint. The benchmark wrapper writes
`/out/benchmark.log`; the Collector `filelog` receiver turns that file into
OTLP logs so the path remains usable without a Go logging exporter.

Mimir is deliberately configured in monolithic `target=all` mode with
multitenancy disabled and filesystem-backed TSDB, blocks, rules, and compactor
state. This is a bounded local E2E fixture, not a production deployment
recommendation.

The images are version-pinned to the following official releases:

Mimir is pinned to the current stable `3.2.0` release tag. Its full
multi-architecture registry manifest digest was not verifiable in the offline
validation environment, so no digest is invented here.

| Service | Image |
| --- | --- |
| Benchmark runner | `docker.io/library/golang:1.26.6-bookworm` |
| go-httpbin | `ghcr.io/mccutchen/go-httpbin:2.25.0@sha256:20739736d4eb8dc1b998dff701f437b8bd62dcc46492bd0d861e89890ca36500` |
| Collector contrib | `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.158.0` |
| Mimir | `grafana/mimir:3.2.0` |
| Tempo | `grafana/tempo:3.0.2` |
| Loki | `grafana/loki:3.7.4` |
| Grafana | `grafana/grafana:13.1.0` |

The scripts prefer Podman when it is installed and otherwise fall back to
Docker. Every Compose operation is invoked as `<runtime> compose`; the selected
runtime must therefore provide that subcommand. The scripts build the
source-mounted runner and remove project-scoped containers, networks, and
volumes when they finish:

```sh
scripts/e2e/run.sh
scripts/e2e/run-concurrent.sh
```

`GRAFANA_HOST_PORT` changes the only published service port. `E2E_TEST_ID`
and `E2E_ITERATIONS` identify and size a run. Set `KEEP_E2E_STACK=1` to retain
resources for inspection. The scripts do not use `container_name`, host
networking, fixed external networks, or a container logging driver.

The original Prometheus Remote Write dashboard 19665 is retained in
`testdata/grafana/dashboards/k6-prometheus-19665.json`. The adapted dashboard
uses Mimir's documented `UnderscoreEscapingWithSuffixes` translation: k6 Trend
milliseconds are queried as the classic Prometheus histogram series
`k6_http_req_duration_milliseconds_bucket`, with matching `_count` and `_sum`
series, while monotonic counters use the `_total` suffix. The `testid` sample
attribute remains a metric label. The assertion script checks these translated
series and both dashboards through Grafana and Mimir.

The adapted p95 panel uses `last_over_time` rather than `rate`. Short benchmark
runs can export only one cumulative histogram point, for which a range-vector
rate is undefined and Grafana displays no data. The resulting quantile is
bounded by the configured OpenTelemetry histogram buckets, so it is an
approximation of k6's in-process trend quantile rather than the exact summary
value.

Dashboard asset inventory:

| Asset | Purpose |
| --- | --- |
| `testdata/grafana/dashboards/k6-prometheus-19665.json` | Preserve the downloaded upstream dashboard 19665 unchanged so Grafana provisioning and the original Prometheus view remain testable. |
| `testdata/grafana/dashboards/k6-otlp-adapted-19665.json` | Provide a local dashboard whose queries match Mimir's OTLP-to-Prometheus translation and can be verified with collected data. |

Both dashboards use Grafana's Prometheus datasource type, which resolves to
the provisioned `Mimir` datasource. The original asset keeps its upstream
Prometheus metric queries byte-for-byte; the adapted asset is the dashboard
intended to display this stack's native OTLP data.

Useful primary documentation:

- [k6 OpenTelemetry output](https://grafana.com/docs/k6/latest/results-output/real-time/opentelemetry/)
- [Mimir OTLP Collector exporter](https://grafana.com/docs/mimir/latest/configure/configure-otel-collector/)
- [Mimir HTTP API](https://grafana.com/docs/mimir/latest/references/http-api/)
- [Mimir monolithic filesystem setup](https://grafana.com/docs/mimir/latest/get-started/)
- [OpenTelemetry Collector Docker image](https://opentelemetry.io/docs/collector/install/docker/)
- [Loki OTLP ingestion](https://grafana.com/docs/loki/latest/send-data/otel/)
- [Grafana provisioning](https://grafana.com/docs/grafana/latest/administration/provisioning/)
- [Podman Compose provider selection](https://docs.podman.io/en/latest/markdown/podman-compose.1.html)
