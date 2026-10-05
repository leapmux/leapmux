//go:build unix

package agenttest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// probeWaitLimit is how long a test waits for the probe to record its runs. A
// run takes milliseconds, so the limit is generous on purpose: only a launch
// that never starts the probe reaches it.
const probeWaitLimit = 30 * time.Second

// EnvProbe is a fake CLI. Each run records its arguments and its whole
// environment, and then exits with status 3. A start test puts it in place of a
// provider's real CLI to read what the launch gives the program, after the
// shell wrapper and the user's profile ran. The probe never answers a
// handshake, so a start that waits for one fails as soon as the probe exits.
type EnvProbe struct {
	// Program is the absolute path of the fake.
	Program string
	// dir holds one `args.<pid>` and one `env.<pid>` file for each run.
	dir string
}

// ProbeRun is one run of an EnvProbe.
type ProbeRun struct {
	// Args are the arguments of the run.
	Args []string
	// env is the environment of the run, one `KEY=value` entry for each line.
	env []string
}

// Values returns the value of each `key=value` entry that the run's
// environment holds for key. A process environment holds each name once, so
// two values mean that the launch put a second entry beside the first.
func (r ProbeRun) Values(key string) []string {
	values := []string{}
	for _, line := range r.env {
		if value, found := strings.CutPrefix(line, key+"="); found {
			values = append(values, value)
		}
	}
	return values
}

// NewEnvProbe writes the fake, named binary, into a new directory. Pass
// Program to a start that takes a launch.Spec, so no PATH lookup can find the
// real CLI in its place.
//
// A run whose first argument is a key of answers prints that value and exits
// with status 0 instead. A provider that asks its CLI for the version before it
// starts the server needs this: without it the first run ends the start.
func NewEnvProbe(t *testing.T, binary string, answers map[string]string) EnvProbe {
	t.Helper()
	dir := t.TempDir()
	probe := EnvProbe{Program: filepath.Join(dir, binary), dir: dir}
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	// The environment file is renamed into place last, so a reader that sees it
	// sees the whole record.
	fmt.Fprintf(&script, "d=%s\n", posixQuote(dir))
	script.WriteString(`printf '%s\n' "$@" > "$d/args.$$.tmp" && mv "$d/args.$$.tmp" "$d/args.$$"` + "\n")
	script.WriteString(`env > "$d/env.$$.tmp" && mv "$d/env.$$.tmp" "$d/env.$$"` + "\n")
	keys := make([]string, 0, len(answers))
	for key := range answers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&script, "if [ \"$1\" = %s ]; then printf '%%s\\n' %s; exit 0; fi\n", posixQuote(key), posixQuote(answers[key]))
	}
	script.WriteString("exit 3\n")
	require.NoError(t, os.WriteFile(probe.Program, []byte(script.String()), 0o755))
	return probe
}

// Runs waits until the probe recorded count runs, and returns every run that it
// recorded, sorted by the name of its record. The test fails when the launch
// never starts that many runs.
func (p EnvProbe) Runs(t *testing.T, count int) []ProbeRun {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(p.records(t)) >= count
	}, probeWaitLimit, 10*time.Millisecond, "the launch must start the probe %d time(s)", count)
	records := p.records(t)
	runs := make([]ProbeRun, 0, len(records))
	for _, record := range records {
		pid := strings.TrimPrefix(filepath.Base(record), "env.")
		runs = append(runs, ProbeRun{
			Args: p.lines(t, filepath.Join(p.dir, "args."+pid)),
			env:  p.lines(t, record),
		})
	}
	return runs
}

// records lists the environment record of each finished run. A record that the
// probe still writes ends in `.tmp` and does not count.
func (p EnvProbe) records(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join(p.dir, "env.*"))
	require.NoError(t, err)
	finished := all[:0]
	for _, record := range all {
		if !strings.HasSuffix(record, ".tmp") {
			finished = append(finished, record)
		}
	}
	sort.Strings(finished)
	return finished
}

