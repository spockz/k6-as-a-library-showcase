package pact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pact-foundation/pact-go/v2/models"
	"k6-as-a-library/internal/dsl"
)

const (
	AttributeConsumerService = "consumer_service"
	AttributeProviderService = "provider_service"
	AttributeEndpoint        = "endpoint"
	AttributeInteraction     = "pact_interaction"
	AttributeProviderState   = "provider_state"
	pactFileMetadata         = "pact_file"
	pactDescriptionMeta      = "pact_description"
	pactMismatchMetadata     = "pact_mismatch"
	pactResponseCheckName    = "pact response matches"
)

const (
	MismatchMetadata  = pactMismatchMetadata
	ResponseCheckName = pactResponseCheckName
)

type pactDocument struct {
	Consumer     pacticipant       `json:"consumer"`
	Provider     pacticipant       `json:"provider"`
	Interactions []pactInteraction `json:"interactions"`
	Metadata     pactMetadata      `json:"metadata"`
}

type pacticipant struct {
	Name string `json:"name"`
}

type pactMetadata struct {
	PactSpecification struct {
		Version string `json:"version"`
	} `json:"pactSpecification"`
}

type pactInteraction struct {
	Description    string                     `json:"description"`
	ID             string                     `json:"id"`
	Key            string                     `json:"key"`
	ProviderState  string                     `json:"providerState"`
	LegacyState    string                     `json:"provider_state"`
	ProviderStates []models.ProviderState     `json:"providerStates"`
	Request        pactHTTPRequest            `json:"request"`
	Response       pactHTTPResponse           `json:"response"`
	MatchingRules  map[string]json.RawMessage `json:"matchingRules"`

	PactFile   string            `json:"-"`
	Name       string            `json:"-"`
	Attributes map[string]string `json:"-"`
}

type pactHTTPRequest struct {
	Method        string                     `json:"method"`
	Path          string                     `json:"path"`
	Query         json.RawMessage            `json:"query"`
	Headers       map[string]json.RawMessage `json:"headers"`
	Cookies       json.RawMessage            `json:"cookies"`
	Body          json.RawMessage            `json:"body"`
	MatchingRules map[string]json.RawMessage `json:"matchingRules"`
}

type pactHTTPResponse struct {
	Status        int                        `json:"status"`
	Headers       map[string]json.RawMessage `json:"headers"`
	Cookies       json.RawMessage            `json:"cookies"`
	Body          json.RawMessage            `json:"body"`
	MatchingRules map[string]json.RawMessage `json:"matchingRules"`
	rules         map[string]pactRule
}

type pactRule struct {
	Match    string            `json:"match"`
	Regex    string            `json:"regex"`
	Min      *int              `json:"min"`
	Max      *int              `json:"max"`
	Value    json.RawMessage   `json:"value"`
	Variants []json.RawMessage `json:"variants"`
}

type Interaction = pactInteraction
type HTTPRequest = pactHTTPRequest
type HTTPResponse = pactHTTPResponse

func LoadDirectory(directory string) ([]Interaction, error) {
	return loadPactDirectory(directory)
}

func loadPactDirectory(directory string) ([]pactInteraction, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("stat PACT directory %q: %w", directory, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("PACT path %q is not a directory", directory)
	}

	var filenames []string
	err = filepath.WalkDir(directory, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(filename), ".json") {
			return nil
		}
		filenames = append(filenames, filename)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk PACT directory %q: %w", directory, err)
	}
	slices.Sort(filenames)
	if len(filenames) == 0 {
		return nil, fmt.Errorf("PACT directory %q contains no JSON pact files", directory)
	}

	interactions := make([]pactInteraction, 0)
	for _, filename := range filenames {
		fileInteractions, err := loadPactFile(filename)
		if err != nil {
			return nil, err
		}
		interactions = append(interactions, fileInteractions...)
	}
	if len(interactions) == 0 {
		return nil, fmt.Errorf("PACT directory %q contains no HTTP interactions", directory)
	}
	return interactions, nil
}

