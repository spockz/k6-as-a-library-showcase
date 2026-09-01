package benchmark

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"k6-as-a-library/internal/dsl"
	k6oteltrace "k6-as-a-library/internal/otel"

	"github.com/sirupsen/logrus"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/executor"
	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/lib/netext/httpext"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/metrics"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/guregu/null.v3"
)

type EngineConfig struct {
	Logger         *logrus.Logger
	TargetURL      string
	ExactTarget    bool
	RequestTimeout time.Duration
	Benchmark      ValidatedBenchmark
	Samples        chan<- metrics.SampleContainer
	TraceProvider  *k6oteltrace.Provider
	BenchmarkSpan  trace.Span
}

type Engine struct {
	options        lib.Options
	requirements   []lib.ExecutionStep
	executors      []scheduledExecutor
	samples        chan<- metrics.SampleContainer
	runner         *Runner
	state          *lib.ExecutionState
	expectedStarts int64
}

type scheduledExecutor struct {
	start    time.Duration
	executor lib.Executor
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
	options := NewRunnerOptions()
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
	scenarios := make(lib.ScenarioConfigs, len(config.Benchmark.Benchmark().LoadPlan.Phases))
	for _, phase := range config.Benchmark.Benchmark().LoadPlan.Phases {
		if phase.Load.Kind != dsl.PlannedLoadSharedIterations && phase.Load.Kind != dsl.PlannedLoadBatch {
			return nil, fmt.Errorf("create k6 executor for phase %q: unsupported planned load kind %q", phase.ID, phase.Load.Kind)
		}
		start, err := phase.Start.Parse()
		if err != nil {
			return nil, fmt.Errorf("parse phase %q start: %w", phase.ID, err)
		}
		maximum, err := phase.MaxDuration.Parse()
		if err != nil {
			return nil, fmt.Errorf("parse phase %q max duration: %w", phase.ID, err)
		}
		executorConfig := executor.NewSharedIterationsConfig(phase.ID)
		executorConfig.StartTime = types.NullDurationFrom(start)
		executorConfig.VUs = null.IntFrom(phase.Load.VUs)
		executorConfig.Iterations = null.IntFrom(phase.Load.Iterations)
		executorConfig.MaxDuration = types.NullDurationFrom(maximum)
		executorConfig.Tags = map[string]string{"load.phase": phase.ID}
		scenarios[phase.ID] = executorConfig
	}
	requirements := scenarios.GetFullExecutionRequirements(tuple)
	options.Scenarios = scenarios

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
	scheduled := make([]scheduledExecutor, 0, len(scenarios))
	for _, executorConfig := range scenarios.GetSortedConfigs() {
		k6Executor, err := executorConfig.NewExecutor(state, logrus.NewEntry(config.Logger))
		if err != nil {
			return nil, fmt.Errorf("create k6 executor %q: %w", executorConfig.GetName(), err)
		}
		if err := k6Executor.Init(ctx); err != nil {
			return nil, fmt.Errorf("initialize k6 executor %q: %w", executorConfig.GetName(), err)
		}
		scheduled = append(scheduled, scheduledExecutor{start: executorConfig.GetStartTime(), executor: k6Executor})
	}
	return &Engine{options: options, requirements: requirements, executors: scheduled, samples: config.Samples, runner: runner, state: state, expectedStarts: config.Benchmark.Benchmark().LoadPlan.ExpectedStarts}, nil
}

func (engine *Engine) Options() lib.Options {
	return engine.options
}

func (engine *Engine) ExecutionPlan() []lib.ExecutionStep {
	return append([]lib.ExecutionStep(nil), engine.requirements...)
}

func (engine *Engine) Run(ctx context.Context) (RunResult, error) {
	if engine == nil || len(engine.executors) == 0 {
		return RunResult{}, fmt.Errorf("run benchmark: engine is not initialized")
	}
	startedAt := time.Now()
	engine.state.MarkStarted()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wait sync.WaitGroup
	errorsOut := make(chan error, len(engine.executors))
	for _, item := range engine.executors {
		wait.Go(func() {
			timer := time.NewTimer(item.start)
			defer timer.Stop()
			select {
			case <-runCtx.Done():
				return
			case <-timer.C:
			}
			if err := item.executor.Run(runCtx, engine.samples); err != nil {
				errorsOut <- err
				cancel()
			}
		})
	}
	wait.Wait()
	close(errorsOut)
	var runErr error
	for err := range errorsOut {
		runErr = errors.Join(runErr, err)
	}
	actual := int64(engine.runner.nextCaseOrdinal.Load())
	if runErr == nil && actual != engine.expectedStarts {
		runErr = fmt.Errorf("execute load plan: completed %d of %d planned operation starts", actual, engine.expectedStarts)
	}
	return RunResult{Duration: time.Since(startedAt)}, runErr
}
