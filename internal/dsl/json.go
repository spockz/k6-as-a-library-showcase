// This file isolates presence-aware JSON because missing, null, and empty values are distinct inputs.
package dsl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
)

// MarshalBenchmarkManifest returns a deterministic, indented JSON snapshot after model
// validation and normalization.
func MarshalBenchmarkManifest(benchmark SynthesizedBenchmark) ([]byte, error) {
	normalized := benchmark.Normalize()
	if err := Validate(normalized); err != nil {
		return nil, err
	}
	return json.MarshalIndent(benchmarkManifestWire(normalized), "", "  ")
}

// UnmarshalBenchmarkManifest decodes, normalizes, and validates one benchmark manifest.
func UnmarshalBenchmarkManifest(data []byte) (SynthesizedBenchmark, error) {
	var benchmark SynthesizedBenchmark
	if err := json.Unmarshal(data, &benchmark); err != nil {
		return SynthesizedBenchmark{}, err
	}
	benchmark = benchmark.Normalize()
	if err := Validate(benchmark); err != nil {
		return SynthesizedBenchmark{}, err
	}
	return benchmark, nil
}

// JSON returns the deterministic benchmark manifest representation.
func (benchmark SynthesizedBenchmark) JSON() ([]byte, error) {
	return MarshalBenchmarkManifest(benchmark)
}

func (benchmark SynthesizedBenchmark) MarshalJSON() ([]byte, error) {
	normalized := benchmark.Normalize()
	if err := Validate(normalized); err != nil {
		return nil, err
	}
	return json.Marshal(benchmarkManifestWire(normalized))
}

func (benchmark *SynthesizedBenchmark) UnmarshalJSON(data []byte) error {
	if benchmark == nil {
		return fmt.Errorf("decode benchmark manifest into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{
		"schemaVersion": true,
		"id":            true,
		"baseline":      true,
		"cases":         true,
		"checks":        true,
		"segments":      true,
		"segmentPolicy": true,
		"thresholds":    true,
		"report":        true,
		"provenance":    true,
	})
	if err != nil {
		return err
	}
	var result SynthesizedBenchmark
	if err := decodeJSONField(fields, "schemaVersion", &result.SchemaVersion); err != nil {
		return err
	}
	if result.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("decode benchmark manifest: unsupported schema version %d", result.SchemaVersion)
	}
	if err := decodeJSONField(fields, "id", &result.ID); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "baseline", &result.Baseline); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "cases", &result.Cases); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "checks", &result.Checks); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "segments", &result.Segments); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "segmentPolicy", &result.SegmentPolicy); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "thresholds", &result.Thresholds); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "report", &result.Report); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "provenance", &result.Provenance); err != nil {
		return err
	}
	*benchmark = result
	return nil
}

type benchmarkManifestWire SynthesizedBenchmark

func (item Case) MarshalJSON() ([]byte, error) {
	expectation, err := marshalOptionalPointer(item.Expectation, item.ExpectationPresence)
	if err != nil {
		return nil, err
	}
	check, err := marshalOptionalPointer(item.Check, item.CheckPresence)
	if err != nil {
		return nil, err
	}
	attributes, err := marshalOptionalSlice(item.Attributes, item.AttributesPresence)
	if err != nil {
		return nil, err
	}
	metadata, err := marshalOptionalSlice(item.Metadata, item.MetadataPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ID          string           `json:"id"`
		Name        string           `json:"name"`
		Operation   OperationRef     `json:"operation"`
		Request     RequestSpec      `json:"request"`
		Expectation *json.RawMessage `json:"expectation,omitempty"`
		Check       *json.RawMessage `json:"check,omitempty"`
		Attributes  *json.RawMessage `json:"attributes,omitempty"`
		Metadata    *json.RawMessage `json:"metadata,omitempty"`
		Source      Provenance       `json:"source"`
	}{
		ID: item.ID, Name: item.Name, Operation: item.Operation, Request: item.Request,
		Expectation: rawMessagePointer(expectation), Check: rawMessagePointer(check),
		Attributes: rawMessagePointer(attributes), Metadata: rawMessagePointer(metadata),
		Source: item.Source,
	})
}