func loadPactFile(filename string) ([]pactInteraction, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read PACT file %q: %w", filename, err)
	}

	var document pactDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		return nil, fmt.Errorf("decode PACT file %q: %w", filename, err)
	}
	if err := validatePactSpecification(document.Metadata.PactSpecification.Version); err != nil {
		return nil, fmt.Errorf("validate PACT file %q: %w", filename, err)
	}
	if document.Consumer.Name == "" {
		return nil, fmt.Errorf("validate PACT file %q: consumer name is empty", filename)
	}
	if document.Provider.Name == "" {
		return nil, fmt.Errorf("validate PACT file %q: provider name is empty", filename)
	}
	if len(document.Interactions) == 0 {
		return nil, fmt.Errorf("validate PACT file %q: no interactions found", filename)
	}

	interactions := make([]pactInteraction, len(document.Interactions))
	for index := range document.Interactions {
		interaction := document.Interactions[index]
		if err := validatePactInteraction(interaction, index, filename); err != nil {
			return nil, err
		}
		rules, err := flattenPactRules(mergePactMatchingRules(interaction.MatchingRules, interaction.Response.MatchingRules))
		if err != nil {
			return nil, fmt.Errorf("validate PACT file %q interaction %d response rules: %w", filename, index, err)
		}
		interaction.Response.rules = rules
		interaction.PactFile = filename
		name, err := pactInteractionName(interaction)
		if err != nil {
			return nil, fmt.Errorf("name PACT file %q interaction %d: %w", filename, index, err)
		}
		interaction.Name = name
		interaction.Attributes = pactInteractionAttributes(interaction, document.Consumer.Name, document.Provider.Name)
		interactions[index] = interaction
	}
	return interactions, nil
}

// The public pact-go API provides the Pact specification model types, but its
// file verifier does not expose individual interactions or match results.
func mergePactMatchingRules(groups ...map[string]json.RawMessage) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage)
	for _, group := range groups {
		maps.Copy(merged, group)
	}
	return merged
}

func validatePactSpecification(version string) error {
	if version == "" {
		return nil
	}
	specification := models.SpecificationVersion(version)
	switch specification {
	case models.V2, models.V3, models.V4:
		return nil
	default:
		return fmt.Errorf("unsupported specification version %q", version)
	}
}

func validatePactInteraction(interaction pactInteraction, index int, filename string) error {
	if strings.TrimSpace(interaction.Request.Method) == "" {
		return fmt.Errorf("validate PACT file %q interaction %d: request method is empty", filename, index)
	}
	if interaction.Request.Path == "" || !strings.HasPrefix(interaction.Request.Path, "/") {
		return fmt.Errorf("validate PACT file %q interaction %d: request path %q is not absolute", filename, index, interaction.Request.Path)
	}
	if interaction.Response.Status < 100 || interaction.Response.Status > 599 {
		return fmt.Errorf("validate PACT file %q interaction %d: response status %d is invalid", filename, index, interaction.Response.Status)
	}
	return nil
}

func pactInteractionName(interaction pactInteraction) (string, error) {
	identifier := interaction.ID
	if identifier == "" {
		identifier = interaction.Key
	}
	if identifier != "" {
		return "pact:" + identifier, nil
	}
	if interaction.Description != "" {
		return "pact:" + interaction.Description, nil
	}

	seed, err := json.Marshal(struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}{Method: interaction.Request.Method, Path: interaction.Request.Path})
	if err != nil {
		return "", fmt.Errorf("encode fallback interaction name: %w", err)
	}
	digest := sha256.Sum256(seed)
	return "pact:" + hex.EncodeToString(digest[:])[:12], nil
}

