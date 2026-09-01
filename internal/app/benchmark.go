// This file assembles and runs the native benchmark lifecycle and its outputs.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"k6-as-a-library/internal/artifact"
	benchmarkpkg "k6-as-a-library/internal/benchmark"
	"k6-as-a-library/internal/k6output"
	"k6-as-a-library/internal/pact"
	"k6-as-a-library/internal/report"

	"github.com/sirupsen/logrus"
	"go.k6.io/k6/lib/fsext"
	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

const (
	defaultTargetURL                 = "http://localhost:8080/headers"
	defaultVirtualUsers              = int64(1)
	defaultIterations                = int64(10000)
	defaultRequestTimeout            = 10 * time.Second
	defaultMaxDuration               = 300 * time.Second
	defaultLoadScalingFactor         = "1"
	defaultMaxPlannedOperations      = int64(1_000_000)
	defaultGeneratorMaxVUs           = int64(10_000)
	defaultJSONFilename              = "metrics.json"
	defaultHTMLFilename              = "report.html"
	defaultDashboardFilename         = ""
	defaultCombinedFilename          = ""
	defaultBenchmarkManifestFilename = ""
	defaultDashboardHost             = "127.0.0.1"
	defaultDashboardPort             = 5665
	dashboardMinPeriod               = time.Second
)

type runConfig struct {
	targetURL                 string
	pactProviderURL           string
	pactDirectory             string
	virtualUsers              int64
	iterations                int64
	virtualUsersFlagSet       bool
	iterationsFlagSet         bool
	agreementsFilename        string
	loadScalingFactor         string
	loadScalingFactorFlagSet  bool
	maxPlannedOperations      int64
	generatorMaxVUs           int64
	requestTimeout            time.Duration
	maxDuration               time.Duration
	maxDurationFlagSet        bool
	jsonFilename              string
	htmlFilename              string
	dashboardFilename         string
	combinedFilename          string
	benchmarkManifestFilename string
	outputs                   []string
	outputsFlagSet            bool
	tracesOutput              string
	tracesOutputFlagSet       bool
	traceConfiguration        benchmarkpkg.TraceConfiguration
	dashboard                 bool
	dashboardHost             string
	dashboardPort             int
	dashboardOpen             bool
}

func defaultRunConfig() runConfig {
	return runConfig{
		targetURL:                 defaultTargetURL,
		pactProviderURL:           "",
		pactDirectory:             "",
		virtualUsers:              defaultVirtualUsers,
		iterations:                defaultIterations,
		loadScalingFactor:         defaultLoadScalingFactor,
		maxPlannedOperations:      defaultMaxPlannedOperations,
		generatorMaxVUs:           defaultGeneratorMaxVUs,
		requestTimeout:            defaultRequestTimeout,
		maxDuration:               defaultMaxDuration,
		jsonFilename:              defaultJSONFilename,
		htmlFilename:              defaultHTMLFilename,
		dashboardFilename:         defaultDashboardFilename,
		combinedFilename:          defaultCombinedFilename,
		benchmarkManifestFilename: defaultBenchmarkManifestFilename,
		tracesOutput:              benchmarkpkg.DefaultTraceOutput,
		dashboard:                 false,
		dashboardHost:             defaultDashboardHost,
		dashboardPort:             defaultDashboardPort,
		dashboardOpen:             false,
	}
}

