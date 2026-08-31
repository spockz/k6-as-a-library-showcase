// Package dsl keeps target-independent model vocabulary at the generator/executor boundary.
package dsl

const (
	// CurrentSchemaVersion is the only benchmark manifest schema accepted by this version.
	CurrentSchemaVersion = 1

	// Built-in report dimensions retained for compatibility with the current
	// contract workload.
	ReportDimensionConsumerService = "consumer_service"
	ReportDimensionProviderService = "provider_service"
	ReportDimensionEndpoint        = "endpoint"
	ReportDimensionInteraction     = "pact_interaction"
	ReportDimensionProviderState   = "provider_state"
	ReportDimensionName            = "name"

	// Stable dimensions that may be explicitly enabled by a benchmark.
	ReportDimensionCaseID      = "case_id"
	ReportDimensionOperationID = "operation_id"
	ReportDimensionSegmentID   = "segment_id"
	ReportDimensionMethod      = "method"
	ReportDimensionPath        = "path"
)

// Presence records whether a JSON field was absent, explicitly null, or held
// a value. It is used only for fields where those states affect execution.
type Presence uint8

const (
	PresenceAbsent Presence = iota
	PresenceNull
	PresenceValue
)

const (
	Absent  = PresenceAbsent
	Null    = PresenceNull
	Present = PresenceValue
)

// SynthesizedBenchmark is a versioned, target-independent benchmark assembled from input sources.
type SynthesizedBenchmark struct {
	SchemaVersion int           `json:"schemaVersion"`
	ID            string        `json:"id"`
	Baseline      LoadSpec      `json:"baseline"`
	Cases         []Case        `json:"cases"`
	Checks        []CheckSpec   `json:"checks,omitempty"`
	Segments      []Segment     `json:"segments,omitempty"`
	SegmentPolicy SegmentPolicy `json:"segmentPolicy"`
	Thresholds    []Threshold   `json:"thresholds,omitempty"`
	Report        ReportSpec    `json:"report"`
	Provenance    []Provenance  `json:"provenance,omitempty"`
}

// Case is one independently selectable request and its expectations.
type Case struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Operation   OperationRef         `json:"operation"`
	Request     RequestSpec          `json:"request"`
	Expectation *ResponseExpectation `json:"expectation,omitempty"`
	Check       *CheckSpec           `json:"check,omitempty"`
	Labels      []Attribute          `json:"labels,omitempty"`
	Metadata    []Attribute          `json:"metadata,omitempty"`
	Source      Provenance           `json:"source"`

	ExpectationPresence Presence `json:"-"`
	CheckPresence       Presence `json:"-"`
	LabelsPresence      Presence `json:"-"`
	MetadataPresence    Presence `json:"-"`
}

// OperationRef identifies an API operation without binding it to a target.
type OperationRef struct {
	ID     string `json:"id,omitempty"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Group  string `json:"group,omitempty"`
}

// OperationIdentity is an alternate name for OperationRef for callers that
// use operation identity terminology.
type OperationIdentity = OperationRef

// RequestSpec contains only serializable request data. Query entries are
// ordered so repeated parameter values are retained exactly.
type RequestSpec struct {
	Method    string       `json:"method"`
	Path      string       `json:"path"`
	Query     []Parameter  `json:"query,omitempty"`
	Headers   []Header     `json:"headers,omitempty"`
	Cookies   []Cookie     `json:"cookies,omitempty"`
	Body      *Payload     `json:"body,omitempty"`
	Redirects RedirectMode `json:"redirects"`

	QueryPresence   Presence `json:"-"`
	HeadersPresence Presence `json:"-"`
	CookiesPresence Presence `json:"-"`
	BodyPresence    Presence `json:"-"`
}

// Parameter is one query parameter occurrence.
type Parameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Header stores all values for one case-insensitive header name.
type Header struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`

	ValuesPresence Presence `json:"-"`
}