func pactInteractionAttributes(interaction pactInteraction, consumer, provider string) map[string]string {
	attributes := make(map[string]string, 5)
	if consumer != "" {
		attributes[AttributeConsumerService] = consumer
	}
	if provider != "" {
		attributes[AttributeProviderService] = provider
	}
	attributes[AttributeEndpoint] = strings.ToUpper(interaction.Request.Method) + " " + pactEndpointPath(interaction.Request.Path)
	if interaction.Description != "" {
		attributes[AttributeInteraction] = interaction.Description
	}
	providerState := pactProviderStateName(interaction)
	if providerState != "" {
		attributes[AttributeProviderState] = providerState
	}
	return attributes
}

func pactEndpointPath(path string) string {
	parsed, err := url.Parse(path)
	if err == nil && parsed.Path != "" {
		return parsed.Path
	}
	return path
}

func pactProviderStateName(interaction pactInteraction) string {
	if interaction.ProviderState != "" {
		return interaction.ProviderState
	}
	if interaction.LegacyState != "" {
		return interaction.LegacyState
	}
	if len(interaction.ProviderStates) == 0 {
		return ""
	}
	names := make([]string, 0, len(interaction.ProviderStates))
	for _, state := range interaction.ProviderStates {
		if state.Name != "" {
			names = append(names, state.Name)
		}
	}
	return strings.Join(names, ", ")
}

func pactQueryString(rawQuery json.RawMessage) (string, error) {
	var query string
	if err := json.Unmarshal(rawQuery, &query); err == nil {
		return query, nil
	}

	var queryValues map[string]json.RawMessage
	if err := json.Unmarshal(rawQuery, &queryValues); err != nil {
		return "", fmt.Errorf("decode Pact query: %w", err)
	}
	values := make(url.Values, len(queryValues))
	keys := slices.Sorted(maps.Keys(queryValues))
	for _, key := range keys {
		items, err := pactRawStringValues(queryValues[key])
		if err != nil {
			return "", fmt.Errorf("decode Pact query parameter %q: %w", key, err)
		}
		for _, item := range items {
			values.Add(key, item)
		}
	}
	return values.Encode(), nil
}

func pactRawStringValues(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var scalar any
	if err := decoder.Decode(&scalar); err == nil {
		switch value := scalar.(type) {
		case json.Number:
			return []string{value.String()}, nil
		case bool:
			return []string{strconv.FormatBool(value)}, nil
		}
	}
	var multiple []json.RawMessage
	if err := json.Unmarshal(raw, &multiple); err != nil {
		return nil, err
	}
	values := make([]string, len(multiple))
	for index, item := range multiple {
		if err := json.Unmarshal(item, &values[index]); err != nil {
			var value any
			if err := json.Unmarshal(item, &value); err != nil {
				return nil, err
			}
			values[index] = fmt.Sprint(value)
		}
	}
	return values, nil
}

func pactBodyBytes(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, err
		}
		return []byte(value), nil
	}
	if !json.Valid(trimmed) {
		return nil, errors.New("body is not valid JSON")
	}
	return bytes.Clone(trimmed), nil
}

