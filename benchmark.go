package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/grafana/xk6-dashboard/dashboard"
	"github.com/sirupsen/logrus"
	"gopkg.in/guregu/null.v3"

	"go.k6.io/k6/cmd/state"
	"go.k6.io/k6/errext"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/executor"
	"go.k6.io/k6/lib/fsext"
	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

const (
	defaultTargetURL            = "http://localhost:8080/headers"
	defaultVirtualUsers         = int64(1)
	defaultIterations           = int64(10000)
	defaultMinIterationDuration = 25 * time.Millisecond
	defaultRequestTimeout       = 10 * time.Second
	defaultMaxDuration          = 300 * time.Second
	defaultJSONFilename         = "metrics.json"
	defaultHTMLFilename         = "report.html"
	defaultDashboardHost        = "127.0.0.1"
	defaultDashboardPort        = 5665
	dashboardTargetPoints       = 200
	dashboardMinPeriod          = time.Second
	dashboardMaxPeriod          = 10 * time.Second
	dashboardPeriodStep         = time.Second
)

type runConfig struct {
	targetURL            string
	virtualUsers         int64
	iterations           int64
	minIterationDuration time.Duration
	requestTimeout       time.Duration
	maxDuration          time.Duration
	jsonFilename         string
	htmlFilename         string
	dashboard            bool
	dashboardHost        string
	dashboardPort        int
	dashboardOpen        bool
}

func defaultRunConfig() runConfig {
	return runConfig{
		targetURL:            defaultTargetURL,
		virtualUsers:         defaultVirtualUsers,
		iterations:           defaultIterations,
		minIterationDuration: defaultMinIterationDuration,
		requestTimeout:       defaultRequestTimeout,
		maxDuration:          defaultMaxDuration,
		jsonFilename:         defaultJSONFilename,
		htmlFilename:         defaultHTMLFilename,
		dashboard:            false,
		dashboardHost:        defaultDashboardHost,
		dashboardPort:        defaultDashboardPort,
		dashboardOpen:        false,
	}
}

func (config runConfig) validate() error {
	target, err := url.ParseRequestURI(config.targetURL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return fmt.Errorf("invalid HTTP URL %q", config.targetURL)
	}
	if config.virtualUsers <= 0 {
		return fmt.Errorf("VUs must be greater than zero")
	}
	if config.iterations < config.virtualUsers {
		return fmt.Errorf("iterations must be greater than or equal to VUs")
	}
	if config.minIterationDuration < 0 {
		return fmt.Errorf("minimum iteration duration must not be negative")
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

type nativeRunner struct {
	lib.Runner
	client               *http.Client
	dialer               *netext.Dialer
	builtin              *metrics.BuiltinMetrics
	runTags              *metrics.TagSet
	requestTags          *metrics.TagSet
	targetURL            string
	minIterationDuration time.Duration
}

func (r *nativeRunner) NewVU(
	_ context.Context,
	idLocal uint64,
	_ uint64,
	out chan<- metrics.SampleContainer,
) (lib.InitializedVU, error) {
	return &nativeVU{id: idLocal, runner: r, out: out}, nil
}

type nativeVU struct {
	id     uint64
	runner *nativeRunner
	out    chan<- metrics.SampleContainer
}

func (vu *nativeVU) GetID() uint64 {
	return vu.id
}

func (vu *nativeVU) Activate(params *lib.VUActivationParams) lib.ActiveVU {
	active := &activeNativeVU{
		nativeVU: vu,
		ctx:      params.RunContext,
		busy:     make(chan struct{}, 1),
	}
	context.AfterFunc(params.RunContext, func() {
		active.busy <- struct{}{}
		if params.DeactivateCallback != nil {
			params.DeactivateCallback(vu)
		}
	})
	return active
}

type activeNativeVU struct {
	*nativeVU
	ctx  context.Context
	busy chan struct{}
}

func (vu *activeNativeVU) RunOnce() error {
	select {
	case <-vu.ctx.Done():
		return lib.ContextErr(vu.ctx)
	case vu.busy <- struct{}{}:
	}
	defer func() { <-vu.busy }()

	iterationStarted := time.Now()
	tracer := &httpext.Tracer{}
	requestContext := httptrace.WithClientTrace(vu.ctx, tracer.Trace())
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, vu.runner.targetURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	response, requestErr := vu.runner.client.Do(request)
	var iterationErr error
	if requestErr != nil {
		iterationErr = fmt.Errorf("send request: %w", requestErr)
	}
	if response != nil {
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			iterationErr = errors.Join(iterationErr, fmt.Errorf("read response: %w", err))
		}
		if err := response.Body.Close(); err != nil {
			iterationErr = errors.Join(iterationErr, fmt.Errorf("close response: %w", err))
		}
		if response.StatusCode != http.StatusOK {
			iterationErr = errors.Join(iterationErr, fmt.Errorf("unexpected HTTP status: %s", response.Status))
		}
	}

	iterationEnded := time.Now()
	vu.emitRequestMetrics(tracer.Done(), iterationErr != nil)
	select {
	case <-vu.ctx.Done():
		return lib.ContextErr(vu.ctx)
	default:
	}

	iterationDuration := iterationEnded.Sub(iterationStarted)
	vu.emitIterationMetrics(iterationEnded, iterationDuration)
	remaining := remainingIterationDuration(vu.runner.minIterationDuration, iterationDuration)
	if remaining > 0 {
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-vu.ctx.Done():
		}
	}
	return iterationErr
}

