package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.k6.io/k6/lib/netext"
	"go.k6.io/k6/metrics"
)

func TestNativeVUMetricsPopulateDashboardPanels(t *testing.T) {
	harness := newNativeVUTestHarness(context.Background(), 0)
	harness.vu.runner.dialer.BytesRead = 4096
	harness.vu.runner.dialer.BytesWritten = 512

	if err := harness.vu.RunOnce(); err != nil {
		t.Fatalf("run native iteration: %v", err)
	}

	emitted := make(map[string]metrics.Sample)
	for len(harness.out) > 0 {
		for _, sample := range (<-harness.out).GetSamples() {
			emitted[sample.Metric.Name] = sample
		}
	}
	for _, name := range []string{
		metrics.HTTPReqsName,
		metrics.HTTPReqDurationName,
		metrics.HTTPReqBlockedName,
		metrics.HTTPReqConnectingName,
		metrics.HTTPReqTLSHandshakingName,
		metrics.HTTPReqSendingName,
		metrics.HTTPReqWaitingName,
		metrics.HTTPReqReceivingName,
		metrics.HTTPReqFailedName,
		metrics.DataSentName,
		metrics.DataReceivedName,
		metrics.IterationsName,
		metrics.IterationDurationName,
	} {
		if _, ok := emitted[name]; !ok {
			t.Errorf("metric %s was not emitted", name)
		}
	}
	if actual := emitted[metrics.DataSentName].Value; actual != 512 {
		t.Errorf("expected 512 sent bytes, got %.0f", actual)
	}
	if actual := emitted[metrics.DataReceivedName].Value; actual != 4096 {
		t.Errorf("expected 4096 received bytes, got %.0f", actual)
	}
	if actual := emitted[metrics.IterationsName].Value; actual != 1 {
		t.Errorf("expected one iteration, got %.0f", actual)
	}
	if actual := emitted[metrics.IterationDurationName].Value; actual <= 0 {
		t.Errorf("expected a positive iteration duration, got %f", actual)
	}
}

func TestNativeVUMinIterationDurationEmitsMetricsBeforeCancellableWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	harness := newNativeVUTestHarness(ctx, time.Minute)
	done := make(chan error, 1)
	go func() {
		done <- harness.vu.RunOnce()
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	var durationSample metrics.Sample
	foundDuration := false
	for !foundDuration {
		select {
		case container := <-harness.out:
			for _, sample := range container.GetSamples() {
				if sample.Metric.Name == metrics.IterationDurationName {
					durationSample = sample
					foundDuration = true
					break
				}
			}
		case err := <-done:
			t.Fatalf("iteration returned before minimum-duration pacing: %v", err)
		case <-deadline.C:
			t.Fatal("iteration metrics were not emitted before pacing")
		}
	}

	if durationSample.Value <= 0 || durationSample.Value >= metrics.D(time.Minute) {
		t.Fatalf("iteration duration includes pacing: %f", durationSample.Value)
	}
	select {
	case err := <-done:
		t.Fatalf("iteration returned before cancellation: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancel minimum-duration pacing: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("minimum-duration pacing did not stop after cancellation")
	}
}

func TestRemainingIterationDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		minimum  time.Duration
		elapsed  time.Duration
		expected time.Duration
	}{
		{name: "iteration is shorter", minimum: 100 * time.Millisecond, elapsed: 40 * time.Millisecond, expected: 60 * time.Millisecond},
		{name: "iteration equals minimum", minimum: 100 * time.Millisecond, elapsed: 100 * time.Millisecond, expected: 0},
		{name: "iteration exceeds minimum", minimum: 100 * time.Millisecond, elapsed: 150 * time.Millisecond, expected: 0},
		{name: "minimum is disabled", minimum: 0, elapsed: 40 * time.Millisecond, expected: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual := remainingIterationDuration(test.minimum, test.elapsed)
			if actual != test.expected {
				t.Fatalf("expected %s remaining, got %s", test.expected, actual)
			}
		})
	}
}

type nativeVUTestHarness struct {
	vu  *activeNativeVU
	out chan metrics.SampleContainer
}

func newNativeVUTestHarness(ctx context.Context, minIterationDuration time.Duration) nativeVUTestHarness {
	registry := metrics.NewRegistry()
	builtin := metrics.RegisterBuiltinMetrics(registry)
	runTags := registry.RootTagSet()
	out := make(chan metrics.SampleContainer, 2)
	runner := &nativeRunner{
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				Status:     "200 OK",
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("response body")),
			}, nil
		})},
		dialer:               &netext.Dialer{},
		builtin:              builtin,
		runTags:              runTags,
		requestTags:          runTags.With("url", "http://localhost:8080/headers"),
		targetURL:            "http://localhost:8080/headers",
		minIterationDuration: minIterationDuration,
	}
	return nativeVUTestHarness{
		vu: &activeNativeVU{
			nativeVU: &nativeVU{id: 1, runner: runner, out: out},
			ctx:      ctx,
			busy:     make(chan struct{}, 1),
		},
		out: out,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
