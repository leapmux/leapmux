package providerkit

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testListenPattern = regexp.MustCompile(`server listening on (\S+)`)

func TestListenWaiter(t *testing.T) {
	t.Parallel()

	t.Run("returns the address of the first matching line", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(testListenPattern)
		assert.False(t, w.Observe([]byte("booting")))
		assert.True(t, w.Observe([]byte("mimocode server listening on http://127.0.0.1:4096")))
		assert.False(t, w.Observe([]byte("mimocode server listening on http://127.0.0.1:9999")), "a later match changes nothing")
		address, err := w.Wait(t.Context(), nil, time.Second)
		require.NoError(t, err)
		assert.Equal(t, "http://127.0.0.1:4096", address)
	})

	t.Run("reads a bare host and port as http", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(testListenPattern)
		w.Observe([]byte("server listening on 127.0.0.1:5000"))
		address, err := w.Wait(t.Context(), nil, time.Second)
		require.NoError(t, err)
		assert.Equal(t, "http://127.0.0.1:5000", address)
	})

	t.Run("returns a line that arrives while it waits", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(testListenPattern)
		done := make(chan string, 1)
		go func() {
			address, _ := w.Wait(t.Context(), nil, 30*time.Second)
			done <- address
		}()
		w.Observe([]byte("server listening on http://[::1]:7000"))
		assert.Equal(t, "http://[::1]:7000", <-done)
	})

	t.Run("fails when the process exits first", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(testListenPattern)
		exited := make(chan struct{})
		close(exited)
		_, err := w.Wait(t.Context(), exited, 30*time.Second)
		assert.ErrorIs(t, err, ErrServerExited)
	})

	t.Run("returns the address although the process exited after it", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(testListenPattern)
		w.Observe([]byte("server listening on http://127.0.0.1:1"))
		exited := make(chan struct{})
		close(exited)
		address, err := w.Wait(t.Context(), exited, 30*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "http://127.0.0.1:1", address)
	})

	t.Run("fails at the timeout", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(testListenPattern)
		_, err := w.Wait(t.Context(), nil, time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stated no address")
	})

	t.Run("fails when the context ends", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(testListenPattern)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := w.Wait(ctx, nil, 30*time.Second)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("ignores a match whose address is blank", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(regexp.MustCompile(`listening on(.*)`))
		assert.False(t, w.Observe([]byte("listening on   ")))
	})

	t.Run("refuses a pattern without exactly one capture group", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() { NewListenWaiter(regexp.MustCompile(`listening`)) })
		assert.Panics(t, func() { NewListenWaiter(regexp.MustCompile(`(a)(b)`)) })
	})

	t.Run("observes concurrent lines once", func(t *testing.T) {
		t.Parallel()
		w := NewListenWaiter(testListenPattern)
		firsts := make(chan bool, 16)
		for i := range 16 {
			go func() {
				firsts <- w.Observe([]byte("server listening on http://127.0.0.1:" + strconv.Itoa(1000+i)))
			}()
		}
		count := 0
		for range 16 {
			if <-firsts {
				count++
			}
		}
		assert.Equal(t, 1, count)
	})
}

// The contract promises a port that was free a moment ago, and nothing more:
// another process can take it before a bind. So this test binds nothing.
func TestReserveLoopbackPort(t *testing.T) {
	t.Parallel()

	port, err := ReserveLoopbackPort()
	require.NoError(t, err)
	assert.Positive(t, port)
	assert.LessOrEqual(t, port, 65535)
}

// fakeListener is a listener on one loopback port that records its close.
type fakeListener struct {
	net.Listener
	port     int
	closed   bool
	closeErr error
}

func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.port} }

func (l *fakeListener) Close() error {
	l.closed = true
	return l.closeErr
}

func TestReserveLoopbackPortReleasesTheListenerItBound(t *testing.T) {
	t.Parallel()
	var network, address string
	listener := &fakeListener{port: 43210}
	port, err := reserveLoopbackPort(func(n, a string) (net.Listener, error) {
		network, address = n, a
		return listener, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 43210, port, "the port is the one that the listener bound")
	assert.True(t, listener.closed, "the reservation holds no listener")
	assert.Equal(t, "tcp", network)
	assert.Equal(t, "127.0.0.1:0", address, "the system picks a free loopback port")
}

func TestReserveLoopbackPortReportsAFailedListenOrClose(t *testing.T) {
	t.Parallel()
	_, err := reserveLoopbackPort(func(string, string) (net.Listener, error) {
		return nil, errors.New("no ports left")
	})
	require.ErrorContains(t, err, "reserve a loopback port: no ports left")

	listener := &fakeListener{port: 43210, closeErr: errors.New("close failed")}
	_, err = reserveLoopbackPort(func(string, string) (net.Listener, error) { return listener, nil })
	require.ErrorContains(t, err, "release the reserved loopback port: close failed")
}

func TestNewServerSecret(t *testing.T) {
	t.Parallel()

	first, err := NewServerSecret()
	require.NoError(t, err)
	second, err := NewServerSecret()
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	decoded, err := base64.RawURLEncoding.DecodeString(first)
	require.NoError(t, err)
	assert.Len(t, decoded, 32)
	assert.Regexp(t, `^[A-Za-z0-9_-]+$`, first, "the secret is safe in a header and in an environment variable")
}
