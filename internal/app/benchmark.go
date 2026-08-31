// This file assembles and runs the native benchmark lifecycle and its outputs.
package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/grafana/xk6-dashboard/dashboard"
	"github.com/sirupsen/logrus"
	"gopkg.in/guregu/null.v3"

	"go.k6.io/k6/errext"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/executor"
	"go.k6.io/k6/lib/fsext"
	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
	"go.opentelemetry.io/otel/trace"
	"k6-as-a-library/internal/artifact"
	benchmarkpkg "k6-as-a-library/internal/benchmark"
	"k6-as-a-library/internal/dsl"
	"k6-as-a-library/internal/k6output"
	k6oteltrace "k6-as-a-library/internal/otel"
	"k6-as-a-library/internal/pact"
	"k6-as-a-library/internal/report"
)

const (
	defaultTargetURL                 = "http://localhost:8080/headers"
	defaultVirtualUsers              = int64(1)
	defaultIterations                = int64(10000)
	defaultMinIterationDuration      = 25 * time.Millisecond
	defaultRequestTimeout            = 10 * time.Second
	defaultMaxDuration               = 300 * time.Second
	defaultJSONFilename              = "metrics.json"
	defaultHTMLFilename              = "report.html"
	defaultDashboardFilename         = ""
	defaultCombinedFilename          = ""
	defaultBenchmarkManifestFilename = ""
	defaultDashboardHost             = "127.0.0.1"
	defaultDashboardPort             = 5665
	dashboardMinPeriod               = time.Second
	defaultMaxRedirects              = int64(10)
	defaultBatchSize                 = int64(20)
	defaultBatchSizePerHost          = int64(6)
	expectedResponseSubmetric        = "expected_response:true"
	pactResponseCheckSubmetric       = "check:" + pact.ResponseCheckName
	pactResponsesValidThreshold      = "rate==1"
)

