//go:build integration

package storetest

import (
	"context"
	"strconv"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// bareNumericPort adapts a DSN template that wants a bare numeric port to the
// callback shape testcontainers-go passes.
//
// Stripping the protocol is the entire point. Since v0.43 the wait.ForSQL
// callback receives moby's network.Port, whose String() renders the
// "<num>/<proto>" form ("32768/tcp"). Formatting that value straight into a
// DSN with %s COMPILES and passes go vet's printf check -- Port implements
// Stringer -- and produces a connection string like
//
//	postgres://test:test@localhost:32768/tcp/leapmux_test
//
// which no driver can parse. The readiness wait then spins to its deadline and
// surfaces a misleading "context deadline exceeded" against the Docker socket
// rather than anything pointing at the DSN.
func bareNumericPort(dsn func(host, port string) string) func(string, network.Port) string {
	return func(host string, port network.Port) string {
		return dsn(host, port.Port())
	}
}

// SQLContainer describes the disposable SQL server a store's integration test
// boots, in the terms the test actually varies: an image, the port the server
// listens on inside it, and how to address it.
//
// It exists so a store never handles a network.Port itself. Before, each store
// spelled its port three times -- in ExposedPorts, in the readiness strategy,
// and again in MappedPort -- and then built its real connection string while
// holding the network.Port that MappedPort returned, which is exactly where the
// protocol-suffix trap documented on bareNumericPort bites. Start owns all
// three spellings, derives them from one number, and hands back plain strings,
// so the mistake is not merely commented against: there is no network.Port in
// scope at any call site to make it with.
type SQLContainer struct {
	// Image is the container image to run.
	Image string
	// Port is the port the server listens on INSIDE the container. The "/tcp"
	// exposed form and the mapped-port lookup are both derived from it.
	Port int
	// Env and Cmd are passed through to the container request.
	Env map[string]string
	Cmd []string
	// Driver is a registered database/sql driver, used only by the readiness
	// probe.
	Driver string
	// ReadyDSN renders the connection string the readiness probe dials, from a
	// host and a BARE numeric port. This is usually a bootstrap database rather
	// than the one the test ends up using -- several backends have to CREATE
	// DATABASE before they can connect to it.
	ReadyDSN func(host, port string) string
}

// Start boots the container, waits until it answers SQL, and returns the mapped
// host and port as plain strings. The container is terminated on test cleanup.
func (c SQLContainer) Start(t *testing.T) (host, port string) {
	t.Helper()
	testutil.ConfigureDockerHost(t)

	// One number, two spellings, derived rather than retyped. A mistyped port
	// in the readiness strategy used to burn the full 60-second startup budget
	// and fail as `context deadline exceeded: port "26258/tcp" not found`.
	exposed := strconv.Itoa(c.Port) + "/tcp"

	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        c.Image,
			ExposedPorts: []string{exposed},
			Env:          c.Env,
			Cmd:          c.Cmd,
			WaitingFor:   wait.ForSQL(exposed, c.Driver, bareNumericPort(c.ReadyDSN)),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err = container.Host(ctx)
	require.NoError(t, err)
	mapped, err := container.MappedPort(ctx, exposed)
	require.NoError(t, err)

	// .Port() applied once, here. This is the only place in the tree that holds
	// a network.Port.
	return host, mapped.Port()
}
