package app

import (
	"path/filepath"

	"k6-as-a-library/internal/pact"
)

const (
	pactConsumerTag       = pact.ConsumerTag
	pactProviderTag       = pact.ProviderTag
	pactEndpointTag       = pact.EndpointTag
	pactInteractionTag    = pact.InteractionTag
	pactProviderStateTag  = pact.ProviderStateTag
	pactMismatchMetadata  = pact.MismatchMetadata
	pactResponseCheckName = pact.ResponseCheckName
)

type pactInteraction = pact.Interaction
type pactHTTPRequest = pact.HTTPRequest
type pactHTTPResponse = pact.HTTPResponse

func loadPactDirectory(directory string) ([]pact.Interaction, error) {
	return pact.LoadDirectory(directory)
}

func pactFixtureDirectory() string {
	return filepath.Join("..", "..", "testdata", "pacts")
}
