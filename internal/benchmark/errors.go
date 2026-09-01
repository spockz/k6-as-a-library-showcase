// This file isolates plan policies and diagnostics so composition and validation share typed context.
package benchmark

import (
	"fmt"

	"k6-as-a-library/internal/dsl"
)

// ConflictPolicy controls how ComposeWithOptions resolves duplicate
// definitions.
type ConflictPolicy string

const (
	ConflictReject         ConflictPolicy = "reject"
	ConflictPreferExisting ConflictPolicy = "prefer_existing"
	ConflictPreferIncoming ConflictPolicy = "prefer_incoming"
	ConflictPreferPriority ConflictPolicy = "prefer_higher_priority"
)

const (
	PrecedenceReject         = ConflictReject
	PrecedenceExisting       = ConflictPreferExisting
	PrecedenceIncoming       = ConflictPreferIncoming
	PrecedenceHigherPriority = ConflictPreferPriority
)

// ComposeOptions makes conflict behavior explicit. An empty policy rejects
// conflicts.
type ComposeOptions struct {
	ConflictPolicy ConflictPolicy
	Precedence     ConflictPolicy
}

// Capabilities describes the executor behavior available to a plan boundary.
type Capabilities struct {
	SharedIterations       bool
	Batch                  bool
	ConstantVUs            bool
	ArrivalRate            bool
	RoundRobinSelection    bool
	WeightedSelection      bool
	SegmentCheckActivation bool
	SegmentDefaults        bool
}

// DefaultCapabilities is the approved phase-one executor matrix.
func DefaultCapabilities() Capabilities {
	return Capabilities{
		SharedIterations:       true,
		Batch:                  true,
		RoundRobinSelection:    true,
		WeightedSelection:      true,
		SegmentCheckActivation: true,
		SegmentDefaults:        true,
	}
}

// CapabilityError reports a valid model feature that the selected executor
// cannot implement.
type CapabilityError struct {
	Diagnostic dsl.Diagnostic
	Capability string
	Message    string
}

func (e *CapabilityError) Error() string {
	if e == nil {
		return "<nil>"
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorCapability
	message := e.Message
	if message == "" {
		message = fmt.Sprintf("capability %q is not supported", e.Capability)
	}
	return (&dsl.ValidationError{Diagnostic: diagnostic, Message: message}).Error()
}

func (e *CapabilityError) Unwrap() error {
	if e == nil {
		return nil
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorCapability
	return &dsl.ValidationError{Diagnostic: diagnostic, Message: e.Message}
}

// ConflictError identifies two definitions that cannot be merged under the
// configured policy.
type ConflictError struct {
	Diagnostic dsl.Diagnostic
	Path       string
	Existing   dsl.Provenance
	Incoming   dsl.Provenance
	Policy     ConflictPolicy
}

func (e *ConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorConflict
	diagnostic.Field = e.Path
	message := fmt.Sprintf("definitions conflict under policy %q", e.Policy)
	if source := provenanceDescription(e.Existing); source != "" {
		message += "; existing source " + source
	}
	if source := provenanceDescription(e.Incoming); source != "" {
		message += "; incoming source " + source
	}
	return (&dsl.ValidationError{Diagnostic: diagnostic, Message: message}).Error()
}

func (e *ConflictError) Unwrap() error {
	if e == nil {
		return nil
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorConflict
	diagnostic.Field = e.Path
	return &dsl.ValidationError{Diagnostic: diagnostic, Message: "definitions conflict"}
}

// ReferenceError reports an unresolved case, operation, check, threshold, or
// segment reference.
type ReferenceError struct {
	Diagnostic dsl.Diagnostic
	Reference  string
}

func (e *ReferenceError) Error() string {
	if e == nil {
		return "<nil>"
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorReference
	message := fmt.Sprintf("reference %q was not found", e.Reference)
	return (&dsl.ValidationError{Diagnostic: diagnostic, Message: message}).Error()
}

func (e *ReferenceError) Unwrap() error {
	if e == nil {
		return nil
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorReference
	return &dsl.ValidationError{Diagnostic: diagnostic, Message: fmt.Sprintf("reference %q was not found", e.Reference)}
}

// CardinalityError reports a report dimension set that exceeds its declared
// series bound.
type CardinalityError struct {
	Diagnostic dsl.Diagnostic
	GroupBy    []string
	Actual     int
	Maximum    int
}

func (e *CardinalityError) Error() string {
	if e == nil {
		return "<nil>"
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorCardinality
	message := fmt.Sprintf("report grouping attributes %v produce cardinality %d, exceeding maximum %d", e.GroupBy, e.Actual, e.Maximum)
	return (&dsl.ValidationError{Diagnostic: diagnostic, Message: message}).Error()
}

func (e *CardinalityError) Unwrap() error {
	if e == nil {
		return nil
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorCardinality
	diagnostic.Field = "report.groupBy"
	return &dsl.ValidationError{Diagnostic: diagnostic, Message: "report cardinality exceeds its maximum"}
}

// SegmentError reports a timeline gap or overlap.
type SegmentError struct {
	Diagnostic dsl.Diagnostic
	Message    string
}

func (e *SegmentError) Error() string {
	if e == nil {
		return "<nil>"
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorSegment
	return (&dsl.ValidationError{Diagnostic: diagnostic, Message: e.Message}).Error()
}

func (e *SegmentError) Unwrap() error {
	if e == nil {
		return nil
	}
	diagnostic := e.Diagnostic
	diagnostic.Kind = dsl.ErrorSegment
	return &dsl.ValidationError{Diagnostic: diagnostic, Message: e.Message}
}

func provenanceDescription(source dsl.Provenance) string {
	if source.Locator != "" {
		return fmt.Sprintf("%q", source.Locator)
	}
	if source.Identifier != "" {
		return fmt.Sprintf("%q", source.Identifier)
	}
	if source.Kind != "" {
		return fmt.Sprintf("%q", source.Kind)
	}
	return ""
}
