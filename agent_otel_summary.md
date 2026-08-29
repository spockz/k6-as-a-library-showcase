# OpenTelemetry implementation notes

Date reviewed: 2026-08-28

The project is pinned to `go.k6.io/k6 v1.8.1`. OpenTelemetry metrics and
traces require two separate integrations:

- metrics consume the existing stream of k6 `metrics.SampleContainer` values
- traces must be created during workload execution so trace context can be
  propagated with each HTTP request

This should be implemented as live export. The generated k6 JSON can be
translated into metrics after a run, but it cannot reconstruct request trace
IDs, parent-child relationships, or context propagated to the provider.

## Relevant k6 API boundaries

| Capability | Availability to this project |
|---|---|
| `output.Manager`, `output.Output`, `output.SampleBuffer`, and `output.NewPeriodicFlusher` | Public and already used |
| k6's current OpenTelemetry metric output | In `internal/output/opentelemetry`; cannot be imported |
| Former `xk6-output-opentelemetry/pkg/opentelemetry` output | Public but archived and frozen before current stable behavior |
| k6's OTLP tracer-provider construction and `--traces-output` parser | In `internal/lib/trace`; cannot be imported |
| OpenTelemetry Go metric and trace SDKs and OTLP exporters | Public |
| `lib.State.TracerProvider` | Public interface and assignable by the native VU |
| `httpext.MakeRequest` | Public, but it does not create OpenTelemetry spans for this native HTTP workload |

The old public xk6 output can be useful as reference code, but it should not be
the maintained dependency. It predates changes such as the stable
`opentelemetry` output name, `K6_OTEL_EXPORTER_PROTOCOL`, and the current Rate
mapping. The two maintainable choices are:

1. Port the small k6 v1.8.1 metric-output package into this repository with
   attribution and parity tests.
2. Implement the same mapping directly with the public k6 output interfaces
   and OpenTelemetry Go SDK.

The first option gives the closest initial parity. The second avoids carrying a
source fork but requires independently tracking k6 mapping changes. In either
case, document the code as compatibility code caused by the `internal`
boundary.

## Desired CLI and configuration

Prefer k6-compatible activation:

```text
--out opentelemetry
--traces-output=otel=http://localhost:4318,proto=http
```

Keep both disabled by default. Preserve the existing JSON, HTML, console, and
dashboard outputs when OpenTelemetry is enabled.

Metrics should recognize the current k6 variables, including:

- `K6_OTEL_SERVICE_NAME`
- `K6_OTEL_SERVICE_VERSION`
- `K6_OTEL_METRIC_PREFIX`
- `K6_OTEL_FLUSH_INTERVAL`
- `K6_OTEL_EXPORT_INTERVAL`
- `K6_OTEL_EXPORTER_PROTOCOL`
- `K6_OTEL_HEADERS`
- the HTTP and gRPC endpoint, TLS, and insecure-connection variables

Trace configuration should support the k6 `otel[=<endpoint>,proto=...,header.*=...]`
form and `K6_TRACES_OUTPUT`. Supporting standard `OTEL_*` variables as well is
desirable. Configuration precedence must be explicit; k6 gives `K6_OTEL_*`
values precedence over standard OpenTelemetry SDK values.

The current `output.Params.Environment` is an empty map. Populate it from the
process environment or pass a validated OpenTelemetry configuration explicitly
before constructing the output. Preserve missing and explicitly empty values as
different states during parsing.

Open decisions before implementation:

- whether `service.name` defaults to `k6` for dashboard compatibility or to
  this application's name
- how `service.version` is populated without importing k6's internal build
  package
- whether exporter failure fails the benchmark or is reported as a separate
  output failure while benchmark execution succeeds
- whether trace propagation is always enabled with trace export or controlled
  by a separate option
- the default trace sampler; exporting every request can be expensive during a
  load test

## Metrics implementation

The existing output creation in `benchmark.go` already fans all samples through
`output.Manager`. Add an optional OpenTelemetry output to the same `outputs`
slice before `outputManager.Start`.

### Metric output structure

Create a package or files such as:

```text
otel_config.go
otel_metrics_output.go
otel_metrics_registry.go
otel_exporter.go
```

