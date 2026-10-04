package kimi

import "testing"

// This fixture uses the same direct native sink as production Start.
func newKimiOutputPathRig(t *testing.T, root string) *kimiOutputRig {
	t.Helper()
	return newKimiOutputRig(t)
}
