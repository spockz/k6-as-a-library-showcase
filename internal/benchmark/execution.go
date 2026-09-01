package benchmark

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"k6-as-a-library/internal/dsl"
	k6oteltrace "k6-as-a-library/internal/otel"

	"github.com/sirupsen/logrus"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/metrics"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/guregu/null.v3"
)

type Execution struct {
	validated      ValidatedBenchmark
	attributeNames []string
	metadataNames  []string
}

var reservedK6AttributeNames = map[string]struct{}{
	"error": {}, "error_code": {}, "expected_response": {}, "group": {}, "ip": {},
	"iter": {}, "method": {}, "name": {}, "path": {}, "proto": {}, "scenario": {},
	"status": {}, "tls_cipher": {}, "tls_version": {}, "url": {}, "vu": {}, "vu_max": {},
}

func NewExecution(validated ValidatedBenchmark) (Execution, error) {
	model := validated.Benchmark()
	if model.ID == "" {
		return Execution{}, errors.New("create benchmark execution: validated benchmark is empty")
	}
	attributeSet := make(map[string]struct{})
	metadataSet := make(map[string]struct{})
	for _, item := range model.Cases {
		for _, name := range item.Attributes.Names() {
			if _, reserved := reservedK6AttributeNames[strings.ToLower(name)]; reserved {
				return Execution{}, fmt.Errorf("create benchmark execution: case %q attribute %q conflicts with a k6 system tag", item.ID, name)
			}
			attributeSet[name] = struct{}{}
		}
		for _, metadata := range item.Metadata {
			metadataSet[metadata.Name] = struct{}{}
		}
	}
	for _, segment := range model.Segments {
		for _, name := range segment.Attributes.Names() {
			if _, reserved := reservedK6AttributeNames[strings.ToLower(name)]; reserved {
				return Execution{}, fmt.Errorf("create benchmark execution: segment %q attribute %q conflicts with a k6 system tag", segment.ID, name)
			}
			attributeSet[name] = struct{}{}
		}
	}
	if model.SegmentPolicy.Default != nil {
		for _, name := range model.SegmentPolicy.Default.Attributes.Names() {
			if _, reserved := reservedK6AttributeNames[strings.ToLower(name)]; reserved {
				return Execution{}, fmt.Errorf("create benchmark execution: default segment attribute %q conflicts with a k6 system tag", name)
			}
			attributeSet[name] = struct{}{}
		}
	}
	return Execution{
		validated:      validated.Clone(),
		attributeNames: slices.Sorted(maps.Keys(attributeSet)),
		metadataNames:  slices.Sorted(maps.Keys(metadataSet)),
	}, nil
}

type RunnerConfig struct {
	Logger         logrus.FieldLogger
	Options        lib.Options
	Resolver       netext.Resolver
	BufferPool     *lib.BufferPool
	BuiltinMetrics *metrics.BuiltinMetrics
	TestStatus     *lib.TestStatus
	RunTags        *metrics.TagSet
	TargetURL      httpext.URL
	ExactTarget    bool
	RequestTimeout time.Duration
	Benchmark      ValidatedBenchmark
	TraceProvider  *k6oteltrace.Provider
	BenchmarkSpan  trace.Span
}

func NewRunner(config RunnerConfig) (*Runner, error) {
	execution, err := NewExecution(config.Benchmark)
	if err != nil {
		return nil, err
	}
	if config.Logger == nil {
		return nil, errors.New("create benchmark runner: logger is nil")
	}
	if config.TargetURL.GetURL() == nil {
		return nil, errors.New("create benchmark runner: target URL is nil")
	}
	if config.TraceProvider == nil {
		return nil, errors.New("create benchmark runner: trace provider is nil")
	}
	if config.BufferPool == nil || config.BuiltinMetrics == nil || config.TestStatus == nil || config.RunTags == nil {
		return nil, errors.New("create benchmark runner: k6 runtime dependencies are incomplete")
	}
	return &Runner{
		logger:             config.Logger,
		options:            config.Options,
		resolver:           config.Resolver,
		bufferPool:         config.BufferPool,
		builtin:            config.BuiltinMetrics,
		testStatus:         config.TestStatus,
		runTags:            config.RunTags,
		targetURL:          config.TargetURL,
		exactTarget:        config.ExactTarget,
		requestTimeout:     config.RequestTimeout,
		benchmark:          execution,
		executionStartedAt: time.Now(),
		traceProvider:      config.TraceProvider,
		benchmarkSpan:      config.BenchmarkSpan,
	}, nil
}

type Runner struct {
	lib.Runner
	logger             logrus.FieldLogger
	options            lib.Options
	resolver           netext.Resolver
	bufferPool         *lib.BufferPool
	builtin            *metrics.BuiltinMetrics
	testStatus         *lib.TestStatus
	runTags            *metrics.TagSet
	targetURL          httpext.URL
	exactTarget        bool
	requestTimeout     time.Duration
	benchmark          Execution
	executionStartedAt time.Time
	nextCaseOrdinal    atomic.Uint64
	traceProvider      *k6oteltrace.Provider
	benchmarkSpan      trace.Span
}