func remainingIterationDuration(minimum, elapsed time.Duration) time.Duration {
	if elapsed >= minimum {
		return 0
	}
	return minimum - elapsed
}

func (vu *activeNativeVU) emitRequestMetrics(trail *httpext.Trail, failed bool) {
	trail.SaveSamples(
		vu.runner.builtin,
		&metrics.TagsAndMeta{Tags: vu.runner.requestTags},
	)
	samples := append(metrics.Samples{}, trail.GetSamples()...)
	samples = append(samples, newSample(
		vu.runner.builtin.HTTPReqFailed,
		vu.runner.requestTags,
		trail.EndTime,
		boolValue(failed),
	))
	ioSamples := vu.runner.dialer.IOSamples(
		trail.EndTime,
		metrics.TagsAndMeta{Tags: vu.runner.runTags},
		vu.runner.builtin,
	)
	samples = append(samples, ioSamples.GetSamples()...)
	metrics.PushIfNotDone(vu.ctx, vu.out, samples)
}

func (vu *activeNativeVU) emitIterationMetrics(at time.Time, duration time.Duration) {
	metrics.PushIfNotDone(vu.ctx, vu.out, metrics.ConnectedSamples{
		Tags: vu.runner.runTags,
		Time: at,
		Samples: []metrics.Sample{
			newSample(vu.runner.builtin.IterationDuration, vu.runner.runTags, at, metrics.D(duration)),
			newSample(vu.runner.builtin.Iterations, vu.runner.runTags, at, 1),
		},
	})
}

func newSample(metric *metrics.Metric, tags *metrics.TagSet, at time.Time, value float64) metrics.Sample {
	return metrics.Sample{
		TimeSeries: metrics.TimeSeries{Metric: metric, Tags: tags},
		Time:       at,
		Value:      value,
	}
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func newHTTPClient(timeout time.Duration) (*http.Client, *netext.Dialer, error) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, nil, fmt.Errorf("default HTTP transport has type %T", http.DefaultTransport)
	}
	resolver := netext.NewResolver(net.LookupIP, 0, types.DNSfirst, types.DNSany)
	dialer := netext.NewDialer(net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}, resolver)
	transport := defaultTransport.Clone()
	transport.DialContext = dialer.DialContext
	return &http.Client{Transport: transport, Timeout: timeout}, dialer, nil
}

