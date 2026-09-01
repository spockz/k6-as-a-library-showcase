# Todo

For each of the TODO items, first verify whether the goal already has been achieved. Ask when in doubt or when it only has been partially applied. Then we first update the todo in this file TOGETHER before continuing.

1. **Add OpenTelemetry support (status: implemented; production hardening remains)**:
   * Use `agent_otel_summary.md` as the implementation plan.
   * Reuse k6's public `output.Manager`, `output.Output`, `output.SampleBuffer`, `output.NewPeriodicFlusher`, `lib.State.TracerProvider`, and HTTP request machinery.
   * Implement local compatibility code for the OTLP metric output and trace-provider configuration because k6's canonical implementations live in `internal/output/opentelemetry` and `internal/lib/trace` and cannot be imported by this external module.
   * Do not move protocol-level HTTP requests into k6's JavaScript runner solely for tracing: its HTTP module delegates to the same `httpext.MakeRequest` path and does not consume `lib.State.TracerProvider`. Automatic k6 tracing currently exists in the browser module, not for `k6/http`; instrument the existing native request transport and interaction lifecycle locally.
   * Add OTLP metrics export through the existing k6 output pipeline.
   * Add request and Pact-interaction tracing with context propagation.
   * Support OTLP/HTTP and OTLP/gRPC configuration through CLI flags and environment variables.
   * Surface configuration, export, flush, and shutdown failures.
   * Add unit and integration coverage for metrics, Pact tags, failed interactions, spans, propagation, cancellation, and final flushing.
   * Document the local compatibility code and remove it if k6 exposes equivalent public APIs or this project moves inside the k6 module tree.
   * Implemented on 2026-08-30 with OTLP/HTTP and OTLP/gRPC metrics, request and interaction traces, propagation, shared `benchmark.run_id` correlation, final flushing, surfaced lifecycle errors, and focused integration coverage.
   * OpenTelemetry metric attributes include the benchmark's bounded `groupBy` selection. Source adapters must provide stable, low-cardinality grouping values and keep richer source identity in metadata or traces.

