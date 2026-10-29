package gemini

import (
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestDeliverGeminiDebugLogLines(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("x", geminiDebugLogMaxLine+1)
	for _, tc := range []struct {
		name         string
		data         string
		wantLines    []string
		wantConsumed int
	}{
		{name: "complete lines", data: "first\nsecond\n", wantLines: []string{"first", "second"}, wantConsumed: 13},
		{name: "an unfinished line waits", data: "first\nsec", wantLines: []string{"first"}, wantConsumed: 6},
		{name: "a CRLF ending", data: "first\r\n", wantLines: []string{"first"}, wantConsumed: 7},
		{name: "an empty line", data: "\n", wantLines: []string{""}, wantConsumed: 1},
		{name: "an oversized line is dropped", data: oversized + "\nnext\n", wantLines: []string{"next"}, wantConsumed: len(oversized) + 6},
		{name: "an unfinished oversized line is dropped", data: oversized, wantConsumed: len(oversized)},
		{name: "no data", data: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var lines []string
			consumed := deliverGeminiDebugLogLines([]byte(tc.data), func(line string) { lines = append(lines, line) })
			assert.Equal(t, tc.wantLines, lines)
			assert.Equal(t, tc.wantConsumed, consumed)
		})
	}
}

// geminiDebugLogRecorder records the lines that a follower delivers.
type geminiDebugLogRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *geminiDebugLogRecorder) observe(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func (r *geminiDebugLogRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}

func appendGeminiDebugLog(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

// The follower delivers each line once Gemini ends it, from the first poll
// after the file appears.
func TestGeminiDebugLogFollowsTheLinesThatGeminiAppends(t *testing.T) {
	t.Parallel()
	log, err := newGeminiDebugLog()
	require.NoError(t, err)
	t.Cleanup(log.close)
	clock := testutil.NewQuartzMock(t)
	recorder := &geminiDebugLogRecorder{}
	log.follow(clock, recorder.observe)

	clock.Advance(geminiDebugLogPollInterval).MustWait(testutil.DeadlineContext(t))
	appendGeminiDebugLog(t, log.path, geminiLongWaitWarning+"\nsec")
	clock.Advance(geminiDebugLogPollInterval).MustWait(testutil.DeadlineContext(t))
	testutil.RequireEventually(t, func() bool { return len(recorder.recorded()) == 1 })
	assert.Equal(t, []string{geminiLongWaitWarning}, recorder.recorded(), "an unfinished line waits for its end")

	appendGeminiDebugLog(t, log.path, "ond\n")
	clock.Advance(geminiDebugLogPollInterval).MustWait(testutil.DeadlineContext(t))
	testutil.RequireEventually(t, func() bool { return len(recorder.recorded()) == 2 })
	assert.Equal(t, []string{geminiLongWaitWarning, "second"}, recorder.recorded())
}

// The follower empties the file once it has read the cap, and goes on to read
// what Gemini appends after that.
func TestGeminiDebugLogEmptiesTheFileAtItsCap(t *testing.T) {
	t.Parallel()
	log, err := newGeminiDebugLog()
	require.NoError(t, err)
	t.Cleanup(log.close)
	clock := testutil.NewQuartzMock(t)
	recorder := &geminiDebugLogRecorder{}
	log.follow(clock, recorder.observe)
	line := strings.Repeat("d", 1023) + "\n"
	lines := geminiDebugLogMaxSize / len(line)
	appendGeminiDebugLog(t, log.path, strings.Repeat(line, lines+1))

	clock.Advance(geminiDebugLogPollInterval).MustWait(testutil.DeadlineContext(t))
	testutil.RequireEventually(t, func() bool {
		info, err := os.Stat(log.path)
		return err == nil && info.Size() == 0
	})
	appendGeminiDebugLog(t, log.path, "after the cap\n")
	clock.Advance(geminiDebugLogPollInterval).MustWait(testutil.DeadlineContext(t))
	testutil.RequireEventually(t, func() bool {
		recorded := recorder.recorded()
		return len(recorded) > 0 && recorded[len(recorded)-1] == "after the cap"
	})
	assert.Len(t, recorder.recorded(), lines+2, "the read that passed the cap delivered every line that Gemini wrote before it")
}

// Close stops the follower and removes the log's directory. A second close,
// and a follow after the close, do nothing.
func TestGeminiDebugLogCloseRemovesTheDirectory(t *testing.T) {
	t.Parallel()
	log, err := newGeminiDebugLog()
	require.NoError(t, err)
	clock := testutil.NewQuartzMock(t)
	recorder := &geminiDebugLogRecorder{}
	log.follow(clock, recorder.observe)
	appendGeminiDebugLog(t, log.path, "written\n")

	log.close()
	log.close()
	_, err = os.Stat(log.dir)
	assert.True(t, os.IsNotExist(err), "the directory is gone: %v", err)
	log.follow(clock, recorder.observe)
	clock.Advance(geminiDebugLogPollInterval).MustWait(testutil.DeadlineContext(t))
	assert.Empty(t, recorder.recorded())
}