func run(ctx context.Context, config runConfig, stdout, stderr io.Writer) error {
	logger := logrus.New()
	logger.SetOutput(stderr)
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	runTags := registry.RootTagSet()
	requestTags := runTags.With("url", config.targetURL)
	out := make(chan metrics.SampleContainer, 128)
	client, dialer, err := newHTTPClient(config.requestTimeout)
	if err != nil {
		return fmt.Errorf("create HTTP client: %w", err)
	}
	defer client.CloseIdleConnections()

	runner := &nativeRunner{
		client:               client,
		dialer:               dialer,
		builtin:              builtin,
		runTags:              runTags,
		requestTags:          requestTags,
		targetURL:            config.targetURL,
		minIterationDuration: config.minIterationDuration,
	}
	test := &lib.TestRunState{
		TestPreInitState: &lib.TestPreInitState{
			Logger:         logger,
			Registry:       registry,
			BuiltinMetrics: builtin,
			TestStatus:     lib.NewTestStatus(),
		},
		Runner:  runner,
		RunTags: registry.RootTagSet(),
	}
	test.Options.MinIterationDuration = types.NullDurationFrom(config.minIterationDuration)

	tuple, err := lib.NewExecutionTuple(nil, nil)
	if err != nil {
		return fmt.Errorf("create execution tuple: %w", err)
	}
	executorConfig := executor.NewSharedIterationsConfig("native-go")
	executorConfig.VUs = null.IntFrom(config.virtualUsers)
	executorConfig.Iterations = null.IntFrom(config.iterations)
	executorConfig.MaxDuration = types.NullDurationFrom(config.maxDuration)
	requirements := executorConfig.GetExecutionRequirements(tuple)
	test.Options.Scenarios = lib.ScenarioConfigs{"native-go": executorConfig}
	state := lib.NewExecutionState(
		test,
		tuple,
		lib.GetMaxPlannedVUs(requirements),
		lib.GetMaxPossibleVUs(requirements),
	)

	state.SetInitVUFunc(func(initCtx context.Context, _ *logrus.Entry) (lib.InitializedVU, error) {
		localID, globalID := state.GetUniqueVUIdentifiers()
		return runner.NewVU(initCtx, localID, globalID, out)
	})
	for range lib.GetMaxPlannedVUs(requirements) {
		localID, globalID := state.GetUniqueVUIdentifiers()
		vu, err := runner.NewVU(ctx, localID, globalID, out)
		if err != nil {
			return fmt.Errorf("initialize VU: %w", err)
		}
		state.AddInitializedVU(vu)
	}

	k6Executor, err := executorConfig.NewExecutor(state, logrus.NewEntry(logger))
	if err != nil {
		return fmt.Errorf("create k6 executor: %w", err)
	}
	if err := k6Executor.Init(ctx); err != nil {
		return fmt.Errorf("initialize k6 executor: %w", err)
	}

	if err := truncateArtifact(config.htmlFilename); err != nil {
		return err
	}
	outputParams := output.Params{
		Logger:         logger,
		Environment:    map[string]string{},
		StdOut:         stdout,
		StdErr:         stderr,
		FS:             fsext.NewOsFs(),
		ScriptOptions:  test.Options,
		RuntimeOptions: test.RuntimeOptions,
		ExecutionPlan:  requirements,
	}
	jsonOutput := newJSONOutput(config.jsonFilename)
	consoleOutput := newSummaryOutput(stdout, config.htmlFilename, config.jsonFilename)
	outputs := make([]output.Output, 0, 3)
	if config.dashboard {
		if err := ensureDashboardAddressAvailable(config.dashboardHost, config.dashboardPort); err != nil {
			return err
		}
		liveDashboard, err := newLiveDashboardOutput(config, outputParams)
		if err != nil {
			return fmt.Errorf("create live dashboard: %w", err)
		}
		outputs = append(outputs, liveDashboard)
	}
	outputs = append(outputs, jsonOutput, consoleOutput)
	outputManager := output.NewManager(
		outputs,
		logger,
		func(stopErr error) { logger.WithError(stopErr).Error("output stopped test") },
	)
	waitForOutputs, finishOutputs, err := outputManager.Start(out)
	if err != nil {
		return fmt.Errorf("start outputs: %w", err)
	}
	if config.dashboard {
		if _, err := fmt.Fprintf(stdout, "Live dashboard: %s\n", dashboardURL(config.dashboardHost, config.dashboardPort)); err != nil {
			close(out)
			waitForOutputs()
			finishOutputs(err)
			return fmt.Errorf("write live dashboard URL: %w", err)
		}
	}

	startedAt := time.Now()
	runErr := k6Executor.Run(ctx, out)
	observedRuntime := time.Since(startedAt)
	close(out)
	waitForOutputs()
	finishOutputs(runErr)
	if runErr != nil {
		return fmt.Errorf("run k6 executor: %w", runErr)
	}
	if err := jsonOutput.Err(); err != nil {
		return fmt.Errorf("write JSON metrics: %w", err)
	}
	if err := generateHTMLReport(
		ctx,
		config.jsonFilename,
		config.htmlFilename,
		logger,
		dashboardPeriod(observedRuntime),
	); err != nil {
		return err
	}
	if err := validateArtifact(config.htmlFilename); err != nil {
		return err
	}
	if err := validateArtifact(config.jsonFilename); err != nil {
		return err
	}
	return nil
}

func newLiveDashboardOutput(config runConfig, params output.Params) (output.Output, error) {
	values := url.Values{
		"host":   {config.dashboardHost},
		"period": {dashboardMinPeriod.String()},
		"port":   {strconv.Itoa(config.dashboardPort)},
	}
	if config.dashboardOpen {
		values.Set("open", "true")
	}
	params.OutputType = dashboard.OutputName
	params.ConfigArgument = values.Encode()
	dashboardOutput, err := dashboard.New(params)
	if err != nil {
		return nil, err
	}
	stopper, ok := dashboardOutput.(output.WithStopWithTestError)
	if !ok {
		return nil, fmt.Errorf("live dashboard does not support test-aware shutdown")
	}
	return &liveDashboardOutput{Output: dashboardOutput, stopper: stopper}, nil
}

type liveDashboardOutput struct {
	output.Output
	stopper output.WithStopWithTestError
}

func (dashboardOutput *liveDashboardOutput) Stop() error {
	return dashboardOutput.StopWithTestError(nil)
}

