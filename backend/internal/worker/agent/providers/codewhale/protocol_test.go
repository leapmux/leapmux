package codewhale

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusItemIsPlumbing(t *testing.T) {
	t.Parallel()
	for _, summary := range []string{
		"Continuing — tool results",
		"Continuing active goal (pass 1 this turn, 1 total)",
		"Executing tools sequentially (writes, approvals, or non-parallel tools detected)",
		"Resuming turn with 1 queued sub-agent completion(s)",
		"Loaded deferred tool 'apply_patch'. Retry the call with its visible schema.",
		"Steer input accepted: Also mention the word banana.",
		"Request cancelled",
		"Policy: Full Access / ACT",
		"Permissions: Ask · Work",
		"  Continuing with leading space",
	} {
		assert.True(t, statusItemIsPlumbing(summary, ""), summary)
	}
	assert.True(t, statusItemIsPlumbing("Anything at all", "internal"), "a later release marks its own notes")
	assert.False(t, statusItemIsPlumbing("Checkpoint saved", ""))
	assert.False(t, statusItemIsPlumbing("", ""))
}

func TestListeningLinePattern(t *testing.T) {
	t.Parallel()
	match := listeningLinePattern.FindStringSubmatch("Runtime API listening on http://127.0.0.1:61234")
	assert.Equal(t, []string{"Runtime API listening on http://127.0.0.1:61234", "http://127.0.0.1:61234"}, match)
	assert.Nil(t, listeningLinePattern.FindStringSubmatch("Runtime API listening on"))
}

func TestRoutesEscapeTheirParameters(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "/v1/threads/thr_1/events", threadPath("thr_1", threadRouteEvents))
	assert.Equal(t, "/v1/threads/a%2Fb/turns/t%20x/steer", turnPath("a/b", "t x", turnRouteSteer))
}
