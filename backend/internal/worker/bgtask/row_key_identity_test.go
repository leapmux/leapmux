package bgtask

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRowKeyKeepsNativeKeysSeparateFromDerivedKeys(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		key  string
	}{
		{name: "overlong native key", key: strings.Repeat("x", RowKeyByteLimit+1)},
		{name: "invalid UTF-8 native key", key: "native\xffkey"},
	} {
		t.Run(test.name, func(t *testing.T) {
			derived := NormalizeRowKey(test.key)
			native := fmt.Sprint(derived)
			require.NotEqual(t, test.key, native)
			require.NoError(t, ValidateRowKey(native))
			assert.NotEqual(t, derived, NormalizeRowKey(native), "a fresh native key must not alias another task's derived key")
		})
	}
}