func (r *Runner) NewVU(
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
	runner    *Runner
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
		scenario: params.Scenario,
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
	ctx      context.Context
	busy     chan struct{}
	scenario string
}

type PreparedRequest struct {
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
	selection, err := vu.runner.benchmark.validated.SelectPhase(vu.scenario, time.Since(vu.runner.executionStartedAt), ordinal)
	if err != nil {
		return fmt.Errorf("select execution case %d: %w", ordinal, err)
	}
	item := selection.Case
	materializedRequest, err := item.Request.Materialize(vu.ctx)
	if err != nil {
		return fmt.Errorf("materialize execution case %q: %w", item.Name, err)
	}
	item.Request = materializedRequest
	prepared, err := vu.runner.prepareCaseRequest(item)
	if err != nil {
		return fmt.Errorf("prepare execution case %q: %w", item.Name, err)
	}
	vu.state.Tags.Modify(func(tagsAndMeta *metrics.TagsAndMeta) {
		applyExecutionCaseAttributes(tagsAndMeta, &vu.runner.benchmark, item, selection.Segment)
	})

	iterationStarted := time.Now()
	request := prepared.Request
	requestCookies := prepared.Cookies
	if vu.state.CookieJar != nil {
		httpext.SetRequestCookies(request, vu.state.CookieJar, requestCookies)
	}
	responseType := httpext.ResponseTypeText
	matchResponse := item.Check != nil && item.Check.Enabled
	if matchResponse {
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
	traceAttributes := vu.runner.interactionTraceAttributes(vu, item, selection.Segment, request)
	requestContext := trace.ContextWithSpan(vu.ctx, vu.runner.benchmarkSpan)
	interactionContext, interactionSpan := vu.runner.traceProvider.StartInteractionSpan(
		requestContext,
		item.Name,
		traceAttributes,
	)
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
	if matchResponse {
		verification, matchErr := vu.checkResponse(item, response, requestErr)
		k6oteltrace.RecordVerification(interactionSpan, traceVerification(verification))
		if matchErr != nil {
			requestErr = errors.Join(requestErr, fmt.Errorf("match execution case %q response: %w", item.Name, matchErr))
		}
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

func PrepareRequest(targetURL httpext.URL, exactTarget bool, item dsl.Case) (PreparedRequest, error) {
	runner := Runner{targetURL: targetURL, exactTarget: exactTarget}
	return runner.prepareCaseRequest(item)
}

func (r *Runner) prepareCaseRequest(item dsl.Case) (PreparedRequest, error) {
	requestURL, err := r.caseRequestURL(item)
	if err != nil {
		return PreparedRequest{}, err
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
			return PreparedRequest{}, fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewBuffer(bodyBytes)
	}
	cookies := make(map[string]*httpext.HTTPRequestCookie, len(item.Request.Cookies))
	for _, cookie := range item.Request.Cookies {
		cookies[cookie.Name] = &httpext.HTTPRequestCookie{Name: cookie.Name, Value: cookie.Value, Replace: true}
	}
	requestK6URL, err := httpext.NewURL(requestURL.String(), requestURL.String())
	if err != nil {
		return PreparedRequest{}, fmt.Errorf("create request URL: %w", err)
	}
	return PreparedRequest{Request: request, URL: &requestK6URL, Body: body, Cookies: cookies}, nil
}

func (r *Runner) caseRequestURL(item dsl.Case) (*url.URL, error) {
	base := r.targetURL.GetURL()
	if base == nil {
		return nil, errors.New("target URL is nil")
	}
	if r.exactTarget {
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
	resolved.Path = joinRequestPath(base.Path, pathURL.Path)
	resolved.RawPath = ""
	values := make(url.Values, len(item.Request.Query))
	for _, parameter := range item.Request.Query {
		values.Add(parameter.Name, parameter.Value)
	}
	resolved.RawQuery = values.Encode()
	resolved.Fragment = ""
	return &resolved, nil
}

func (r *Runner) interactionTraceAttributes(
	vu *activeNativeVU,
	item dsl.Case,
	segment dsl.Segment,
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
		Attributes: traceStringAttributes(item, segment),
		Request:    requestAttributes,
	}
	if item.Expectation != nil && item.Expectation.Status != nil {
		attributes.Request.ExpectedStatus = item.Expectation.Status.Equals
	}
	return attributes
}

func traceStringAttributes(item dsl.Case, segment dsl.Segment) []k6oteltrace.StringAttribute {
	attributes := item.Attributes.WithOverrides(segment.Attributes)
	result := make([]k6oteltrace.StringAttribute, 0, len(attributes))
	for _, name := range attributes.Names() {
		value, _ := attributes.Get(name)
		result = append(result, k6oteltrace.StringAttribute{Name: name, Value: value})
	}
	return result
}

func applyExecutionCaseAttributes(tagsAndMeta *metrics.TagsAndMeta, execution *Execution, item dsl.Case, segment dsl.Segment) {
	for _, name := range execution.attributeNames {
		tagsAndMeta.DeleteTag(name)
	}
	for _, name := range execution.metadataNames {
		tagsAndMeta.DeleteMetadata(name)
	}
	for _, attribute := range item.Attributes.WithOverrides(segment.Attributes) {
		tagsAndMeta.SetTag(attribute.Name, attribute.Value)
	}
	for _, metadata := range item.Metadata {
		tagsAndMeta.SetMetadata(metadata.Name, metadata.Value)
	}
}

func (vu *activeNativeVU) checkResponse(
	item dsl.Case,
	response *httpext.Response,
	requestErr error,
) (dsl.MatchResult, error) {
	result := dsl.MatchResult{Matched: false, Kind: dsl.MatchTransport, MismatchCount: 1, Mismatch: requestErr}
	if item.Expectation != nil && item.Expectation.Status != nil {
		result.ExpectedStatus = item.Expectation.Status.Equals
	}
	if response != nil {
		result.ActualStatus = response.Status
	}
	if requestErr == nil {
		actual, err := responseSnapshot(response)
		if err != nil {
			return dsl.MatchResult{}, fmt.Errorf("snapshot HTTP response: %w", err)
		}
		result, err = item.Request.Match(vu.ctx, actual)
		if err != nil {
			return dsl.MatchResult{}, err
		}
	}

	at := time.Now()
	tagsAndMeta := vu.state.Tags.GetCurrentValues()
	tagsAndMeta.SetSystemTagOrMetaIfEnabled(
		vu.state.Options.SystemTags,
		metrics.TagCheck,
		item.Check.Name,
	)
	if result.Mismatch != nil {
		metadataName := "response_mismatch"
		fields := logrus.Fields{"case": item.Name}
		if result.MismatchMetadata != "" {
			metadataName = result.MismatchMetadata
		}
		if item.Source.Interaction != "" {
			fields["interaction"] = item.Source.Interaction
		}
		if item.Source.Locator != "" {
			fields["source"] = item.Source.Locator
		}
		tagsAndMeta.SetMetadata(metadataName, result.Mismatch.Error())
		vu.state.Logger.WithFields(fields).Warnf("response mismatch: %v", result.Mismatch)
	}
	value := float64(1)
	if !result.Matched {
		value = 0
	}
	metrics.PushIfNotDone(vu.ctx, vu.state.Samples, metrics.ConnectedSamples{
		Tags: tagsAndMeta.Tags,
		Time: at,
		Samples: []metrics.Sample{
			newSampleWithMetadata(vu.state.BuiltinMetrics.Checks, tagsAndMeta, at, value),
		},
	})
	return result, nil
}

func responseSnapshot(response *httpext.Response) (*dsl.HTTPResponse, error) {
	if response == nil {
		return nil, nil
	}
	body, err := responseSnapshotBody(response.Body)
	if err != nil {
		return nil, err
	}
	result := &dsl.HTTPResponse{
		StatusCode: response.Status,
		Headers:    make(map[string]string, len(response.Headers)),
		Cookies:    make(map[string][]dsl.ResponseCookie, len(response.Cookies)),
		Body:       body,
	}
	maps.Copy(result.Headers, response.Headers)
	for name, values := range response.Cookies {
		result.Cookies[name] = make([]dsl.ResponseCookie, 0, len(values))
		for _, value := range values {
			if value != nil {
				result.Cookies[name] = append(result.Cookies[name], dsl.ResponseCookie{Name: value.Name, Value: value.Value})
			}
		}
	}
	return result, nil
}

func responseSnapshotBody(body any) ([]byte, error) {
	switch value := body.(type) {
	case nil:
		return nil, nil
	case string:
		return []byte(value), nil
	case []byte:
		return append([]byte(nil), value...), nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode HTTP response body: %w", err)
		}
		return encoded, nil
	}
}

func traceVerification(result dsl.MatchResult) k6oteltrace.VerificationResult {
	return k6oteltrace.VerificationResult{
		Passed:         result.Matched,
		Kind:           k6oteltrace.MismatchKind(result.Kind),
		ExpectedStatus: result.ExpectedStatus,
		ActualStatus:   result.ActualStatus,
		MismatchCount:  result.MismatchCount,
		Mismatch:       result.Mismatch,
	}
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
	return requestErr
}

func isExpectedResponse(status int) bool {
	return status >= http.StatusOK && status < http.StatusBadRequest
}

func joinRequestPath(basePath, requestPath string) string {
	if requestPath == "" {
		requestPath = "/"
	}
	if basePath == "" || basePath == "/" {
		if strings.HasPrefix(requestPath, "/") {
			return requestPath
		}
		return "/" + requestPath
	}
	return strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(requestPath, "/")
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
	sample := NewSample(metric, tagsAndMeta.Tags, at, value)
	sample.Metadata = tagsAndMeta.Metadata
	return sample
}

func NewSample(metric *metrics.Metric, tags *metrics.TagSet, at time.Time, value float64) metrics.Sample {
	return metrics.Sample{
		TimeSeries: metrics.TimeSeries{Metric: metric, Tags: tags},
		Time:       at,
		Value:      value,
	}
}
