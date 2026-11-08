package servicelaunch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowsAccountLookupNamePreservesScope(t *testing.T) {
	for _, test := range []struct{ account, computer, expected string }{
		{`.\local user`, "HOST", `HOST\local user`},
		{`DOMAIN\user`, "HOST", `DOMAIN\user`},
		{`DOMAIN\user`, "", `DOMAIN\user`},
		{"user@example.com", "", "user@example.com"},
		{"local user", "HOST", "local user"},
	} {
		actual, err := windowsAccountLookupName(test.account, test.computer)
		require.NoError(t, err)
		assert.Equal(t, test.expected, actual)
	}
	for _, test := range []struct{ account, computer string }{
		{"", "HOST"}, {"bad\x00name", "HOST"}, {`.\`, "HOST"},
		{`.\user`, ""}, {`.\user`, `bad\host`}, {`.\user`, "bad\x00host"},
		{`DOMAIN\user\extra`, "HOST"},
	} {
		_, err := windowsAccountLookupName(test.account, test.computer)
		require.Error(t, err)
	}
}
