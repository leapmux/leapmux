package providerkittest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

// WorkspaceTrustQuestionPayload builds the JSON payload that WorkspaceTrust publishes.
func WorkspaceTrustQuestionPayload(t testing.TB, path string, configuration any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		contracts.WorkspaceTrustFieldType:          contracts.WorkspaceTrustControlTypeQuestion,
		contracts.WorkspaceTrustFieldPath:          path,
		contracts.WorkspaceTrustFieldConfiguration: configuration,
	})
	require.NoError(t, err)
	return payload
}
