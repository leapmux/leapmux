package auth_test

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

func TestIsUnixSocket_True(t *testing.T) {
	ctx := context.WithValue(context.Background(), http.LocalAddrContextKey, &net.UnixAddr{
		Name: "/tmp/test.sock",
		Net:  "unix",
	})
	assert.True(t, auth.IsUnixSocket(ctx))
}

func TestIsUnixSocket_False_TCP(t *testing.T) {
	ctx := context.WithValue(context.Background(), http.LocalAddrContextKey, &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 4327,
	})
	assert.False(t, auth.IsUnixSocket(ctx))
}

func TestIsUnixSocket_False_NoAddr(t *testing.T) {
	assert.False(t, auth.IsUnixSocket(context.Background()))
}

func TestIsUnixSocket_False_NilAddr(t *testing.T) {
	ctx := context.WithValue(context.Background(), http.LocalAddrContextKey, nil)
	assert.False(t, auth.IsUnixSocket(ctx))
}