2. **Facilitate an end-to-end telemetry test (status: implemented and verified)**:
   * Add a version-pinned Compose Specification stack validated with an explicitly selected Podman Compose provider.
   * Run the benchmark from a read-only source mount against a pinned `go-httpbin` provider service and write reports to a separate writable output volume.
   * Use the OpenTelemetry Collector as the OTLP ingress for metrics, traces, and benchmark logs. Export metrics over OTLP/HTTP to a single-process, filesystem-backed Mimir service, traces to Tempo, and logs to Loki.
   * Provision Grafana datasources and dashboards from version-controlled files. Include the original [k6 Prometheus dashboard 19665](https://grafana.com/grafana/dashboards/19665-k6-prometheus/) and, if its Prometheus-specific schema does not match the OTLP translation, a clearly named adapted dashboard whose queries are verified against collected data.
   * Isolate concurrent stacks with Compose project names, internal service DNS, per-project volumes, and configurable published Grafana ports. Do not use `container_name`, host networking, fixed external networks, or Docker-specific logging drivers.
   * Add bounded readiness and shutdown checks and assert provider requests, report artifacts, collector health and flush, metrics in Mimir, traces in Tempo, benchmark logs in Loki, Grafana provisioning, dashboard data, and two concurrent project instances.
   * Verified on 2026-08-30 with both the single-project and two-project concurrent scripts using Podman through `podman compose` and the installed external Compose provider.
   * The adapted dashboard uses the last cumulative histogram sample for p95 because a short run may not provide enough points for `rate`. Improve quantile fidelity by defining load-test-specific OpenTelemetry histogram boundaries if closer agreement with k6's in-process trend quantiles is required.

3. **Create hybrid reporting in two stages (status: implemented; distribution review remains)**:
   * Stage 1 now exposes `--dashboard-output`, emits the interactive dashboard report beside the JSON and table reports, and uses the same managed output lifecycle and final sample stream.
   * First export the interactive xk6-dashboard report alongside the existing k6-reporter table report from the same benchmark sample stream.
   * Add parity tests for metric totals, tag-split series, named and nested groups, checks, threshold definitions and results, failed Pact interactions, short runs, final snapshots, and cancellation-adjacent runs before combining the reports.
   * After parity is established, create one self-contained HTML artifact using the k6-reporter document as the visual base, an isolated xk6-dashboard graph region, and a locally generated table section for tags, groups, checks, and thresholds.
   * Do not fork the dashboard frontend for the first combined version. Keep graph and table rendering independently testable and preserve the two-report output for diagnostics.
   * Preserve arbitrary Pact attribute combinations in the table section even where the dashboard graph model supports only one grouping attribute, and document any graph-filter limitation visibly.
   * Remove or bundle external table-report resources so the combined artifact works offline, and review the xk6-dashboard AGPL and k6-reporter MIT distribution obligations.
   * The runner now supplies plan thresholds through k6's public `output.WithThresholds` contract before outputs start. The Pact integration test verifies the failed threshold in the dashboard event stream.
   * Stage-1 parity tests now cover shared aggregate and trend values, arbitrary Pact tag combinations, checks, passing and failing thresholds, named and nested groups, the graph model diagnostic, short runs, final snapshots, and cancellation-adjacent finalization.
   * The stage-2 implementation design is recorded in `agent_todo_3_combined_report_implementation.md`. Composition must reuse finalized summary and dashboard state rather than introduce another sample aggregator.
   * `--combined-output` now atomically publishes one self-contained document from the finalized existing summary and dashboard states. The graph payload remains unchanged, while semantic tables retain all tagged series, groups, checks, thresholds, diagnostics, and explicit text statuses.
   * The combined document contains visible source and license notices for the pinned AGPL-3.0 xk6-dashboard components and the MIT k6-reporter document. External reporter resource links are removed so the artifact remains usable offline. Distribution owners must still approve the corresponding-source and notice mechanism for their packaging context.

4. **Introduce a versioned intermediate execution DSL (status: core model and runtime migration implemented; follow-ups pending)**:
   * Add a pure `internal/dsl` Go model for validated request cases, operation identity, response expectations, checks, thresholds, load profiles, time segments, attributes, metadata, and source provenance.
   * Keep synthesized benchmarks target-independent and serializable as deterministic, human-inspectable versioned JSON. Do not store k6 runtime objects, bound target URLs, live HTTP requests, cookie jars, transports, contexts, or response objects in the model.
   * Add separate source adapters for Pact and future OpenAPI, SLA/SLO, and segment inputs. Keep source-specific decoding and matcher compilation outside the execution layer.
   * Add a composition and validation layer for normalization, reference resolution, conflict diagnostics, schema versions, methods, paths, bodies, matchers, IDs, attributes, segment windows, load, checks, and thresholds.
   * Add a pure response-verification layer that returns structured results, and a separate k6 adapter that consumes only validated synthesized benchmarks and preserves `httpext.MakeRequest`, built-in HTTP metrics, system tags, cookies, redirects, cancellation, and iteration pacing.
   * Preserve all current Pact request, matching, attribute, metadata, check, request-failure, report, and JSON behavior during migration.
   * Initially support fixed shared-iteration load, deterministic case selection, and segment-based check activation. Reject dynamic VU, arrival-rate, segment-gap, and other unsupported semantics before execution unless an explicit supported policy is configured.
   * Reject conflicting SLA/SLO policies unless precedence or merge behavior is declared. Allowlist report grouping attributes to control cardinality while retaining richer identity as metadata and trace attributes.
   * Add an optional benchmark manifest and observable tests for source-to-benchmark synthesis, JSON round trips, validation failures, request translation, verification, selection, segment boundaries, capability rejection, k6 samples, reports, metadata, and future OTEL correlation.
   * Direct and Pact inputs now adapt into a validated `internal/dsl.SynthesizedBenchmark` before k6 execution. Composition, deterministic selection, request translation, response expectations, checks, thresholds, metadata, and provenance are represented in the model.
   * Nested header, cookie, and body matcher collections preserve missing, explicit null, and explicit empty JSON states.
   * `--benchmark-manifest-output` writes the exact validated direct or Pact benchmark as a deterministic versioned `BenchmarkManifest` with a trailing newline and publishes it atomically after round-trip validation.
   * Benchmark manifest output is disabled by default; an empty path disables it, `-` is rejected, artifact-path collisions are rejected, and successful runs announce the path.
   * Tests cover direct and Pact generation, deterministic encoding, round trips, target independence, explicit-empty state, invalid-plan preservation, and output collisions.
   * Remaining work includes additional source adapters and broader load policies.

5. **Review current progress**: For each of the differences in README.md review if they are still applicable, what we can do to circumvent them or can implement them, also noting what we had to do with suggestions what could be improved in the public interfaces of k6.

6. **Review the Go code thoroughly (status: complete)**:
   * On 2026-08-29, `go fix -diff`, `gofmt`, `gopls`, `go vet`, `staticcheck`, `modernize`, and `errcheck` completed without Go source findings.
   * **Completed: surface output shutdown errors.** Managed outputs retain start, export, flush, and stop failures and join them into the error returned by `run`.
   * **Completed: strengthen artifact publication and validation.** JSON and standalone HTML artifacts are rendered to temporary files, structurally validated, and atomically renamed; failed generation or validation preserves existing destinations.
   * **Completed: preserve all summary aggregation errors.** Aggregation failures are joined instead of retaining only the first error.
   * **Completed review finding: bound OpenTelemetry metric cardinality.** Metric attributes include the benchmark's stable `groupBy` selection, and raw URLs, errors, IPs, and other high-cardinality tags are excluded.
   * **Completed review finding: correlate telemetry signals.** One generated `benchmark.run_id` is attached to metric and trace resources and propagated into benchmark, interaction, and request spans.
   * **Completed review finding: validate metric descriptors.** Reusing an instrument name with a conflicting k6 type, OpenTelemetry unit, or value kind is rejected.
   * **Completed review finding: make output shutdown terminal.** The OpenTelemetry output cannot be started again after `Stop`, including a stop-before-start call.
   * **Completed review finding: preserve nested DSL presence.** Header values and matchers, cookie values and matchers, and body matchers distinguish missing, null, and empty states through JSON, normalization, and validation.
   * **Completed review finding: propagate dashboard thresholds.** The external runner performs the threshold handoff that k6's internal engine normally owns before starting outputs.

7. **Refine OpenTelemetry tracing (status: implemented and verified)**:
   * Each interaction is the root span of an independent trace with an OpenTelemetry link to the benchmark span. Its HTTP client span remains a child in the interaction trace.
   * The benchmark span remains open for the complete benchmark run.
   * Pact interaction root spans emit the source-owned `pact.provider_state` attribute when applicable.
   * Tests cover independent interaction trace IDs, benchmark links, HTTP parentage, propagation, and provider-state attributes.
   * Every Pact-owned attribute is namespaced with `pact` globally, including
     DSL manifests, k6 tags, reports, OpenTelemetry metrics, and traces. The
     names are
     `pact.consumer_service`, `pact.provider_service`, `pact.endpoint`,
     `pact.interaction`, and `pact.provider_state`.

8. **Generate maximum-stress load plans from SLA agreements (status: implemented)**:
   * Decode `example_agreements.yaml` into source-neutral DSL load envelopes.
   * Preserve every agreed amount and rolling time window in the manifest instead
     of reducing constraints to a floating-point rate.
   * Calculate a deterministic maximum-stress schedule before execution. At each
     point, schedule the earliest and largest batch allowed by the intersection
     of all applicable rolling-window constraints.
   * Apply an exact positive `loadScalingFactor` to constraint amounts while
     retaining their periods: `1` uses the agreement, `10` generates ten times
     the agreed load, and `0.1` generates ten percent.
   * Store the original requirements, effective scaled constraints, planning
     assumptions, VU calculation, and complete executor-ready phase plan in a
     schema-v3 manifest so it can be validated without generating traffic.
   * Replace the legacy baseline and segment load fields. Direct and Pact runs
     without agreements emit an explicit shared-iterations phase.
   * Map precomputed phases to public k6 executors. Executors perform no SLA or
     rate calculations; a small local orchestrator only starts them at planned
     offsets.
   * Retain `--iterations`, `--vus`, and `--max-duration` only for explicit
     direct/Pact load. Reject them with agreement-derived load, remove
     `--min-iteration-duration`, and
     add `--load-scaling-factor`, `--max-planned-operations`, and
     `--generator-max-vus` planning controls.
   * Treat dropped iterations or unmet planned starts as load-generation failure.
   * Backwards compatibility is not required because the software is unreleased;
     reject pre-v3 manifests and do not retain legacy load decoding paths.
