package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestPortNumber(t *testing.T) {
	// testcontainers-go v0.42's wait.ForSQL callback passes the mapped port as
	// "<num>/<proto>" (network.Port.String()); PortNumber must strip the
	// protocol so the resulting DSN stays valid.
	assert.Equal(t, "32768", testutil.PortNumber("32768/tcp"))
	assert.Equal(t, "5432", testutil.PortNumber("5432/tcp"))
	assert.Equal(t, "4000", testutil.PortNumber("4000/udp"))
	// A bare port (no protocol) is returned unchanged.
	assert.Equal(t, "32768", testutil.PortNumber("32768"))
	assert.Equal(t, "", testutil.PortNumber(""))
}
