// This file verifies deterministic and atomic benchmark manifest publication.
package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.k6.io/k6/lib/netext/httpext"
	benchmarkpkg "k6-as-a-library/internal/benchmark"
	"k6-as-a-library/internal/dsl"
)

func TestWriteBenchmarkManifestPublishesCanonicalValidatedBenchmark(t *testing.T) {
	t.Parallel()

	target, err := httpext.NewURL("http://provider.example/base?source=direct", "direct")
	if err != nil {
		t.Fatalf("create target URL: %v", err)
	}
	execution, err := synthesizeBenchmark(defaultRunConfig(), &target, nil)
	if err != nil {
		t.Fatalf("synthesize benchmark: %v", err)
	}
	benchmark := execution.validated.Benchmark()
	benchmark.Cases[0].Request.Headers = []dsl.Header{}
	benchmark.Cases[0].Request.HeadersPresence = dsl.PresenceValue
	filename := filepath.Join(t.TempDir(), "benchmark-manifest.json")

	if err := benchmarkpkg.WriteManifest(filename, benchmark); err != nil {
		t.Fatalf("write benchmark manifest: %v", err)
	}
	encoded := mustReadFile(t, filename)
	canonical, err := dsl.MarshalBenchmarkManifest(benchmark)
	if err != nil {
		t.Fatalf("marshal expected benchmark: %v", err)
	}
	want := append(append([]byte(nil), canonical...), '\n')
	if !bytes.Equal(encoded, want) {
		t.Fatalf("benchmark manifest differs from canonical encoding\ngot:\n%s\nwant:\n%s", encoded, want)
	}
	decoded, err := dsl.UnmarshalBenchmarkManifest(encoded)
	if err != nil {
		t.Fatalf("decode benchmark manifest: %v", err)
	}
	if decoded.Cases[0].Request.HeadersPresence != dsl.PresenceValue || len(decoded.Cases[0].Request.Headers) != 0 {
		t.Fatalf("explicit empty headers were not preserved: %#v", decoded.Cases[0].Request)
	}
	assertNoTemporaryBenchmarkManifests(t, filename)
}

func TestWriteBenchmarkManifestPreservesExistingArtifactOnInvalidBenchmark(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "benchmark-manifest.json")
	existing := []byte("existing manifest\n")
	if err := os.WriteFile(filename, existing, 0o600); err != nil {
		t.Fatalf("write existing benchmark manifest: %v", err)
	}

	err := benchmarkpkg.WriteManifest(filename, dsl.SynthesizedBenchmark{})
	if err == nil {
		t.Fatal("write invalid benchmark manifest unexpectedly succeeded")
	}
	if got := mustReadFile(t, filename); !bytes.Equal(got, existing) {
		t.Fatalf("existing benchmark manifest changed after failed publication: %q", got)
	}
	assertNoTemporaryBenchmarkManifests(t, filename)
}

func assertBenchmarkManifestMatchesExecution(
	t *testing.T,
	filename string,
	expected dsl.SynthesizedBenchmark,
	forbiddenValues ...string,
) dsl.SynthesizedBenchmark {
	t.Helper()
	if err := benchmarkpkg.ValidateManifest(filename); err != nil {
		t.Fatalf("validate benchmark manifest: %v", err)
	}
	encoded := mustReadFile(t, filename)
	canonical, err := dsl.MarshalBenchmarkManifest(expected)
	if err != nil {
		t.Fatalf("marshal expected benchmark: %v", err)
	}
	want := append(append([]byte(nil), canonical...), '\n')
	if !bytes.Equal(encoded, want) {
		t.Fatalf("published benchmark manifest differs from the validated benchmark\ngot:\n%s\nwant:\n%s", encoded, want)
	}
	for _, forbidden := range forbiddenValues {
		if forbidden != "" && strings.Contains(string(encoded), forbidden) {
			t.Errorf("benchmark manifest contains bound runtime value %q", forbidden)
		}
	}
	decoded, err := dsl.UnmarshalBenchmarkManifest(encoded)
	if err != nil {
		t.Fatalf("decode benchmark manifest: %v", err)
	}
	assertNoTemporaryBenchmarkManifests(t, filename)
	return decoded
}

func assertNoTemporaryBenchmarkManifests(t *testing.T, filename string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(filename), "."+filepath.Base(filename)+".tmp-*"))
	if err != nil {
		t.Fatalf("find temporary benchmark manifests: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary benchmark manifests remain: %v", matches)
	}
}
