package droid

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegistrationDoesNotOfferAnUnknownCustomModel(t *testing.T) {
	t.Parallel()
	// The CLI builds custom model IDs from the user's settings. The worker
	// cannot choose one before it reads the running session's catalog.
	assert.Empty(t, Registration().DefaultModels)
}
