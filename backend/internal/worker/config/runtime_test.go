package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

func TestValidateRuntime(t *testing.T) {
	for _, test := range []struct {
		name    string
		size    int
		failure string
	}{
		{name: "default"},
		{name: "negative default", size: -1},
		{name: "minimum", size: contracts.MaxPlaintextPerChunk},
		{name: "maximum", size: contracts.MaxConfigurableMessageSize},
		{name: "below minimum", size: 1, failure: "max_message_size"},
		{name: "above maximum", size: contracts.MaxConfigurableMessageSize + 1, failure: "max_message_size"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "not-created")
			cfg := Config{DataDir: path, MaxMessageSize: test.size}
			err := cfg.ValidateRuntime()
			if test.failure == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.failure)
			}
			assert.NoDirExists(t, path, "runtime validation must not create resources")
		})
	}
}
