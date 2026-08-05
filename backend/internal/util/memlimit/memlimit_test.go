package memlimit

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizePhysicalMemory covers the darwin and windows probes' shared
// narrowing, which no test could reach while it was copied into two
// build-tagged files -- neither guard ran on any platform CI builds.
//
// The rejected values are the ones that would go NEGATIVE as an int64 and be
// handed to the pool sizing as a basis.
func TestSanitizePhysicalMemory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		total uint64
		want  int64
	}{
		{"an ordinary machine", 16 << 30, 16 << 30},
		{"one byte", 1, 1},
		{"the largest value that still fits", 1 << 62, 1 << 62},
		{"zero is a broken answer, not an empty machine", 0, 0},
		{"one past the plausible ceiling", (1 << 62) + 1, 0},
		{"a value that would land negative as int64", 1 << 63, 0},
		{"all bits set", math.MaxUint64, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sanitizePhysicalMemory(tt.total)
			if tt.want == 0 {
				require.ErrorIs(t, err, errNoLimit,
					"a broken reading must report no limit, not a failure: the probe itself ran fine")
				assert.Zero(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Positive(t, got, "a basis that is not positive would size every pool at or below zero")
		})
	}
}

func TestParseMeminfoTotal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    int64
		wantErr bool
	}{
		{
			name: "reads MemTotal in kibibytes",
			// Real /proc/meminfo shape: MemTotal is not the only line, and the
			// value is kB, so a bare strconv on the field would be 1024x off.
			content: "MemTotal:       16311456 kB\nMemFree:         1234567 kB\n",
			want:    16311456 * 1024,
		},
		{
			name:    "tolerates MemTotal not being first",
			content: "SwapTotal:  0 kB\nMemTotal: 1024 kB\n",
			want:    1024 * 1024,
		},
		{
			name:    "rejects a file without MemTotal",
			content: "MemFree: 100 kB\n",
			wantErr: true,
		},
		{
			name:    "rejects a MemTotal with no value",
			content: "MemTotal:\n",
			wantErr: true,
		},
		{
			name:    "rejects a non-numeric MemTotal",
			content: "MemTotal: lots kB\n",
			wantErr: true,
		},
		{
			name:    "rejects a zero MemTotal",
			content: "MemTotal: 0 kB\n",
			wantErr: true,
		},
		{
			name:    "rejects an empty file",
			content: "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseMeminfoTotal(tt.content)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseCgroupLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		content   string
		want      int64
		wantNoCap bool
		wantErr   bool
	}{
		{
			name:    "reads a cgroup v2 byte limit",
			content: "536870912\n",
			want:    536870912,
		},
		{
			name: "treats the cgroup v2 literal max as no limit",
			// v2 spells unlimited as a value, not an absent file. Parsing it as
			// a number would fail; treating the failure as an error would then
			// report a machine we read perfectly well as unprobeable.
			content:   "max\n",
			wantNoCap: true,
		},
		{
			name: "treats the cgroup v1 sentinel as no limit",
			// v1 writes PAGE_COUNTER_MAX * PAGE_SIZE. Taken at face value it is
			// an eight-exabyte machine.
			content:   "9223372036854771712\n",
			wantNoCap: true,
		},
		{
			name:      "treats an empty file as no limit",
			content:   "\n",
			wantNoCap: true,
		},
		{
			name:      "treats a zero limit as no limit",
			content:   "0",
			wantNoCap: true,
		},
		{
			name:    "rejects garbage",
			content: "not-a-number",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseCgroupLimit(tt.content)
			switch {
			case tt.wantNoCap:
				require.ErrorIs(t, err, errNoLimit)
			case tt.wantErr:
				require.Error(t, err)
				assert.NotErrorIs(t, err, errNoLimit,
					"a malformed file is a failure, not an absent limit")
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestGoMemLimitReportsUnsetAsZero(t *testing.T) {
	// Not parallel: SetMemoryLimit is process-global.
	original := goMemLimit()

	// math.MaxInt64 is the runtime's "no limit", not an 8 EiB machine. Reading
	// it as a basis would size every derived budget off a nonsense number.
	assert.Zero(t, mustSetLimit(t, math.MaxInt64))

	assert.Equal(t, int64(1<<30), mustSetLimit(t, 1<<30))

	if original == 0 {
		mustSetLimit(t, math.MaxInt64)
	} else {
		mustSetLimit(t, original)
	}
}

// mustSetLimit installs a GOMEMLIMIT and returns what goMemLimit makes of it.
func mustSetLimit(t *testing.T, limit int64) int64 {
	t.Helper()
	setMemoryLimit(limit)
	return goMemLimit()
}

func TestDetectAlwaysReturnsAUsableBasis(t *testing.T) {
	t.Parallel()

	// Whatever this host is -- container, VM, laptop, or a platform with no
	// probe at all -- Detect must hand back something a caller can size
	// against. Refusing to start over an undetectable machine would trade a
	// tuning default for an outage.
	basis := Detect()
	assert.Positive(t, basis.Bytes)
	assert.Contains(t,
		[]Source{SourceGoMemLimit, SourceCgroup, SourcePhysical, SourceFallback},
		basis.Source)
	assert.Contains(t, basis.String(), string(basis.Source))
}

func TestDetectFallsBackWhenEveryProbeFails(t *testing.T) {
	// Not parallel: swaps the package-level probes.
	restore := stubProbes(
		func() (int64, error) { return 0, errNoLimit },
		func() (int64, error) { return 0, errors.New("no /proc here") },
	)
	defer restore()

	assert.Equal(t, Basis{Bytes: FallbackBytes, Source: SourceFallback}, detectFrom())
}

func TestDetectPrefersTheLimitThatActuallyBinds(t *testing.T) {
	// Not parallel: swaps the package-level probes.
	tests := []struct {
		name     string
		cgroup   func() (int64, error)
		physical func() (int64, error)
		want     Basis
	}{
		{
			name: "a cgroup limit below physical memory is the real bound",
			// The container case: physical memory is the HOST's and is two
			// orders of magnitude too large.
			cgroup:   func() (int64, error) { return 512 << 20, nil },
			physical: func() (int64, error) { return 64 << 30, nil },
			want:     Basis{Bytes: 512 << 20, Source: SourceCgroup},
		},
		{
			name:     "a cgroup limit above physical memory is not a limit",
			cgroup:   func() (int64, error) { return 128 << 30, nil },
			physical: func() (int64, error) { return 16 << 30, nil },
			want:     Basis{Bytes: 16 << 30, Source: SourcePhysical},
		},
		{
			name:     "physical memory alone when there is no cgroup",
			cgroup:   func() (int64, error) { return 0, errNoLimit },
			physical: func() (int64, error) { return 8 << 30, nil },
			want:     Basis{Bytes: 8 << 30, Source: SourcePhysical},
		},
		{
			name:     "a cgroup limit alone when physical memory is unreadable",
			cgroup:   func() (int64, error) { return 2 << 30, nil },
			physical: func() (int64, error) { return 0, errors.New("unreadable") },
			want:     Basis{Bytes: 2 << 30, Source: SourceCgroup},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := stubProbes(tt.cgroup, tt.physical)
			defer restore()
			assert.Equal(t, tt.want, detectFrom())
		})
	}
}

// A cgroup probe that FAILED used to be discarded the moment physical memory
// answered, so a confined process whose limit could not be read logged
// `source=physical` -- byte for byte what an unconfined host logs, and the only
// other place that difference surfaces is the OOM kill.
func TestDetectKeepsACgroupProbeFailureVisible(t *testing.T) {
	// Not parallel: swaps the package-level probes.
	boom := errors.New("permission denied")

	t.Run("a failure the precedence stepped over is carried on the basis", func(t *testing.T) {
		restore := stubProbes(
			func() (int64, error) { return 0, boom },
			func() (int64, error) { return 8 << 30, nil },
		)
		defer restore()

		basis := detectFrom()
		assert.Equal(t, int64(8<<30), basis.Bytes)
		assert.Equal(t, SourcePhysical, basis.Source)
		require.ErrorIs(t, basis.CgroupErr, boom)
		assert.Contains(t, basis.String(), "permission denied",
			"the startup log is the only place an operator can see this")
		assert.Contains(t, basis.String(), "source=physical",
			"the figure in force still has to be named")
	})

	t.Run("the fallback basis carries it too", func(t *testing.T) {
		restore := stubProbes(
			func() (int64, error) { return 0, boom },
			func() (int64, error) { return 0, errors.New("no /proc here") },
		)
		defer restore()

		basis := detectFrom()
		assert.Equal(t, FallbackBytes, basis.Bytes)
		assert.ErrorIs(t, basis.CgroupErr, boom)
	})

	// The other half of the requirement: a probe that ran fine and found nothing
	// in force is not a failure. An unconfined host that warned on every start
	// would teach its operator to ignore the warning.
	t.Run("a probe that found no limit is not a failure", func(t *testing.T) {
		restore := stubProbes(
			func() (int64, error) { return 0, errNoLimit },
			func() (int64, error) { return 8 << 30, nil },
		)
		defer restore()

		basis := detectFrom()
		assert.NoError(t, basis.CgroupErr)
		assert.Equal(t, "8.0 GiB (source=physical)", basis.String())
	})

	t.Run("a cgroup limit that was found leaves nothing to report", func(t *testing.T) {
		restore := stubProbes(
			func() (int64, error) { return 512 << 20, nil },
			func() (int64, error) { return 8 << 30, nil },
		)
		defer restore()

		basis := detectFrom()
		assert.Equal(t, SourceCgroup, basis.Source)
		assert.NoError(t, basis.CgroupErr)
	})
}

// Two renderings, and which one a caller picks decides whether a probe failure
// is reported once or once per number derived from the basis. The Hub's three
// queue budgets each embedded String() and printed one failure three times
// inside a single log line; Figure exists so a caller that surfaces CgroupErr
// itself can name the figure without repeating the diagnosis.
func TestBasisRenderings(t *testing.T) {
	t.Parallel()

	boom := errors.New("open /custom/inner/memory.max: permission denied")

	t.Run("Figure states the figure in force and nothing else", func(t *testing.T) {
		t.Parallel()

		basis := Basis{Bytes: 8 << 30, Source: SourcePhysical, CgroupErr: boom}
		assert.Equal(t, "8.0 GiB (source=physical)", basis.Figure())
		assert.NotContains(t, basis.Figure(), "permission denied",
			"a caller that reports the failure itself must not have it smuggled into the figure too")
	})

	t.Run("String appends the failure for a standalone report", func(t *testing.T) {
		t.Parallel()

		basis := Basis{Bytes: 8 << 30, Source: SourcePhysical, CgroupErr: boom}
		assert.Contains(t, basis.String(), "8.0 GiB",
			"the figure in force still has to be named")
		assert.Contains(t, basis.String(), "source=physical")
		assert.Contains(t, basis.String(), "permission denied",
			"a caller with nowhere else to put the failure would otherwise lose it entirely")
	})

	t.Run("the two agree when there is nothing wrong", func(t *testing.T) {
		t.Parallel()

		basis := Basis{Bytes: 512 << 20, Source: SourceCgroup}
		assert.Equal(t, "512.0 MiB (source=cgroup)", basis.Figure())
		assert.Equal(t, basis.Figure(), basis.String(),
			"a healthy host must read identically whichever rendering its caller chose")
	})
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{32 << 20, "32.0 MiB"},
		{1 << 30, "1.0 GiB"},
		{1 << 40, "1.0 TiB"},
		{1 << 50, "1.0 PiB"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, HumanBytes(tt.in), "HumanBytes(%d)", tt.in)
	}
}
