package app

import (
	"path/filepath"

	"k6-as-a-library/internal/pact"
)

const (
	pactConsumerTag             = pact.AttributeConsumerService
	pactProviderTag             = pact.AttributeProviderService
	pactEndpointTag             = pact.AttributeEndpoint
	pactInteractionTag          = pact.AttributeInteraction
	pactProviderStateTag        = pact.AttributeProviderState
	pactMismatchMetadata        = pact.MismatchMetadata
	pactResponseCheckName       = pact.ResponseCheckName
	pactResponsesValidThreshold = "rate==1"
	pactProviderStateHeader     = pact.ProviderStateHeader
)

var pactResponseCheckSubmetric = "check:" + pact.ResponseCheckName

func loadPactDirectory(directory string) ([]pact.Interaction, error) {
	return pact.LoadDirectory(directory)
}

func pactFixtureDirectory() string {
	return filepath.Join("..", "..", "testdata", "pacts")
}
