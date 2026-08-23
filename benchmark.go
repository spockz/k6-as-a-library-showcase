package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
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
	defaultMaxRedirects         = int64(10)
	defaultBatchSize            = int64(20)
	defaultBatchSizePerHost     = int64(6)
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
	logger         logrus.FieldLogger
	options        lib.Options
	resolver       netext.Resolver
	bufferPool     *lib.BufferPool
	builtin        *metrics.BuiltinMetrics
	testStatus     *lib.TestStatus
	runTags        *metrics.TagSet
	targetURL      httpext.URL
	requestTimeout time.Duration
}

func (r *nativeRunner) NewVU(
	_ context.Context,
	idLocal uint64,
	idGlobal uint64,
	out chan<- metrics.SampleContainer,
) (lib.InitializedVU, error) {
	dialer := netext.NewDialer(
		net.Dialer{Timeout: r.requestTimeout, KeepAlive: 30 * time.Second},
		r.resolver,
	)
	tlsConfig := &tls.Config{Renegotiation: tls.RenegotiateFreelyAsClient}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         dialer.DialContext,
		TLSClientConfig:     tlsConfig,
		DisableCompression:  true,
		MaxIdleConns:        int(r.options.Batch.Int64),
		MaxIdleConnsPerHost: int(r.options.BatchPerHost.Int64),
		ForceAttemptHTTP2:   true,
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create VU cookie jar: %w", err)
	}
	state := &lib.State{
		Options:        r.options,
		BuiltinMetrics: r.builtin,
		Logger:         r.logger,
		Dialer:         dialer,
		Transport:      transport,
		CookieJar:      jar,
		TLSConfig:      tlsConfig,
		Samples:        out,
		BufferPool:     r.bufferPool,
		VUID:           idLocal,
		VUIDGlobal:     idGlobal,
		Iteration:      -1,
		Tags:           lib.NewVUStateTags(r.runTags),
		TestStatus:     r.testStatus,
	}
	return &nativeVU{
		id:        idLocal,
		runner:    r,
		state:     state,
		dialer:    dialer,
		transport: transport,
	}, nil
}

type nativeVU struct {
	id        uint64
	runner    *nativeRunner
	state     *lib.State
	dialer    *netext.Dialer
	transport *http.Transport
}

func (vu *nativeVU) GetID() uint64 {
	return vu.id
}

func (vu *nativeVU) Activate(params *lib.VUActivationParams) lib.ActiveVU {
	vu.state.Tags.Modify(func(tagsAndMeta *metrics.TagsAndMeta) {
		tagsAndMeta.Tags = vu.runner.runTags.WithTagsFromMap(params.Tags)
		tagsAndMeta.Metadata = nil
		if vu.state.Options.SystemTags.Has(metrics.TagVU) {
			tagsAndMeta.SetSystemTagOrMeta(metrics.TagVU, strconv.FormatUint(vu.state.VUID, 10))
		}
		tagsAndMeta.SetSystemTagOrMetaIfEnabled(
			vu.state.Options.SystemTags,
			metrics.TagGroup,
			lib.RootGroupPath,
		)
		tagsAndMeta.SetSystemTagOrMetaIfEnabled(
			vu.state.Options.SystemTags,
			metrics.TagScenario,
			params.Scenario,
		)
	})
	active := &activeNativeVU{
		nativeVU: vu,
		ctx:      params.RunContext,
		busy:     make(chan struct{}, 1),
	}
	context.AfterFunc(params.RunContext, func() {
		active.busy <- struct{}{}
		vu.transport.CloseIdleConnections()
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

	vu.state.Iteration++
	if vu.state.Options.SystemTags.Has(metrics.TagIter) {
		vu.state.Tags.Modify(func(tagsAndMeta *metrics.TagsAndMeta) {
			tagsAndMeta.SetSystemTagOrMeta(metrics.TagIter, strconv.FormatInt(vu.state.Iteration, 10))
		})
	}
	if !vu.state.Options.NoCookiesReset.ValueOrZero() {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return fmt.Errorf("reset VU cookie jar: %w", err)
		}
		vu.state.CookieJar = jar
	}

	iterationStarted := time.Now()
	requestURL := *vu.runner.targetURL.GetURL()
	request := &http.Request{
		Method: http.MethodGet,
		URL:    &requestURL,
		Header: make(http.Header),
	}
	requestCookies := make(map[string]*httpext.HTTPRequestCookie)
	if vu.state.CookieJar != nil {
		httpext.SetRequestCookies(request, vu.state.CookieJar, requestCookies)
	}
	responseType := httpext.ResponseTypeText
	if vu.state.Options.DiscardResponseBodies.Bool {
		responseType = httpext.ResponseTypeNone
	}
	_, requestErr := httpext.MakeRequest(vu.ctx, vu.state, &httpext.ParsedHTTPRequest{
		URL:              &vu.runner.targetURL,
		Req:              request,
		Timeout:          vu.runner.requestTimeout,
		Throw:            vu.state.Options.Throw.Bool,
		ResponseType:     responseType,
		ResponseCallback: isExpectedResponse,
		Redirects:        vu.state.Options.MaxRedirects,
		ActiveJar:        vu.state.CookieJar,
		Cookies:          requestCookies,
		TagsAndMeta:      vu.state.Tags.GetCurrentValues(),
	})
	if requestErr != nil {
		requestErr = fmt.Errorf("make k6 HTTP request: %w", requestErr)
	}

	iterationEnded := time.Now()
	currentTags := vu.state.Tags.GetCurrentValues()
	metrics.PushIfNotDone(
		vu.ctx,
		vu.state.Samples,
		vu.dialer.IOSamples(iterationEnded, currentTags, vu.state.BuiltinMetrics),
	)
	select {
	case <-vu.ctx.Done():
		return lib.ContextErr(vu.ctx)
	default:
	}

	iterationDuration := iterationEnded.Sub(iterationStarted)
	vu.emitIterationMetrics(iterationEnded, iterationDuration, currentTags)
	remaining := remainingIterationDuration(
		vu.state.Options.MinIterationDuration.TimeDuration(),
		iterationDuration,
	)
	if remaining > 0 {
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-vu.ctx.Done():
		}
	}
	return requestErr
}

