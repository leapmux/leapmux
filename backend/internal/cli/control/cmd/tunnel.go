package cmd

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/tunnel"
)

func newTunnelCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("tunnel", "Forward TCP ports or connect stdio to a host on a worker over E2EE",
		cli.NewLeaf(cli.Leaf{
			Use:              "forward <spec>",
			Aliases:          []string{"fwd"},
			Short:            "Listen on a local TCP port and forward connections to a target through a worker",
			QuietUnsupported: true,
			Long: `Open a Noise_NK end-to-end encrypted channel to a Worker, listen on a local TCP
address, and forward every accepted connection to <target-host>:<target-port>
on that Worker's network.

The spec is [bind-addr:]<local-port>:<target-host>:<target-port>. When
bind-addr is omitted, the listener binds 127.0.0.1. Pass local port 0 to let
the OS pick a free port. Press Ctrl-C to stop forwarding.`,
			Args: map[string]string{
				"spec": `Port forward specification: "[bind:]<local-port>:<target-host>:<target-port>" (for example "5432:127.0.0.1:5432" or "0.0.0.0:8080:localhost:3000").`,
			},
			Examples: []cli.Example{
				{Comment: "Forward local port 5432 to Postgres on a remote Worker.", Command: "leapmux tunnel forward 5432:127.0.0.1:5432 --worker name:db-box"},
				{Comment: "Bind on all interfaces and forward to a dev server.", Command: "leapmux tunnel forward 0.0.0.0:8080:localhost:3000 --worker name:dev-box"},
			},
			Flags: addWorkerFlag,
			Run:   runTunnelForward,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "connect <target>",
			Aliases:          []string{"nc"},
			Short:            "Bridge stdin/stdout to <host>:<port> through a worker (for ssh ProxyCommand)",
			QuietUnsupported: true,
			Long: `Open a Noise_NK end-to-end encrypted channel to a Worker, dial
<host>:<port> from that Worker, and bridge stdin and stdout directly to the
TCP stream. Useful with "ssh -o ProxyCommand='leapmux tunnel connect %h:%p --worker <name>'".`,
			Args: map[string]string{
				"target": `Target address in "<host>:<port>" form (for example "127.0.0.1:22" or "[::1]:8080").`,
			},
			Examples: []cli.Example{
				{Comment: "Use as an SSH ProxyCommand to reach a remote Worker behind NAT.", Command: `ssh -o ProxyCommand="leapmux tunnel connect %h:%p --worker name:build-box" user@127.0.0.1`},
			},
			Flags: addWorkerFlag,
			Run:   runTunnelConnect,
		}),
	), "tun")
}

type tunnelForwardSpec struct {
	BindAddr   string
	LocalPort  uint16
	TargetHost string
	TargetPort uint16
}

// parseTunnelForwardSpec parses "[bind:]<local-port>:<target-host>:<target-port>".
// IPv6 literals are supported in bracket form, e.g. "[::1]:8080:[::1]:3000".
func parseTunnelForwardSpec(raw string) (tunnelForwardSpec, error) {
	parts, err := splitHostPortSpec(strings.TrimSpace(raw))
	if err != nil {
		return tunnelForwardSpec{}, err
	}
	var bindAddr, localPortRaw, targetHost, targetPortRaw string
	switch len(parts) {
	case 3:
		bindAddr = "127.0.0.1"
		localPortRaw, targetHost, targetPortRaw = parts[0], parts[1], parts[2]
	case 4:
		bindAddr, localPortRaw, targetHost, targetPortRaw = parts[0], parts[1], parts[2], parts[3]
		if bindAddr == "" {
			bindAddr = "127.0.0.1"
		}
	default:
		return tunnelForwardSpec{}, cli.Usagef("invalid tunnel spec %q: want [bind:]<local-port>:<target-host>:<target-port>", raw)
	}
	localPort, err := parsePort(localPortRaw, true)
	if err != nil {
		return tunnelForwardSpec{}, cli.Usagef("invalid local port %q: %v", localPortRaw, err)
	}
	if strings.TrimSpace(targetHost) == "" {
		return tunnelForwardSpec{}, cli.Usagef("target host is empty in %q", raw)
	}
	targetPort, err := parsePort(targetPortRaw, false)
	if err != nil {
		return tunnelForwardSpec{}, cli.Usagef("invalid target port %q: %v", targetPortRaw, err)
	}
	return tunnelForwardSpec{
		BindAddr:   bindAddr,
		LocalPort:  localPort,
		TargetHost: targetHost,
		TargetPort: targetPort,
	}, nil
}