func verifyPactResponse(
	expected pactHTTPResponse,
	actual *dsl.HTTPResponse,
) dsl.MatchResult {
	result := dsl.MatchResult{
		Matched:        true,
		Kind:           dsl.MatchNone,
		ExpectedStatus: expected.Status,
	}
	if actual == nil {
		result.Matched = false
		result.Kind = dsl.MatchUnknown
		result.Mismatch = errors.New("response was not received")
		result.MismatchCount = 1
		return result
	}
	result.ActualStatus = actual.StatusCode

	type mismatch struct {
		kind dsl.MatchKind
		err  error
	}
	var mismatches []mismatch
	if actual.StatusCode != expected.Status {
		mismatches = append(mismatches, mismatch{
			kind: dsl.MatchStatus,
			err:  fmt.Errorf("status: expected %d, got %d", expected.Status, actual.StatusCode),
		})
	}
	if err := matchPactHeaders(expected.Headers, actual.Headers, expected.rules); err != nil {
		mismatches = append(mismatches, mismatch{kind: dsl.MatchHeader, err: err})
	}
	if err := matchPactCookies(expected.Cookies, actual.Cookies); err != nil {
		mismatches = append(mismatches, mismatch{kind: dsl.MatchCookie, err: err})
	}
	if err := matchPactBody(expected, actual.Body); err != nil {
		mismatches = append(mismatches, mismatch{kind: pactBodyMismatchKind(expected.Body), err: err})
	}
	if len(mismatches) == 0 {
		return result
	}
	result.Matched = false
	result.Kind = mismatches[0].kind
	result.MismatchCount = len(mismatches)
	mismatchErrors := make([]error, len(mismatches))
	for index, current := range mismatches {
		mismatchErrors[index] = current.err
	}
	result.Mismatch = fmt.Errorf("PACT response mismatch: %w", errors.Join(mismatchErrors...))
	return result
}

func matchPactResponse(expected pactHTTPResponse, actual *dsl.HTTPResponse) error {
	return verifyPactResponse(expected, actual).Mismatch
}

func pactBodyMismatchKind(raw json.RawMessage) dsl.MatchKind {
	if raw == nil {
		return dsl.MatchUnknown
	}
	body, err := pactBodyBytes(raw)
	if err == nil {
		if _, err := decodePactJSON(body); err == nil {
			return dsl.MatchJSONBody
		}
	}
	return dsl.MatchTextBody
}

func matchPactCookies(expectedRaw json.RawMessage, actual map[string][]dsl.ResponseCookie) error {
	trimmed := bytes.TrimSpace(expectedRaw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var expected map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &expected); err != nil {
		return fmt.Errorf("cookies: invalid expectation: %w", err)
	}
	keys := slices.Sorted(maps.Keys(expected))
	var mismatches []error
	for _, key := range keys {
		expectedValues, err := pactRawStringValues(expected[key])
		if err != nil {
			mismatches = append(mismatches, fmt.Errorf("cookie %q expected value: %w", key, err))
			continue
		}
		actualValues := actual[key]
		if len(actualValues) == 0 {
			mismatches = append(mismatches, fmt.Errorf("cookie %q: expected value, cookie was missing", key))
			continue
		}
		if len(expectedValues) == 0 {
			continue
		}
		matched := false
		for _, actualValue := range actualValues {
			if actualValue.Value == expectedValues[0] {
				matched = true
				break
			}
		}
		if !matched {
			actualValue := actualValues[0].Value
			mismatches = append(mismatches, fmt.Errorf("cookie %q: expected %q, got %q", key, expectedValues[0], actualValue))
		}
	}
	return errors.Join(mismatches...)
}

func matchPactHeaders(expected map[string]json.RawMessage, actual map[string]string, rules map[string]pactRule) error {
	keys := slices.Sorted(maps.Keys(expected))
	var mismatches []error
	for _, key := range keys {
		expectedValues, err := pactRawStringValues(expected[key])
		if err != nil {
			mismatches = append(mismatches, fmt.Errorf("header %q expected value: %w", key, err))
			continue
		}
		expectedValue := normalizePactHeaderValue(strings.Join(expectedValues, ", "))
		actualValue, found := pactHeaderValue(actual, key)
		if !found {
			mismatches = append(mismatches, fmt.Errorf("header %q: expected %q, header was missing", key, expectedValue))
			continue
		}

		rule, hasRule := pactRuleAt(rules, "$.header."+key)
		if hasRule {
			if _, err := applyPactRule(rule, expectedValue, actualValue, "$.header."+key); err != nil {
				mismatches = append(mismatches, fmt.Errorf("header %q: %w", key, err))
			}
			continue
		}
		if expectedValue != normalizePactHeaderValue(actualValue) {
			mismatches = append(mismatches, fmt.Errorf("header %q: expected %q, got %q", key, expectedValue, actualValue))
		}
	}
	return errors.Join(mismatches...)
}

