//go:build darwin

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDarwinSpecs_GatesMatchPlatform(t *testing.T) {
	t.Parallel()
	ids := map[string]bool{}
	for _, spec := range defaultEditorSpecs() {
		ids[spec.ID] = true
	}
	assert.True(t, ids["xcode"], "Xcode must appear on macOS")
	assert.False(t, ids["notepad-plus-plus"], "Notepad++ must not appear on macOS")
}