// Cookie is one request cookie.
type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Payload keeps an explicit empty payload distinct from an absent payload.
type Payload struct {
	MediaType string          `json:"mediaType,omitempty"`
	Encoding  PayloadEncoding `json:"encoding"`
	Content   string          `json:"content"`

	ContentPresence Presence `json:"-"`
	contentDecoded  bool     `json:"-"`
}

// ResponseExpectation contains independent HTTP and contract assertions.
type ResponseExpectation struct {
	Status  *StatusExpectation  `json:"status,omitempty"`
	Headers []HeaderExpectation `json:"headers,omitempty"`
	Cookies []CookieExpectation `json:"cookies,omitempty"`
	Body    *BodyExpectation    `json:"body,omitempty"`

	StatusPresence  Presence `json:"-"`
	HeadersPresence Presence `json:"-"`
	CookiesPresence Presence `json:"-"`
	BodyPresence    Presence `json:"-"`
}

// StatusExpectation asserts one exact response status.
type StatusExpectation struct {
	Equals int `json:"equals"`
}

// HeaderExpectation asserts one response header and may attach normalized
// matcher nodes to it.
type HeaderExpectation struct {
	Name     string    `json:"name"`
	Values   []string  `json:"values,omitempty"`
	Matchers []Matcher `json:"matchers,omitempty"`

	ValuesPresence   Presence `json:"-"`
	MatchersPresence Presence `json:"-"`
}

// CookieExpectation asserts one response cookie and may attach matchers.
type CookieExpectation struct {
	Name     string    `json:"name"`
	Values   []string  `json:"values,omitempty"`
	Matchers []Matcher `json:"matchers,omitempty"`

	ValuesPresence   Presence `json:"-"`
	MatchersPresence Presence `json:"-"`
}

// BodyExpectation distinguishes no body expectation from an explicit empty
// expectation object.
type BodyExpectation struct {
	Example  *Payload   `json:"example,omitempty"`
	Matchers []Matcher  `json:"matchers,omitempty"`
	Schema   *SchemaRef `json:"schema,omitempty"`

	ExamplePresence  Presence `json:"-"`
	MatchersPresence Presence `json:"-"`
	SchemaPresence   Presence `json:"-"`
}

// Matcher is a source-neutral normalized assertion node.
type Matcher struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Pattern string `json:"pattern,omitempty"`
	Value   string `json:"value,omitempty"`
	Min     *int   `json:"min,omitempty"`
	Max     *int   `json:"max,omitempty"`
}

// SchemaRef points at a schema in a source document without embedding that
// document in the synthesized benchmark.
type SchemaRef struct {
	Source string `json:"source"`
	Ref    string `json:"ref"`
}

// CheckSpec identifies a check and its optional selector scope.
type CheckSpec struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Enabled bool       `json:"enabled"`
	Scope   Selector   `json:"scope,omitempty"`
	Source  Provenance `json:"source,omitempty"`
}

// Attribute is an ordered name/value pair used for labels and metadata.
type Attribute struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Provenance records a diagnostics-safe source identity. Raw source
// documents and target-bound values do not belong here.
type Provenance struct {
	Kind        string `json:"kind"`
	Locator     string `json:"locator,omitempty"`
	Document    string `json:"document,omitempty"`
	Identifier  string `json:"identifier,omitempty"`
	Interaction string `json:"interaction,omitempty"`
	Version     string `json:"version,omitempty"`
	Priority    int    `json:"priority,omitempty"`
}

// Segment is a half-open time window [Start, End). A nil End is unbounded.
// A segment stored in SegmentPolicy.Default is behavior-only and has no
// window.
type Segment struct {
	ID               string        `json:"id"`
	Start            Duration      `json:"start,omitempty"`
	End              *Duration     `json:"end,omitempty"`
	Selection        SelectionSpec `json:"selection"`
	Load             LoadOverride  `json:"load,omitempty"`
	Checks           CheckMode     `json:"checks"`
	ActiveChecks     []string      `json:"activeChecks,omitempty"`
	ActiveThresholds []string      `json:"activeThresholds,omitempty"`
	Labels           []Attribute   `json:"labels,omitempty"`

	EndPresence Presence `json:"-"`
}