func pactHeaderValue(headers map[string]string, expectedKey string) (string, bool) {
	for key, value := range headers {
		if strings.EqualFold(key, expectedKey) {
			return value, true
		}
	}
	return "", false
}

func normalizePactHeaderValue(value string) string {
	parts := strings.Split(value, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return strings.Join(parts, ",")
}

func matchPactBody(expected pactHTTPResponse, actualBody any) error {
	if expected.Body == nil {
		return nil
	}
	expectedBytes, err := pactBodyBytes(expected.Body)
	if err != nil {
		return fmt.Errorf("body expectation: %w", err)
	}
	actualBytes, err := responseBodyBytes(actualBody)
	if err != nil {
		return fmt.Errorf("body received: %w", err)
	}
	if len(actualBytes) == 0 {
		if len(expectedBytes) == 0 {
			return nil
		}
		return fmt.Errorf("body: expected %q, body was empty", string(expectedBytes))
	}

	expectedValue, expectedJSONErr := decodePactJSON(expectedBytes)
	actualValue, actualJSONErr := decodePactJSON(actualBytes)
	if expectedJSONErr == nil {
		if actualJSONErr != nil {
			return fmt.Errorf("body: expected valid JSON, got invalid JSON: %w", actualJSONErr)
		}
		if err := matchPactJSONValue(expectedValue, actualValue, "$.body", expected.rules); err != nil {
			return fmt.Errorf("body: %w", err)
		}
		return nil
	}
	if rule, ok := pactRuleAt(expected.rules, "$.body"); ok {
		if _, err := applyPactRule(rule, string(expectedBytes), string(actualBytes), "$.body"); err != nil {
			return fmt.Errorf("body: %w", err)
		}
		return nil
	}
	if !bytes.Equal(expectedBytes, actualBytes) {
		return fmt.Errorf("body: expected %q, got %q", string(expectedBytes), string(actualBytes))
	}
	return nil
}

func responseBodyBytes(body any) ([]byte, error) {
	switch value := body.(type) {
	case nil:
		return nil, nil
	case string:
		return []byte(value), nil
	case []byte:
		return bytes.Clone(value), nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return encoded, nil
	}
}

func decodePactJSON(contents []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func matchPactJSONValue(expected, actual any, path string, rules map[string]pactRule) error {
	rule, hasRule := pactRuleAt(rules, path)
	if hasRule {
		fullyMatched, err := applyPactRule(rule, expected, actual, path)
		if err != nil {
			return err
		}
		if fullyMatched {
			return nil
		}
	}

	switch expectedValue := expected.(type) {
	case map[string]any:
		actualValue, ok := actual.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object, got %s", path, pactJSONType(actual))
		}
		if len(expectedValue) == 0 && len(actualValue) > 0 && !hasRule {
			return fmt.Errorf("%s: expected an empty object, got %d fields", path, len(actualValue))
		}
		keys := slices.Sorted(maps.Keys(expectedValue))
		for _, key := range keys {
			actualField, ok := actualValue[key]
			if !ok {
				return fmt.Errorf("%s.%s: field was missing", path, key)
			}
			if err := matchPactJSONValue(expectedValue[key], actualField, path+"."+key, rules); err != nil {
				return err
			}
		}
		return nil
	case []any:
		actualValue, ok := actual.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array, got %s", path, pactJSONType(actual))
		}
		allowExtra := hasRule && (strings.EqualFold(rule.Match, "type") || rule.Min != nil || rule.Max != nil)
		if !allowExtra && len(expectedValue) != len(actualValue) {
			return fmt.Errorf("%s: expected %d items, got %d", path, len(expectedValue), len(actualValue))
		}
		if len(actualValue) < len(expectedValue) {
			return fmt.Errorf("%s: expected at least %d items, got %d", path, len(expectedValue), len(actualValue))
		}
		for index, expectedItem := range expectedValue {
			if err := matchPactJSONValue(expectedItem, actualValue[index], path+"["+strconv.Itoa(index)+"]", rules); err != nil {
				return err
			}
		}
		return nil
	default:
		if !pactJSONEqual(expected, actual) {
			return fmt.Errorf("%s: expected %s, got %s", path, pactJSONValue(expected), pactJSONValue(actual))
		}
		return nil
	}
}

