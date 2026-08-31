// This file isolates diagnostics so validation can carry stable context across package boundaries.
package dsl

import (
	"fmt"
	"strings"
)

// ErrorKind identifies the class of a model diagnostic.
type ErrorKind string

const (
	ErrorInvalid     ErrorKind = "invalid"
	ErrorDuplicate   ErrorKind = "duplicate"
	ErrorReference   ErrorKind = "reference"
	ErrorConflict    ErrorKind = "conflict"
	ErrorCapability  ErrorKind = "capability"
	ErrorCardinality ErrorKind = "cardinality"
	ErrorSegment     ErrorKind = "segment"
)

// Diagnostic carries stable context for a plan error.
type Diagnostic struct {
	Kind        ErrorKind
	PlanID      string
	CaseID      string
	OperationID string
	SegmentID   string
	CheckID     string
	ThresholdID string
	Field       string
	Source      string
}

// ValidationError is one context-rich model diagnostic.
type ValidationError struct {
	Diagnostic
	Message string
	Cause   error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	parts := make([]string, 0, 8)
	if e.PlanID != "" {
		parts = append(parts, fmt.Sprintf("plan %q", e.PlanID))
	}
	if e.CaseID != "" {
		parts = append(parts, fmt.Sprintf("case %q", e.CaseID))
	}
	if e.OperationID != "" {
		parts = append(parts, fmt.Sprintf("operation %q", e.OperationID))
	}
	if e.SegmentID != "" {
		parts = append(parts, fmt.Sprintf("segment %q", e.SegmentID))
	}
	if e.CheckID != "" {
		parts = append(parts, fmt.Sprintf("check %q", e.CheckID))
	}
	if e.ThresholdID != "" {
		parts = append(parts, fmt.Sprintf("threshold %q", e.ThresholdID))
	}
	if e.Field != "" {
		parts = append(parts, "field "+e.Field)
	}
	context := strings.Join(parts, ", ")
	if context != "" {
		context += ": "
	}
	kind := string(e.Kind)
	if kind == "" {
		kind = string(ErrorInvalid)
	}
	message := e.Message
	if message == "" && e.Cause != nil {
		message = e.Cause.Error()
	}
	if message == "" {
		message = "validation failed"
	}
	result := context + kind + ": " + message
	if e.Source != "" {
		result += fmt.Sprintf(" (source %q)", e.Source)
	}
	return result
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ValidationErrors preserves every deterministic diagnostic produced by a
// validation pass while remaining compatible with errors.As and errors.Is.
type ValidationErrors struct {
	Problems []*ValidationError
}

func (e *ValidationErrors) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "validation failed"
	}
	messages := make([]string, 0, len(e.Problems))
	for _, problem := range e.Problems {
		if problem != nil {
			messages = append(messages, problem.Error())
		}
	}
	if len(messages) == 0 {
		return "validation failed"
	}
	return strings.Join(messages, "; ")
}

func (e *ValidationErrors) Unwrap() []error {
	if e == nil {
		return nil
	}
	errs := make([]error, 0, len(e.Problems))
	for _, problem := range e.Problems {
		if problem != nil {
			errs = append(errs, problem)
		}
	}
	return errs
}

// NewValidationError creates a single diagnostic with optional source
// context.
func NewValidationError(diagnostic Diagnostic, message string, cause error) *ValidationError {
	return &ValidationError{Diagnostic: diagnostic, Message: message, Cause: cause}
}

// JoinValidationErrors returns nil for no diagnostics and otherwise keeps all
// supplied diagnostics available through errors.As.
func JoinValidationErrors(problems ...*ValidationError) error {
	filtered := make([]*ValidationError, 0, len(problems))
	for _, problem := range problems {
		if problem != nil {
			filtered = append(filtered, problem)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return &ValidationErrors{Problems: filtered}
}

func isNullJSON(data []byte) bool {
	return strings.TrimSpace(string(data)) == "null"
}

func wrapDecodeError(field string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("decode %s: %w", field, err)
}