The output should implement `output.Output`, preferably by embedding
`output.SampleBuffer`:

1. `Start` validates configuration and creates the OTLP exporter.
2. Create an OpenTelemetry resource with at least `service.name`,
   `service.version`, and a benchmark run identifier.
3. Create a `metric.MeterProvider` with a periodic reader.
4. Start a public k6 `output.PeriodicFlusher` to drain the sample buffer.
5. `AddMetricSamples` only buffers data and never performs network I/O.
6. Each flush dispatches every k6 sample to a cached OpenTelemetry instrument.
7. `StopWithTestError` stops the flusher, force-flushes, and shuts down the
   provider with a bounded context.
8. Retain asynchronous and shutdown errors and expose an `Err` method so
   `run` can check them after `output.Manager` finishes, as it already does for
   the JSON output.

Do not rely only on `output.Manager` logging stop failures: the project brief
requires output failures to be surfaced.

### k6 metric mapping

Match the pinned k6 implementation and protect it with compatibility tests:

| k6 metric type | OpenTelemetry representation |
|---|---|
| Counter | `Float64Counter` |
| Gauge | `Float64Gauge` |
| Trend | `Float64Histogram` |
| Rate | `Int64Counter` with `condition=zero` or `condition=nonzero` |

Also port or reproduce k6's metric-name normalization, metric-prefix behavior,
unit conversion, instrument caching, and duplicate-type error handling.

Convert every non-empty k6 tag to an OpenTelemetry attribute. This naturally
exports the Pact dimensions currently used by the console and dashboard:

- `consumer_service`
- `provider_service`
- `endpoint`
- `pact_interaction`
- `provider_state`
- `name`

Review cardinality before exporting arbitrary future tags. In particular, use
logical endpoint names rather than raw URLs containing identifiers.

### Pact mismatch data in metrics

The existing `pact_mismatch` value is sample metadata. k6's OpenTelemetry
metric output maps tags, not metadata, so the detailed mismatch text should not
become a metric attribute. Full messages would also create unbounded
cardinality and may contain response data.

Keep the following in metrics:

- the `checks` pass/fail observations
- `http_req_failed` for expected-status failures
- existing bounded Pact tags
- optionally a bounded `pact_mismatch_kind` such as `status`, `header`,
  `cookie`, `json_body`, or `text_body`

Put the complete mismatch description in JSON and in a trace event.

## Trace implementation

Creating an OTLP trace exporter alone is insufficient. The native workload must
also create spans and propagate their context.

### Trace provider lifecycle

Create the trace provider before constructing VUs:

1. Parse and validate `--traces-output` or `K6_TRACES_OUTPUT`.
2. Construct the OTLP/HTTP or OTLP/gRPC span exporter.
3. Construct an OpenTelemetry resource shared with metrics.
4. Configure a batch span processor and explicit sampler.
5. Store the provider, tracer, and propagator on `nativeRunner`.
6. Assign the provider to each `lib.State.TracerProvider`.
7. When tracing is disabled, use a public no-op provider rather than leaving
   the VU field nil.
8. After the executor and all VUs have finished, force-flush and shut down the
   provider with a bounded context.

Pass providers explicitly. Avoid changing OpenTelemetry's process-global
provider because tests or other embedded users may share the process.

### HTTP instrumentation

The cleanest layering is:

```text
k6 httpext measuring transport
  -> OpenTelemetry HTTP transport
    -> existing per-VU *http.Transport
```

In `nativeRunner.NewVU`:

1. Keep the raw `*http.Transport` so activation shutdown can still call
   `CloseIdleConnections`.
2. Wrap it with `otelhttp.NewTransport` using the explicit tracer provider and
   propagator.
3. Pass an explicit no-op meter provider unless standard OpenTelemetry HTTP
   client metrics are intentionally wanted in addition to the k6 metrics.
4. Set the wrapper as `lib.State.Transport`.

`httpext.MakeRequest` will continue to collect k6 HTTP phase metrics around the
wrapped transport. The OpenTelemetry wrapper will create an HTTP client span
for each request, including redirects, and inject W3C `traceparent` and
`tracestate` headers.

