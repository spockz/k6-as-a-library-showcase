# Todo

For each of the TODO items, first verify whether the goal already has been achieved. Ask when in doubt or when it only has been partially applied. Then we first update the todo in this file TOGETHER before continuing.

1. **Add OTEL support**: Add export to OTEL, previous analysis was saved in agent_otel_summary.md

2. **Facilitate End-to-end test**: Create a docker compose file containing:
   * A complete OTEL stack (metrics, tracing, logs, dashboards, collector) plus grafana to display graphs
   * A service entry mounting the current source code to run the benchmark from inside the containers. This prevents port clashes when exporting and running multiple instannces.
   * NB: Locally I run with `podman` instead of `docker`
   * Include https://grafana.com/grafana/dashboards/19665-k6-prometheus/ in the grafana stack

3. Investigate whether we can create a hybrid report which contains both the nice interactive graphs from the original k6 report and the tables information from the third-party k6-reporter.

4. **Introduce intermediate language aka dsl**:
   The current codebase directly iterates over the inputs (PACTS) to call k6 code for executing the requests. Wiring multiple future inputs into this will become spaghetti.
   Lets create an intermediate, inspectable, data model, which contains which requests we want to do at which load, etc, decoupled from the PACT code.
   Then wire the execution of that model to the actuall calling of k6 code.
   This allows us to seperately test the generation of the model from the PACT and future inputs, as well as the interaction between the model and the k6 running code.

5. **Review current progress**: For each of the differences in README.md review if they are still applicable, what we can do to circumvent them or can implement them, also noting what we had to do with suggestions what could be improved in the public interfaces of k6.

6. Review the Go code thoroughly on best practices, first using `go fix`.
