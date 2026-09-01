package benchmark

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/sirupsen/logrus"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/executor"
	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/metrics"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/guregu/null.v3"
	k6oteltrace "k6-as-a-library/internal/otel"
)

const scenarioName = "native-go"

type EngineConfig struct {
	Logger                   *logrus.Logger
	TargetURL                string
	ExactTarget              bool
	RequestTimeout           time.Duration
	MinimumIterationDuration time.Duration
	MaximumDuration          time.Duration
	Benchmark                ValidatedBenchmark
	Samples                  chan<- metrics.SampleContainer
	TraceProvider            *k6oteltrace.Provider
	BenchmarkSpan            trace.Span
}

type Engine struct {
	options      lib.Options
	requirements []lib.ExecutionStep
	executor     lib.Executor
	samples      chan<- metrics.SampleContainer
}

type RunResult struct {
	Duration time.Duration
}

func NewEngine(ctx context.Context, config EngineConfig) (*Engine, error) {
	if config.Logger == nil {
		return nil, fmt.Errorf("create benchmark engine: logger is nil")
	}
	if config.Samples == nil {
		return nil, fmt.Errorf("create benchmark engine: samples channel is nil")
	}
	targetURL, err := httpext.NewURL(config.TargetURL, config.TargetURL)
	if err != nil {
		return nil, fmt.Errorf("create k6 target URL: %w", err)
	}
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	options := NewRunnerOptions(config.MinimumIterationDuration)
	if err := InitializeThresholds(registry, builtin, &options, config.Benchmark); err != nil {
		return nil, err
	}
	if err := InitializeSummarySubmetrics(builtin, options); err != nil {
		return nil, err
	}
	dnsTTL, err := types.ParseExtendedDuration(options.DNS.TTL.String)
	if err != nil {
		return nil, fmt.Errorf("parse default DNS TTL: %w", err)
	}
	resolver := netext.NewResolver(net.LookupIP, dnsTTL, options.DNS.Select.DNSSelect, options.DNS.Policy.DNSPolicy)
	testStatus := lib.NewTestStatus()
	runTags := registry.RootTagSet()

	tuple, err := lib.NewExecutionTuple(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create execution tuple: %w", err)
	}
	executorConfig := executor.NewSharedIterationsConfig(scenarioName)
	baseline := config.Benchmark.Benchmark().Baseline
	executorConfig.VUs = null.IntFrom(baseline.VUs)
	executorConfig.Iterations = null.IntFrom(baseline.Iterations)
	executorConfig.MaxDuration = types.NullDurationFrom(config.MaximumDuration)
	requirements := executorConfig.GetExecutionRequirements(tuple)
	options.Scenarios = lib.ScenarioConfigs{scenarioName: executorConfig}

	runner, err := NewRunner(RunnerConfig{
		Logger:         config.Logger,
		Options:        options,
		Resolver:       resolver,
		BufferPool:     lib.NewBufferPool(),
		BuiltinMetrics: builtin,
		TestStatus:     testStatus,
		RunTags:        runTags,
		TargetURL:      targetURL,
		ExactTarget:    config.ExactTarget,
		RequestTimeout: config.RequestTimeout,
		Benchmark:      config.Benchmark,
		TraceProvider:  config.TraceProvider,
		BenchmarkSpan:  config.BenchmarkSpan,
	})
	if err != nil {
		return nil, err
	}
	test := &lib.TestRunState{
		TestPreInitState: &lib.TestPreInitState{
			Logger: config.Logger, Registry: registry, BuiltinMetrics: builtin, TestStatus: testStatus,
		},
		Options: options,
		Runner:  runner,
		RunTags: runTags,
	}
	state := lib.NewExecutionState(test, tuple, lib.GetMaxPlannedVUs(requirements), lib.GetMaxPossibleVUs(requirements))
	state.SetInitVUFunc(func(initCtx context.Context, _ *logrus.Entry) (lib.InitializedVU, error) {
		localID, globalID := state.GetUniqueVUIdentifiers()
		return runner.NewVU(initCtx, localID, globalID, config.Samples)
	})
	for range lib.GetMaxPlannedVUs(requirements) {
		localID, globalID := state.GetUniqueVUIdentifiers()
		vu, err := runner.NewVU(ctx, localID, globalID, config.Samples)
		if err != nil {
			return nil, fmt.Errorf("initialize VU: %w", err)
		}
		state.AddInitializedVU(vu)
	}
	k6Executor, err := executorConfig.NewExecutor(state, logrus.NewEntry(config.Logger))
	if err != nil {
		return nil, fmt.Errorf("create k6 executor: %w", err)
	}
	if err := k6Executor.Init(ctx); err != nil {
		return nil, fmt.Errorf("initialize k6 executor: %w", err)
	}
	return &Engine{options: options, requirements: requirements, executor: k6Executor, samples: config.Samples}, nil
}

func (engine *Engine) Options() lib.Options {
	return engine.options
}

func (engine *Engine) ExecutionPlan() []lib.ExecutionStep {
	return append([]lib.ExecutionStep(nil), engine.requirements...)
}

func (engine *Engine) Run(ctx context.Context) (RunResult, error) {
	if engine == nil || engine.executor == nil {
		return RunResult{}, fmt.Errorf("run benchmark: engine is not initialized")
	}
	startedAt := time.Now()
	err := engine.executor.Run(ctx, engine.samples)
	return RunResult{Duration: time.Since(startedAt)}, err
}