type runConfig struct {
	targetURL                 string
	pactProviderURL           string
	pactDirectory             string
	virtualUsers              int64
	iterations                int64
	minIterationDuration      time.Duration
	requestTimeout            time.Duration
	maxDuration               time.Duration
	jsonFilename              string
	htmlFilename              string
	dashboardFilename         string
	combinedFilename          string
	benchmarkManifestFilename string
	outputs                   []string
	outputsFlagSet            bool
	tracesOutput              string
	tracesOutputFlagSet       bool
	traceConfiguration        k6oteltrace.TraceOutputConfiguration
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
		minIterationDuration:      defaultMinIterationDuration,
		requestTimeout:            defaultRequestTimeout,
		maxDuration:               defaultMaxDuration,
		jsonFilename:              defaultJSONFilename,
		htmlFilename:              defaultHTMLFilename,
		dashboardFilename:         defaultDashboardFilename,
		combinedFilename:          defaultCombinedFilename,
		benchmarkManifestFilename: defaultBenchmarkManifestFilename,
		tracesOutput:              k6oteltrace.DefaultTracesOutput,
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
	if err := k6oteltrace.ValidateOutputNames(config.outputs); err != nil {
		return err
	}
	tracesOutput := config.tracesOutput
	if tracesOutput == "" && !config.tracesOutputFlagSet {
		tracesOutput = k6oteltrace.DefaultTracesOutput
	}
	if _, err := k6oteltrace.ParseTracesOutput(tracesOutput); err != nil {
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
	return config.targetURL
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

type nativeRunner struct {
	lib.Runner
	logger             logrus.FieldLogger
	options            lib.Options
	resolver           netext.Resolver
	bufferPool         *lib.BufferPool
	builtin            *metrics.BuiltinMetrics
	testStatus         *lib.TestStatus
	runTags            *metrics.TagSet
	targetURL          httpext.URL
	requestTimeout     time.Duration
	benchmark          benchmarkExecution
	executionStartedAt time.Time
	nextCaseOrdinal    atomic.Uint64
	traceProvider      *k6oteltrace.Provider
	benchmarkSpan      trace.Span
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
	if r.traceProvider == nil {
		return nil, errors.New("create VU: trace provider is not initialized")
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
		TracerProvider: r.traceProvider.TracerProvider(),
	}
	state.Transport = r.traceProvider.WrapTransport(transport)
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

type preparedRequest struct {
	Request *http.Request
	URL     *httpext.URL
	Body    *bytes.Buffer
	Cookies map[string]*httpext.HTTPRequestCookie
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

	if vu.runner.benchmark.validated.Benchmark().ID == "" {
		return errors.New("execute plan: validated execution plan is not initialized")
	}
	ordinal := vu.runner.nextCaseOrdinal.Add(1) - 1
	selection, err := vu.runner.benchmark.validated.SelectAt(time.Since(vu.runner.executionStartedAt), ordinal)
	if err != nil {
		return fmt.Errorf("select execution case %d: %w", ordinal, err)
	}
	item := selection.Case
	interaction, isPact := vu.runner.benchmark.pactSources[item.ID]
	var pactSource *pact.Interaction
	if isPact {
		pactSource = &interaction
	}
	prepared, err := vu.runner.prepareCaseRequest(item)
	if err != nil {
		return fmt.Errorf("prepare execution case %q: %w", item.Name, err)
	}
	vu.state.Tags.Modify(func(tagsAndMeta *metrics.TagsAndMeta) {
		applyExecutionCaseTags(tagsAndMeta, &vu.runner.benchmark, item, vu.state.Options.SystemTags)
	})

	iterationStarted := time.Now()
	request := prepared.Request
	requestCookies := prepared.Cookies
	if vu.state.CookieJar != nil {
		httpext.SetRequestCookies(request, vu.state.CookieJar, requestCookies)
	}
	responseType := httpext.ResponseTypeText
	if isPact {
		responseType = httpext.ResponseTypeBinary
	} else if vu.state.Options.DiscardResponseBodies.Bool {
		responseType = httpext.ResponseTypeNone
	}
	responseCallback := isExpectedResponse
	if item.Expectation != nil && item.Expectation.Status != nil {
		expectedStatus := item.Expectation.Status.Equals
		responseCallback = func(status int) bool {
			return status == expectedStatus
		}
	}
	redirects := vu.state.Options.MaxRedirects
	if item.Request.Redirects == dsl.RedirectNone {
		redirects = null.IntFrom(0)
	}
	traceAttributes := vu.runner.interactionTraceAttributes(vu, item, pactSource, request)
	requestContext := trace.ContextWithSpan(vu.ctx, vu.runner.benchmarkSpan)
	var interactionContext context.Context
	var interactionSpan trace.Span
	if isPact {
		interactionContext, interactionSpan = vu.runner.traceProvider.StartPactInteractionSpan(requestContext, traceAttributes)
	} else {
		interactionContext, interactionSpan = vu.runner.traceProvider.StartInteractionSpan(
			requestContext,
			item.Name,
			traceAttributes,
		)
	}
	response, requestErr := httpext.MakeRequest(interactionContext, vu.state, &httpext.ParsedHTTPRequest{
		URL:              prepared.URL,
		Body:             prepared.Body,
		Req:              request,
		Timeout:          vu.runner.requestTimeout,
		Throw:            vu.state.Options.Throw.Bool,
		ResponseType:     responseType,
		ResponseCallback: responseCallback,
		Redirects:        redirects,
		ActiveJar:        vu.state.CookieJar,
		Cookies:          requestCookies,
		TagsAndMeta:      vu.state.Tags.GetCurrentValues(),
	})
	if requestErr == nil && (response == nil || response.Status == 0) {
		requestErr = vu.ctx.Err()
	}
	if isPact {
		verification := vu.checkPactResponse(response, pactSource, requestErr)
		k6oteltrace.RecordPactVerification(interactionSpan, verification)
	} else if requestErr != nil {
		k6oteltrace.RecordTransportError(interactionSpan, requestErr)
	}
	if response != nil {
		requestTraceAttributes := traceAttributes.Request
		requestTraceAttributes.ActualStatus = response.Status
		k6oteltrace.ApplyRequestAttributes(interactionSpan, requestTraceAttributes)
	}
	if requestErr != nil {
		requestErr = fmt.Errorf("make k6 HTTP request: %w", requestErr)
	}
	interactionSpan.End()

	iterationEnded := time.Now()
	return vu.finishIteration(iterationStarted, iterationEnded, requestErr)
}

func (r *nativeRunner) prepareCaseRequest(item dsl.Case) (preparedRequest, error) {
	requestURL, err := r.caseRequestURL(item)
	if err != nil {
		return preparedRequest{}, err
	}
	request := &http.Request{Method: item.Request.Method, URL: requestURL, Header: make(http.Header)}
	for _, header := range item.Request.Headers {
		if strings.EqualFold(header.Name, "Host") {
			continue
		}
		for _, value := range header.Values {
			request.Header.Add(header.Name, value)
		}
	}
	var body *bytes.Buffer
	if item.Request.Body != nil {
		bodyBytes, err := item.Request.Body.Bytes()
		if err != nil {
			return preparedRequest{}, fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewBuffer(bodyBytes)
	}
	cookies := make(map[string]*httpext.HTTPRequestCookie, len(item.Request.Cookies))
	for _, cookie := range item.Request.Cookies {
		cookies[cookie.Name] = &httpext.HTTPRequestCookie{Name: cookie.Name, Value: cookie.Value, Replace: true}
	}
	requestName := item.Name
	if item.Source.Kind == "generated" {
		requestName = requestURL.String()
	}
	requestK6URL, err := httpext.NewURL(requestURL.String(), requestName)
	if err != nil {
		return preparedRequest{}, fmt.Errorf("create request URL: %w", err)
	}
	return preparedRequest{Request: request, URL: &requestK6URL, Body: body, Cookies: cookies}, nil
}

func (r *nativeRunner) caseRequestURL(item dsl.Case) (*url.URL, error) {
	base := r.targetURL.GetURL()
	if base == nil {
		return nil, errors.New("target URL is nil")
	}
	if item.Source.Kind == "generated" && item.Source.Identifier == directCaseID {
		copy := *base
		return &copy, nil
	}
	pathURL, err := url.Parse(item.Request.Path)
	if err != nil {
		return nil, fmt.Errorf("parse request path %q: %w", item.Request.Path, err)
	}
	if pathURL.IsAbs() || pathURL.Host != "" || pathURL.Fragment != "" {
		return nil, fmt.Errorf("request path %q must be relative", item.Request.Path)
	}
	resolved := *base
	resolved.Path = pact.JoinPath(base.Path, pathURL.Path)
	resolved.RawPath = ""
	values := make(url.Values, len(item.Request.Query))
	for _, parameter := range item.Request.Query {
		values.Add(parameter.Name, parameter.Value)
	}
	resolved.RawQuery = values.Encode()
	resolved.Fragment = ""
	return &resolved, nil
}

func (r *nativeRunner) interactionTraceAttributes(
	vu *activeNativeVU,
	item dsl.Case,
	interaction *pact.Interaction,
	request *http.Request,
) k6oteltrace.InteractionAttributes {
	requestAttributes := k6oteltrace.RequestAttributes{}
	if request != nil {
		requestAttributes.Method = request.Method
		if request.URL != nil {
			requestAttributes.URL = request.URL.String()
		}
	}
	attributes := k6oteltrace.InteractionAttributes{
		Benchmark: k6oteltrace.BenchmarkAttributes{
			Name:        "native-go",
			RunID:       r.traceProvider.Config().RunID,
			BenchmarkID: r.benchmark.validated.Benchmark().ID,
			Scenario:    "native-go",
			VUID:        vu.state.VUID,
			GlobalVUID:  vu.state.VUIDGlobal,
			Iteration:   vu.state.Iteration,
		},
		Request: requestAttributes,
	}
	if interaction != nil {
		attributes.Pact = k6oteltrace.PactAttributes{
			ConsumerService: caseLabel(item, pact.ConsumerTag),
			ProviderService: caseLabel(item, pact.ProviderTag),
			Endpoint:        caseLabel(item, pact.EndpointTag),
			Interaction:     caseLabel(item, pact.InteractionTag),
			ProviderState:   caseLabel(item, pact.ProviderStateTag),
			Name:            item.Name,
		}
		if item.Expectation != nil && item.Expectation.Status != nil {
			attributes.Request.ExpectedStatus = item.Expectation.Status.Equals
		}
	}
	return attributes
}

func applyExecutionCaseTags(tagsAndMeta *metrics.TagsAndMeta, execution *benchmarkExecution, item dsl.Case, systemTags *metrics.SystemTagSet) {
	for _, name := range pact.RequestTagNames() {
		tagsAndMeta.DeleteTag(name)
	}
	for _, name := range execution.tagNames {
		tagsAndMeta.DeleteTag(name)
	}
	for _, name := range pact.RequestMetadataNames() {
		tagsAndMeta.DeleteMetadata(name)
	}
	for _, name := range execution.metadataNames {
		tagsAndMeta.DeleteMetadata(name)
	}
	for _, label := range item.Labels {
		tagsAndMeta.SetTag(label.Name, label.Value)
	}
	for _, metadata := range item.Metadata {
		tagsAndMeta.SetMetadata(metadata.Name, metadata.Value)
	}
	if item.Source.Kind == "pact" {
		tagsAndMeta.SetSystemTagOrMetaIfEnabled(systemTags, metrics.TagName, item.Name)
	} else {
		tagsAndMeta.DeleteTag(metrics.TagName.String())
	}
}

func caseLabel(item dsl.Case, name string) string {
	for _, label := range item.Labels {
		if label.Name == name {
			return label.Value
		}
	}
	return ""
}

func (vu *activeNativeVU) checkPactResponse(
	response *httpext.Response,
	interaction *pact.Interaction,
	requestErr error,
) k6oteltrace.PactVerificationResult {
	result := pact.VerifyResponse(interaction.Response, response, requestErr)

	at := time.Now()
	tagsAndMeta := vu.state.Tags.GetCurrentValues()
	tagsAndMeta.SetSystemTagOrMetaIfEnabled(
		vu.state.Options.SystemTags,
		metrics.TagCheck,
		pact.ResponseCheckName,
	)
	if result.Mismatch != nil {
		tagsAndMeta.SetMetadata(pact.MismatchMetadata, result.Mismatch.Error())
		vu.state.Logger.WithFields(logrus.Fields{
			"interaction": interaction.Name,
			"pact_file":   interaction.PactFile,
		}).Warnf("PACT response mismatch: %v", result.Mismatch)
	}
	value := float64(1)
	if !result.Passed {
		value = 0
	}
	metrics.PushIfNotDone(vu.ctx, vu.state.Samples, metrics.ConnectedSamples{
		Tags: tagsAndMeta.Tags,
		Time: at,
		Samples: []metrics.Sample{
			newSampleWithMetadata(vu.state.BuiltinMetrics.Checks, tagsAndMeta, at, value),
		},
	})
	return result
}

func (vu *activeNativeVU) finishIteration(
	iterationStarted time.Time,
	iterationEnded time.Time,
	requestErr error,
) error {
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
		SummaryTrendStats:     slices.Clone(lib.DefaultSummaryTrendStats),
	}
}

func initializeSummarySubmetrics(builtin *metrics.BuiltinMetrics, options lib.Options) error {
	if options.SystemTags == nil || !options.SystemTags.Has(metrics.TagExpectedResponse) {
		return nil
	}
	if _, err := builtin.HTTPReqDuration.AddSubmetric(expectedResponseSubmetric); err != nil {
		return fmt.Errorf("initialize expected-response duration submetric: %w", err)
	}
	return nil
}

func initializeBenchmarkThresholds(
	registry *metrics.Registry,
	builtin *metrics.BuiltinMetrics,
	options *lib.Options,
	validated benchmarkpkg.ValidatedBenchmark,
) error {
	for _, threshold := range validated.Benchmark().Thresholds {
		baseMetric, submetricName, err := splitBenchmarkThresholdMetric(threshold.Metric)
		if err != nil {
			return err
		}
		if baseMetric != metrics.ChecksName || threshold.Aggregation != dsl.ThresholdAggregationRate {
			return fmt.Errorf("threshold %q uses unsupported execution metric %q or aggregation %q", threshold.ID, threshold.Metric, threshold.Aggregation)
		}
		submetric, err := builtin.Checks.AddSubmetric(submetricName)
		if err != nil {
			return fmt.Errorf("initialize threshold %q submetric: %w", threshold.ID, err)
		}
		expression := fmt.Sprintf("rate%s%s", threshold.Operator, strconv.FormatFloat(threshold.Target, 'f', -1, 64))
		thresholds := metrics.NewThresholds([]string{expression})
		if err := thresholds.Parse(); err != nil {
			return fmt.Errorf("parse threshold %q: %w", threshold.ID, err)
		}
		if err := thresholds.Validate(submetric.Name, registry); err != nil {
			return fmt.Errorf("validate threshold %q: %w", threshold.ID, err)
		}
		submetric.Metric.Thresholds = thresholds
		if options.Thresholds == nil {
			options.Thresholds = make(map[string]metrics.Thresholds)
		}
		options.Thresholds[submetric.Name] = thresholds
	}
	return nil
}

func splitBenchmarkThresholdMetric(metric string) (string, string, error) {
	open := strings.IndexByte(metric, '{')
	close := strings.LastIndexByte(metric, '}')
	if open <= 0 || close != len(metric)-1 || close <= open+1 {
		return "", "", fmt.Errorf("threshold metric %q must identify a tagged submetric", metric)
	}
	baseMetric := metric[:open]
	submetric := metric[open+1 : close]
	if strings.Contains(submetric, ",") || !strings.HasPrefix(submetric, "check:") {
		return "", "", fmt.Errorf("threshold metric %q has unsupported tag scope", metric)
	}
	return baseMetric, submetric, nil
}

func newTraceProvider(
	ctx context.Context,
	configuration k6oteltrace.TraceOutputConfiguration,
	factorySets ...k6oteltrace.ExporterFactories,
) (*k6oteltrace.Provider, error) {
	traceConfig := k6oteltrace.DefaultConfig()
	traceConfig.Enabled = configuration.Enabled
	traceConfig.ServiceName = k6oteltrace.DefaultServiceName
	traceConfig.ServiceVersion = k6oteltrace.DefaultServiceVersion
	traceConfig.PropagationEnabled = configuration.Enabled
	traceConfig.Headers = cloneTraceHeaders(configuration.Headers)
	traceConfig.RunID = configuration.RunID
	traceConfig.Insecure = configuration.Insecure
	traceConfig.Protocol = k6oteltrace.Protocol(configuration.Protocol)
	if configuration.Enabled {
		scheme := "https"
		if configuration.Insecure {
			scheme = "http"
		}
		traceConfig.Endpoint = scheme + "://" + configuration.Endpoint
		if configuration.Protocol == string(k6oteltrace.ProtocolHTTP) {
			traceConfig.Endpoint += configuration.URLPath
		}
	}
	provider, err := k6oteltrace.New(ctx, traceConfig, factorySets...)
	if err != nil {
		return nil, fmt.Errorf("create trace provider: %w", err)
	}
	return provider, nil
}

func cloneTraceHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		clone[key] = value
	}
	return clone
}

func finalizeTraceProvider(provider *k6oteltrace.Provider, benchmarkSpan trace.Span) error {
	if benchmarkSpan != nil {
		benchmarkSpan.End()
	}
	if provider == nil || !provider.Enabled() {
		return nil
	}
	flushErr := provider.ForceFlush(context.Background())
	if errors.Is(flushErr, k6oteltrace.ErrProviderClosed) {
		return nil
	}
	shutdownErr := provider.Shutdown(context.Background())
	if flushErr != nil {
		flushErr = fmt.Errorf("flush traces: %w", flushErr)
	}
	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("shutdown traces: %w", shutdownErr)
	}
	return errors.Join(flushErr, shutdownErr)
}

