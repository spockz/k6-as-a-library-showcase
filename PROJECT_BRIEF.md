# Project brief

## Purpose

Demonstrate how to generate and execute k6 benchmarks directly from Go without
requiring JavaScript workload definitions.

Reuse k6's public Go APIs for execution, virtual users, built-in metrics, HTTP
requests, and outputs. Keep local compatibility code limited to functionality
that k6 or xk6-dashboard exposes only through internal packages.

## Intended inputs

The eventual benchmark generator should combine:

- OpenAPI specifications for grouping logical endpoints
- consumer- and provider-side SLA/SLO specifications defining response-time,
  failure-rate, throughput, load-generation, threshold, and check requirements
- concrete consumer requests derived from consumer contracts such as PACT
- a segments file that varies load and activates or deactivates thresholds and
  checks during specific time periods

## Current scope

The current program executes a fixed HTTP GET workload using k6 shared
iterations. It provides:

- configurable VUs, iterations, minimum iteration duration, request timeout,
  and maximum duration
- k6-compatible JSON metric observations
- a final xk6-dashboard HTML report
- an optional live dashboard
- a small console summary
- CLI, metrics, HTTP behavior, report, and dashboard tests

The fixed workload is intentionally narrower than a general replacement for
the k6 CLI or JavaScript runtime.

## k6 integration requirements

- Use lib.Runner, lib.InitializedVU, and lib.ActiveVU for workload lifecycle.
- Use executor.SharedIterationsConfig and lib.ExecutionState for scheduling.
- Use metrics.RegisterBuiltinMetrics and the returned BuiltinMetrics objects.
- Give each VU its own lib.State, netext.Dialer, HTTP transport, TLS
  configuration, and cookie jar.
- Share only resources that k6 itself shares, such as the resolver and buffer
  pool.
- Use httpext.MakeRequest for HTTP execution, redirect accounting, response
  handling, error classification, expected-response behavior, and request
  metrics.
- Treat HTTP status codes 200 through 399 as expected by default.
- Emit applicable k6 system tags, including request method, URL, name, status,
  protocol, scenario, expected response, TLS information, and error details.
- Emit data_sent and data_received from each VU's netext.Dialer.IOSamples.
- Emit iterations and iteration_duration using k6's built-in metric objects.
- Preserve sample tags and metadata.
- Reset cookies between iterations by default while keeping cookie state
  isolated between VUs.
- Close idle per-VU connections when a VU activation ends.

## Iteration pacing requirements

- Model k6's minIterationDuration behavior.
- Measure the iteration before pacing.
- Sleep only for the remaining portion of the configured minimum.
- Exclude the pacing wait from iteration_duration.
- Emit completed-iteration metrics before waiting.
- Make the pacing wait cancellable.

## Output requirements

- Fan metric samples out through output.Manager.
- Keep JSON observations compatible with the k6 multiline JSON envelope.
- Generate the final report using xk6-dashboard's aggregate and report
  commands.
- Keep the live dashboard disabled by default.
- Validate generated JSON and HTML artifacts.
- Surface output and artifact errors rather than silently ignoring them.

## Remaining differences from the k6 binary

1. The internal k6 scheduler is bypassed, so vus and vus_max are not emitted on
   k6's one-second ticker.
2. The JSON output is a local compatibility implementation. It buffers samples
   until shutdown instead of flushing every 200 milliseconds.
3. Threshold configuration, propagation, evaluation, and failure reporting are
   not implemented.
4. xk6-dashboard's aggregate command omits aggregate names required by its
   report data. The local compatibility step is coupled to the pinned
   xk6-dashboard version.
5. lib.Runner references an internal summary type. The native runner therefore
   embeds lib.Runner and only safely overrides methods used by the direct
   executor path.
6. The fixed request does not set the k6 CLI user agent, so Go supplies its
   default user agent.
7. Response bodies are always discarded, unlike the k6 CLI default.
8. Cancellation can suppress data_sent and data_received because IOSamples are
   sent through metrics.PushIfNotDone with the canceled VU context.
9. The console summary is custom and reports only request count, failure count,
   and average request duration.

## Next priorities

1. Emit vus and vus_max on a one-second loop using active and initialized VU
   counts.
2. Preserve per-VU I/O samples when an iteration is canceled.
3. Align fixed-request defaults, including the user agent and response-body
   policy.
4. Add threshold configuration and evaluation based on generated SLA/SLO
   inputs.
5. Replace shutdown-only JSON buffering with periodic streaming.
6. Expand the summary only where it supports the generated benchmark workflow.

## Testing expectations

- Assert observable behavior rather than private implementation details.
- Cover independent redirect trails and redirect cookie propagation.
- Cover expected and unexpected HTTP statuses.
- Cover network error tags and error codes.
- Cover per-VU cookie isolation.
- Cover iteration pacing and cancellation.
- Cover required dashboard metrics and report snapshots.
- Keep JSON schema compatibility checks against the pinned k6 source.
- Run gofmt, gopls diagnostics, go vet, the full test suite, and the race
  detector after Go changes.

## Working constraints

- Read README.md, this brief, and AGENTS.md before planning or editing.
- Preserve unrelated user changes in the worktree.
- Prefer public k6 APIs and document unavoidable compatibility code.
- Keep benchmark configuration and CLI concerns separate from workload
  execution.
- Do not add an AI assistant as a commit co-author.
- Do not commit changes unless explicitly requested.
