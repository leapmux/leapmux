//go:build integration

package storetest

import (
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go/wait"
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
//
// Calling Port() once, here, is what keeps that mistake out of reach: a store's
// own template never sees the network.Port, so it cannot format the wrong half
// of it.
func bareNumericPort(dsn func(host, port string) string) func(string, network.Port) string {
	return func(host string, port network.Port) string {
		return dsn(host, port.Port())
	}
}

// SQLReadyWait builds the readiness strategy a containerized SQL store waits
// on. `exposedPort` is the container port in "<num>/<proto>" form, `driver` a
// registered database/sql driver, and `dsn` renders a connection string from a
// host and a BARE numeric port.
//
// Every store's wait strategy goes through here so the protocol-suffix trap
// documented on bareNumericPort is handled in one place instead of five.
func SQLReadyWait(exposedPort, driver string, dsn func(host, port string) string) wait.Strategy {
	return wait.ForSQL(exposedPort, driver, bareNumericPort(dsn))
}
