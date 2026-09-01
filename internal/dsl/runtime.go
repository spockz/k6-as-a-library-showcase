package dsl

import (
	"context"
	"errors"
	"fmt"
)

func (request RequestSpec) WithRuntime(runtime RequestRuntime, description BehaviorDescription) RequestSpec {
	result := cloneRequest(request)
	result.runtime = &RequestRuntime{Materialize: runtime.Materialize, Match: runtime.Match}
	result.Behavior = cloneBehaviorDescription(&description)
	return result
}

func (request RequestSpec) Materialize(ctx context.Context) (RequestSpec, error) {
	if ctx == nil {
		return RequestSpec{}, errors.New("materialize request: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return RequestSpec{}, fmt.Errorf("materialize request: %w", err)
	}

	input := cloneRequest(request)
	if request.runtime == nil || request.runtime.Materialize == nil {
		return input, nil
	}
	input.runtime = nil
	materialized, err := request.runtime.Materialize(ctx, input)
	if err != nil {
		return RequestSpec{}, fmt.Errorf("materialize request: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return RequestSpec{}, fmt.Errorf("materialize request: %w", err)
	}

	result := cloneRequest(materialized)
	result.runtime = &RequestRuntime{Match: request.runtime.Match}
	result.Behavior = cloneBehaviorDescription(request.Behavior)
	return result, nil
}

func (request RequestSpec) Match(ctx context.Context, response *HTTPResponse) (MatchResult, error) {
	if ctx == nil {
		return MatchResult{}, errors.New("match response: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return MatchResult{}, fmt.Errorf("match response: %w", err)
	}
	if request.runtime == nil || request.runtime.Match == nil {
		return MatchResult{Matched: true, Kind: MatchNone}, nil
	}

	result, err := request.runtime.Match(ctx, cloneHTTPResponse(response))
	if err != nil {
		return MatchResult{}, fmt.Errorf("match response: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return MatchResult{}, fmt.Errorf("match response: %w", err)
	}
	return result, nil
}

func (request RequestSpec) WithoutRuntime() RequestSpec {
	result := cloneRequest(request)
	result.runtime = nil
	return result
}

func cloneHTTPResponse(response *HTTPResponse) *HTTPResponse {
	if response == nil {
		return nil
	}
	result := &HTTPResponse{
		StatusCode: response.StatusCode,
		Headers:    make(map[string]string, len(response.Headers)),
		Cookies:    make(map[string][]ResponseCookie, len(response.Cookies)),
		Body:       append([]byte(nil), response.Body...),
	}
	for name, value := range response.Headers {
		result.Headers[name] = value
	}
	for name, values := range response.Cookies {
		result.Cookies[name] = append([]ResponseCookie(nil), values...)
	}
	return result
}