func (item *Case) UnmarshalJSON(data []byte) error {
	if item == nil {
		return fmt.Errorf("decode case into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{
		"id": true, "name": true, "operation": true, "request": true,
		"expectation": true, "check": true, "attributes": true, "metadata": true, "source": true,
	})
	if err != nil {
		return err
	}
	var result Case
	if err := decodeJSONField(fields, "id", &result.ID); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "name", &result.Name); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "operation", &result.Operation); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "request", &result.Request); err != nil {
		return err
	}
	if raw, ok := fields["expectation"]; ok {
		result.Expectation, result.ExpectationPresence, err = decodeOptionalPointer[ResponseExpectation](raw)
		if err != nil {
			return wrapDecodeError("expectation", err)
		}
	}
	if raw, ok := fields["check"]; ok {
		result.Check, result.CheckPresence, err = decodeOptionalPointer[CheckSpec](raw)
		if err != nil {
			return wrapDecodeError("check", err)
		}
	}
	if err := decodeSliceField(fields, "attributes", &result.Attributes); err != nil {
		return err
	}
	if raw, ok := fields["attributes"]; ok {
		result.AttributesPresence = presenceOfJSON(raw)
	}
	if err := decodeSliceField(fields, "metadata", &result.Metadata); err != nil {
		return err
	}
	if raw, ok := fields["metadata"]; ok {
		result.MetadataPresence = presenceOfJSON(raw)
	}
	if err := decodeJSONField(fields, "source", &result.Source); err != nil {
		return err
	}
	if _, ok := fields["expectation"]; !ok {
		result.ExpectationPresence = PresenceAbsent
	}
	if _, ok := fields["check"]; !ok {
		result.CheckPresence = PresenceAbsent
	}
	*item = result
	return nil
}

func (request RequestSpec) MarshalJSON() ([]byte, error) {
	query, err := marshalOptionalSlice(request.Query, request.QueryPresence)
	if err != nil {
		return nil, err
	}
	headers, err := marshalOptionalSlice(request.Headers, request.HeadersPresence)
	if err != nil {
		return nil, err
	}
	cookies, err := marshalOptionalSlice(request.Cookies, request.CookiesPresence)
	if err != nil {
		return nil, err
	}
	body, err := marshalOptionalPointer(request.Body, request.BodyPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Method    string               `json:"method"`
		Path      string               `json:"path"`
		Query     *json.RawMessage     `json:"query,omitempty"`
		Headers   *json.RawMessage     `json:"headers,omitempty"`
		Cookies   *json.RawMessage     `json:"cookies,omitempty"`
		Body      *json.RawMessage     `json:"body,omitempty"`
		Redirects RedirectMode         `json:"redirects"`
		Behavior  *BehaviorDescription `json:"behavior,omitempty"`
	}{
		Method: request.Method, Path: request.Path, Query: rawMessagePointer(query),
		Headers: rawMessagePointer(headers), Cookies: rawMessagePointer(cookies),
		Body: rawMessagePointer(body), Redirects: request.Redirects, Behavior: request.Behavior,
	})
}

func (request *RequestSpec) UnmarshalJSON(data []byte) error {
	if request == nil {
		return fmt.Errorf("decode request specification into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{
		"method": true, "path": true, "query": true, "headers": true,
		"cookies": true, "body": true, "redirects": true, "behavior": true,
	})
	if err != nil {
		return err
	}
	var result RequestSpec
	if err := decodeJSONField(fields, "method", &result.Method); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "path", &result.Path); err != nil {
		return err
	}
	if err := decodeSliceField(fields, "query", &result.Query); err != nil {
		return err
	}
	if raw, ok := fields["query"]; ok {
		result.QueryPresence = presenceOfJSON(raw)
	}
	if err := decodeSliceField(fields, "headers", &result.Headers); err != nil {
		return err
	}
	if raw, ok := fields["headers"]; ok {
		result.HeadersPresence = presenceOfJSON(raw)
	}
	if err := decodeSliceField(fields, "cookies", &result.Cookies); err != nil {
		return err
	}
	if raw, ok := fields["cookies"]; ok {
		result.CookiesPresence = presenceOfJSON(raw)
	}
	if raw, ok := fields["body"]; ok {
		result.Body, result.BodyPresence, err = decodeOptionalPointer[Payload](raw)
		if err != nil {
			return wrapDecodeError("body", err)
		}
	}
	if err := decodeJSONField(fields, "redirects", &result.Redirects); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "behavior", &result.Behavior); err != nil {
		return err
	}
	if _, ok := fields["query"]; !ok {
		result.QueryPresence = PresenceAbsent
	}
	if _, ok := fields["headers"]; !ok {
		result.HeadersPresence = PresenceAbsent
	}
	if _, ok := fields["cookies"]; !ok {
		result.CookiesPresence = PresenceAbsent
	}
	if _, ok := fields["body"]; !ok {
		result.BodyPresence = PresenceAbsent
	}
	*request = result
	return nil
}

