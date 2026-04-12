package main

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSocks5Server_ReturnsNonNil(t *testing.T) {
	server := newSocks5Server(func(_ context.Context, _, _ string) (net.Conn, error) {
		return nil, nil
	})
	assert.NotNil(t, server)
}

func TestNoopResolver_ReturnsNilIP(t *testing.T) {
	r := noopResolver{}
	_, ip, err := r.Resolve(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Nil(t, ip, "noopResolver should return nil IP so FQDN is preserved")
}
