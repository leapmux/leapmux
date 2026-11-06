package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registeredState builds a config holding a state the fill function
// shapes. The keypair generates FIRST -- a state that already holds a
// token refuses to generate one -- and the credentials stamp after it.
func registeredState(t *testing.T, fill func(*State)) *Config {
	t.Helper()
	cfg := &Config{DataDir: t.TempDir()}
	state := &State{}
	// The keypair generates FIRST: a state that already holds a token
	// refuses to generate one, and the fills below stamp credentials.
	_, _, err := state.EnsureCompositeKeypair()
	require.NoError(t, err)
	if fill != nil {
		fill(state)
	}
	require.NoError(t, cfg.SaveState(state))
	return cfg
}

// Registered is exactly the complete-credential predicate, and
// UsableRegistration is the runtime gate: incomplete credentials refuse,
// and so does a registered state whose keypair cannot restore.
func TestRegisteredAndUsableRegistration(t *testing.T) {
	t.Run("complete credentials are registered", func(t *testing.T) {
		cfg := registeredState(t, func(s *State) {
			s.HubURL, s.WorkerID, s.AuthToken = "http://hub.example", "w1", "tok"
		})
		state, err := cfg.UsableRegistration()
		require.NoError(t, err)
		assert.True(t, state.Registered())
	})

	t.Run("each missing credential refuses", func(t *testing.T) {
		for _, tc := range []struct {
			missing string
			fill    func(*State)
		}{
			{"hub", func(s *State) { s.WorkerID, s.AuthToken = "w1", "tok" }},
			{"worker", func(s *State) { s.HubURL, s.AuthToken = "http://hub.example", "tok" }},
			{"token", func(s *State) { s.HubURL, s.WorkerID = "http://hub.example", "w1" }},
		} {
			cfg := registeredState(t, tc.fill)
			_, err := cfg.UsableRegistration()
			require.Error(t, err, "a state without its %s refuses", tc.missing)
		}
	})

	t.Run("a registered state with a broken keypair refuses, not regenerates", func(t *testing.T) {
		cfg := registeredState(t, func(s *State) {
			s.HubURL, s.WorkerID, s.AuthToken = "http://hub.example", "w1", "tok"
			s.X25519PrivateKey = "not!valid!base64"
		})
		_, err := cfg.UsableRegistration()
		require.ErrorIs(t, err, ErrUnusableRegisteredKeypair)
	})

	t.Run("no state at all refuses", func(t *testing.T) {
		cfg := &Config{DataDir: t.TempDir()}
		_, err := cfg.UsableRegistration()
		require.Error(t, err)
	})
}
