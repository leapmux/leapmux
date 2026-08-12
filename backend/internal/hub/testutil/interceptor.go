package testutil

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// NewAuthInterceptor builds an auth interceptor for a test and stops its
// background sweep when the test ends.
//
// auth.NewInterceptor starts a goroutine that lives until AuthContextRegistry
// .Stop runs, and most call sites discarded the registry entirely, so every
// test that built one leaked a sweeper for the rest of the run. Routing through
// this helper makes the cleanup a property of asking for an interceptor rather
// than a line each caller has to remember.
func NewAuthInterceptor(t *testing.T, opts auth.InterceptorOptions) (connect.Interceptor, *auth.AuthContextRegistry) {
	t.Helper()
	interceptor, registry := auth.NewInterceptor(opts)
	t.Cleanup(registry.Stop)
	return interceptor, registry
}

// NewStoreAuthInterceptor is NewAuthInterceptor for the common case: cookie
// auth against a store, with every other option left at its default.
func NewStoreAuthInterceptor(t *testing.T, st store.Store) connect.Interceptor {
	t.Helper()
	interceptor, _ := NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	return interceptor
}