// parseTunnelTarget parses "<host>:<port>".
func parseTunnelTarget(raw string) (string, uint16, error) {
	host, portRaw, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", 0, cli.Usagef("invalid target %q: want <host>:<port>", raw)
	}
	if strings.TrimSpace(host) == "" {
		return "", 0, cli.Usagef("target host is empty in %q", raw)
	}
	port, err := parsePort(portRaw, false)
	if err != nil {
		return "", 0, cli.Usagef("invalid target port %q: %v", portRaw, err)
	}
	return host, port, nil
}

// splitHostPortSpec splits colon-delimited tokens while respecting [...] IPv6
// brackets (stripping the surrounding brackets from bracketed segments and
// rejecting characters immediately before '[' or after ']').
func splitHostPortSpec(s string) ([]string, error) {
	if s == "" {
		return nil, cli.Usagef("tunnel spec is empty")
	}
	var out []string
	var cur strings.Builder
	inBracket := false
	justClosedBracket := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '[':
			if inBracket || cur.Len() > 0 || justClosedBracket {
				return nil, cli.Usagef("unexpected '[' in tunnel spec %q", s)
			}
			inBracket = true
		case ']':
			if !inBracket {
				return nil, cli.Usagef("unmatched ']' in tunnel spec %q", s)
			}
			inBracket = false
			justClosedBracket = true
		case ':':
			if !inBracket {
				out = append(out, cur.String())
				cur.Reset()
				justClosedBracket = false
				continue
			}
			cur.WriteByte(ch)
		default:
			if justClosedBracket {
				return nil, cli.Usagef("unexpected character %q after ']' in tunnel spec %q", ch, s)
			}
			cur.WriteByte(ch)
		}
	}
	if inBracket {
		return nil, cli.Usagef("unclosed '[' in tunnel spec %q", s)
	}
	out = append(out, cur.String())
	return out, nil
}

func parsePort(raw string, allowZero bool) (uint16, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 16)
	if err != nil {
		return 0, errors.New("must be a TCP port number (1-65535)")
	}
	if n == 0 && !allowZero {
		return 0, errors.New("must be between 1 and 65535")
	}
	return uint16(n), nil
}

// tunnelDialer opens one E2EE TCP stream to (host, port) on the target Worker.
// Overridable in tests to exercise forward and connect without a live Hub.
type tunnelDialer func(ctx context.Context, host string, port uint16) (net.Conn, error)

func openTunnelChannel(inv *cli.Invocation, s *session) (*tunnel.Channel, string, error) {
	hub, ok := s.c.(*control.HubClient)
	if !ok {
		return nil, "", cli.Usagef("tunnel commands require a direct Hub connection and cannot run over a tab's local IPC socket").
			WithHint("run from a shell outside LeapMux, or pass --hub <url>")
	}
	bounded := s.bounded()
	workerID, err := bounded.workerScope()
	bounded.Close()
	if err != nil {
		return nil, "", err
	}
	openCtx, cancelOpen := rpcDeadline(s.ctx)
	ch, err := hub.OpenE2EEChannel(openCtx, s.ctx, workerID)
	cancelOpen()
	if err != nil {
		return nil, "", err
	}
	return ch, workerID, nil
}

