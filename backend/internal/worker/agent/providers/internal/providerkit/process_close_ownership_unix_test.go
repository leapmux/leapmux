//go:build unix

package providerkit

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/util/procutil"
)

func TestOwnerCloseChildPeer(t *testing.T) {
	if len(os.Args) < 3 {
		return
	}
	role, address := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	switch role {
	case "owner-before-close-root":
		child := ownerClosePeerCommand(t, "owner-before-close-child", address)
		require.NoError(t, child.Start())
		_, err := fmt.Fprintln(os.Stdout, child.Process.Pid)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, os.Stdin)
		require.NoError(t, err)
		os.Exit(0)
	case "owner-before-close-child":
		conn, err := net.Dial("tcp4", address)
		require.NoError(t, err)
		_, err = fmt.Fprintln(conn, os.Getpid(), os.Getppid())
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, conn)
		require.NoError(t, err)
		require.NoError(t, conn.Close())
		os.Exit(0)
	}
}

func ownerClosePeerCommand(t *testing.T, role, address string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.run=^TestOwnerCloseChildPeer$", "--", role, address)
	cmd.Env = []string{}
	procutil.DetachFromTerminal(cmd)
	return cmd
}

type waitForRootOnStdinClose struct {
	io.WriteCloser
	done <-chan struct{}
}

func (p waitForRootOnStdinClose) Close() error {
	if err := p.WriteCloser.Close(); err != nil {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("the root did not exit after stdin closed")
	}
}

func TestPipeCloseCapturesChildrenBeforeClosingInput(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepared=%t", prepared), func(t *testing.T) {
			listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			require.NoError(t, listener.SetDeadline(time.Now().Add(15*time.Second)))
			cmd := ownerClosePeerCommand(t, "owner-before-close-root", listener.Addr().String())
			var pipes *ProcessPipes
			if prepared {
				pipes, err = SetupLaunchPipes(&launch.PreparedLaunch{Cmd: cmd}, nil)
			} else {
				pipes, err = SetupProcessPipes(cmd, nil)
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = pipes.Close() })
			require.NoError(t, pipes.Start(quartz.NewReal()))
			conn, err := listener.AcceptTCP()
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			require.NoError(t, conn.SetDeadline(time.Now().Add(15*time.Second)))
			var childPID, parentPID int
			_, err = fmt.Fscanln(conn, &childPID, &parentPID)
			require.NoError(t, err)
			child, present := procutil.IdentifyProcess(childPID)
			require.True(t, present)
			t.Cleanup(func() { _, _ = child.Kill() })
			root, present := procutil.IdentifyProcess(parentPID)
			require.True(t, present)
			require.Equal(t, pipes.owner.PID(), parentPID)
			require.NoError(t, outputPipeFile(pipes.Stdout()).SetReadDeadline(time.Now().Add(15*time.Second)))
			started, err := bufio.NewReader(pipes.Stdout()).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(childPID), strings.TrimSpace(started))
			group, err := syscall.Getpgid(childPID)
			require.NoError(t, err)
			require.Equal(t, childPID, group)
			require.NotEqual(t, root.PID, group)
			wait := make(chan struct{})
			go func() {
				_ = pipes.Wait()
				close(wait)
			}()
			pipes.stdin = waitForRootOnStdinClose{WriteCloser: pipes.stdin, done: wait}
			require.NoError(t, pipes.Close())
			require.False(t, root.Runs(), "the owned root finishes before Close returns")
			require.False(t, child.Runs(), "the owner must stop the verified child before its parent can exit on stdin EOF")
		})
	}
}
