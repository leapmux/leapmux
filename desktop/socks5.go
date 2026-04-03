package main

import (
	"context"
	"net"

	socks5 "github.com/things-go/go-socks5"
)

// noopResolver is a NameResolver that passes hostnames through without
// resolving them. DNS resolution happens at the Worker, not locally.
type noopResolver struct{}

func (noopResolver) Resolve(_ context.Context, name string) (context.Context, net.IP, error) {
	// Return an unspecified IP so the library uses the FQDN in DestAddr.String().
	return context.Background(), nil, nil
}

// newSocks5Server creates a SOCKS5 server that routes all CONNECT requests
// through the provided dial function. The dial function should open a tunnel
// connection via the E2EE channel.
func newSocks5Server(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *socks5.Server {
	return socks5.NewServer(
		socks5.WithDial(dial),
		socks5.WithResolver(noopResolver{}),
	)
}