func applyPactRule(rule pactRule, expected, actual any, path string) (bool, error) {
	match := strings.ToLower(rule.Match)
	switch match {
	case "", "equality", "equalto":
		return false, nil
	case "type":
		if pactJSONType(expected) != pactJSONType(actual) {
			return false, fmt.Errorf("%s: expected type %s, got %s", path, pactJSONType(expected), pactJSONType(actual))
		}
		if err := checkPactCollectionBounds(rule, actual, path); err != nil {
			return false, err
		}
		if _, collection := actual.(map[string]any); collection {
			return false, nil
		}
		if _, collection := actual.([]any); collection {
			return false, nil
		}
		return true, nil
	case "regex", "regexp":
		pattern := rule.Regex
		if pattern == "" && rule.Value != nil {
			if err := json.Unmarshal(rule.Value, &pattern); err != nil {
				return false, fmt.Errorf("%s: invalid regex matcher value: %w", path, err)
			}
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return false, fmt.Errorf("%s: invalid regex %q: %w", path, pattern, err)
		}
		if !compiled.MatchString(pactStringValue(actual)) {
			return false, fmt.Errorf("%s: value %q did not match %q", path, pactStringValue(actual), pattern)
		}
		return true, nil
	case "integer":
		if number, ok := actual.(json.Number); !ok || !isPactInteger(number) {
			return false, fmt.Errorf("%s: expected an integer", path)
		}
		return true, nil
	case "decimal":
		if number, ok := actual.(json.Number); !ok || !isPactDecimal(number) {
			return false, fmt.Errorf("%s: expected a decimal", path)
		}
		return true, nil
	case "number":
		if _, ok := actual.(json.Number); !ok {
			return false, fmt.Errorf("%s: expected a number", path)
		}
		return true, nil
	case "boolean":
		if !isPactBoolean(actual) {
			return false, fmt.Errorf("%s: expected a boolean", path)
		}
		return true, nil
	case "null":
		if actual != nil {
			return false, fmt.Errorf("%s: expected null", path)
		}
		return true, nil
	case "include":
		var value string
		if err := json.Unmarshal(rule.Value, &value); err != nil {
			return false, fmt.Errorf("%s: invalid include matcher: %w", path, err)
		}
		if !strings.Contains(pactStringValue(actual), value) {
			return false, fmt.Errorf("%s: value %q does not contain %q", path, pactStringValue(actual), value)
		}
		return true, nil
	case "arraycontains":
		actualArray, ok := actual.([]any)
		if !ok {
			return false, fmt.Errorf("%s: expected an array", path)
		}
		for index, variant := range rule.Variants {
			variantValue, err := decodePactJSON(variant)
			if err != nil {
				return false, fmt.Errorf("%s: invalid array variant %d: %w", path, index, err)
			}
			matched := false
			for _, item := range actualArray {
				if matchErr := matchPactJSONValue(variantValue, item, path, nil); matchErr == nil {
					matched = true
					break
				}
			}
			if !matched {
				return false, fmt.Errorf("%s: array variant %d was not found", path, index)
			}
		}
		return true, nil
	case "values":
		expectedMap, expectedOK := expected.(map[string]any)
		actualMap, actualOK := actual.(map[string]any)
		if !expectedOK || !actualOK || len(expectedMap) != len(actualMap) {
			return false, fmt.Errorf("%s: expected maps with the same number of values", path)
		}
		remaining := make([]any, 0, len(actualMap))
		for _, value := range actualMap {
			remaining = append(remaining, value)
		}
		for _, expectedValue := range expectedMap {
			found := false
			for index, actualValue := range remaining {
				if pactJSONEqual(expectedValue, actualValue) {
					remaining = append(remaining[:index], remaining[index+1:]...)
					found = true
					break
				}
			}
			if !found {
				return false, fmt.Errorf("%s: expected value was not found", path)
			}
		}
		return true, nil
	case "notempty":
		if actual == nil || pactStringValue(actual) == "" {
			return false, fmt.Errorf("%s: value must not be empty", path)
		}
		return true, nil
	default:
		return false, fmt.Errorf("%s: unsupported matcher %q", path, rule.Match)
	}
}