func runTunnelForward(inv *cli.Invocation) (any, error) {
	spec, err := parseTunnelForwardSpec(inv.Arg("spec"))
	if err != nil {
		return nil, err
	}
	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ch, workerID, err := openTunnelChannel(inv, s)
	if err != nil {
		return nil, err
	}
	defer ch.Close()

	listenAddr := net.JoinHostPort(spec.BindAddr, strconv.Itoa(int(spec.LocalPort)))
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUnavailable, err, "listen on "+listenAddr)
	}
	defer func() { _ = ln.Close() }()

	targetAddr := net.JoinHostPort(spec.TargetHost, strconv.Itoa(int(spec.TargetPort)))
	inv.Printer.Notice("Forwarding %s -> %s via worker %s (Ctrl-C to stop)",
		ln.Addr().String(), targetAddr, cli.ShortID(workerID))

	// Tie forwarding lifetime to both the CLI signal context AND the underlying
	// E2EE channel so channel death stops accepting local connections cleanly.
	fwdCtx, cancelFwd := context.WithCancel(s.ctx)
	defer cancelFwd()
	channelDied := make(chan struct{})
	go func() {
		select {
		case <-ch.Context().Done():
			close(channelDied)
			cancelFwd()
		case <-fwdCtx.Done():
		}
	}()

	dial := func(ctx context.Context, host string, port uint16) (net.Conn, error) {
		return tunnel.DialTunnelContext(ctx, ch, host, uint32(port))
	}
	err = serveTunnelForward(fwdCtx, ln, spec.TargetHost, spec.TargetPort, dial, inv.Printer.Notice)
	if err == nil && s.ctx.Err() == nil {
		select {
		case <-channelDied:
			return nil, cli.Newf(cli.CodeWorkerOffline, "the E2EE channel to worker %s closed", cli.ShortID(workerID))
		default:
		}
	}
	return nil, err
}

func serveTunnelForward(
	ctx context.Context,
	ln net.Listener,
	targetHost string,
	targetPort uint16,
	dial tunnelDialer,
	notice func(string, ...any),
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		localConn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Go(func() {
			defer func() { _ = localConn.Close() }()
			dialCtx, dialCancel := rpcDeadline(ctx)
			remoteConn, err := dial(dialCtx, targetHost, targetPort)
			dialCancel()
			if err != nil {
				if notice != nil && ctx.Err() == nil {
					notice("tunnel dial %s failed: %v", net.JoinHostPort(targetHost, strconv.Itoa(int(targetPort))), err)
				}
				return
			}
			defer func() { _ = remoteConn.Close() }()
			pipeBidirectional(ctx, localConn, remoteConn)
		})
	}
}

func runTunnelConnect(inv *cli.Invocation) (any, error) {
	host, port, err := parseTunnelTarget(inv.Arg("target"))
	if err != nil {
		return nil, err
	}
	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ch, _, err := openTunnelChannel(inv, s)
	if err != nil {
		return nil, err
	}
	defer ch.Close()

	dialCtx, dialCancel := rpcDeadline(s.ctx)
	conn, err := tunnel.DialTunnelContext(dialCtx, ch, host, uint32(port))
	dialCancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	in := inv.Stdin
	if in == nil {
		in = os.Stdin
	}
	out := inv.Command.OutOrStdout()
	return nil, bridgeStdioToConn(s.ctx, in, out, conn)
}

type closeWriter interface {
	CloseWrite() error
}

func pipeBidirectional(ctx context.Context, a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = a.Close()
			_ = b.Close()
		case <-done:
		}
	}()
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = io.Copy(b, a)
		if cw, ok := b.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
	})
	wg.Go(func() {
		_, _ = io.Copy(a, b)
		if cw, ok := a.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
	})
	wg.Wait()
	close(done)
}

func bridgeStdioToConn(ctx context.Context, in io.Reader, out io.Writer, conn net.Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	outErrCh := make(chan error, 1)
	inErrCh := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, conn)
		outErrCh <- err
	}()
	go func() {
		_, err := io.Copy(conn, in)
		if cw, ok := conn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		inErrCh <- err
	}()

	isBenign := func(err error) bool {
		return err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || ctx.Err() != nil
	}

	var outErr, inErr error
	outDone, inDone := false, false
	for !outDone {
		select {
		case <-ctx.Done():
			_ = conn.Close()
			outErr = <-outErrCh
			outDone = true
		case outErr = <-outErrCh:
			outDone = true
		case inErr = <-inErrCh:
			inDone = true
			if !isBenign(inErr) {
				_ = conn.Close()
				outErr = <-outErrCh
				outDone = true
			}
		}
	}
	if !isBenign(outErr) {
		return outErr
	}
	if inDone && !isBenign(inErr) {
		return inErr
	}
	// Non-blocking check if stdin already finished with an error.
	if !inDone {
		select {
		case err := <-inErrCh:
			if !isBenign(err) {
				return err
			}
		default:
		}
	}
	return nil
}