func (config runConfig) validate() error {
	if config.pactDirectory != "" {
		if config.pactProviderURL == "" {
			return errors.New("pact-provider-url is required with pacts-dir")
		}
		if err := validateHTTPURL("PACT provider URL", config.pactProviderURL); err != nil {
			return err
		}
		info, err := os.Stat(config.pactDirectory)
		if err != nil {
			return fmt.Errorf("invalid PACT directory %q: %w", config.pactDirectory, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("PACT path %q is not a directory", config.pactDirectory)
		}
	} else if err := validateHTTPURL("HTTP endpoint", config.targetURL); err != nil {
		return err
	}
	if config.agreementsFilename != "" {
		if config.virtualUsersFlagSet || config.iterationsFlagSet || config.maxDurationFlagSet {
			return fmt.Errorf("vus, iterations, and max-duration cannot be used with agreements")
		}
		if config.maxPlannedOperations <= 0 || config.generatorMaxVUs <= 0 {
			return fmt.Errorf("agreement planner safety limits must be greater than zero")
		}
		factor, ok := new(big.Rat).SetString(config.loadScalingFactor)
		if !ok || factor.Sign() <= 0 {
			return fmt.Errorf("load scaling factor must be an exact positive number")
		}
	} else {
		if config.loadScalingFactorFlagSet {
			return fmt.Errorf("load-scaling-factor requires agreements")
		}
		if config.virtualUsers <= 0 {
			return fmt.Errorf("VUs must be greater than zero")
		}
		if config.iterations < config.virtualUsers {
			return fmt.Errorf("iterations must be greater than or equal to VUs")
		}
	}
	if config.requestTimeout <= 0 {
		return fmt.Errorf("request timeout must be greater than zero")
	}
	if config.maxDuration < time.Second {
		return fmt.Errorf("max duration must be at least 1s")
	}
	if config.jsonFilename == "" {
		return fmt.Errorf("JSON output path must not be empty")
	}
	if config.htmlFilename == "" {
		return fmt.Errorf("HTML output path must not be empty")
	}
	if config.dashboardFilename == "-" {
		return fmt.Errorf("dashboard output path must be a file path")
	}
	if config.combinedFilename == "-" {
		return fmt.Errorf("combined output path must be a file path")
	}
	if config.benchmarkManifestFilename == "-" {
		return fmt.Errorf("benchmark manifest output path must be a file path")
	}
	if err := validateArtifactOutputPaths(
		config.jsonFilename,
		config.htmlFilename,
		config.dashboardFilename,
		config.combinedFilename,
		config.benchmarkManifestFilename,
	); err != nil {
		return err
	}
	if err := benchmarkpkg.ValidateOutputNames(config.outputs); err != nil {
		return err
	}
	tracesOutput := config.tracesOutput
	if tracesOutput == "" && !config.tracesOutputFlagSet {
		tracesOutput = benchmarkpkg.DefaultTraceOutput
	}
	if _, err := benchmarkpkg.ParseTraceConfiguration(tracesOutput); err != nil {
		return err
	}
	if config.dashboard && config.dashboardHost == "" {
		return fmt.Errorf("dashboard host must not be empty")
	}
	if config.dashboard && (config.dashboardPort < 1 || config.dashboardPort > 65535) {
		return fmt.Errorf("dashboard port must be between 1 and 65535")
	}
	if config.dashboardOpen && !config.dashboard {
		return fmt.Errorf("dashboard-open requires dashboard")
	}
	return nil
}

func validateHTTPURL(name, value string) error {
	target, err := url.ParseRequestURI(value)
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", name, value, err)
	}
	if target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	return nil
}

func (config runConfig) executionTargetURL() string {
	if config.pactDirectory != "" {
		return config.pactProviderURL
	}
	target, err := url.Parse(config.targetURL)
	if err != nil {
		return config.targetURL
	}
	target.Path = ""
	target.RawPath = ""
	target.RawQuery = ""
	target.ForceQuery = false
	target.Fragment = ""
	return target.String()
}

func validateArtifactOutputPaths(paths ...string) error {
	names := []string{"JSON", "HTML", "dashboard", "combined", "benchmark manifest"}
	seen := make(map[string]string, len(paths))
	for index, path := range paths {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve %s output path %q: %w", names[index], path, err)
		}
		absolute = filepath.Clean(absolute)
		if previous, exists := seen[absolute]; exists {
			return fmt.Errorf("%s output path %q conflicts with %s output path", names[index], path, previous)
		}
		seen[absolute] = names[index]
	}
	return nil
}