func (header Header) MarshalJSON() ([]byte, error) {
	values, err := marshalOptionalSlice(header.Values, header.ValuesPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Name   string           `json:"name"`
		Values *json.RawMessage `json:"values,omitempty"`
	}{Name: header.Name, Values: rawMessagePointer(values)})
}

func (header *Header) UnmarshalJSON(data []byte) error {
	if header == nil {
		return fmt.Errorf("decode header into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{"name": true, "values": true})
	if err != nil {
		return err
	}
	var result Header
	if err := decodeJSONField(fields, "name", &result.Name); err != nil {
		return err
	}
	if err := decodeSliceField(fields, "values", &result.Values); err != nil {
		return err
	}
	if raw, ok := fields["values"]; ok {
		result.ValuesPresence = presenceOfJSON(raw)
	}
	if _, ok := fields["values"]; !ok {
		result.ValuesPresence = PresenceAbsent
	}
	*header = result
	return nil
}

func (expectation HeaderExpectation) MarshalJSON() ([]byte, error) {
	values, err := marshalOptionalSlice(expectation.Values, expectation.ValuesPresence)
	if err != nil {
		return nil, err
	}
	matchers, err := marshalOptionalSlice(expectation.Matchers, expectation.MatchersPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Name     string           `json:"name"`
		Values   *json.RawMessage `json:"values,omitempty"`
		Matchers *json.RawMessage `json:"matchers,omitempty"`
	}{Name: expectation.Name, Values: rawMessagePointer(values), Matchers: rawMessagePointer(matchers)})
}

func (expectation *HeaderExpectation) UnmarshalJSON(data []byte) error {
	if expectation == nil {
		return fmt.Errorf("decode header expectation into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{"name": true, "values": true, "matchers": true})
	if err != nil {
		return err
	}
	var result HeaderExpectation
	if err := decodeJSONField(fields, "name", &result.Name); err != nil {
		return err
	}
	if err := decodeSliceField(fields, "values", &result.Values); err != nil {
		return err
	}
	if raw, ok := fields["values"]; ok {
		result.ValuesPresence = presenceOfJSON(raw)
	}
	if err := decodeSliceField(fields, "matchers", &result.Matchers); err != nil {
		return err
	}
	if raw, ok := fields["matchers"]; ok {
		result.MatchersPresence = presenceOfJSON(raw)
	}
	*expectation = result
	return nil
}

func (expectation CookieExpectation) MarshalJSON() ([]byte, error) {
	values, err := marshalOptionalSlice(expectation.Values, expectation.ValuesPresence)
	if err != nil {
		return nil, err
	}
	matchers, err := marshalOptionalSlice(expectation.Matchers, expectation.MatchersPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Name     string           `json:"name"`
		Values   *json.RawMessage `json:"values,omitempty"`
		Matchers *json.RawMessage `json:"matchers,omitempty"`
	}{Name: expectation.Name, Values: rawMessagePointer(values), Matchers: rawMessagePointer(matchers)})
}

func (expectation *CookieExpectation) UnmarshalJSON(data []byte) error {
	if expectation == nil {
		return fmt.Errorf("decode cookie expectation into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{"name": true, "values": true, "matchers": true})
	if err != nil {
		return err
	}
	var result CookieExpectation
	if err := decodeJSONField(fields, "name", &result.Name); err != nil {
		return err
	}
	if err := decodeSliceField(fields, "values", &result.Values); err != nil {
		return err
	}
	if raw, ok := fields["values"]; ok {
		result.ValuesPresence = presenceOfJSON(raw)
	}
	if err := decodeSliceField(fields, "matchers", &result.Matchers); err != nil {
		return err
	}
	if raw, ok := fields["matchers"]; ok {
		result.MatchersPresence = presenceOfJSON(raw)
	}
	*expectation = result
	return nil
}

func (payload Payload) MarshalJSON() ([]byte, error) {
	content := json.RawMessage(nil)
	switch payload.ContentPresence {
	case PresenceAbsent:
		if !payload.contentDecoded || payload.Content != "" {
			encoded, err := json.Marshal(payload.Content)
			if err != nil {
				return nil, err
			}
			content = encoded
		}
	case PresenceNull:
		content = json.RawMessage("null")
	default:
		encoded, err := json.Marshal(payload.Content)
		if err != nil {
			return nil, err
		}
		content = encoded
	}
	return json.Marshal(struct {
		MediaType string           `json:"mediaType,omitempty"`
		Encoding  PayloadEncoding  `json:"encoding"`
		Content   *json.RawMessage `json:"content,omitempty"`
	}{MediaType: payload.MediaType, Encoding: payload.Encoding, Content: rawMessagePointer(content)})
}

func (payload *Payload) UnmarshalJSON(data []byte) error {
	if payload == nil {
		return fmt.Errorf("decode payload into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{"mediaType": true, "encoding": true, "content": true})
	if err != nil {
		return err
	}
	var result Payload
	if err := decodeJSONField(fields, "mediaType", &result.MediaType); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "encoding", &result.Encoding); err != nil {
		return err
	}
	if raw, ok := fields["content"]; ok {
		if isNullJSON(raw) {
			result.ContentPresence = PresenceNull
		} else {
			if err := decodeJSONField(fields, "content", &result.Content); err != nil {
				return err
			}
			result.ContentPresence = PresenceValue
		}
	} else {
		result.ContentPresence = PresenceAbsent
	}
	result.contentDecoded = true
	*payload = result
	return nil
}

func (expectation ResponseExpectation) MarshalJSON() ([]byte, error) {
	status, err := marshalOptionalPointer(expectation.Status, expectation.StatusPresence)
	if err != nil {
		return nil, err
	}
	headers, err := marshalOptionalSlice(expectation.Headers, expectation.HeadersPresence)
	if err != nil {
		return nil, err
	}
	cookies, err := marshalOptionalSlice(expectation.Cookies, expectation.CookiesPresence)
	if err != nil {
		return nil, err
	}
	body, err := marshalOptionalPointer(expectation.Body, expectation.BodyPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Status  *json.RawMessage `json:"status,omitempty"`
		Headers *json.RawMessage `json:"headers,omitempty"`
		Cookies *json.RawMessage `json:"cookies,omitempty"`
		Body    *json.RawMessage `json:"body,omitempty"`
	}{Status: rawMessagePointer(status), Headers: rawMessagePointer(headers),
		Cookies: rawMessagePointer(cookies), Body: rawMessagePointer(body)})
}

func (expectation *ResponseExpectation) UnmarshalJSON(data []byte) error {
	if expectation == nil {
		return fmt.Errorf("decode response expectation into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{"status": true, "headers": true, "cookies": true, "body": true})
	if err != nil {
		return err
	}
	var result ResponseExpectation
	if raw, ok := fields["status"]; ok {
		result.Status, result.StatusPresence, err = decodeOptionalPointer[StatusExpectation](raw)
		if err != nil {
			return wrapDecodeError("status", err)
		}
	}
	if err := decodeSliceField(fields, "headers", &result.Headers); err != nil {
		return err
	}
	if raw, ok := fields["headers"]; ok {
		result.HeadersPresence = presenceOfJSON(raw)
	}
	if err := decodeSliceField(fields, "cookies", &result.Cookies); err != nil {
		return err
	}
	if raw, ok := fields["cookies"]; ok {
		result.CookiesPresence = presenceOfJSON(raw)
	}
	if raw, ok := fields["body"]; ok {
		result.Body, result.BodyPresence, err = decodeOptionalPointer[BodyExpectation](raw)
		if err != nil {
			return wrapDecodeError("body", err)
		}
	}
	if _, ok := fields["status"]; !ok {
		result.StatusPresence = PresenceAbsent
	}
	if _, ok := fields["headers"]; !ok {
		result.HeadersPresence = PresenceAbsent
	}
	if _, ok := fields["cookies"]; !ok {
		result.CookiesPresence = PresenceAbsent
	}
	if _, ok := fields["body"]; !ok {
		result.BodyPresence = PresenceAbsent
	}
	*expectation = result
	return nil
}

func (expectation BodyExpectation) MarshalJSON() ([]byte, error) {
	example, err := marshalOptionalPointer(expectation.Example, expectation.ExamplePresence)
	if err != nil {
		return nil, err
	}
	matchers, err := marshalOptionalSlice(expectation.Matchers, expectation.MatchersPresence)
	if err != nil {
		return nil, err
	}
	schema, err := marshalOptionalPointer(expectation.Schema, expectation.SchemaPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Example  *json.RawMessage `json:"example,omitempty"`
		Matchers *json.RawMessage `json:"matchers,omitempty"`
		Schema   *json.RawMessage `json:"schema,omitempty"`
	}{Example: rawMessagePointer(example), Matchers: rawMessagePointer(matchers),
		Schema: rawMessagePointer(schema)})
}

func (expectation *BodyExpectation) UnmarshalJSON(data []byte) error {
	if expectation == nil {
		return fmt.Errorf("decode body expectation into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{"example": true, "matchers": true, "schema": true})
	if err != nil {
		return err
	}
	var result BodyExpectation
	if raw, ok := fields["example"]; ok {
		result.Example, result.ExamplePresence, err = decodeOptionalPointer[Payload](raw)
		if err != nil {
			return wrapDecodeError("example", err)
		}
	}
	if err := decodeSliceField(fields, "matchers", &result.Matchers); err != nil {
		return err
	}
	if raw, ok := fields["matchers"]; ok {
		result.MatchersPresence = presenceOfJSON(raw)
	}
	if raw, ok := fields["schema"]; ok {
		result.Schema, result.SchemaPresence, err = decodeOptionalPointer[SchemaRef](raw)
		if err != nil {
			return wrapDecodeError("schema", err)
		}
	}
	if _, ok := fields["example"]; !ok {
		result.ExamplePresence = PresenceAbsent
	}
	if _, ok := fields["schema"]; !ok {
		result.SchemaPresence = PresenceAbsent
	}
	*expectation = result
	return nil
}

func (segment Segment) MarshalJSON() ([]byte, error) {
	end, err := marshalOptionalPointer(segment.End, segment.EndPresence)
	if err != nil {
		return nil, err
	}
	load := json.RawMessage(nil)
	if !isZeroLoadOverride(segment.Load) {
		load, err = json.Marshal(segment.Load)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		ID               string           `json:"id"`
		Start            Duration         `json:"start,omitempty"`
		End              *json.RawMessage `json:"end,omitempty"`
		Selection        SelectionSpec    `json:"selection"`
		Load             *json.RawMessage `json:"load,omitempty"`
		Checks           CheckMode        `json:"checks"`
		ActiveChecks     []string         `json:"activeChecks,omitempty"`
		ActiveThresholds []string         `json:"activeThresholds,omitempty"`
		Attributes       AttributeSet     `json:"attributes,omitempty"`
	}{
		ID: segment.ID, Start: segment.Start, End: rawMessagePointer(end), Selection: segment.Selection,
		Load: rawMessagePointer(load), Checks: segment.Checks, ActiveChecks: segment.ActiveChecks,
		ActiveThresholds: segment.ActiveThresholds, Attributes: segment.Attributes,
	})
}

func (segment *Segment) UnmarshalJSON(data []byte) error {
	if segment == nil {
		return fmt.Errorf("decode segment into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{
		"id": true, "start": true, "end": true, "selection": true, "load": true,
		"checks": true, "activeChecks": true, "activeThresholds": true, "attributes": true,
	})
	if err != nil {
		return err
	}
	var result Segment
	if err := decodeJSONField(fields, "id", &result.ID); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "start", &result.Start); err != nil {
		return err
	}
	if raw, ok := fields["end"]; ok {
		result.End, result.EndPresence, err = decodeOptionalPointer[Duration](raw)
		if err != nil {
			return wrapDecodeError("end", err)
		}
	}
	if err := decodeJSONField(fields, "selection", &result.Selection); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "load", &result.Load); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "checks", &result.Checks); err != nil {
		return err
	}
	if err := decodeSliceField(fields, "activeChecks", &result.ActiveChecks); err != nil {
		return err
	}
	if err := decodeSliceField(fields, "activeThresholds", &result.ActiveThresholds); err != nil {
		return err
	}
	if err := decodeSliceField(fields, "attributes", &result.Attributes); err != nil {
		return err
	}
	if _, ok := fields["end"]; !ok {
		result.EndPresence = PresenceAbsent
	}
	*segment = result
	return nil
}

func (policy SegmentPolicy) MarshalJSON() ([]byte, error) {
	defaultSegment, err := marshalOptionalPointer(policy.Default, policy.DefaultPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Gap     GapPolicy        `json:"gap"`
		Default *json.RawMessage `json:"default,omitempty"`
	}{Gap: policy.Gap, Default: rawMessagePointer(defaultSegment)})
}

func (policy *SegmentPolicy) UnmarshalJSON(data []byte) error {
	if policy == nil {
		return fmt.Errorf("decode segment policy into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{"gap": true, "default": true})
	if err != nil {
		return err
	}
	var result SegmentPolicy
	if err := decodeJSONField(fields, "gap", &result.Gap); err != nil {
		return err
	}
	if raw, ok := fields["default"]; ok {
		result.Default, result.DefaultPresence, err = decodeOptionalPointer[Segment](raw)
		if err != nil {
			return wrapDecodeError("default", err)
		}
	}
	if _, ok := fields["default"]; !ok {
		result.DefaultPresence = PresenceAbsent
	}
	*policy = result
	return nil
}

func (check CheckSpec) MarshalJSON() ([]byte, error) {
	scope, err := marshalSelector(check.Scope, false)
	if err != nil {
		return nil, err
	}
	source, err := marshalOptionalValue(check.Source, !isZeroProvenance(check.Source))
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ID      string           `json:"id"`
		Name    string           `json:"name"`
		Enabled bool             `json:"enabled"`
		Scope   *json.RawMessage `json:"scope,omitempty"`
		Source  *json.RawMessage `json:"source,omitempty"`
	}{ID: check.ID, Name: check.Name, Enabled: check.Enabled,
		Scope: rawMessagePointer(scope), Source: rawMessagePointer(source)})
}

func (check *CheckSpec) UnmarshalJSON(data []byte) error {
	if check == nil {
		return fmt.Errorf("decode check into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{"id": true, "name": true, "enabled": true, "scope": true, "source": true})
	if err != nil {
		return err
	}
	var result CheckSpec
	if err := decodeJSONField(fields, "id", &result.ID); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "name", &result.Name); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "enabled", &result.Enabled); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "scope", &result.Scope); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "source", &result.Source); err != nil {
		return err
	}
	*check = result
	return nil
}

