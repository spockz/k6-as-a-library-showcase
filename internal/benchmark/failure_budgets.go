// This file keeps exact rolling DSL failure accounting beside the k6 check adapter that exposes breaches.
package benchmark

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"k6-as-a-library/internal/dsl"
)

type failureBudgetBinding struct {
	name    string
	window  time.Duration
	failure dsl.PermittedFailure
}

type observedResponse struct {
	status       int
	requestErr   error
	verification *dsl.MatchResult
}

type runtimeCheckOutcome struct {
	name   string
	passed bool
}

type failureBudgetBreach struct {
	name    string
	allowed int64
	actual  int
	window  time.Duration
}

type failureBudgetTracker struct {
	mu       sync.Mutex
	byCase   map[string][]failureBudgetBinding
	history  map[string][]time.Time
	breaches map[string]failureBudgetBreach
}

func newFailureBudgetTracker(model dsl.SynthesizedBenchmark) (*failureBudgetTracker, error) {
	tracker := &failureBudgetTracker{
		byCase: make(map[string][]failureBudgetBinding), history: make(map[string][]time.Time), breaches: make(map[string]failureBudgetBreach),
	}
	for _, envelope := range model.LoadRequirements {
		for _, constraint := range envelope.Constraints {
			window, err := constraint.Window.Parse()
			if err != nil {
				return nil, fmt.Errorf("compile failure budgets for constraint %q: %w", constraint.ID, err)
			}
			for _, failure := range constraint.PermittedFailures {
				binding := failureBudgetBinding{name: failureBudgetCheckName(envelope.ID, constraint.ID, failure.ID), window: window, failure: failure}
				for _, item := range model.Cases {
					if selectorMatchesCase(envelope.Scope, item) {
						tracker.byCase[item.ID] = append(tracker.byCase[item.ID], binding)
					}
				}
			}
		}
	}
	return tracker, nil
}

func failureBudgetCheckName(envelopeID, constraintID, failureID string) string {
	return fmt.Sprintf("failure budget: %s/%s/%s", envelopeID, constraintID, failureID)
}

func (tracker *failureBudgetTracker) evaluate(caseID string, observed observedResponse) []runtimeCheckOutcome {
	return tracker.evaluateAt(caseID, observed, time.Now())
}

func (tracker *failureBudgetTracker) evaluateAt(caseID string, observed observedResponse, at time.Time) []runtimeCheckOutcome {
	if tracker == nil {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	bindings := tracker.byCase[caseID]
	outcomes := make([]runtimeCheckOutcome, 0, len(bindings))
	for _, binding := range bindings {
		passed := true
		if matchesPermittedFailure(binding.failure, observed) {
			history := tracker.history[binding.name]
			first := 0
			for first < len(history) && !history[first].After(at.Add(-binding.window)) {
				first++
			}
			history = append(slices.Clone(history[first:]), at)
			tracker.history[binding.name] = history
			passed = int64(len(history)) <= binding.failure.Amount
			if !passed {
				if _, exists := tracker.breaches[binding.name]; !exists {
					tracker.breaches[binding.name] = failureBudgetBreach{
						name: binding.name, allowed: binding.failure.Amount, actual: len(history), window: binding.window,
					}
				}
			}
		}
		outcomes = append(outcomes, runtimeCheckOutcome{name: binding.name, passed: passed})
	}
	return outcomes
}

func (tracker *failureBudgetTracker) err() error {
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
		result = errors.Join(result, fmt.Errorf("%s exceeded: observed %d failures, permitted %d in rolling %s", breach.name, breach.actual, breach.allowed, breach.window))
	}
	return result
}

func matchesPermittedFailure(failure dsl.PermittedFailure, observed observedResponse) bool {
	switch failure.Category {
	case dsl.FailureCategoryTransport:
		return observed.requestErr != nil || observed.status == http.StatusGatewayTimeout
	case dsl.FailureCategoryHTTP5xx:
		return observed.status >= http.StatusInternalServerError && observed.status <= 599 && observed.status != http.StatusGatewayTimeout
	case dsl.FailureCategoryFunctional:
		if observed.status >= http.StatusInternalServerError {
			return false
		}
		for _, statusCode := range failure.StatusCodes {
			if matchesStatusCode(statusCode, observed.status) {
				return true
			}
		}
		return observed.verification != nil && !observed.verification.Matched && observed.verification.Kind != dsl.MatchTransport
	default:
		return false
	}
}

func matchesStatusCode(pattern string, status int) bool {
	if len(pattern) == 3 && pattern[1:] == "xx" {
		return status/100 == int(pattern[0]-'0')
	}
	return pattern == fmt.Sprint(status)
}