func run(ctx context.Context, config runConfig, stdout, stderr io.Writer) (runErr error) {
	environment := k6oteltrace.EnvironmentSnapshot()
	resolvedConfig, err := resolveRunConfig(config, environment)
	if err != nil {
		return err
	}
	if err := resolvedConfig.validate(); err != nil {
		return err
	}
	config = resolvedConfig
	runID, err := k6oteltrace.NewRunID()
	if err != nil {
		return fmt.Errorf("create benchmark run ID: %w", err)
	}

	logger := logrus.New()
	logger.SetOutput(stderr)
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	runTags := registry.RootTagSet()
	out := make(chan metrics.SampleContainer, 128)
	executionTarget := config.executionTargetURL()
	targetURL, err := httpext.NewURL(executionTarget, executionTarget)
	if err != nil {
		return fmt.Errorf("create k6 target URL: %w", err)
	}
	var interactions []pact.Interaction
	if config.pactDirectory != "" {
		interactions, err = pact.LoadDirectory(config.pactDirectory)
		if err != nil {
			return err
		}
	}
	execution, err := synthesizeBenchmark(config, &targetURL, interactions)
	if err != nil {
		return err
	}
	if config.benchmarkManifestFilename != "" {
		if err := benchmarkpkg.WriteManifest(config.benchmarkManifestFilename, execution.validated.Benchmark()); err != nil {
			return fmt.Errorf("write benchmark manifest: %w", err)
		}
	}
	options := newRunnerOptions(config)
	if err := initializeBenchmarkThresholds(registry, builtin, &options, execution.validated); err != nil {
		return err
	}
	if err := initializeSummarySubmetrics(builtin, options); err != nil {
		return err
	}
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
	traceConfiguration := config.traceConfiguration
	traceConfiguration.RunID = runID
	traceProvider, err := newTraceProvider(ctx, traceConfiguration)
	if err != nil {
		return fmt.Errorf("initialize tracing: %w", err)
	}
	_, benchmarkSpan := traceProvider.StartBenchmarkSpan(ctx, k6oteltrace.BenchmarkAttributes{
		Name:        "native-go",
		RunID:       traceProvider.Config().RunID,
		BenchmarkID: "native-go",
		Scenario:    "native-go",
	})
	defer func() {
		runErr = errors.Join(runErr, finalizeTraceProvider(traceProvider, benchmarkSpan))
	}()

	tuple, err := lib.NewExecutionTuple(nil, nil)
	if err != nil {
		return fmt.Errorf("create execution tuple: %w", err)
	}
	executorConfig := executor.NewSharedIterationsConfig("native-go")
	baseline := execution.validated.Benchmark().Baseline
	executorConfig.VUs = null.IntFrom(baseline.VUs)
	executorConfig.Iterations = null.IntFrom(baseline.Iterations)
	executorConfig.MaxDuration = types.NullDurationFrom(config.maxDuration)
	requirements := executorConfig.GetExecutionRequirements(tuple)
	options.Scenarios = lib.ScenarioConfigs{"native-go": executorConfig}
	runner := &nativeRunner{
		logger:             logger,
		options:            options,
		resolver:           resolver,
		bufferPool:         lib.NewBufferPool(),
		builtin:            builtin,
		testStatus:         testStatus,
		runTags:            runTags,
		targetURL:          targetURL,
		requestTimeout:     config.requestTimeout,
		benchmark:          execution,
		executionStartedAt: time.Now(),
		traceProvider:      traceProvider,
		benchmarkSpan:      benchmarkSpan,
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

	outputParams := output.Params{
		Logger:         logger,
		Environment:    environment,
		StdOut:         stdout,
		StdErr:         stderr,
		FS:             fsext.NewOsFs(),
		ScriptOptions:  test.Options,
		RuntimeOptions: test.RuntimeOptions,
		ExecutionPlan:  requirements,
	}
	jsonOutput := k6output.NewJSON(config.jsonFilename)
	consoleOutput := report.NewSummaryOutput(
		stdout,
		config.htmlFilename,
		config.jsonFilename,
		options,
		len(interactions) > 0,
	)
	var dashboardReportOutput *report.DashboardReportOutput
	if config.dashboardFilename != "" {
		dashboardReportOutput, err = report.NewDashboardReportOutputWithOptions(outputParams, report.DashboardReportOptions{
			Filename: config.dashboardFilename,
			Period:   report.DashboardDefaultPeriod,
			Tags:     dashboardReportTags(config),
		})
		if err != nil {
			return fmt.Errorf("create dashboard report output: %w", err)
		}
	} else if config.combinedFilename != "" {
		dashboardReportOutput, err = report.NewDashboardModelOutput(
			outputParams,
			report.DashboardDefaultPeriod,
			dashboardReportTags(config),
		)
		if err != nil {
			return fmt.Errorf("create combined report dashboard model: %w", err)
		}
	}
	var otelOutput output.Output
	if k6oteltrace.HasOutput(config.outputs, k6oteltrace.OutputName) {
		otelOutput, err = k6oteltrace.NewMetricsOutputWithRunID(outputParams, runID)
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
		liveDashboard, err := newLiveDashboardOutput(config, outputParams)
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

	startedAt := time.Now()
	runErr = k6Executor.Run(ctx, out)
	observedRuntime := time.Since(startedAt)
	close(out)
	waitForOutputs()
	consoleOutput.SetTestRunDuration(observedRuntime)
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

func newLiveDashboardOutput(config runConfig, params output.Params) (output.Output, error) {
	values := url.Values{
		"host":   {config.dashboardHost},
		"period": {dashboardMinPeriod.String()},
		"port":   {strconv.Itoa(config.dashboardPort)},
	}
	if config.pactDirectory != "" {
		for _, tag := range pact.SummaryTags() {
			values.Add("tag", tag)
		}
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

func dashboardReportTags(config runConfig) []string {
	if config.pactDirectory != "" {
		return pact.SummaryTags()
	}
	return []string{report.DashboardDefaultTag}
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