// SegmentPolicy controls what happens outside explicitly declared windows.
type SegmentPolicy struct {
	Gap     GapPolicy `json:"gap"`
	Default *Segment  `json:"default,omitempty"`

	DefaultPresence Presence `json:"-"`
}

// SelectionSpec selects cases for a segment.
type SelectionSpec struct {
	Mode  SelectionMode `json:"mode"`
	Cases []CaseWeight  `json:"cases,omitempty"`
	Seed  uint64        `json:"seed,omitempty"`
}

// CaseWeight associates a positive weight with a case.
type CaseWeight struct {
	CaseID string  `json:"caseId"`
	Weight float64 `json:"weight"`
}

// LoadSpec describes the benchmark's baseline load.
type LoadSpec struct {
	Kind          LoadKind `json:"kind"`
	VUs           int64    `json:"vus,omitempty"`
	Iterations    int64    `json:"iterations,omitempty"`
	RatePerSecond float64  `json:"ratePerSecond,omitempty"`
	Duration      Duration `json:"duration,omitempty"`
}

// LoadOverride describes a segment load change. Factor and absolute fields
// are mutually exclusive.
type LoadOverride struct {
	Factor        *float64  `json:"factor,omitempty"`
	VUs           *int64    `json:"vus,omitempty"`
	Iterations    *int64    `json:"iterations,omitempty"`
	RatePerSecond *float64  `json:"ratePerSecond,omitempty"`
	Duration      *Duration `json:"duration,omitempty"`
}

// Threshold is a structured objective that can be rendered by an execution
// adapter later.
type Threshold struct {
	ID             string     `json:"id"`
	Metric         string     `json:"metric"`
	Aggregation    string     `json:"aggregation"`
	Percentile     *float64   `json:"percentile,omitempty"`
	Operator       string     `json:"operator"`
	Target         float64    `json:"target"`
	Scope          Selector   `json:"scope"`
	ActiveSegments []string   `json:"activeSegments,omitempty"`
	Source         Provenance `json:"source,omitempty"`
}

// Selector identifies cases, operations, or labels to which a policy applies.
// Empty selectors mean the whole benchmark.
type Selector struct {
	CaseIDs      []string    `json:"caseIds,omitempty"`
	OperationIDs []string    `json:"operationIds,omitempty"`
	Labels       []Attribute `json:"labels,omitempty"`
}

// ReportSpec limits which stable attributes may become report series
// dimensions. Other labels and metadata remain available for richer outputs.
type ReportSpec struct {
	SeriesDimensions     []string `json:"seriesDimensions,omitempty"`
	AllowedDimensions    []string `json:"allowedDimensions,omitempty"`
	MaxSeriesCardinality int      `json:"maxSeriesCardinality,omitempty"`

	SeriesDimensionsPresence  Presence `json:"-"`
	AllowedDimensionsPresence Presence `json:"-"`
}

type Duration string
type PayloadEncoding string
type RedirectMode string
type SelectionMode string
type LoadKind string
type CheckMode string
type GapPolicy string

const (
	PayloadEncodingJSON   PayloadEncoding = "json"
	PayloadEncodingText   PayloadEncoding = "text"
	PayloadEncodingBase64 PayloadEncoding = "base64"
)

const (
	PayloadEncodingJSONValue   = PayloadEncodingJSON
	PayloadEncodingTextValue   = PayloadEncodingText
	PayloadEncodingBase64Value = PayloadEncodingBase64
)

const (
	RedirectFollow RedirectMode = "follow"
	RedirectNone   RedirectMode = "none"
)

