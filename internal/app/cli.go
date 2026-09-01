// This file defines the command-line surface for configuring benchmark runs.
package app

import (
	"github.com/spf13/cobra"
	benchmarkpkg "k6-as-a-library/internal/benchmark"
)

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "k6-as-a-library",
		Short:         "Run native Go workloads with the k6 execution engine",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(newRunCommand())
	return root
}

func newRunCommand() *cobra.Command {
	config := defaultRunConfig()
	command := &cobra.Command{
		Use:   "run",
		Short: "Run the native HTTP load test",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			config.tracesOutputFlagSet = command.Flags().Changed("traces-output")
			if err := config.validate(); err != nil {
				return err
			}
			return run(command.Context(), config, command.OutOrStdout(), command.ErrOrStderr())
		},
	}

	flags := command.Flags()
	flags.StringVar(&config.targetURL, "url", config.targetURL, "HTTP endpoint for a direct request workload")
	flags.StringVar(&config.pactProviderURL, "pact-provider-url", config.pactProviderURL, "provider base URL for PACT requests")
	flags.StringVar(&config.pactDirectory, "pacts-dir", config.pactDirectory, "directory containing PACT JSON files")
	flags.Int64Var(&config.virtualUsers, "vus", config.virtualUsers, "number of virtual users")
	flags.Int64Var(&config.iterations, "iterations", config.iterations, "total iterations shared by all VUs")
	flags.DurationVar(
		&config.minIterationDuration,
		"min-iteration-duration",
		config.minIterationDuration,
		"minimum amount of time spent executing one iteration",
	)
	flags.DurationVar(&config.requestTimeout, "request-timeout", config.requestTimeout, "HTTP request timeout")
	flags.DurationVar(&config.maxDuration, "max-duration", config.maxDuration, "maximum test duration")
	flags.StringVar(&config.jsonFilename, "json-output", config.jsonFilename, "JSON metrics output path")
	flags.StringVar(&config.htmlFilename, "html-output", config.htmlFilename, "HTML report output path")
	flags.StringVar(&config.dashboardFilename, "dashboard-output", config.dashboardFilename, "interactive dashboard HTML report output path")
	flags.StringVar(&config.combinedFilename, "combined-output", config.combinedFilename, "combined interactive and detailed HTML report output path")
	flags.StringVar(&config.benchmarkManifestFilename, "benchmark-manifest-output", config.benchmarkManifestFilename, "deterministic benchmark manifest JSON output path")
	flags.Var(
		benchmarkpkg.NewOutputSelectionFlag(&config.outputs, &config.outputsFlagSet),
		"out",
		"additional output to enable (repeatable; supported value: opentelemetry)",
	)
	flags.StringVar(
		&config.tracesOutput,
		"traces-output",
		config.tracesOutput,
		"trace output configuration (none or otel[=endpoint,proto=...,header.*=...])",
	)
	flags.BoolVar(&config.dashboard, "dashboard", config.dashboard, "host a live dashboard during the benchmark")
	flags.StringVar(&config.dashboardHost, "dashboard-host", config.dashboardHost, "live dashboard bind host")
	flags.IntVar(&config.dashboardPort, "dashboard-port", config.dashboardPort, "live dashboard TCP port")
	flags.BoolVar(&config.dashboardOpen, "dashboard-open", config.dashboardOpen, "open the live dashboard in a browser")
	return command
}
