//go:build linux

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLinuxSpecs_GatesMatchPlatform(t *testing.T) {
	t.Parallel()
	ids := map[string]bool{}
	for _, spec := range defaultEditorSpecs() {
		ids[spec.ID] = true
	}
	assert.False(t, ids["xcode"], "Xcode must not appear on Linux")
	assert.False(t, ids["notepad-plus-plus"], "Notepad++ must not appear on Linux")
}