func (threshold Threshold) MarshalJSON() ([]byte, error) {
	scope, err := marshalSelector(threshold.Scope, true)
	if err != nil {
		return nil, err
	}
	source, err := marshalOptionalValue(threshold.Source, !isZeroProvenance(threshold.Source))
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ID             string           `json:"id"`
		Metric         string           `json:"metric"`
		Aggregation    string           `json:"aggregation"`
		Percentile     *float64         `json:"percentile,omitempty"`
		Operator       string           `json:"operator"`
		Target         float64          `json:"target"`
		Scope          *json.RawMessage `json:"scope"`
		ActiveSegments []string         `json:"activeSegments,omitempty"`
		Source         *json.RawMessage `json:"source,omitempty"`
	}{ID: threshold.ID, Metric: threshold.Metric, Aggregation: threshold.Aggregation,
		Percentile: threshold.Percentile, Operator: threshold.Operator, Target: threshold.Target,
		Scope: rawMessagePointer(scope), ActiveSegments: threshold.ActiveSegments,
		Source: rawMessagePointer(source)})
}

func (threshold *Threshold) UnmarshalJSON(data []byte) error {
	if threshold == nil {
		return fmt.Errorf("decode threshold into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{
		"id": true, "metric": true, "aggregation": true, "percentile": true,
		"operator": true, "target": true, "scope": true, "activeSegments": true, "source": true,
	})
	if err != nil {
		return err
	}
	var result Threshold
	if err := decodeJSONField(fields, "id", &result.ID); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "metric", &result.Metric); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "aggregation", &result.Aggregation); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "percentile", &result.Percentile); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "operator", &result.Operator); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "target", &result.Target); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "scope", &result.Scope); err != nil {
		return err
	}
	if err := decodeSliceField(fields, "activeSegments", &result.ActiveSegments); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "source", &result.Source); err != nil {
		return err
	}
	*threshold = result
	return nil
}

