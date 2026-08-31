// managed_output.go preserves output lifecycle errors across k6's non-returning manager shutdown callback.
package k6output

import (
	"errors"
	"fmt"
	"sync"

	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

type ManagedOutput struct {
	wrapped    output.Output
	stopper    output.WithStopWithTestError
	thresholds output.WithThresholds

	errMu sync.Mutex
	errs  []error
}

var _ output.Output = (*ManagedOutput)(nil)
var _ output.WithStopWithTestError = (*ManagedOutput)(nil)
var _ output.WithThresholds = (*ManagedOutput)(nil)

func NewManaged(wrapped output.Output) *ManagedOutput {
	managed := &ManagedOutput{wrapped: wrapped}
	if stopper, ok := wrapped.(output.WithStopWithTestError); ok {
		managed.stopper = stopper
	}
	if thresholds, ok := wrapped.(output.WithThresholds); ok {
		managed.thresholds = thresholds
	}
	return managed
}

func (o *ManagedOutput) Description() string {
	return o.wrapped.Description()
}

func (o *ManagedOutput) Start() error {
	err := o.wrapped.Start()
	o.recordError(err)
	return err
}

func (o *ManagedOutput) AddMetricSamples(samples []metrics.SampleContainer) {
	o.wrapped.AddMetricSamples(samples)
}

func (o *ManagedOutput) SetThresholds(thresholds map[string]metrics.Thresholds) {
	if o.thresholds != nil {
		o.thresholds.SetThresholds(thresholds)
	}
}

func (o *ManagedOutput) Stop() error {
	err := o.wrapped.Stop()
	o.recordError(err)
	return err
}

func (o *ManagedOutput) StopWithTestError(testRunErr error) error {
	var err error
	if o.stopper != nil {
		err = o.stopper.StopWithTestError(testRunErr)
	} else {
		err = o.wrapped.Stop()
	}
	o.recordError(err)
	return err
}

func (o *ManagedOutput) Err() error {
	o.errMu.Lock()
	defer o.errMu.Unlock()
	return errors.Join(o.errs...)
}

func (o *ManagedOutput) recordError(err error) {
	if err == nil {
		return
	}
	o.errMu.Lock()
	o.errs = append(o.errs, err)
	o.errMu.Unlock()
}

func Errors(outputs []*ManagedOutput) error {
	var result error
	for _, managed := range outputs {
		if err := managed.Err(); err != nil {
			result = errors.Join(result, fmt.Errorf("output %s: %w", managed.Description(), err))
		}
	}
	return result
}
