package codex

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexTestTZData encodes valid TZif data with at most one offset transition.
func codexTestTZData(t *testing.T, offsets []int32, transition *int32) []byte {
	t.Helper()
	var data bytes.Buffer
	data.WriteString("TZif")
	data.Write(make([]byte, 16))
	count := uint32(0)
	if transition != nil {
		count = 1
	}
	require.NoError(t, binary.Write(&data, binary.BigEndian, [6]uint32{0, 0, 0, count, uint32(len(offsets)), 4}))
	if transition != nil {
		require.NoError(t, binary.Write(&data, binary.BigEndian, *transition))
		data.WriteByte(1)
	}
	for _, offset := range offsets {
		require.NoError(t, binary.Write(&data, binary.BigEndian, offset))
		data.Write([]byte{0, 0})
	}
	data.WriteString("TST\x00")
	return data.Bytes()
}

func TestCodexNativeTimezoneFollowsFilePrecedence(t *testing.T) {
	t.Parallel()
	valid := codexTestTZData(t, []int32{9 * 3600}, nil)
	for _, tc := range []struct {
		label, value     string
		present, tracked bool
		files            map[string][]byte
		wantPaths        []string
		wantOffset       int
		unknown          bool
	}{
		{label: "an unrequested native timezone is unknown", unknown: true},
		{label: "set empty is UTC", tracked: true, present: true},
		{label: "unset uses the system file", tracked: true, files: map[string][]byte{"/etc/localtime": valid}, wantPaths: []string{"/etc/localtime"}, wantOffset: 9 * 3600},
		{label: "localtime uses the system file", tracked: true, present: true, value: "localtime", files: map[string][]byte{"/etc/localtime": valid}, wantPaths: []string{"/etc/localtime"}, wantOffset: 9 * 3600},
		{label: "an absolute file is opened directly", tracked: true, present: true, value: ":/native/zone", files: map[string][]byte{"/native/zone": valid}, wantPaths: []string{"/native/zone"}, wantOffset: 9 * 3600},
		{label: "a relative file searches the native system directories", tracked: true, present: true, value: "Asia/Probe", files: map[string][]byte{"/share/zoneinfo/Asia/Probe": valid}, wantPaths: []string{"/usr/share/zoneinfo/Asia/Probe", "/share/zoneinfo/Asia/Probe"}, wantOffset: 9 * 3600},
		{label: "colon localtime is a relative file", tracked: true, present: true, value: ":localtime", files: map[string][]byte{"/usr/share/zoneinfo/localtime": valid}, wantPaths: []string{"/usr/share/zoneinfo/localtime"}, wantOffset: 9 * 3600},
		{label: "dot segments retain native symlink semantics", tracked: true, present: true, value: ":link/../zone", files: map[string][]byte{"/usr/share/zoneinfo/link/../zone": valid}, wantPaths: []string{"/usr/share/zoneinfo/link/../zone"}, wantOffset: 9 * 3600},
		{label: "an opened invalid file prevents later fallback", tracked: true, present: true, value: "UTC0", files: map[string][]byte{"/usr/share/zoneinfo/UTC0": []byte("invalid"), "/share/zoneinfo/UTC0": valid}, wantPaths: []string{"/usr/share/zoneinfo/UTC0"}, unknown: true},
		{label: "a real file precedes a valid POSIX rule", tracked: true, present: true, value: "UTC0", files: map[string][]byte{"/usr/share/zoneinfo/UTC0": valid}, wantPaths: []string{"/usr/share/zoneinfo/UTC0"}, wantOffset: 9 * 3600},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			snapshot := envprobe.Snapshot{Values: map[string]string{}, Present: map[string]bool{}}
			if tc.tracked {
				snapshot.Values["TZ"], snapshot.Present["TZ"] = tc.value, tc.present
			}
			var paths []string
			location := codexUnixRetryTextLocation(snapshot, func(path string) (io.ReadCloser, error) {
				paths = append(paths, path)
				data, exists := tc.files[path]
				if !exists {
					return nil, os.ErrNotExist
				}
				return io.NopCloser(bytes.NewReader(data)), nil
			})
			assert.Equal(t, tc.wantPaths, paths)
			if tc.unknown {
				assert.Nil(t, location)
				return
			}
			require.NotNil(t, location)
			_, offset := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).In(location).Zone()
			assert.Equal(t, tc.wantOffset, offset)
		})
	}
}