func (report ReportSpec) MarshalJSON() ([]byte, error) {
	groupBy, err := marshalOptionalSlice(report.GroupBy, report.GroupByPresence)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		GroupBy              *json.RawMessage `json:"groupBy,omitempty"`
		MaxSeriesCardinality int              `json:"maxSeriesCardinality,omitempty"`
	}{
		GroupBy:              rawMessagePointer(groupBy),
		MaxSeriesCardinality: report.MaxSeriesCardinality,
	})
}

func (report *ReportSpec) UnmarshalJSON(data []byte) error {
	if report == nil {
		return fmt.Errorf("decode report specification into nil receiver")
	}
	fields, err := decodeObject(data, map[string]bool{
		"groupBy": true, "maxSeriesCardinality": true,
	})
	if err != nil {
		return err
	}
	var result ReportSpec
	if err := decodeSliceField(fields, "groupBy", &result.GroupBy); err != nil {
		return err
	}
	if raw, ok := fields["groupBy"]; ok {
		result.GroupByPresence = presenceOfJSON(raw)
	}
	if err := decodeJSONField(fields, "maxSeriesCardinality", &result.MaxSeriesCardinality); err != nil {
		return err
	}
	*report = result
	return nil
}

func marshalOptionalPointer(value any, presence Presence) (json.RawMessage, error) {
	valueIsNil := value == nil
	if !valueIsNil {
		reflected := reflect.ValueOf(value)
		valueIsNil = (reflected.Kind() == reflect.Ptr || reflected.Kind() == reflect.Interface) && reflected.IsNil()
	}
	if !valueIsNil {
		encoded, err := json.Marshal(value)
		return json.RawMessage(encoded), err
	}
	if presence == PresenceNull || presence == PresenceValue {
		return json.RawMessage("null"), nil
	}
	return nil, nil
}

