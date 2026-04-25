//go:build windows

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWindowsSpecs_GatesMatchPlatform(t *testing.T) {
	t.Parallel()
	ids := map[string]bool{}
	for _, spec := range defaultEditorSpecs() {
		ids[spec.ID] = true
	}
	assert.True(t, ids["notepad-plus-plus"], "Notepad++ must appear on Windows")
	assert.False(t, ids["xcode"], "Xcode must not appear on Windows")
}
