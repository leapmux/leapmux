//go:build unix

package agenttest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/util/procutil"
)

const inheritedWriterAddress = "LEAPMUX_TEST_INHERITED_WRITER_ADDRESS"
const inheritedWriterShellPID = "LEAPMUX_TEST_INHERITED_WRITER_SHELL_PID"

// RunInheritedWriterPeer holds inherited descriptors until its test releases
// the connection. It does nothing when called by the main test process.
func RunInheritedWriterPeer() {
	address := os.Getenv(inheritedWriterAddress)
	if address == "" {
		return
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		os.Exit(41)
	}
	if _, err := fmt.Fprintf(conn, "%d %s\n", os.Getpid(), os.Getenv(inheritedWriterShellPID)); err != nil {
		os.Exit(42)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		os.Exit(43)
	}
	_ = conn.Close()
	os.Exit(0)
}

// StartupExhaustionProfile gives a provider an isolated Bash profile, a command
// search path with no installed provider, and a writer that keeps stdout open
// after the shell finishes selection.
type StartupExhaustionProfile struct {
	Shell      string
	Initialize string
	writer     InheritedWriter
	release    InheritedWriter
}

// InstallStartupExhaustionProfile installs a profile for a subprocess running
// peerTest. If readInitialize is set, the profile records the first input line
// before starting selection.
func InstallStartupExhaustionProfile(t *testing.T, peerTest string, readInitialize bool) StartupExhaustionProfile {
	t.Helper()
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	dir := t.TempDir()
	fixture := StartupExhaustionProfile{Shell: bash, Initialize: filepath.Join(dir, "initialize"),
		writer: NewInheritedWriter(t, peerTest), release: NewInheritedWriter(t, peerTest)}
	var profile strings.Builder
	if readInitialize {
		profile.WriteString("IFS= read -r initialize\nprintf '%s\n' \"$initialize\" > " + shellquote.POSIX.Quote(fixture.Initialize) + "\n")
	}
	profile.WriteString(fixture.writer.ShellCommand + " 2>/dev/null &\n")
	profile.WriteString(fixture.release.ShellCommand + " >/dev/null 2>/dev/null\n")
	profile.WriteString("export PATH=" + shellquote.POSIX.Quote(dir) + "\nprintf 'provider absent diagnostic' >&2\n")
	path := filepath.Join(dir, "profile")
	require.NoError(t, os.WriteFile(path, []byte(profile.String()), 0o600))
	t.Setenv("BASH_ENV", path)
	return fixture
}

// FinishSelection releases the profile and waits for the shell to exit.
// The returned connection lets the test release the writer after startup
// observes its failure while stdout is still open.
func (f StartupExhaustionProfile) FinishSelection(t *testing.T) *net.TCPConn {
	t.Helper()
	conn, writer, _ := f.writer.Accept(t)
	release, _, shell := f.release.Accept(t)
	require.NoError(t, release.CloseWrite())
	require.Eventually(t, func() bool { return !shell.Runs() }, 10*time.Second, 10*time.Millisecond)
	require.True(t, writer.Runs(), "the inherited writer must outlive the shell")
	return conn
}

// InheritedWriter runs a test peer under a shell command and controls its
// lifetime over a socket independent of the inherited streams.
type InheritedWriter struct {
	ShellCommand string
	listener     *net.TCPListener
}

func NewInheritedWriter(t *testing.T, peerTest string) InheritedWriter {
	t.Helper()
	listener := ListenForProcessPeer(t, 30*time.Second)
	executable, err := os.Executable()
	require.NoError(t, err)
	command := inheritedWriterAddress + "=" + shellquote.POSIX.Quote(listener.Addr().String()) +
		" " + inheritedWriterShellPID + "=$$ " + shellquote.POSIX.Quote(executable) + " -test.run=^" + peerTest + "$"
	return InheritedWriter{ShellCommand: command, listener: listener}
}

// AwaitStartupOutcome waits for the startup goroutine after releasing its writer.
func AwaitStartupOutcome(t *testing.T, deadline context.Context, outcome <-chan error) error {
	t.Helper()
	select {
	case err := <-outcome:
		return err
	case <-deadline.Done():
		t.Fatal("startup did not return after the writer closed stdout")
		return nil
	}
}

// Accept returns the connection and the exact identities of the peer and its
// shell, both of which must still be running.
func (w InheritedWriter) Accept(t *testing.T) (*net.TCPConn, procutil.ProcessIdentity, procutil.ProcessIdentity) {
	t.Helper()
	conn, err := w.listener.AcceptTCP()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(30*time.Second)))
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	fields := strings.Fields(line)
	require.Len(t, fields, 2)
	pid, err := strconv.Atoi(fields[0])
	require.NoError(t, err)
	identity, present := procutil.IdentifyProcess(pid)
	require.True(t, present, "the connected peer holds the reported process ID")
	t.Cleanup(func() {
		_ = conn.Close()
		assert.Eventually(t, func() bool { return !identity.Runs() }, 10*time.Second, 10*time.Millisecond, "the released writer must exit")
	})
	pid, err = strconv.Atoi(fields[1])
	require.NoError(t, err)
	shell, present := procutil.IdentifyProcess(pid)
	require.True(t, present, "the shell remains alive until the test releases selection")
	return conn, identity, shell
}
