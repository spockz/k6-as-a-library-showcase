// This file publishes the exact synthesized benchmark for reproducible inspection.
package benchmark

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"k6-as-a-library/internal/artifact"
	"k6-as-a-library/internal/dsl"
)

func WriteManifest(filename string, benchmark dsl.SynthesizedBenchmark) error {
	return artifact.PublishAtomically(filename, ValidateManifest, func(writer io.Writer) error {
		encoded, err := dsl.MarshalBenchmarkManifest(benchmark)
		if err != nil {
			return fmt.Errorf("marshal benchmark manifest: %w", err)
		}
		if _, err := writer.Write(encoded); err != nil {
			return fmt.Errorf("write benchmark manifest: %w", err)
		}
		if len(encoded) == 0 || encoded[len(encoded)-1] != '\n' {
			if _, err := writer.Write([]byte{'\n'}); err != nil {
				return fmt.Errorf("write benchmark manifest newline: %w", err)
			}
		}
		return nil
	})
}

func ValidateManifest(filename string) (err error) {
	file, err := artifact.OpenForValidation(filename)
	if err != nil {
		return fmt.Errorf("open benchmark manifest %q: %w", filename, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close benchmark manifest %q: %w", filename, closeErr))
		}
	}()

	encoded, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read benchmark manifest %q: %w", filename, err)
	}
	if !bytes.HasSuffix(encoded, []byte{'\n'}) {
		return fmt.Errorf("benchmark manifest %q does not end with a newline", filename)
	}
	if _, err := dsl.UnmarshalBenchmarkManifest(encoded); err != nil {
		return fmt.Errorf("unmarshal benchmark manifest %q: %w", filename, err)
	}
	return nil
}
