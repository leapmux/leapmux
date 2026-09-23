package providerkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWarnUnmarshal(t *testing.T) {
	t.Parallel()

	var ok struct {
		Method string `json:"method"`
	}
	assert.True(t, WarnUnmarshal([]byte(`{"method":"m"}`), &ok, "test"), "valid JSON decodes and returns true")
	assert.Equal(t, "m", ok.Method)

	var bad struct{}
	assert.False(t, WarnUnmarshal([]byte(`not json`), &bad, "test"), "malformed JSON returns false")
}
