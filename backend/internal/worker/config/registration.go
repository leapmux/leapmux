package config

import (
	"fmt"

	"github.com/leapmux/leapmux/internal/noise"
)

// Registered reports whether the state holds a complete registration:
// the endpoint, the identity, and the token. A state that fails this
// check is not a registration a runtime command may start from, and the
// register command owns producing it.
func (s *State) Registered() bool {
	return s != nil && s.HubURL != "" && s.WorkerID != "" && s.AuthToken != ""
}

// UsableRegistration reports whether the saved registration can run:
// complete credentials AND a keypair that restores. A registered state
// whose keypair is missing or broken is an error rather than a
// regenerate-and-continue, because the Hub has pinned keys this machine
// can no longer reproduce.
func (c *Config) UsableRegistration() (*State, error) {
	state, err := c.LoadState()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	if _, err := state.UsableKeypair(); err != nil {
		return nil, err
	}
	return state, nil
}

// UsableKeypair validates this registration snapshot and returns its restored
// keypair. It never reloads or writes saved credentials.
func (s *State) UsableKeypair() (*noise.CompositeKeypair, error) {
	if !s.Registered() {
		return nil, fmt.Errorf("worker is unregistered: run `leapmux worker register` first")
	}
	if !s.HasKeypair() {
		return nil, fmt.Errorf("%w: the keypair is missing", ErrUnusableRegisteredKeypair)
	}
	keypair, err := s.CompositeKeypair()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnusableRegisteredKeypair, err)
	}
	return keypair, nil
}
