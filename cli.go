package main

import "github.com/spf13/cobra"

func newRootCommand() *cobra.Command {
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
			if err := config.validate(); err != nil {
				return err
			}
			return run(command.Context(), config, command.OutOrStdout(), command.ErrOrStderr())
		},
	}

	flags := command.Flags()
	flags.StringVar(&config.targetURL, "url", config.targetURL, "HTTP endpoint to load test")
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
	flags.BoolVar(&config.dashboard, "dashboard", config.dashboard, "host a live dashboard during the benchmark")
	flags.StringVar(&config.dashboardHost, "dashboard-host", config.dashboardHost, "live dashboard bind host")
	flags.IntVar(&config.dashboardPort, "dashboard-port", config.dashboardPort, "live dashboard TCP port")
	flags.BoolVar(&config.dashboardOpen, "dashboard-open", config.dashboardOpen, "open the live dashboard in a browser")
	return command
}
