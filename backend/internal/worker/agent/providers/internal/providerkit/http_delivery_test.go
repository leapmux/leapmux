package providerkit

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestClassifyHTTPDeliveryError(t *testing.T) {
	t.Parallel()
	assert.NoError(t, ClassifyHTTPDeliveryError("approval", nil))
	refusal := &HTTPStatusError{StatusCode: http.StatusBadRequest, Body: "the request is invalid"}
	err := ClassifyHTTPDeliveryError("approval", fmt.Errorf("native reply: %w", refusal))
	assert.ErrorIs(t, err, refusal)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	err = ClassifyHTTPDeliveryError("approval", fmt.Errorf("native transport: %w", io.EOF))
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.ErrorIs(t, err, io.EOF)
	assert.Contains(t, err.Error(), "approval")
	assert.False(t, errors.Is(err, refusal))
}