### Span model

Use a per-iteration or per-Pact-interaction parent span rather than one giant
span for the whole benchmark:

```text
Pact interaction span
  -> HTTP client span
  -> Pact verification event
```

The benchmark run identifier should be a resource or span attribute that
correlates independent concurrent traces.

In `activeNativeVU.RunOnce`:

1. Select the Pact interaction and establish its tags.
2. Start the interaction span from the VU run context.
3. Add bounded attributes for VU, iteration, scenario, consumer, provider,
   endpoint, interaction, provider state, request method, and expected status.
4. Pass the returned span context to `httpext.MakeRequest` instead of the
   unmodified VU context.
5. Record network errors on the interaction span.
6. Record the Pact verification result as an event.
7. Mark the interaction span as error for any contract mismatch.
8. End the interaction span after response matching and iteration metrics have
   been emitted, but before minimum-iteration-duration pacing.

Do not record request or response bodies, authorization headers, cookies, or
the complete header set by default.

The HTTP and contract results must remain distinct. For the deliberate
`/status/200` interaction that expects 300:

- the HTTP child span represents a successful HTTP 200 exchange
- the parent Pact span has error status because contract verification failed
- the `checks` metric records failure
- `http_req_failed` records failure because the expected-response callback
  expected 300

Likewise, an expected HTTP 418 may be a passing Pact interaction even though
standard HTTP semantic conventions classify the client response as an error.
The parent interaction span is the authoritative contract result.

### Avoid duplicate Pact verification

`checkPactResponse` currently performs matching and emits the check without
returning the result. Refactor it so one verification result drives both the
k6 check metric and trace annotation. A named result structure should contain:

- whether verification passed
- the mismatch error, if present
- a bounded mismatch category

Do not run `matchPactResponse` a second time solely for tracing.

### Propagation caveat

Injecting trace headers changes the outbound request. Most providers tolerate
additional headers, but echo endpoints such as httpbin `/headers` expose them
in the response and may therefore change Pact body verification.

Tests and example Pacts must either:

- use matching rules that permit additional reflected tracing headers, or
- disable propagation for interactions whose exact echo body is under test

If propagation is configurable independently, trace export can still show
load-generator spans while leaving requests unchanged. Distributed linkage to
provider spans requires propagation.

## Metrics and trace correlation

Metrics and traces will share resource attributes and a benchmark run ID, but
they will not automatically produce trace-linked metric exemplars. The metric
output consumes samples later using a background context, after the request
span context has been lost.

Adding exemplars would be a separate feature. It would require preserving trace
and span IDs in non-indexed sample metadata and restoring the context when
recording the OpenTelemetry metric, or recording a second metric directly in
the request path. The latter risks diverging from k6's metric output and should
not be part of the first implementation.

## Implementation sequence

1. **Choose compatibility behavior.** Decide resource names, exporter-error
   policy, propagation policy, sampling defaults, and whether to mirror only
   k6 variables or also standard `OTEL_*` variables.
2. **Extend CLI configuration.** Add repeatable output selection and traces
   output fields to `runConfig`, Cobra flags, validation, help text, and CLI
   tests. Keep OpenTelemetry disabled by default.
3. **Add environment handling.** Populate `output.Params.Environment` or build
   a typed configuration with documented precedence. Add tests for missing,
   empty, invalid, and conflicting values.
4. **Implement metrics export.** Add the buffered output, instrument registry,
   k6-compatible mapping, OTLP exporters, resource attributes, lifecycle, and
   explicit error reporting.
5. **Wire metrics into `output.Manager`.** Append the output only when enabled
   and verify that JSON, console, HTML, live dashboard, and OTLP output can run
   together.
6. **Implement trace-provider setup.** Add OTLP/HTTP and OTLP/gRPC exporters,
   batching, sampling, resource configuration, no-op behavior, flush, and
   shutdown.
7. **Instrument VUs and HTTP.** Store tracing state on `nativeRunner`, wrap the
   per-VU transport, create interaction spans, pass span contexts into
   `httpext.MakeRequest`, and propagate trace headers when enabled.