func (dashboardOutput *liveDashboardOutput) StopWithTestError(testErr error) error {
	if testErr == nil {
		testErr = errors.New("live dashboard server shutdown")
	}
	testErr = errext.WithAbortReasonIfNone(testErr, errext.AbortedByOutput)
	return dashboardOutput.stopper.StopWithTestError(testErr)
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

func validateArtifact(filename string) error {
	info, err := os.Stat(filename)
	if err != nil {
		return fmt.Errorf("validate %s: %w", filename, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("validate %s: file is empty", filename)
	}
	return nil
}

func truncateArtifact(filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", filename, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("prepare %s: %w", filename, err)
	}
	return nil
}

func dashboardPeriod(runtime time.Duration) time.Duration {
	period := runtime / dashboardTargetPoints
	if period < dashboardMinPeriod {
		return dashboardMinPeriod
	}
	if period > dashboardMaxPeriod {
		return dashboardMaxPeriod
	}
	return ((period + dashboardPeriodStep - 1) / dashboardPeriodStep) * dashboardPeriodStep
}

func generateHTMLReport(
	ctx context.Context,
	jsonFilename string,
	htmlFilename string,
	logger *logrus.Logger,
	period time.Duration,
) (resultErr error) {
	eventsFile, err := os.CreateTemp("", "k6-dashboard-*.ndjson")
	if err != nil {
		return fmt.Errorf("create temporary dashboard events file: %w", err)
	}
	eventsFilename := eventsFile.Name()
	if err := eventsFile.Close(); err != nil {
		return errors.Join(
			fmt.Errorf("close temporary dashboard events file: %w", err),
			removeTemporaryFile(eventsFilename),
		)
	}
	defer func() {
		resultErr = errors.Join(resultErr, removeTemporaryFile(eventsFilename))
	}()

	globalState := state.NewGlobalState(ctx)
	globalState.FS = fsext.NewOsFs()
	globalState.Env = map[string]string{}
	globalState.Logger = logger
	globalState.FallbackLogger = logger

	if err := executeDashboardCommand(
		ctx,
		globalState,
		[]string{"aggregate", jsonFilename, eventsFilename, "--period", period.String()},
	); err != nil {
		return fmt.Errorf("aggregate dashboard observations: %w", err)
	}
	if err := addDashboardAggregates(eventsFilename); err != nil {
		return err
	}
	if err := executeDashboardCommand(
		ctx,
		globalState,
		[]string{"report", eventsFilename, htmlFilename},
	); err != nil {
		return fmt.Errorf("render HTML report: %w", err)
	}

	return nil
}

func executeDashboardCommand(ctx context.Context, globalState *state.GlobalState, args []string) error {
	command := dashboard.NewCommand(globalState)
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs(args)
	return command.ExecuteContext(ctx)
}

func addDashboardAggregates(filename string) error {
	events, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read dashboard events: %w", err)
	}
	lineEnd := bytes.IndexByte(events, '\n')
	if lineEnd < 0 {
		return fmt.Errorf("normalize dashboard events: parameter event is missing")
	}

	var parameterEvent struct {
		Event string                     `json:"event"`
		Data  map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(events[:lineEnd], &parameterEvent); err != nil {
		return fmt.Errorf("decode dashboard parameter event: %w", err)
	}
	if parameterEvent.Event != "param" {
		return fmt.Errorf("normalize dashboard events: first event is %q, expected %q", parameterEvent.Event, "param")
	}
	aggregates, err := json.Marshal(dashboardAggregates())
	if err != nil {
		return fmt.Errorf("encode dashboard aggregates: %w", err)
	}
	parameterEvent.Data["aggregates"] = aggregates
	parameterLine, err := json.Marshal(parameterEvent)
	if err != nil {
		return fmt.Errorf("encode dashboard parameter event: %w", err)
	}

	normalized := make([]byte, 0, len(parameterLine)+1+len(events)-lineEnd-1)
	normalized = append(normalized, parameterLine...)
	normalized = append(normalized, '\n')
	normalized = append(normalized, events[lineEnd+1:]...)
	if err := os.WriteFile(filename, normalized, 0o600); err != nil {
		return fmt.Errorf("write dashboard events: %w", err)
	}
	return nil
}

func dashboardAggregates() map[string][]string {
	return map[string][]string{
		metrics.Counter.String(): {"count", "rate"},
		metrics.Gauge.String():   {"value"},
		metrics.Rate.String():    {"rate"},
		metrics.Trend.String():   {"avg", "max", "med", "min", "p(90)", "p(95)", "p(99)"},
	}
}

func removeTemporaryFile(filename string) error {
	if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove temporary dashboard events file: %w", err)
	}
	return nil
}
