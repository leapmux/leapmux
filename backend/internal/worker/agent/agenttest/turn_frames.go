package agenttest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The turn flag is the input queue's only dispatch guard and the client's only
// activity signal, and it LATCHES: a turn armed with nothing to end it holds
// every message the user sends -- queued, unpaused, with no visible cause --
// and runs a thinking indicator that nothing stops, until the process exits.
//
// So each provider NAMES the frames that move that flag, and everything else is
// inert. The vendors own these vocabularies and extend them between releases, so
// the rule that matters is the one about frames this build has never seen: a
// message added after it must move nothing. That is what each provider's table
// states, against the vocabulary its vendor ships today plus a message that does
// not exist yet. Each table runs through AssertTurnFrames.
//
// A frame that starts moving the flag is a deliberate act. It shows up here as a
// changed table entry, not as a wedged tab.

// TurnFrameCase is one output frame and whether it may move the turn flag.
type TurnFrameCase struct {
	Name string
	Line string
	// Moves is true for a NAMED turn signal of this provider. Every other frame
	// must publish nothing at all.
	Moves bool
}

// AssertTurnFrames feeds each frame to a FRESH agent, so no case can be answered
// by state another one left.
func AssertTurnFrames(t *testing.T, cases []TurnFrameCase, feed func(t *testing.T, tc TurnFrameCase) []bool) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			published := feed(t, tc)
			if tc.Moves {
				assert.NotEmpty(t, published, "a named turn signal must move the flag")
				return
			}
			assert.Empty(t, published, "only a named turn signal may move the flag")
		})
	}
}
