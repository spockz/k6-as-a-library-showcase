// This file isolates payload values because encoded request bodies need one decoding boundary.
package dsl

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Bytes decodes a payload into the bytes an execution adapter should send.
func (payload Payload) Bytes() ([]byte, error) {
	if payload.ContentPresence == PresenceNull || (payload.contentDecoded && payload.ContentPresence != PresenceValue) {
		return nil, fmt.Errorf("payload content is missing or null")
	}
	switch payload.Encoding {
	case PayloadEncodingText, PayloadEncodingJSON:
		if payload.Encoding == PayloadEncodingJSON && !json.Valid([]byte(payload.Content)) {
			return nil, fmt.Errorf("JSON payload is invalid")
		}
		return []byte(payload.Content), nil
	case PayloadEncodingBase64:
		decoded, err := base64.StdEncoding.DecodeString(payload.Content)
		if err != nil {
			return nil, fmt.Errorf("decode base64 payload: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unknown payload encoding %q", payload.Encoding)
	}
}

// HasBody reports whether a request or expectation has a concrete payload.
func (request RequestSpec) HasBody() bool {
	return request.Body != nil
}

// RequiresBody reports whether a response body must be retained for matching.
func (expectation ResponseExpectation) RequiresBody() bool {
	return expectation.Body != nil
}

// Key returns the stable operation ID when present, otherwise a normalized
// method/path identity.
func (operation OperationRef) Key() string {
	if operation.ID != "" {
		return operation.ID
	}
	return operation.Method + " " + operation.Path
}