8. **Expose one Pact verification result.** Refactor matching/check emission so
   metrics and spans consume the same result, then annotate failed interaction
   spans without aborting the load run.
9. **Add unit and integration tests.** Verify mapping, tags, failures,
   propagation, span hierarchy, cancellation, output errors, and final flush.
10. **Document the feature and difference from k6.** Explain that the canonical
    metric output and trace setup are local compatibility implementations
    because their k6 equivalents are internal.
11. **Run project verification.** Run `gofmt`, gopls diagnostics, `go vet`, the
    full test suite, and the race detector.

## Testing plan

### Unit tests

- configuration precedence and validation for HTTP, gRPC, TLS, headers, and
  disabled outputs
- all four k6 metric types, normalized names, units, and metric prefixes
- preservation of Pact and system tags as OpenTelemetry attributes
- Rate values exported with `condition=zero` and `condition=nonzero`
- short tests force-flush metrics even when they finish before the periodic
  export interval
- exporter and shutdown errors are observable from `run`
- tracing disabled creates no spans and injects no headers
- interaction span attributes and status for passing and failing Pacts
- expected-300/actual-200 produces an HTTP 200 child and failed Pact parent
- cancellation closes spans and does not leak exporter goroutines

Use OpenTelemetry in-memory metric readers and span exporters where possible.
Assertions should inspect exported telemetry rather than private registries.

### Integration test

Run the existing go-httpbin-backed Pact benchmark against an in-process OTLP
receiver or test collector and assert that:

- HTTP and `checks` metrics arrive
- metrics are split by the Pact attributes
- the intentional Pact failure appears in `checks` and `http_req_failed`
- interaction and HTTP child spans arrive with the same trace ID
- the provider receives a valid `traceparent` header when propagation is
  enabled
- the failed interaction span contains a bounded mismatch category and error
  status
- all pending telemetry arrives during shutdown

Test at least OTLP/HTTP end to end. Unit-test gRPC construction separately, or
add a second integration case if protocol parity is a requirement.

## Dependencies

The project already receives some OpenTelemetry modules transitively through
k6. The implementation should declare every package it imports directly and
pin compatible versions, including as needed:

- `go.opentelemetry.io/otel`
- `go.opentelemetry.io/otel/sdk/metric`
- `go.opentelemetry.io/otel/sdk/trace`
- OTLP metric exporters for HTTP and gRPC
- OTLP trace exporters for HTTP and gRPC
- `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`

Avoid depending on an observability backend. Export OTLP to a user-selected
collector or backend.

## Acceptance criteria

- Existing behavior is unchanged when OpenTelemetry is disabled.
- Metrics export concurrently with all current outputs without blocking VUs.
- Every current Pact reporting tag is present as an OpenTelemetry attribute.
- The intentional failed Pact interaction is visible in exported metrics and
  as a failed interaction span.
- Trace propagation connects the HTTP client span to an instrumented provider
  when enabled.
- Pact verification detail is available as a trace event without becoming a
  high-cardinality metric attribute.
- Short and canceled runs flush or explicitly report telemetry that could not
  be exported.
- Invalid configuration and network/export failures never look like successful
  empty output.
- No k6 `internal` package is imported through a module-path workaround.

## Primary references

- [k6 OpenTelemetry metrics output](https://grafana.com/docs/k6/latest/results-output/real-time/opentelemetry/)
- [k6 traces-output option](https://grafana.com/docs/k6/latest/using-k6/k6-options/reference/#traces-output)
- [k6 v1.8.1 metric output source](https://github.com/grafana/k6/tree/v1.8.1/internal/output/opentelemetry)
- [k6 v1.8.1 trace provider source](https://github.com/grafana/k6/blob/v1.8.1/internal/lib/trace/otel.go)
- [Archived public xk6 OpenTelemetry output](https://github.com/grafana-cold-storage/xk6-output-opentelemetry)
- [OpenTelemetry Go exporters](https://opentelemetry.io/docs/languages/go/exporters/)
- [OpenTelemetry Go instrumentation](https://opentelemetry.io/docs/languages/go/instrumentation/)
- [OpenTelemetry `otelhttp` transport](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp)
