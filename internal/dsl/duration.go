// This file isolates duration values because their model string form needs explicit Go conversion.
package dsl

import (
	"encoding/json"
	"fmt"
	"time"
)

// NewDuration creates the canonical JSON duration representation for a Go
// duration.
func NewDuration(value time.Duration) Duration {
	return Duration(value.String())
}

// Parse converts a model duration into a Go duration.
func (d Duration) Parse() (time.Duration, error) {
	if d == "" {
		return 0, fmt.Errorf("duration is empty")
	}
	value, err := time.ParseDuration(string(d))
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", d, err)
	}
	return value, nil
}

// GoDuration is an alias for Parse with a name that makes call sites clear.
func (d Duration) GoDuration() (time.Duration, error) {
	return d.Parse()
}

func (d Duration) String() string {
	return string(d)
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(d))
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("decode duration into nil receiver")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	*d = Duration(value)
	return nil
}