func isExpectedResponse(status int) bool {
	return status >= http.StatusOK && status < http.StatusBadRequest
}

func remainingIterationDuration(minimum, elapsed time.Duration) time.Duration {
	if elapsed >= minimum {
		return 0
	}
	return minimum - elapsed
}

func (vu *activeNativeVU) emitIterationMetrics(
	at time.Time,
	duration time.Duration,
	tagsAndMeta metrics.TagsAndMeta,
) {
	metrics.PushIfNotDone(vu.ctx, vu.state.Samples, metrics.ConnectedSamples{
		Tags: tagsAndMeta.Tags,
		Time: at,
		Samples: []metrics.Sample{
			newSampleWithMetadata(
				vu.state.BuiltinMetrics.IterationDuration,
				tagsAndMeta,
				at,
				metrics.D(duration),
			),
			newSampleWithMetadata(vu.state.BuiltinMetrics.Iterations, tagsAndMeta, at, 1),
		},
	})
}

func newSampleWithMetadata(
	metric *metrics.Metric,
	tagsAndMeta metrics.TagsAndMeta,
	at time.Time,
	value float64,
) metrics.Sample {
	sample := newSample(metric, tagsAndMeta.Tags, at, value)
	sample.Metadata = tagsAndMeta.Metadata
	return sample
}

func newSample(metric *metrics.Metric, tags *metrics.TagSet, at time.Time, value float64) metrics.Sample {
	return metrics.Sample{
		TimeSeries: metrics.TimeSeries{Metric: metric, Tags: tags},
		Time:       at,
		Value:      value,
	}
}

func newRunnerOptions(config runConfig) lib.Options {
	systemTags := metrics.DefaultSystemTagSet
	return lib.Options{
		DNS:                   types.DefaultDNSConfig(),
		MaxRedirects:          null.IntFrom(defaultMaxRedirects),
		Batch:                 null.IntFrom(defaultBatchSize),
		BatchPerHost:          null.IntFrom(defaultBatchSizePerHost),
		Throw:                 null.BoolFrom(false),
		MinIterationDuration:  types.NullDurationFrom(config.minIterationDuration),
		SystemTags:            &systemTags,
		NoCookiesReset:        null.BoolFrom(false),
		DiscardResponseBodies: null.BoolFrom(true),
	}
}

func run(ctx context.Context, config runConfig, stdout, stderr io.Writer) error {
	logger := logrus.New()
	logger.SetOutput(stderr)
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	runTags := registry.RootTagSet()
	out := make(chan metrics.SampleContainer, 128)
	targetURL, err := httpext.NewURL(config.targetURL, config.targetURL)
	if err != nil {
		return fmt.Errorf("create k6 target URL: %w", err)
	}
	options := newRunnerOptions(config)
	dnsTTL, err := types.ParseExtendedDuration(options.DNS.TTL.String)
	if err != nil {
		return fmt.Errorf("parse default DNS TTL: %w", err)
	}
	testStatus := lib.NewTestStatus()
	resolver := netext.NewResolver(
		net.LookupIP,
		dnsTTL,
		options.DNS.Select.DNSSelect,
		options.DNS.Policy.DNSPolicy,
	)

	tuple, err := lib.NewExecutionTuple(nil, nil)
	if err != nil {
		return fmt.Errorf("create execution tuple: %w", err)
	}
	executorConfig := executor.NewSharedIterationsConfig("native-go")
	executorConfig.VUs = null.IntFrom(config.virtualUsers)
	executorConfig.Iterations = null.IntFrom(config.iterations)
	executorConfig.MaxDuration = types.NullDurationFrom(config.maxDuration)
	requirements := executorConfig.GetExecutionRequirements(tuple)
	options.Scenarios = lib.ScenarioConfigs{"native-go": executorConfig}
	runner := &nativeRunner{
		logger:         logger,
		options:        options,
		resolver:       resolver,
		bufferPool:     lib.NewBufferPool(),
		builtin:        builtin,
		testStatus:     testStatus,
		runTags:        runTags,
		targetURL:      targetURL,
		requestTimeout: config.requestTimeout,
	}
	test := &lib.TestRunState{
		TestPreInitState: &lib.TestPreInitState{
			Logger:         logger,
			Registry:       registry,
			BuiltinMetrics: builtin,
			TestStatus:     testStatus,
		},
		Options: options,
		Runner:  runner,
		RunTags: runTags,
	}
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