func checkPactCollectionBounds(rule pactRule, value any, path string) error {
	length := -1
	switch collection := value.(type) {
	case []any:
		length = len(collection)
	case map[string]any:
		length = len(collection)
	}
	if length < 0 {
		return nil
	}
	if rule.Min != nil && length < *rule.Min {
		return fmt.Errorf("%s: expected at least %d items, got %d", path, *rule.Min, length)
	}
	if rule.Max != nil && length > *rule.Max {
		return fmt.Errorf("%s: expected at most %d items, got %d", path, *rule.Max, length)
	}
	return nil
}

func pactJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func pactJSONEqual(expected, actual any) bool {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualValue, ok := actual.(map[string]any)
		if !ok || len(expectedValue) != len(actualValue) {
			return false
		}
		for key, value := range expectedValue {
			actualField, ok := actualValue[key]
			if !ok || !pactJSONEqual(value, actualField) {
				return false
			}
		}
		return true
	case []any:
		actualValue, ok := actual.([]any)
		if !ok || len(expectedValue) != len(actualValue) {
			return false
		}
		for index, value := range expectedValue {
			if !pactJSONEqual(value, actualValue[index]) {
				return false
			}
		}
		return true
	case json.Number:
		actualValue, ok := actual.(json.Number)
		if !ok {
			return false
		}
		expectedFloat, expectedErr := expectedValue.Float64()
		actualFloat, actualErr := actualValue.Float64()
		return expectedErr == nil && actualErr == nil && expectedFloat == actualFloat
	default:
		return expected == actual
	}
}

func pactJSONValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

func pactStringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	default:
		return pactJSONValue(value)
	}
}

func isPactInteger(value json.Number) bool {
	if _, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
		return true
	}
	parsed, err := strconv.ParseFloat(value.String(), 64)
	return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && math.Trunc(parsed) == parsed
}

func isPactDecimal(value json.Number) bool {
	parsed, err := strconv.ParseFloat(value.String(), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || math.Trunc(parsed) == parsed {
		return false
	}
	return true
}

func isPactBoolean(value any) bool {
	if _, ok := value.(bool); ok {
		return true
	}
	if stringValue, ok := value.(string); ok {
		_, err := strconv.ParseBool(stringValue)
		return err == nil
	}
	return false
}

func flattenPactRules(raw map[string]json.RawMessage) (map[string]pactRule, error) {
	rules := make(map[string]pactRule)
	keys := slices.Sorted(maps.Keys(raw))
	for _, key := range keys {
		if err := collectPactRules(rules, key, raw[key], ""); err != nil {
			return nil, err
		}
	}
	return rules, nil
}

func collectPactRules(rules map[string]pactRule, path string, raw json.RawMessage, category string) error {
	rule, isRule, err := decodePactRule(raw)
	if err != nil {
		return fmt.Errorf("decode matcher %q: %w", path, err)
	}
	if isRule {
		addPactRule(rules, normalizePactRulePath(path, category), rule)
		return nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("matcher %q is not an object: %w", path, err)
	}
	nestedCategory := category
	if category == "" && isPactRuleCategory(path) {
		nestedCategory = path
	}
	keys := slices.Sorted(maps.Keys(object))
	for _, key := range keys {
		nestedPath := key
		if !strings.HasPrefix(key, "$") && nestedCategory == "" && path != "" {
			nestedPath = path + "." + key
		}
		if err := collectPactRules(rules, nestedPath, object[key], nestedCategory); err != nil {
			return err
		}
	}
	return nil
}

func decodePactRule(raw json.RawMessage) (pactRule, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return pactRule{}, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return pactRule{}, false, err
	}
	if _, ok := fields["match"]; ok {
		var rule pactRule
		if err := json.Unmarshal(trimmed, &rule); err != nil {
			return pactRule{}, false, err
		}
		return rule, true, nil
	}
	if matchers, ok := fields["matchers"]; ok {
		var rules []pactRule
		if err := json.Unmarshal(matchers, &rules); err != nil {
			return pactRule{}, false, err
		}
		if len(rules) == 0 {
			return pactRule{}, false, errors.New("matcher list is empty")
		}
		return rules[0], true, nil
	}
	return pactRule{}, false, nil
}