func TestCodexNativeTimezoneAcceptsOnlyKnownFixedPOSIXRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value   string
		offset  int
		unknown bool
	}{
		{value: "UTC0"},
		{value: "ABCDEFG0"}, {value: "<ABCDEFG>0"}, {value: "ABCDEFGH0", unknown: true}, {value: "<ABCDEFGH>0", unknown: true}, {value: "GMT+0"}, {value: "UTC-0"},
		{value: "EST5", offset: -5 * 3600}, {value: "NST3:30", offset: -(3*3600 + 30*60)},
		{value: "<+0545>-5:45", offset: 5*3600 + 45*60},
		{value: "ABC-23:59:59", offset: 24*3600 - 1},
		{value: " \tUTC0\n"},
		{value: "UTC", unknown: true}, {value: ":UTC0", unknown: true},
		{value: "ABC24", unknown: true}, {value: "ABC0:60", unknown: true}, {value: "ABC0:00:60", unknown: true},
		{value: "EST5EDT,M3.2.0,M11.1.0", unknown: true}, {value: "\u00a0UTC0\u00a0", unknown: true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			snapshot := envprobe.Snapshot{Values: map[string]string{"TZ": tc.value}, Present: map[string]bool{"TZ": true}}
			location := codexUnixRetryTextLocation(snapshot, func(string) (io.ReadCloser, error) { return nil, os.ErrNotExist })
			if tc.unknown {
				assert.Nil(t, location)
				return
			}
			require.NotNil(t, location)
			_, offset := time.Unix(0, 0).In(location).Zone()
			assert.Equal(t, tc.offset, offset)
		})
	}
}

type codexTimezoneReadCounter struct {
	io.Reader
	bytes  int
	closed bool
}

func (file *codexTimezoneReadCounter) Read(data []byte) (int, error) {
	count, err := file.Reader.Read(data)
	file.bytes += count
	return count, err
}
func (file *codexTimezoneReadCounter) Close() error { file.closed = true; return nil }

func TestCodexNativeTimezoneCapsItsReadAndClosesTheFile(t *testing.T) {
	t.Parallel()
	file := &codexTimezoneReadCounter{Reader: strings.NewReader(strings.Repeat("x", 4<<20))}
	location, opened := readCodexTimezoneFile("/native/large", "large", func(string) (io.ReadCloser, error) { return file, nil })
	assert.True(t, opened)
	assert.Nil(t, location)
	assert.LessOrEqual(t, file.bytes, (1<<20)+1)
	assert.True(t, file.closed)
}

func TestCodexLocalMinuteIncludesPartialRepeatedMinutes(t *testing.T) {
	t.Parallel()
	civil := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	transition := int32(civil.Add(30 * time.Second).Unix())
	for _, tc := range []struct {
		offset  int32
		seconds int
	}{{-20, 80}, {20, 40}} {
		t.Run(fmt.Sprint(tc.offset), func(t *testing.T) {
			t.Parallel()
			location, err := time.LoadLocationFromTZData("partial", codexTestTZData(t, []int32{0, tc.offset}, &transition))
			require.NoError(t, err)
			result, ok := parseCodexRetryText("Try again at 12:00 AM.", codexRetryTextObservation{location: location, startedAt: &civil, completedAt: &civil}, civil)
			require.True(t, ok)
			assert.Equal(t, civil.Add(time.Duration(tc.seconds)*time.Second), result)
		})
	}
}

func TestCodexNativeTimezoneReadFailureRetainsTheFirstOpenedFile(t *testing.T) {
	t.Parallel()
	file := &codexTimezoneReadCounter{Reader: iotest.ErrReader(assert.AnError)}
	var paths []string
	snapshot := envprobe.Snapshot{Values: map[string]string{"TZ": "UTC0"}, Present: map[string]bool{"TZ": true}}
	location := codexUnixRetryTextLocation(snapshot, func(path string) (io.ReadCloser, error) { paths = append(paths, path); return file, nil })
	assert.Nil(t, location)
	assert.Equal(t, []string{"/usr/share/zoneinfo/UTC0"}, paths)
	assert.True(t, file.closed)
}

