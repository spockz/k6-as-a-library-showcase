package app

import (
	"io"

	"k6-as-a-library/internal/artifact"
)

func validateK6JSONArtifact(filename string) error {
	return artifact.ValidateK6JSON(filename)
}

func validateGeneratedHTMLArtifact(filename string) error {
	return artifact.ValidateHTML(filename)
}

func validateGeneratedHTMLContents(contents []byte) error {
	return artifact.ValidateHTMLContents(contents)
}

func publishArtifactAtomically(filename string, validate func(string) error, generate func(io.Writer) error) error {
	return artifact.PublishAtomically(filename, validate, generate)
}
