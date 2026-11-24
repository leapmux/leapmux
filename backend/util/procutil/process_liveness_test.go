package procutil

import (
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspectLiveProcessBoundaries(t *testing.T) {
	t.Parallel()
	assert.Equal(t, LiveProcessInfo{}, InspectLiveProcess(0))
	assert.Equal(t, LiveProcessInfo{}, InspectLiveProcess(-1))
	if overflow := int64(math.MaxInt32) + 1; overflow <= math.MaxInt {
		assert.Equal(t, LiveProcessInfo{}, InspectLiveProcess(int(overflow)))
	}

	self := InspectLiveProcess(os.Getpid())
	assert.True(t, self.Alive)
	assert.Positive(t, self.StartToken)
	assert.Equal(t, self, InspectLiveProcess(os.Getpid()), "start token of a running process must stay stable")
}

func TestInspectLiveProcessTracksChildLifecycle(t *testing.T) {
	t.Parallel()
	child := startWaitingProcess(t)
	pid := child.cmd.Process.Pid

	info := InspectLiveProcess(pid)
	require.True(t, info.Alive)
	require.Positive(t, info.StartToken)
	assert.True(t, info.SameProcess(info.StartToken))
	assert.True(t, info.SameProcess(0))
	assert.False(t, info.SameProcess(info.StartToken+1))

	require.True(t, child.endedByItself(t))
	after := InspectLiveProcess(pid)
	assert.False(t, after.Alive)
	assert.Zero(t, after.StartToken)
	assert.False(t, after.SameProcess(info.StartToken))
	assert.False(t, after.SameProcess(0))
}

func TestLiveProcessInfoSameProcess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		info     LiveProcessInfo
		expected int64
		want     bool
	}{
		{name: "dead with no expected token", info: LiveProcessInfo{}, expected: 0, want: false},
		{name: "dead with expected token", info: LiveProcessInfo{}, expected: 100, want: false},
		{name: "dead with matching token still fails", info: LiveProcessInfo{Alive: false, StartToken: 100}, expected: 100, want: false},
		{name: "alive without token when no token expected", info: LiveProcessInfo{Alive: true}, expected: 0, want: true},
		{name: "alive without token when negative token expected", info: LiveProcessInfo{Alive: true}, expected: -1, want: true},
		{name: "alive without token fails when token expected", info: LiveProcessInfo{Alive: true}, expected: 100, want: false},
		{name: "alive with token matches when no token expected", info: LiveProcessInfo{Alive: true, StartToken: 100}, expected: 0, want: true},
		{name: "alive with matching token", info: LiveProcessInfo{Alive: true, StartToken: 100}, expected: 100, want: true},
		{name: "alive with different token detects PID reuse", info: LiveProcessInfo{Alive: true, StartToken: 200}, expected: 100, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.info.SameProcess(tc.expected))
		})
	}
}

func TestParseLinuxLiveProcessStat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data string
		want LiveProcessInfo
	}{
		{
			name: "running process without starttime field",
			data: "1234 (node) R 1 1234 1234 0 -1",
			want: LiveProcessInfo{Alive: true},
		},
		{
			name: "sleeping process with starttime ticks",
			data: "1234 (bash) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 987654 20 21",
			want: LiveProcessInfo{Alive: true, StartToken: 987654},
		},
		{
			name: "comm containing spaces and parentheses with starttime ticks",
			data: "1234 (my ) weird (proc)) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242",
			want: LiveProcessInfo{Alive: true, StartToken: 424242},
		},
		{
			name: "zombie process with starttime ticks reports not alive",
			data: "1234 (sh) Z 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 987654",
			want: LiveProcessInfo{},
		},
		{
			name: "dead uppercase X",
			data: "1234 (sh) X 1 1234 1234 0 -1",
			want: LiveProcessInfo{},
		},
		{
			name: "dead lowercase x",
			data: "1234 (sh) x 1 1234 1234 0 -1",
			want: LiveProcessInfo{},
		},
		{
			name: "comm containing spaces and parentheses zombie",
			data: "1234 (my ) weird (proc)) Z 1 1234 1234",
			want: LiveProcessInfo{},
		},
		{
			name: "whitespace before state",
			data: "1234 (sh) \t\n Z 1",
			want: LiveProcessInfo{},
		},
		{
			name: "zero starttime ticks omits token",
			data: "1234 (init) S 0 1 1 0 -1 4194560 1 2 3 4 5 6 7 8 20 0 1 0 0",
			want: LiveProcessInfo{Alive: true},
		},
		{
			name: "invalid starttime ticks omits token",
			data: "1234 (bash) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 notanumber",
			want: LiveProcessInfo{Alive: true},
		},
		{
			name: "negative starttime ticks omits token",
			data: "1234 (bash) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 -42",
			want: LiveProcessInfo{Alive: true, StartToken: 0},
		},
		{
			name: "overflowing starttime ticks omits token",
			data: "1234 (bash) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 99999999999999999999",
			want: LiveProcessInfo{Alive: true, StartToken: 0},
		},
		{
			name: "empty payload returns alive conservatively",
			data: "",
			want: LiveProcessInfo{Alive: true},
		},
		{
			name: "missing closing paren returns alive conservatively",
			data: "1234 (sh Z 1",
			want: LiveProcessInfo{Alive: true},
		},
		{
			name: "truncated after closing paren returns alive conservatively",
			data: "1234 (sh)   ",
			want: LiveProcessInfo{Alive: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, parseLinuxLiveProcessStat([]byte(tc.data)))
		})
	}
}
