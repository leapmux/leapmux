package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestIsTestSupportPackage(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"testutil":          true,
		"storetest":         true,
		"agenttest":         true,
		"test":              true,
		"agent":             false,
		"testing":           false,
		"testutilextension": false,
		"tests":             false,
		"":                  false,
	} {
		assert.Equalf(t, want, testutil.IsTestSupportPackage(name), "%q", name)
	}
}
