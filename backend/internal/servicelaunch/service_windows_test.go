//go:build windows

package servicelaunch

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

// stubbedSc records the sc.exe argv.
type stubbedSc struct {
	mu       sync.Mutex
	commands [][]string
}

func (s *stubbedSc) run(ctx context.Context, args []string, escalated bool) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, args)
	if strings.Join(args, " ") == "" {
		return nil, nil
	}
	return []byte("STATE : 2 START_PENDING\n"), nil
}

func (s *stubbedSc) flat() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sb strings.Builder
	for _, cmd := range s.commands {
		sb.WriteString(strings.Join(cmd, " "))
		sb.WriteString("\n")
	}
	return sb.String()
}

func windowsSpec(t *testing.T, startup lifecycle.StartupKind) LaunchSpec {
	test := testSpec()
	test.Startup = startup
	test.AccountID = "S-1-5-21-1001"
	spec, err := LaunchSpecFromService(test)
	require.NoError(t, err)
	return spec
}

// TestServiceDefinitionArguments pins the boot-mode service: literal
// arguments, the selected account's SID, the run command, and the
// start-pending/stop-pending handling.
func TestServiceDefinitionArguments(t *testing.T) {
	sc := &stubbedSc{}
	mgr := &windowsServiceManager{runSc: sc.run}
	spec := windowsSpec(t, lifecycle.StartupBoot)
	spec.ConfigFile = `C:\Users\trustin\My Config\worker.yaml`

	ctx := context.Background()
	require.NoError(t, mgr.Install(ctx, spec))

	create := ""
	for _, line := range strings.Split(sc.flat(), "\n") {
		if strings.Contains(line, "create") {
			create = line
		}
	}
	require.NotEmpty(t, create, "create ran")
	assert.Contains(t, create, spec.Executable+" worker run --config "+spec.ConfigFile,
		"the binary path holds the run command with literal arguments")
	assert.Contains(t, create, "S-1-5-21-1001", "the service runs as the selected account's SID")
	assert.Contains(t, create, "start= auto", "boot startup is automatic")

	// The pending states report as their own state, distinct from
	// readiness.
	status, err := mgr.Inspect(ctx, spec)
	require.NoError(t, err)
	assert.True(t, status.Installed)
	assert.Equal(t, "start-pending", status.State, "start-pending is its own state")
}

// The task adapter composes the noninteractive logon task: onlogon
// trigger, the account, the /it interactive flag, and the run command.
func TestTaskDefinitionArguments(t *testing.T) {
	var mu sync.Mutex
	var commands [][]string
	mgr := &windowsTaskManager{
		credential: func() string { return "pw" },
		runSchtasks: func(_ context.Context, args []string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			commands = append(commands, args)
			return []byte("Running\n"), nil
		}}
	spec := windowsSpec(t, lifecycle.StartupLogin)

	ctx := context.Background()
	require.NoError(t, mgr.Install(ctx, spec))

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, commands)
	joined := strings.Join(commands[0], " ")
	assert.Contains(t, joined, "/sc onlogon", "login mode triggers at logon")
	assert.Contains(t, joined, "/ru S-1-5-21-1001", "the task runs as the selected account")
	assert.Contains(t, joined, "/it", "the task is interactive-token based")
	assert.Contains(t, joined, "/rp pw", "the password reaches the native call")
	assert.Contains(t, joined, " worker run --config ", "the task runs the run command")

	status, err := mgr.Inspect(ctx, spec)
	require.NoError(t, err)
	assert.True(t, status.Running)
}

// TestCredentialRepairAndFailures states the credential classes: a
// missing password refuses with the batch-logon explanation's context,
// and the native error classes name their remedy.
func TestCredentialRepairAndFailures(t *testing.T) {
	outcome := RepairServiceCredential("trustin", "")
	require.False(t, outcome.Updated)
	assert.Contains(t, outcome.Reason, "password")

	ok := RepairServiceCredential("trustin", "pw")
	require.True(t, ok.Updated)

	assert.Contains(t, credentialErrorClass(errOfCode(1326)), "wrong")
	assert.Contains(t, credentialErrorClass(errOfCode(1907)), "expired")
	assert.Contains(t, credentialErrorClass(errOfCode(1385)), "batch logon right")
	assert.Contains(t, credentialErrorClass(assert.AnError), "refused")
}

// errOfCode carries a windows error code.
type codedError struct{ code uintptr }

func (e codedError) ErrorCode() uintptr { return e.code }
func (codedError) Error() string        { return "coded" }

func errOfCode(code uintptr) error { return codedError{code: code} }
