package main

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPackageDependencyBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		directory string
		forbidden []string
	}{
		{directory: "internal/dsl", forbidden: []string{"go.k6.io/", "go.opentelemetry.io/", "k6-as-a-library/internal/pact", "k6-as-a-library/internal/report", "k6-as-a-library/internal/otel"}},
		{directory: "internal/pact", forbidden: []string{"go.k6.io/", "go.opentelemetry.io/", "k6-as-a-library/internal/benchmark", "k6-as-a-library/internal/report", "k6-as-a-library/internal/otel"}},
		{directory: "internal/benchmark", forbidden: []string{"k6-as-a-library/internal/pact", "k6-as-a-library/internal/report"}},
		{directory: "internal/report", forbidden: []string{"go.opentelemetry.io/", "k6-as-a-library/internal/pact", "k6-as-a-library/internal/otel"}},
		{directory: "internal/otel", forbidden: []string{"k6-as-a-library/internal/app", "k6-as-a-library/internal/benchmark", "k6-as-a-library/internal/dsl", "k6-as-a-library/internal/pact", "k6-as-a-library/internal/report"}},
		{directory: "internal/app", forbidden: []string{"go.opentelemetry.io/", "k6-as-a-library/internal/otel", "go.k6.io/k6/lib/executor", "go.k6.io/k6/lib/netext/httpext"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.directory, func(t *testing.T) {
			t.Parallel()
			assertPackageImports(t, test.directory, test.forbidden)
		})
	}
}

func assertPackageImports(t *testing.T, directory string, forbidden []string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatalf("list Go files in %s: %v", directory, err)
	}
	set := token.NewFileSet()
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(set, filename, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("decode import in %s: %v", filename, err)
			}
			for _, prefix := range forbidden {
				if strings.HasPrefix(path, prefix) {
					position := set.Position(imported.Pos())
					t.Errorf("%s imports forbidden dependency %q", position, path)
				}
			}
		}
	}
}
