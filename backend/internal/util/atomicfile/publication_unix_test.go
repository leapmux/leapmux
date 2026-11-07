//go:build unix

package atomicfile

import "testing"

func observeDurableReplacement(_ *testing.T, _ func()) {}

func injectDurableReplacementFailure(_ *testing.T, _ string) {}