func TestCodexNativeTimezoneOpenErrorsAllowTheNextSystemDirectory(t *testing.T) {
	t.Parallel()
	data := codexTestTZData(t, []int32{3600}, nil)
	var paths []string
	snapshot := envprobe.Snapshot{Values: map[string]string{"TZ": "Zone"}, Present: map[string]bool{"TZ": true}}
	location := codexUnixRetryTextLocation(snapshot, func(path string) (io.ReadCloser, error) {
		paths = append(paths, path)
		if len(paths) == 1 {
			return nil, os.ErrPermission
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	})
	require.NotNil(t, location)
	assert.Equal(t, []string{"/usr/share/zoneinfo/Zone", "/share/zoneinfo/Zone"}, paths)
	_, offset := time.Unix(0, 0).In(location).Zone()
	assert.Equal(t, 3600, offset)
}

func TestCodexTurnTimeKeepsZeroAndRejectsUnusableValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "null", `"1"`, "1.5", "{}", "9223372036854775807", "-9223372036854775808"} {
		assert.Nil(t, decodeCodexTurnTime(json.RawMessage(value)), value)
	}
	for _, seconds := range []int64{-1, 0, 1} {
		raw, err := json.Marshal(seconds)
		require.NoError(t, err)
		decoded := decodeCodexTurnTime(raw)
		require.NotNil(t, decoded)
		assert.Equal(t, seconds, decoded.Unix())
	}
}

func TestCodexUnknownTimezoneKeepsAuthoritativeResetPriority(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	outcome := codexTurnOutcome{message: "Try again at Nov 2nd, 2026 3:00 PM."}
	unknown := codexUsageLimitFailure(outcome, codexUsageLimitSources{}, now)
	assert.Equal(t, providerkit.TurnFailureUsageLimited, unknown.Kind)
	assert.True(t, unknown.ResetAt.IsZero())
	reset := now.Add(time.Hour)
	known := codexUsageLimitFailure(outcome, codexUsageLimitSources{bucketReset: &reset}, now)
	assert.Equal(t, reset, known.ResetAt)
}

func TestCodexNativeTimezoneDeclinesInvalidOrUnsupportedTZif(t *testing.T) {
	t.Parallel()
	for label, change := range map[string]func([]byte) []byte{
		"a daylight saving flag must be zero or one": func(data []byte) []byte { data[48] = 2; return data },
		"a designation must use native characters":   func(data []byte) []byte { data[52] = '!'; return data },
		"a version one file must end after its data": func(data []byte) []byte { return append(data, 1) },
		"leap second semantics require independent support": func(data []byte) []byte {
			binary.BigEndian.PutUint32(data[28:32], 1)
			var record bytes.Buffer
			require.NoError(t, binary.Write(&record, binary.BigEndian, [2]int32{78796800, 1}))
			return append(data, record.Bytes()...)
		},
	} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			data := change(codexTestTZData(t, []int32{9 * 3600}, nil))
			_, err := time.LoadLocationFromTZData("go-accepts", data)
			require.NoError(t, err, "this regression must exercise native validation beyond Go's parser")
			location, opened := readCodexTimezoneFile("/native/zone", "zone", func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
			assert.True(t, opened)
			assert.Nil(t, location)
		})
	}
}

func TestCodexRetryDateCannotLeaveAndReturnToTheSameDate(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 1, 1, 23, 45, 0, 0, time.UTC)
	completed := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
	transition := int32(time.Date(2026, 1, 2, 0, 30, 0, 0, time.UTC).Unix())
	location, err := time.LoadLocationFromTZData("midnight-rollback", codexTestTZData(t, []int32{0, -7200}, &transition))
	require.NoError(t, err)
	require.Equal(t, started.In(location).Format("2006-01-02"), completed.In(location).Format("2006-01-02"))
	parsed, ok := parseCodexRetryText("Try again at 11:59 PM.", codexRetryTextObservation{location: location, startedAt: &started, completedAt: &completed}, completed)
	assert.False(t, ok, "the interval contains a different local date before the rollback")
	assert.True(t, parsed.IsZero())
}