func addPactRule(rules map[string]pactRule, path string, rule pactRule) {
	rules[path] = rule
	if strings.HasPrefix(path, "$.header.") {
		parts := strings.SplitN(path, ".", 3)
		if len(parts) == 3 {
			rules["$.header."+strings.ToLower(parts[2])] = rule
		}
	}
}

func normalizePactRulePath(path, category string) string {
	if category == "" {
		if strings.HasPrefix(path, "$") {
			return path
		}
		return "$." + path
	}
	if category == "headers" {
		category = "header"
	}
	if path == "$" {
		return "$." + category
	}
	if strings.HasPrefix(path, "$."+category+".") || path == "$."+category {
		return path
	}
	if strings.HasPrefix(path, "$.") {
		return "$." + category + path[1:]
	}
	return "$." + category + "." + path
}

func isPactRuleCategory(value string) bool {
	switch strings.ToLower(value) {
	case "body", "header", "headers", "path", "query", "status":
		return true
	default:
		return false
	}
}

func pactRuleAt(rules map[string]pactRule, path string) (pactRule, bool) {
	if len(rules) == 0 {
		return pactRule{}, false
	}
	pathSegments := pactPathSegments(path)
	bestScore := -1
	var best pactRule
	found := false
	for rulePath, rule := range rules {
		ruleSegments := pactPathSegments(rulePath)
		if len(ruleSegments) != len(pathSegments) {
			continue
		}
		score := 0
		matches := true
		for index := range pathSegments {
			if ruleSegments[index] == "*" {
				score++
				continue
			}
			if index >= 2 && strings.EqualFold(ruleSegments[index], pathSegments[index]) && strings.EqualFold(pathSegments[1], "header") {
				score += 2
				continue
			}
			if ruleSegments[index] != pathSegments[index] {
				matches = false
				break
			}
			score += 2
		}
		if matches && score > bestScore {
			bestScore = score
			best = rule
			found = true
		}
	}
	return best, found
}

func pactPathSegments(path string) []string {
	if path == "$" {
		return []string{"$"}
	}
	if !strings.HasPrefix(path, "$") {
		return nil
	}
	segments := []string{"$"}
	for index := 1; index < len(path); {
		switch path[index] {
		case '.':
			start := index + 1
			index = start
			for index < len(path) && path[index] != '.' && path[index] != '[' {
				index++
			}
			if start < index {
				segments = append(segments, path[start:index])
			}
		case '[':
			end := strings.IndexByte(path[index:], ']')
			if end < 0 {
				return nil
			}
			value := path[index+1 : index+end]
			value = strings.Trim(value, "'\"")
			if value == "" {
				value = "*"
			}
			segments = append(segments, value)
			index += end + 1
		default:
			return nil
		}
	}
	return segments
}