func run(ctx context.Context, config runConfig, stdout, stderr io.Writer) (runErr error) {
	environment := benchmarkpkg.EnvironmentSnapshot()
	resolvedConfig, err := resolveRunConfig(config, environment)
	if err != nil {
		return err
	}
	if err := resolvedConfig.validate(); err != nil {
		return err
	}
	config = resolvedConfig
	runID, err := benchmarkpkg.NewRunID()
	if err != nil {
		return fmt.Errorf("create benchmark run ID: %w", err)
	}

	logger := logrus.New()
	logger.SetOutput(stderr)
	out := make(chan metrics.SampleContainer, 128)
	synthesisTarget := config.targetURL
	if config.pactDirectory != "" {
		synthesisTarget = config.pactProviderURL
	}
	targetURL, err := url.Parse(synthesisTarget)
	if err != nil {
		return fmt.Errorf("parse benchmark target URL: %w", err)
	}
	var interactions []pact.Interaction
	if config.pactDirectory != "" {
		interactions, err = pact.LoadDirectory(config.pactDirectory)
		if err != nil {
			return err
		}
	}
	execution, err := synthesizeBenchmark(config, targetURL, interactions)
	if err != nil {
		return err
	}
	if config.benchmarkManifestFilename != "" {
		if err := benchmarkpkg.WriteManifest(config.benchmarkManifestFilename, execution.Benchmark()); err != nil {
			return fmt.Errorf("write benchmark manifest: %w", err)
		}
	}
	traceConfiguration := config.traceConfiguration
	traceConfiguration.RunID = runID
	traceProvider, err := benchmarkpkg.NewTraceProvider(ctx, traceConfiguration)
	if err != nil {
		return fmt.Errorf("initialize tracing: %w", err)
	}
	_, benchmarkSpan := traceProvider.StartBenchmarkSpan(ctx, benchmarkpkg.BenchmarkTraceAttributes(
		"native-go",
		runID,
		"native-go",
		"native-go",
	))
	defer func() {
		runErr = errors.Join(runErr, benchmarkpkg.FinalizeTraceProvider(traceProvider, benchmarkSpan))
	}()

	executionTarget := config.executionTargetURL()
	engine, err := benchmarkpkg.NewEngine(ctx, benchmarkpkg.EngineConfig{
		Logger:         logger,
		TargetURL:      executionTarget,
		RequestTimeout: config.requestTimeout,
		Benchmark:      execution,
		Samples:        out,
		TraceProvider:  traceProvider,
		BenchmarkSpan:  benchmarkSpan,
	})
	if err != nil {
		return err
	}
	options := engine.Options()
	reportGroupBy := execution.Benchmark().Report.GroupBy

	outputParams := output.Params{
		Logger:        logger,
		Environment:   environment,
		StdOut:        stdout,
		StdErr:        stderr,
		FS:            fsext.NewOsFs(),
		ScriptOptions: options,
		ExecutionPlan: engine.ExecutionPlan(),
	}
	jsonOutput := k6output.NewJSON(config.jsonFilename)
	consoleOutput := report.NewSummaryOutput(
		stdout,
		config.htmlFilename,
		config.jsonFilename,
		options,
		reportGroupBy,
	)
	var dashboardReportOutput *report.DashboardReportOutput
	if config.dashboardFilename != "" {
		dashboardReportOutput, err = report.NewDashboardReportOutputWithOptions(outputParams, report.DashboardReportOptions{
			Filename: config.dashboardFilename,
			Period:   report.DashboardDefaultPeriod,
			Tags:     report.DashboardTags(reportGroupBy),
		})
		if err != nil {
			return fmt.Errorf("create dashboard report output: %w", err)
		}
	} else if config.combinedFilename != "" {
		dashboardReportOutput, err = report.NewDashboardModelOutput(
			outputParams,
			report.DashboardDefaultPeriod,
			report.DashboardTags(reportGroupBy),
		)
		if err != nil {
			return fmt.Errorf("create combined report dashboard model: %w", err)
		}
	}
	var otelOutput output.Output
	if benchmarkpkg.HasTelemetryOutput(config.outputs) {
		otelOutput, err = benchmarkpkg.NewTelemetryMetricsOutput(outputParams, runID, reportGroupBy)
		if err != nil {
			return fmt.Errorf("create OpenTelemetry metrics output: %w", err)
		}
	}
	outputs := make([]output.Output, 0, 5)
	managedOutputs := make([]*k6output.ManagedOutput, 0, 5)
	appendOutput := func(out output.Output) {
		managed := k6output.NewManaged(out)
		managedOutputs = append(managedOutputs, managed)
		outputs = append(outputs, managed)
	}
	if config.dashboard {
		if err := ensureDashboardAddressAvailable(config.dashboardHost, config.dashboardPort); err != nil {
			return err
		}
		liveDashboard, err := report.NewLiveDashboardOutput(outputParams, report.LiveDashboardOptions{
			Host: config.dashboardHost, Port: config.dashboardPort, Period: dashboardMinPeriod,
			Tags: report.DashboardTags(reportGroupBy), Open: config.dashboardOpen,
		})
		if err != nil {
			return fmt.Errorf("create live dashboard: %w", err)
		}
		appendOutput(liveDashboard)
	}
	appendOutput(jsonOutput)
	appendOutput(consoleOutput)
	if dashboardReportOutput != nil {
		appendOutput(dashboardReportOutput)
	}
	if otelOutput != nil {
		appendOutput(otelOutput)
	}
	for _, managed := range managedOutputs {
		managed.SetThresholds(options.Thresholds)
	}
	outputManager := output.NewManager(
		outputs,
		logger,
		func(stopErr error) { logger.WithError(stopErr).Error("output stopped test") },
	)
	waitForOutputs, finishOutputs, err := outputManager.Start(out)
	if err != nil {
		return errors.Join(fmt.Errorf("start outputs: %w", err), k6output.Errors(managedOutputs))
	}
	if config.dashboard {
		if _, err := fmt.Fprintf(stdout, "Live dashboard: %s\n", dashboardURL(config.dashboardHost, config.dashboardPort)); err != nil {
			close(out)
			waitForOutputs()
			finishOutputs(err)
			return errors.Join(fmt.Errorf("write live dashboard URL: %w", err), k6output.Errors(managedOutputs))
		}
	}

	runResult, executionErr := engine.Run(ctx)
	runErr = executionErr
	close(out)
	waitForOutputs()
	consoleOutput.SetTestRunDuration(runResult.Duration)
	finishOutputs(runErr)
	var resultErr error
	if runErr != nil {
		resultErr = errors.Join(resultErr, fmt.Errorf("run k6 executor: %w", runErr))
	}
	if err := k6output.Errors(managedOutputs); err != nil {
		resultErr = errors.Join(resultErr, err)
	}
	if dashboardReportOutput != nil {
		if err := report.WriteDashboardDiagnostics(stderr, dashboardReportOutput); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}
	if config.combinedFilename != "" {
		if err := report.WriteCombined(config.combinedFilename, consoleOutput, dashboardReportOutput); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}
	if resultErr != nil {
		return resultErr
	}
	if err := artifact.ValidateHTML(config.htmlFilename); err != nil {
		return err
	}
	if err := artifact.ValidateK6JSON(config.jsonFilename); err != nil {
		return err
	}
	if config.dashboardFilename != "" {
		if err := artifact.ValidateHTML(config.dashboardFilename); err != nil {
			return fmt.Errorf("validate dashboard report: %w", err)
		}
		if _, err := fmt.Fprintf(stdout, "Dashboard report: %s\n", config.dashboardFilename); err != nil {
			return fmt.Errorf("write dashboard report path: %w", err)
		}
	}
	if config.combinedFilename != "" {
		if err := artifact.ValidateHTML(config.combinedFilename); err != nil {
			return fmt.Errorf("validate combined report: %w", err)
		}
		if _, err := fmt.Fprintf(stdout, "Combined report: %s\n", config.combinedFilename); err != nil {
			return fmt.Errorf("write combined report path: %w", err)
		}
	}
	if config.benchmarkManifestFilename != "" {
		if _, err := fmt.Fprintf(stdout, "Benchmark manifest: %s\n", config.benchmarkManifestFilename); err != nil {
			return fmt.Errorf("write execution plan path: %w", err)
		}
	}
	return nil
}

func ensureDashboardAddressAvailable(host string, port int) error {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("dashboard address %s is unavailable: %w", address, err)
	}
	if err := listener.Close(); err != nil {
		return fmt.Errorf("release dashboard address %s: %w", address, err)
	}
	return nil
}

func dashboardURL(host string, port int) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}
