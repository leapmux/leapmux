package config

import "fmt"

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
	if state == nil || !state.Registered() {
		return nil, fmt.Errorf("worker is unregistered: run `leapmux worker register` first")
	}
	if !state.HasKeypair() {
		return nil, fmt.Errorf("%w: the keypair is missing", ErrUnusableRegisteredKeypair)
	}
	if _, err := state.CompositeKeypair(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnusableRegisteredKeypair, err)
	}
	return state, nil
}
