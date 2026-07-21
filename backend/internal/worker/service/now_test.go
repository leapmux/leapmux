package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNowMillisIsUTCOnMillisecondGrid(t *testing.T) {
	got := nowMillis()

	assert.Equal(t, time.UTC, got.Location())
	assert.Zero(t, got.Nanosecond()%int(time.Millisecond),
		"stamp must sit on the millisecond grid or the persisted row drifts from the streamed one")
	assert.False(t, got.After(time.Now()),
		"a floored stamp must never postdate the clock")
}