func rawMessagePointer(value json.RawMessage) *json.RawMessage {
	if value == nil {
		return nil
	}
	copyValue := append(json.RawMessage(nil), value...)
	return &copyValue
}

func marshalOptionalValue(value any, present bool) (json.RawMessage, error) {
	if !present {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	return json.RawMessage(encoded), err
}

func marshalOptionalSlice[S ~[]E, E any](values S, presence Presence) (json.RawMessage, error) {
	if values == nil {
		if presence == PresenceNull {
			return json.RawMessage("null"), nil
		}
		if presence == PresenceValue {
			return json.RawMessage("[]"), nil
		}
		return nil, nil
	}
	encoded, err := json.Marshal(values)
	return json.RawMessage(encoded), err
}

func decodeOptionalPointer[T any](raw json.RawMessage) (*T, Presence, error) {
	if isNullJSON(raw) {
		return nil, PresenceNull, nil
	}
	var value T
	if err := decodeStrictJSON(raw, &value); err != nil {
		return nil, PresenceValue, err
	}
	return &value, PresenceValue, nil
}

func presenceOfJSON(raw json.RawMessage) Presence {
	if isNullJSON(raw) {
		return PresenceNull
	}
	return PresenceValue
}

func decodeSliceField[S ~[]E, E any](fields map[string]json.RawMessage, name string, target *S) error {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	if err := decodeStrictJSON(raw, target); err != nil {
		return wrapDecodeError(name, err)
	}
	return nil
}

func decodeJSONField(fields map[string]json.RawMessage, name string, target any) error {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	if err := decodeStrictJSON(raw, target); err != nil {
		return wrapDecodeError(name, err)
	}
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON value contains multiple values")
		}
		return err
	}
	return nil
}

func decodeObject(data []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("JSON value must be an object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("JSON value contains multiple values")
		}
		return nil, err
	}
	unknown := make([]string, 0)
	for name := range fields {
		if !allowed[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown field %q", unknown[0])
	}
	return fields, nil
}

func isZeroLoadOverride(load LoadOverride) bool {
	return load.Factor == nil && load.VUs == nil && load.Iterations == nil &&
		load.RatePerSecond == nil && load.Duration == nil
}

func isZeroProvenance(source Provenance) bool {
	return source == (Provenance{})
}

func marshalSelector(selector Selector, required bool) (json.RawMessage, error) {
	if !required && isZeroSelector(selector) {
		return nil, nil
	}
	encoded, err := json.Marshal(selector)
	return json.RawMessage(encoded), err
}

func isZeroSelector(selector Selector) bool {
	return len(selector.CaseIDs) == 0 && len(selector.OperationIDs) == 0 && len(selector.Attributes) == 0
}