const (
	RedirectModeFollow = RedirectFollow
	RedirectModeNone   = RedirectNone
)

const (
	SelectionRoundRobin SelectionMode = "round_robin"
	SelectionWeighted   SelectionMode = "weighted"
)

const (
	SelectionModeRoundRobin = SelectionRoundRobin
	SelectionModeWeighted   = SelectionWeighted
)

const (
	LoadSharedIterations LoadKind = "shared_iterations"
	LoadConstantVUs      LoadKind = "constant_vus"
	LoadArrivalRate      LoadKind = "arrival_rate"
)

const (
	LoadKindSharedIterations = LoadSharedIterations
	LoadKindConstantVUs      = LoadConstantVUs
	LoadKindArrivalRate      = LoadArrivalRate
)

const (
	CheckInherit  CheckMode = "inherit"
	CheckEnabled  CheckMode = "enabled"
	CheckDisabled CheckMode = "disabled"
)

const (
	CheckModeInherit  = CheckInherit
	CheckModeEnabled  = CheckEnabled
	CheckModeDisabled = CheckDisabled
)

const (
	GapReject     GapPolicy = "reject"
	GapUseDefault GapPolicy = "use_default"
)

const (
	GapPolicyReject     = GapReject
	GapPolicyUseDefault = GapUseDefault
)

const (
	MatcherEquality      = "equality"
	MatcherType          = "type"
	MatcherRegex         = "regex"
	MatcherInteger       = "integer"
	MatcherDecimal       = "decimal"
	MatcherNumber        = "number"
	MatcherBoolean       = "boolean"
	MatcherNull          = "null"
	MatcherInclude       = "include"
	MatcherArrayContains = "array_contains"
	MatcherValues        = "values"
	MatcherNotEmpty      = "not_empty"
)

const (
	MatcherKindEquality      = MatcherEquality
	MatcherKindType          = MatcherType
	MatcherKindRegex         = MatcherRegex
	MatcherKindInteger       = MatcherInteger
	MatcherKindDecimal       = MatcherDecimal
	MatcherKindNumber        = MatcherNumber
	MatcherKindBoolean       = MatcherBoolean
	MatcherKindNull          = MatcherNull
	MatcherKindInclude       = MatcherInclude
	MatcherKindArrayContains = MatcherArrayContains
	MatcherKindValues        = MatcherValues
	MatcherKindNotEmpty      = MatcherNotEmpty
)

const (
	ThresholdAggregationCount      = "count"
	ThresholdAggregationRate       = "rate"
	ThresholdAggregationAverage    = "avg"
	ThresholdAggregationMin        = "min"
	ThresholdAggregationMax        = "max"
	ThresholdAggregationPercentile = "percentile"
)

// Validate checks the benchmark's target-independent invariants.
func (p SynthesizedBenchmark) Validate() error {
	return Validate(p)
}

// Normalized returns the canonical copy of the benchmark.
func (p SynthesizedBenchmark) Normalized() SynthesizedBenchmark {
	return p.Normalize()
}

func defaultSeriesDimensions() []string {
	return []string{
		ReportDimensionConsumerService,
		ReportDimensionProviderService,
		ReportDimensionEndpoint,
		ReportDimensionInteraction,
		ReportDimensionProviderState,
		ReportDimensionName,
	}
}

func defaultAllowedDimensions() []string {
	return append(defaultSeriesDimensions(),
		ReportDimensionOperationID,
		ReportDimensionMethod,
		ReportDimensionPath,
	)
}

// DefaultReportDimensions returns the compatibility dimension set used when a
// report does not specify one explicitly.
func DefaultReportDimensions() []string {
	return defaultSeriesDimensions()
}

// DefaultReportDimensionAllowlist returns the built-in safe dimension
// allowlist. A benchmark may provide a narrower or explicitly extended list.
func DefaultReportDimensionAllowlist() []string {
	return defaultAllowedDimensions()
}
