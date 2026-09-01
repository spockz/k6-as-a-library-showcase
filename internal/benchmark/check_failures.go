// This file turns failed DSL response checks into deterministic benchmark execution errors.
package benchmark

import (
	"errors"
	"fmt"
	"slices"
	"sync"
)

type checkFailureTracker struct {
	mu       sync.Mutex
	failures map[string]int64
}

func newCheckFailureTracker() *checkFailureTracker {
	return &checkFailureTracker{failures: make(map[string]int64)}
}

func (tracker *checkFailureTracker) record(name string, passed bool) {
	if tracker == nil || passed {
		return
	}
	tracker.mu.Lock()
	tracker.failures[name]++
	tracker.mu.Unlock()
}

func (tracker *checkFailureTracker) err() error {
	if tracker == nil {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	names := make([]string, 0, len(tracker.failures))
	for name := range tracker.failures {
		names = append(names, name)
	}
	slices.Sort(names)
	var result error
	for _, name := range names {
		result = errors.Join(result, fmt.Errorf("check %q failed %d times", name, tracker.failures[name]))
	}
	return result
}