// lines reads one record of the probe, one entry a line.
func (p EnvProbe) lines(t *testing.T, record string) []string {
	t.Helper()
	data, err := os.ReadFile(record)
	require.NoError(t, err, "the probe must have written %s", filepath.Base(record))
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return []string{}
	}
	return strings.Split(text, "\n")
}

// LaunchProbe states one provider launch for RunLaunch and RequireLaunchPins.
type LaunchProbe struct {
	// Binary is the program name that the provider's locator asks the shell for.
	Binary string
	// Start is the provider's start function.
	Start agent.StartFunc
	// Pins are the `NAME=VALUE` entries that every run of the program must
	// receive, whatever the inherited environment and the user's profile state.
	Pins []string
	// Answers makes the probe answer a first argument and exit with status 0, as
	// NewEnvProbe states. Nil for a provider that starts its CLI once.
	Answers map[string]string
	// Runs is how many times the launch starts the program. Zero means once.
	Runs int
}

// RunLaunch starts a provider with an EnvProbe in place of its CLI, and returns
// every run that the probe recorded.
//
// Each pin of launch has two opposing values:
//   - The environment of the test holds one, so the worker inherits it.
//   - The user's login profile exports another, and the shell runs the profile
//     after the worker hands it the environment.
//
// The profile also puts the probe first on PATH, because a login profile can
// rebuild PATH (Debian's /etc/profile does) and hide the probe from the
// provider's locator.
//
// It sets HOME and the pinned names with t.Setenv, so the test cannot call
// t.Parallel (see InstallFakeCLI).
func RunLaunch(t *testing.T, launch LaunchProbe) []ProbeRun {
	t.Helper()
	probe := NewEnvProbe(t, launch.Binary, launch.Answers)

	var profile strings.Builder
	fmt.Fprintf(&profile, "export PATH=%s:\"$PATH\"\n", posixQuote(filepath.Dir(probe.Program)))
	for _, pin := range launch.Pins {
		name, _, found := strings.Cut(pin, "=")
		require.True(t, found, "pin %q must be NAME=VALUE", pin)
		t.Setenv(name, "inherited-by-the-worker")
		fmt.Fprintf(&profile, "export %s=exported-by-the-profile\n", name)
	}
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"), []byte(profile.String()), 0o600))
	t.Setenv("HOME", home)

	// The probe ends every start that waits for a handshake, so the error is the
	// expected outcome and carries no information. A start that returns an agent
	// at once (Claude Code does) leaves a process to stop.
	started, _ := launch.Start(t.Context(), agent.Options{
		AgentID:        "launch-probe",
		WorkingDir:     t.TempDir(),
		HomeDir:        home,
		Shell:          "/bin/sh",
		LoginShell:     true,
		StartupTimeout: probeWaitLimit,
	}, agent.NewProviderServices(&Sink{}))
	if started != nil {
		t.Cleanup(started.Stop)
	}

	count := launch.Runs
	if count == 0 {
		count = 1
	}
	return probe.Runs(t, count)
}

// RequireLaunchPins fails unless every run of the probe received each pin of
// launch, and received it once. A pin that only the inherited environment or
// the login profile could replace fails here: see RunLaunch. It returns the
// runs, for a test that asserts the arguments also.
func RequireLaunchPins(t *testing.T, launch LaunchProbe) []ProbeRun {
	t.Helper()
	require.NotEmpty(t, launch.Pins, "a launch test states at least one pin")
	runs := RunLaunch(t, launch)
	for index, run := range runs {
		for _, pin := range launch.Pins {
			name, value, _ := strings.Cut(pin, "=")
			assert.Equal(t, []string{value}, run.Values(name), "run %d (%s) must receive %s once, with the pinned value", index+1, strings.Join(run.Args, " "), name)
		}
	}
	return runs
}

// posixQuote quotes one word for a POSIX shell.
func posixQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
