// Package agreement adapts SLA agreement YAML into source-neutral DSL load requirements.
package agreement

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"k6-as-a-library/internal/dsl"

	"gopkg.in/yaml.v3"
)

type document struct {
	Agreements []agreement `yaml:"agreements"`
}
type agreement struct {
	Consumer string `yaml:"consumer"`
	Provider string `yaml:"provider"`
	SLO      []slo  `yaml:"slo"`
}
type slo struct {
	Endpoint        endpoint       `yaml:"endpoint"`
	LoadConstraints []constraint   `yaml:"loadConstraints"`
	ResponseTimes   []responseTime `yaml:"responseTimes"`
}
type endpoint struct {
	Host         string `yaml:"host"`
	Method       string `yaml:"method"`
	PathTemplate string `yaml:"pathTemplate"`
}
type constraint struct {
	Amount            int64              `yaml:"amount"`
	PerTimeUnit       string             `yaml:"per-time-unit"`
	PermittedFailures []permittedFailure `yaml:"permittedFailures"`
}
type permittedFailure struct {
	Category    string `yaml:"category"`
	Amount      int64  `yaml:"amount"`
	StatusCodes []any  `yaml:"statusCodes"`
}
type responseTime struct {
	StatusCode              any `yaml:"statusCode"`
	Mean, Median, P99, P100 string
}

type Result struct {
	Requirements []dsl.LoadEnvelope
}

func Load(filename string, cases []dsl.Case) (Result, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return Result{}, fmt.Errorf("read agreements %q: %w", filename, err)
	}
	return Decode(bytes.NewReader(contents), filename, cases)
}

func Decode(reader io.Reader, source string, cases []dsl.Case) (Result, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var input document
	if err := decoder.Decode(&input); err != nil {
		return Result{}, fmt.Errorf("decode agreements %q: %w", source, err)
	}
	if len(input.Agreements) == 0 {
		return Result{}, fmt.Errorf("decode agreements %q: no agreements", source)
	}
	var result Result
	for agreementIndex, item := range input.Agreements {
		for sloIndex, objective := range item.SLO {
			caseIDs, err := matchingCases(objective.Endpoint, cases)
			if err != nil {
				return Result{}, fmt.Errorf("agreement %d SLO %d: %w", agreementIndex, sloIndex, err)
			}
			envelopeID := fmt.Sprintf("agreement-%d-slo-%d", agreementIndex+1, sloIndex+1)
			envelope := dsl.LoadEnvelope{ID: envelopeID, Scope: dsl.Selector{CaseIDs: caseIDs}, Source: dsl.Provenance{Kind: "sla_agreement", Identifier: item.Consumer + "->" + item.Provider, Locator: source}}
			for constraintIndex, limit := range objective.LoadConstraints {
				window, err := parseTimeUnit(limit.PerTimeUnit)
				if err != nil {
					return Result{}, fmt.Errorf("agreement %d SLO %d constraint %d: %w", agreementIndex, sloIndex, constraintIndex, err)
				}
				constraintID := fmt.Sprintf("%s-limit-%d", envelopeID, constraintIndex+1)
				adapted := dsl.LoadConstraint{ID: constraintID, Amount: limit.Amount, Window: dsl.Duration(window.String()), WindowKind: dsl.LoadWindowRolling, Unit: dsl.LoadUnitOperationStart}
				for failureIndex, failure := range limit.PermittedFailures {
					statusCodes := make([]string, len(failure.StatusCodes))
					for statusIndex, statusCode := range failure.StatusCodes {
						statusCodes[statusIndex] = fmt.Sprint(statusCode)
					}
					adapted.PermittedFailures = append(adapted.PermittedFailures, dsl.PermittedFailure{
						ID: fmt.Sprintf("%s-failure-%d", constraintID, failureIndex+1), Category: dsl.FailureCategory(failure.Category),
						Amount: failure.Amount, StatusCodes: statusCodes,
					})
				}
				envelope.Constraints = append(envelope.Constraints, adapted)
			}
			if len(envelope.Constraints) == 0 {
				return Result{}, fmt.Errorf("agreement %d SLO %d: no load constraints", agreementIndex, sloIndex)
			}
			for _, timing := range objective.ResponseTimes {
				if timing.StatusCode == nil || strings.TrimSpace(fmt.Sprint(timing.StatusCode)) == "" {
					return Result{}, fmt.Errorf("agreement %d SLO %d: response-time status code is required", agreementIndex, sloIndex)
				}
				envelope.ResponseTimes = append(envelope.ResponseTimes, dsl.ResponseTimeObjective{
					StatusCode: fmt.Sprint(timing.StatusCode), Mean: dsl.Duration(timing.Mean), Median: dsl.Duration(timing.Median), P99: dsl.Duration(timing.P99), P100: dsl.Duration(timing.P100),
				})
			}
			result.Requirements = append(result.Requirements, envelope)
		}
	}
	return result, nil
}

func matchingCases(target endpoint, cases []dsl.Case) ([]string, error) {
	pattern := regexp.QuoteMeta(target.PathTemplate)
	pattern = regexp.MustCompile(`\\\{[^/]+\\\}`).ReplaceAllString(pattern, `[^/]+`)
	matcher, err := regexp.Compile("^" + pattern + "$")
	if err != nil {
		return nil, fmt.Errorf("compile path template %q: %w", target.PathTemplate, err)
	}
	var ids []string
	for _, item := range cases {
		if strings.EqualFold(item.Operation.Method, target.Method) && matcher.MatchString(item.Operation.Path) {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("endpoint %s %s matches no benchmark case", target.Method, target.PathTemplate)
	}
	return ids, nil
}

func parseTimeUnit(value string) (time.Duration, error) {
	switch value {
	case "nanosecond", "nanoseconds", "ns":
		return time.Nanosecond, nil
	case "microsecond", "microseconds", "us", "µs":
		return time.Microsecond, nil
	case "millisecond", "milliseconds", "ms":
		return time.Millisecond, nil
	case "second", "seconds", "s":
		return time.Second, nil
	case "minute", "minutes":
		return time.Minute, nil
	case "hour", "hours":
		return time.Hour, nil
	case "day", "days":
		return 24 * time.Hour, nil
	default:
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return 0, fmt.Errorf("parse per-time-unit %q: %w", value, err)
		}
		return parsed, nil
	}
}
