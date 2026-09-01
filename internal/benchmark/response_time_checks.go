// This file maps status-specific DSL p100 objectives to k6 checks without treating aggregate percentiles as per-response predicates.
package benchmark

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"k6-as-a-library/internal/dsl"

	"go.k6.io/k6/lib/netext/httpext"
)

type responseTimeBinding struct {
	name       string
	statusCode string
	maximum    time.Duration
}

type responseTimeBreach struct {
	name     string
	maximum  time.Duration
	observed time.Duration
}

type responseTimeTracker struct {
	mu       sync.Mutex
	byCase   map[string][]responseTimeBinding
	breaches map[string]responseTimeBreach
}

func newResponseTimeTracker(model dsl.SynthesizedBenchmark) (*responseTimeTracker, error) {
	tracker := &responseTimeTracker{byCase: make(map[string][]responseTimeBinding), breaches: make(map[string]responseTimeBreach)}
	for _, envelope := range model.LoadRequirements {
		for index, objective := range envelope.ResponseTimes {
			if objective.P100 == "" {
				continue
			}
			maximum, err := objective.P100.Parse()
			if err != nil {
				return nil, fmt.Errorf("compile response-time objective for envelope %q: %w", envelope.ID, err)
			}
			binding := responseTimeBinding{
				name:       fmt.Sprintf("response time p100: %s/%d/%s", envelope.ID, index+1, objective.StatusCode),
				statusCode: objective.StatusCode,
				maximum:    maximum,
			}
			for _, item := range model.Cases {
				if selectorMatchesCase(envelope.Scope, item) {
					tracker.byCase[item.ID] = append(tracker.byCase[item.ID], binding)
				}
			}
		}
	}
	return tracker, nil
}

func (tracker *responseTimeTracker) evaluate(caseID string, response *httpext.Response) []runtimeCheckOutcome {
	if tracker == nil || response == nil {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	duration := time.Duration(math.Round(response.Timings.Duration * float64(time.Millisecond)))
	bindings := tracker.byCase[caseID]
	outcomes := make([]runtimeCheckOutcome, 0, len(bindings))
	for _, binding := range bindings {
		if !matchesStatusCode(binding.statusCode, response.Status) {
			continue
		}
		passed := duration <= binding.maximum
		if !passed {
			if _, exists := tracker.breaches[binding.name]; !exists {
				tracker.breaches[binding.name] = responseTimeBreach{name: binding.name, maximum: binding.maximum, observed: duration}
			}
		}
		outcomes = append(outcomes, runtimeCheckOutcome{name: binding.name, passed: passed})
	}
	return outcomes
}

func (tracker *responseTimeTracker) err() error {
	if tracker == nil {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	names := make([]string, 0, len(tracker.breaches))
	for name := range tracker.breaches {
		names = append(names, name)
	}
	slices.Sort(names)
	var result error
	for _, name := range names {
		breach := tracker.breaches[name]
		result = errors.Join(result, fmt.Errorf("%s exceeded: observed %s, permitted %s", breach.name, breach.observed, breach.maximum))
	}
	return result
}
