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

func TestGeminiDebugLineReader(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("x", geminiDebugLogMaxLine+1)
	for _, tc := range []struct {
		name        string
		data        string
		wantLines   []string
		wantPending string
	}{
		{name: "complete lines", data: "first\nsecond\n", wantLines: []string{"first", "second"}},
		{name: "an unfinished line waits", data: "first\nsec", wantLines: []string{"first"}, wantPending: "sec"},
		{name: "a CRLF ending", data: "first\r\n", wantLines: []string{"first"}},
		{name: "an empty line", data: "\n", wantLines: []string{""}},
		{name: "an oversized line is dropped", data: oversized + "\nnext\n", wantLines: []string{"next"}},
		{name: "an unfinished oversized line is dropped", data: oversized},
		{name: "no data", data: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var lines []string
			var reader geminiDebugLineReader
			reader.feed([]byte(tc.data), func(line string) { lines = append(lines, line) })
			assert.Equal(t, tc.wantLines, lines)
			assert.Equal(t, tc.wantPending, string(reader.pending))
		})
	}
}

// geminiDebugLogRecorder records the lines that a follower delivers.
type geminiDebugLogRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *geminiDebugLogRecorder) observe(line geminiDebugLogLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line.text)
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

func TestGeminiDebugLogKeepsAnAppendDuringDelivery(t *testing.T) {
	t.Parallel()
	log, err := newGeminiDebugLog()
	require.NoError(t, err)
	t.Cleanup(log.close)
	appendGeminiDebugLog(t, log.path, strings.Repeat("x", geminiDebugLogMaxReadBytes)+"\n")
	var recorded []string
	appended := false
	observe := func(line geminiDebugLogLine) {
		recorded = append(recorded, line.text)
		if !appended {
			appended = true
			appendGeminiDebugLog(t, log.path, "later\n")
		}
	}
	// The first short line causes a delivery while the old follower still
	// owns the active log's size check and subsequent truncation.
	file, err := os.OpenFile(log.path, os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteAt([]byte("first\n"), 0)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	log.poll(observe)
	log.poll(observe)
	assert.Equal(t, []string{"first", "later"}, recorded)
}

func TestGeminiDebugLogDiscardsTheWholeOversizedLine(t *testing.T) {
	t.Parallel()
	log, err := newGeminiDebugLog()
	require.NoError(t, err)
	t.Cleanup(log.close)
	appendGeminiDebugLog(t, log.path, strings.Repeat("x", geminiDebugLogMaxLine+1))
	var recorded []string
	observe := func(line geminiDebugLogLine) { recorded = append(recorded, line.text) }
	log.poll(observe)
	appendGeminiDebugLog(t, log.path, geminiLongWaitWarning+"\nnext\n")
	log.poll(observe)
	assert.Equal(t, []string{"next"}, recorded, "the warning-shaped suffix still belongs to the dropped line")
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

// One poll has a fixed byte limit and leaves the active writer's file intact.
func TestGeminiDebugLogLimitsOnePollWithoutTruncatingTheFile(t *testing.T) {
	t.Parallel()
	log, err := newGeminiDebugLog()
	require.NoError(t, err)
	t.Cleanup(log.close)
	line := strings.Repeat("d", 1023) + "\n"
	count := geminiDebugLogMaxReadBytes / len(line)
	appendGeminiDebugLog(t, log.path, strings.Repeat(line, count+1))
	var recorded []string
	observe := func(line geminiDebugLogLine) { recorded = append(recorded, line.text) }
	log.poll(observe)
	assert.Equal(t, int64(geminiDebugLogMaxReadBytes), log.offset)
	assert.Len(t, recorded, count)
	info, err := os.Stat(log.path)
	require.NoError(t, err)
	assert.Equal(t, int64((count+1)*len(line)), info.Size())
	appendGeminiDebugLog(t, log.path, "after the cap\n")
	log.poll(observe)
	assert.Len(t, recorded, count+2)
	assert.Equal(t, "after the cap", recorded[len(recorded)-1])
}

func TestGeminiDebugLogPreparationDiscardsOnlyTheOldEpoch(t *testing.T) {
	t.Parallel()
	log, err := newGeminiDebugLog()
	require.NoError(t, err)
	t.Cleanup(log.close)
	appendGeminiDebugLog(t, log.path, "old\nunfinished")
	old := log.readLines()
	require.Len(t, old, 1)
	assert.Equal(t, "old", old[0].text)
	epoch := log.preparePrompt()
	info, err := os.Stat(log.path)
	require.NoError(t, err)
	assert.Zero(t, info.Size())
	appendGeminiDebugLog(t, log.path, "new\n")
	newLines := log.readLines()
	require.Len(t, newLines, 1)
	assert.Equal(t, "new", newLines[0].text)
	assert.Equal(t, epoch, newLines[0].epoch)
	assert.NotEqual(t, old[0].epoch, newLines[0].epoch)
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
