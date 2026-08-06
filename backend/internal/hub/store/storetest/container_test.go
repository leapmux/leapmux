//go:build integration

package storetest

import (
	"fmt"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
)

// These need the integration build tag to compile (testcontainers and moby are
// only imported under it) but they need no Docker daemon, so they run in the
// no-Docker pass that skips the five containerized store packages. That is
// deliberate: it is the only place the protocol-suffix contract is checked
// without standing up a database.
func TestBareNumericPort(t *testing.T) {
	t.Run("strips the protocol suffix before the template sees it", func(t *testing.T) {
		render := bareNumericPort(func(host, port string) string {
			return fmt.Sprintf("postgres://test:test@%s:%s/leapmux_test?sslmode=disable", host, port)
		})

		got := render("localhost", network.MustParsePort("32768/tcp"))

		assert.Equal(t, "postgres://test:test@localhost:32768/leapmux_test?sslmode=disable", got)
		assert.NotContains(t, got, "/tcp", "the protocol suffix must never reach a DSN")
	})

	t.Run("passes the port as a bare number for any protocol", func(t *testing.T) {
		for _, port := range []string{"5432/tcp", "4000/udp", "26257/tcp"} {
			var seen string
			render := bareNumericPort(func(_, port string) string {
				seen = port
				return ""
			})

			render("host", network.MustParsePort(port))

			assert.NotContains(t, seen, "/", "port %q reached the template unstripped", port)
		}
	})

	t.Run("passes the host through untouched", func(t *testing.T) {
		var seen string
		render := bareNumericPort(func(host, _ string) string {
			seen = host
			return ""
		})

		render("127.0.0.1", network.MustParsePort("5432/tcp"))

		assert.Equal(t, "127.0.0.1", seen)
	})
}
