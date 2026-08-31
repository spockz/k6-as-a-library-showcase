package dsl

import (
	"maps"
	"net/url"
	"slices"
)

func ParametersFromQuery(values url.Values) []Parameter {
	keys := slices.Sorted(maps.Keys(values))
	result := make([]Parameter, 0)
	for _, key := range keys {
		for _, value := range values[key] {
			result = append(result, Parameter{Name: key, Value: value})
		}
	}
	return result
}
