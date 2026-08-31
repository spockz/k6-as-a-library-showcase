// managed_output_test.go verifies error visibility for both ordinary and test-aware output shutdown paths.
package k6output

import (
	"errors"
	"testing"

	"go.k6.io/k6/metrics"
)

type managedOutputTestBase struct {
	startErr error
	stopErr  error
}

func (o *managedOutputTestBase) Description() string {
	return "managed-output test"
}

func (o *managedOutputTestBase) Start() error {
	return o.startErr
}

func (o *managedOutputTestBase) AddMetricSamples([]metrics.SampleContainer) {}

func (o *managedOutputTestBase) Stop() error {
	return o.stopErr
}

type managedOutputTestWithShutdown struct {
	managedOutputTestBase
	shutdownErr error
}

func (o *managedOutputTestWithShutdown) StopWithTestError(error) error {
	return o.shutdownErr
}

func TestManagedOutputRecordsEveryLifecycleError(t *testing.T) {
	t.Parallel()

	startErr := errors.New("start failed")
	stopErr := errors.New("stop failed")
	shutdownErr := errors.New("shutdown failed")
	managed := NewManaged(&managedOutputTestWithShutdown{
		managedOutputTestBase: managedOutputTestBase{startErr: startErr, stopErr: stopErr},
		shutdownErr:           shutdownErr,
	})
	if err := managed.Start(); !errors.Is(err, startErr) {
		t.Fatalf("start error: expected %v, got %v", startErr, err)
	}
	if err := managed.Stop(); !errors.Is(err, stopErr) {
		t.Fatalf("stop error: expected %v, got %v", stopErr, err)
	}
	if err := managed.StopWithTestError(errors.New("test run failed")); !errors.Is(err, shutdownErr) {
		t.Fatalf("shutdown error: expected %v, got %v", shutdownErr, err)
	}
	for _, expected := range []error{startErr, stopErr, shutdownErr} {
		if err := managed.Err(); !errors.Is(err, expected) {
			t.Errorf("stored error does not contain %v: %v", expected, err)
		}
	}
}

func TestManagedOutputUsesStopForOutputsWithoutTestAwareShutdown(t *testing.T) {
	t.Parallel()

	startErr := errors.New("start failed")
	stopErr := errors.New("stop failed")
	underlying := &managedOutputTestBase{startErr: startErr, stopErr: stopErr}
	managed := NewManaged(underlying)
	if managed.Description() != underlying.Description() {
		t.Fatalf("description: expected %q, got %q", underlying.Description(), managed.Description())
	}
	if err := managed.Start(); !errors.Is(err, startErr) {
		t.Fatalf("start error: expected %v, got %v", startErr, err)
	}
	if err := managed.StopWithTestError(nil); !errors.Is(err, stopErr) {
		t.Fatalf("test-aware stop error: expected %v, got %v", stopErr, err)
	}
	if err := managed.Err(); !errors.Is(err, startErr) || !errors.Is(err, stopErr) {
		t.Fatalf("stored errors: expected start and stop failures, got %v", err)
	}
}

func TestManagedOutputHasNoErrorBeforeLifecycleFailure(t *testing.T) {
	t.Parallel()

	managed := NewManaged(&managedOutputTestBase{})
	if err := managed.Err(); err != nil {
		t.Fatalf("unexpected initial managed output error: %v", err)
	}
}
