package hub

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/util/memlimit"
)

// captureLogs returns a logger writing text records into a buffer, plus a
// reader for everything written so far.
//
// Level Debug so nothing under test can be filtered out by the handler rather
// than by the code, and the timestamp is dropped so an assertion counting
// occurrences of a rendered figure cannot trip over a clock.
func captureLogs() (*slog.Logger, func() string) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	return logger, buf.String
}

// budgetsOf resolves the three queue budgets the way NewServer does -- from
// the queue_budget settings key's configured values, where a zero field means
// auto -- so these tests read the Source strings config actually produces
// rather than ones spelled here that could drift away from it.
func budgetsOf(configured settings.QueueBudgetValue, basis memlimit.Basis) []config.QueueMemoryBudget {
	return []config.QueueMemoryBudget{
		config.ResolveRelayQueueMemoryBudget(configured.RelayBytes, basis),
		config.ResolveWorkerQueueMemoryBudget(configured.WorkerBytes, basis),
		config.ResolveUserEventsQueueMemoryBudget(configured.UserEventsBytes, basis),
	}
}

// warnLines returns the WARN records in captured output.
func warnLines(out string) []string {
	var warns []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "level=WARN") {
			warns = append(warns, line)
		}
	}
	return warns
}

// The basis is the PROCESS's, probed once, and it used to be rendered into
// every budget's Source -- so a machine whose cgroup limit could not be read
// printed that diagnosis three times inside ONE log line, which is where a
// signal goes to be missed.
//
// This is the log's half of that: given budgets that account for themselves
// without restating the basis, one failed probe produces one mention. The other
// half -- that a budget's Source never carries the failure in the first place --
// is pinned where the Source is built, in config's
// TestResolveQueueMemoryBudgets/"a budget names the basis without rendering it";
// a Config cannot be handed a synthetic failing basis from outside its package.
func TestLogQueueMemoryBudgetsReportsAProbeFailureOnce(t *testing.T) {
	logger, logged := captureLogs()
	// The shape that makes this matter: a confined machine whose limit could not
	// be read, so the basis is the HOST's physical memory and is far too large.
	basis := memlimit.Basis{
		Bytes:     64 << 30,
		Source:    memlimit.SourcePhysical,
		CgroupErr: errors.New("open /custom/inner/memory.max: permission denied"),
	}

	logQueueMemoryBudgets(logger, basis, budgetsOf(settings.QueueBudgetValue{}, basis)...)

	out := logged()
	assert.Equal(t, 1, strings.Count(out, "permission denied"),
		"one probe, one failure, one mention -- three budgets each carrying it is the defect this exists to prevent")
	assert.Equal(t, 1, strings.Count(out, basis.Figure()),
		"the basis and its source belong to the process, so they are stated once")

	// Easy to spot: its own record, at a level an operator filters on, naming
	// what is actually at risk rather than only what failed.
	warns := warnLines(out)
	require.Len(t, warns, 1, "a basis that may not bind is an operational problem, not a fact")
	assert.Contains(t, warns[0], "cgroup memory limit could not be read")
	assert.Contains(t, warns[0], "permission denied")
	assert.NotContains(t, warns[0], "4/32",
		"the warning is about the basis, not about any one budget's share of it")
}

// The other half of the requirement. A warning on every start teaches its
// operator to ignore the warning, so the healthy paths -- a cgroup limit that
// WAS read, and a probe that ran fine and found none -- must add nothing.
func TestLogQueueMemoryBudgetsIsSilentOnAHealthyHost(t *testing.T) {
	for _, tc := range []struct {
		name  string
		basis memlimit.Basis
	}{
		{"an unconfined host", memlimit.Basis{Bytes: 32 << 30, Source: memlimit.SourcePhysical}},
		{"a container whose limit was read", memlimit.Basis{Bytes: 512 << 20, Source: memlimit.SourceCgroup}},
		{"a process told its budget outright", memlimit.Basis{Bytes: 8 << 30, Source: memlimit.SourceGoMemLimit}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, logged := captureLogs()

			logQueueMemoryBudgets(logger, tc.basis, budgetsOf(settings.QueueBudgetValue{}, tc.basis)...)

			out := logged()
			assert.Empty(t, warnLines(out), "nothing is wrong here")
			assert.NotContains(t, out, "cgroup memory limit could not be read")
			assert.Equal(t, 1, strings.Count(out, "msg="),
				"the whole account is one record on a host with nothing to report")
			assert.Contains(t, out, tc.basis.Figure())
		})
	}
}

// "Why is the relay budget this number?" has to stay answerable from the log
// after the basis moved out of the per-budget strings: the line pairs each
// pool's own name with the share it took of the basis stated beside it.
func TestLogQueueMemoryBudgetsAccountsForEveryBudget(t *testing.T) {
	t.Run("an auto-sized budget names its share of the basis", func(t *testing.T) {
		logger, logged := captureLogs()
		basis := memlimit.Detect()

		logQueueMemoryBudgets(logger, basis, budgetsOf(settings.QueueBudgetValue{}, basis)...)

		out := logged()
		// End to end, through the real resolver: the budgets and the line they
		// are logged on divide the SAME basis, so a budget that went back to
		// rendering it would show up here as four occurrences of one figure.
		assert.Equal(t, 1, strings.Count(out, basis.Figure()),
			"the figure is the process's, stated once, not once per share of it")
		for _, pool := range []string{metrics.PoolRelay, metrics.PoolWorker, metrics.PoolUserEvents} {
			assert.Contains(t, out, pool+"=",
				"%s: keyed by the same name that labels its /metrics series", pool)
		}
		assert.Contains(t, out, "source=auto", "an operator must be able to tell a guess from a setting")
		assert.Contains(t, out, "of basis", "and to derive the figure from the basis on the same line")
	})

	t.Run("a configured budget says so instead", func(t *testing.T) {
		logger, logged := captureLogs()
		configured := settings.QueueBudgetValue{
			RelayBytes:      321 << 20,
			WorkerBytes:     222 << 20,
			UserEventsBytes: 111 << 20,
		}

		basis := memlimit.Detect()
		logQueueMemoryBudgets(logger, basis, budgetsOf(configured, basis)...)

		out := logged()
		assert.Equal(t, 3, strings.Count(out, "source=config"),
			"each of the three has to account for itself")
		assert.NotContains(t, out, "of basis",
			"a configured budget took no share of anything")
		assert.Contains(t, out, "321.0 MiB")
		assert.Contains(t, out, "222.0 MiB")
		assert.Contains(t, out, "111.0 MiB")
	})
}
