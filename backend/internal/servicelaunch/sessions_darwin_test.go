//go:build darwin

package servicelaunch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The darwin monitor parses the who output into the account's sessions,
// and a failed who is an error, never an empty list.
func TestDarwinSessionMonitor(t *testing.T) {
	t.Run("parses console and remote sessions", func(t *testing.T) {
		m := newDarwinSessionMonitor()
		m.runWho = func() ([]byte, error) {
			return []byte("trustin console  Nov 6 09:00\ntrustin ttys001 Nov 6 09:15\nother ttys002  Nov 6 09:20\n"), nil
		}
		sessions, err := m.Sessions(AccountRef{ID: "501", Name: "trustin"})
		require.NoError(t, err)
		// Two of the three lines belong to trustin.
		assert.Len(t, sessions, 2)
		for _, s := range sessions {
			assert.Equal(t, SessionInteractive, s.Kind)
		}
	})

	t.Run("a failed who is an error", func(t *testing.T) {
		m := newDarwinSessionMonitor()
		m.runWho = func() ([]byte, error) { return nil, assert.AnError }
		_, err := m.Sessions(AccountRef{ID: "501", Name: "trustin"})
		require.Error(t, err)
	})
}
