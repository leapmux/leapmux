package codex

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexTestTZVersionTwo(t *testing.T, footer string) []byte {
	t.Helper()
	first := codexTestTZData(t, []int32{0}, nil)
	first[4] = '2'
	data := append([]byte(nil), first...)
	data = append(data, first...)
	return append(data, []byte("\n"+footer+"\n")...)
}

func TestCodexTimezoneDataValidatesNativeRecords(t *testing.T) {
	t.Parallel()
	for label, change := range map[string]func([]byte) []byte{
		"unsupported version":            func(data []byte) []byte { data[4] = '4'; return data },
		"absent types":                   func(data []byte) []byte { binary.BigEndian.PutUint32(data[36:40], 0); return data },
		"absent designations":            func(data []byte) []byte { binary.BigEndian.PutUint32(data[40:44], 0); return data },
		"excessive count":                func(data []byte) []byte { binary.BigEndian.PutUint32(data[32:36], math.MaxUint32); return data },
		"invalid designation index":      func(data []byte) []byte { data[49] = 4; return data },
		"missing designation terminator": func(data []byte) []byte { data[len(data)-1] = 'T'; return data },
		"unrepresentable native offset":  func(data []byte) []byte { binary.BigEndian.PutUint32(data[44:48], 86400); return data },
		"incomplete type":                func(data []byte) []byte { return data[:47] },
		"incomplete header":              func(data []byte) []byte { return data[:43] },
		"inconsistent indicator counts":  func(data []byte) []byte { binary.BigEndian.PutUint32(data[20:24], 2); return data },
		"universal without standard":     func(data []byte) []byte { binary.BigEndian.PutUint32(data[20:24], 1); return append(data, 1) },
	} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			assert.Nil(t, codexTimezoneData(change(codexTestTZData(t, []int32{0}, nil)), "test"))
		})
	}
	assert.NotNil(t, codexTimezoneData(codexTestTZData(t, []int32{0}, nil), "valid"))
	assert.NotNil(t, codexTimezoneData(codexTestTZVersionTwo(t, "TST0"), "valid-v2"))
	assert.NotNil(t, codexTimezoneData(codexTestTZVersionTwo(t, ""), "empty-footer"))
}

func TestCodexTimezoneDataDeclinesUnknownFooterSemantics(t *testing.T) {
	t.Parallel()
	for _, footer := range []string{
		"TST0DST", "TST0,", " TST0 ", ":TST0", "TST0\x00", "TST0DST;M3.2.0,M11.1.0",
		"TST0DST,M0.1.0,M11.1.0", "TST0DST,M3.0.0,M11.1.0", "TST0DST,M3.2.7,M11.1.0",
		"TST0DST,M3.2.0/2:60,M11.1.0", "TST0DST,M3.2.0/-1,M11.1.0",
		"TST0DST,J0,J365", "TST0DST,366,300", "TST0DST,M3.2.0/24,M11.1.0",
		"STD-14DST-15,J1/0,J365/23", "TST0DST,M3.2.0,M11.1.0,extra",
	} {
		t.Run(footer, func(t *testing.T) {
			t.Parallel()
			assert.Nil(t, codexTimezoneData(codexTestTZVersionTwo(t, footer), "test"))
		})
	}
}

func TestCodexTimezoneFooterPreservesNorthernAndSouthernRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		footer                     string
		winterOffset, summerOffset int
	}{
		{"EST5EDT,M3.2.0,M11.1.0", -5 * 3600, -4 * 3600},
		{"AEST-10AEDT-11,M10.1.0,M4.1.0/3", 11 * 3600, 10 * 3600},
		{"TST0DST,J60/2,J300/2", 0, 3600},
		{"TST0DST,60/2,300/2", 0, 3600},
	} {
		t.Run(tc.footer, func(t *testing.T) {
			t.Parallel()
			location := codexTimezoneData(codexTestTZVersionTwo(t, tc.footer), "test")
			require.NotNil(t, location)
			_, winter := time.Date(2026, time.January, 15, 0, 0, 0, 0, location).Zone()
			_, summer := time.Date(2026, time.July, 15, 0, 0, 0, 0, location).Zone()
			assert.Equal(t, tc.winterOffset, winter)
			assert.Equal(t, tc.summerOffset, summer)
		})
	}
}

func TestCodexTimezoneDataValidatesTransitionOwnership(t *testing.T) {
	t.Parallel()
	transition := int32(1000)
	data := codexTestTZData(t, []int32{3600, 0}, &transition)
	assert.NotNil(t, codexTimezoneData(data, "valid"))
	badIndex := append([]byte(nil), data...)
	badIndex[48] = 2
	assert.Nil(t, codexTimezoneData(badIndex, "bad-index"))
	// Go chooses a standard type before this transition; Chrono chooses type zero.
	firstDST := append([]byte(nil), data...)
	firstDST[48] = 0
	firstDST[53] = 1
	assert.Nil(t, codexTimezoneData(firstDST, "different-initial-type"))

	// The final explicit type is TST at UTC, but this footer selects UTC+1.
	first := codexTestTZData(t, []int32{0, 0}, &transition)
	first[4] = '2'
	second := append([]byte(nil), first[:44]...)
	second = binary.BigEndian.AppendUint64(second, uint64(transition))
	second = append(second, first[48:]...)
	combined := append(append(first, second...), []byte("\nTST-1\n")...)
	assert.Nil(t, codexTimezoneData(combined, "inconsistent-footer"))
	for _, footer := range []string{"ABC0", "TST0DST0,M3.2.0,M11.1.0"} {
		// At Unix second 1000, northern DST is inactive. Make the explicit
		// final type daylight to test the flag independently of its offset.
		candidate := append([]byte(nil), combined[:len(combined)-len("\nTST-1\n")]...)
		if footer != "ABC0" {
			candidate[len(first)+44+8+1+6+4] = 1
		}
		candidate = append(candidate, []byte("\n"+footer+"\n")...)
		assert.Nil(t, codexTimezoneData(candidate, "different-final-type"), footer)
	}
	duplicate := append([]byte(nil), data[:44]...)
	binary.BigEndian.PutUint32(duplicate[32:36], 2)
	duplicate = append(duplicate, data[44:48]...)
	duplicate = append(duplicate, data[44:48]...)
	duplicate = append(duplicate, 1, 1)
	duplicate = append(duplicate, data[49:]...)
	assert.Nil(t, codexTimezoneData(duplicate, "duplicate-transition"))
}

func TestCodexRetryMinuteUsesTheCompleteLeapYear(t *testing.T) {
	t.Parallel()
	location := codexTimezoneData(codexTestTZVersionTwo(t, "EST5EDT,M3.2.0,M11.1.0"), "native-zone")
	require.NotNil(t, location)
	now := time.Date(2028, time.December, 31, 18, 0, 0, 0, time.UTC)
	started := now.Add(-time.Minute)
	for _, text := range []string{"Try again at Dec 31st, 2028 3:00 PM.", "Try again at 3:00 PM."} {
		reset, ok := parseCodexRetryText(text, codexRetryTextObservation{location: location, startedAt: &started, completedAt: &now}, now)
		assert.True(t, ok, text)
		assert.Equal(t, time.Date(2028, time.December, 31, 20, 1, 0, 0, time.UTC), reset, text)
	}
	for _, instant := range []time.Time{
		time.Date(2028, time.December, 30, 23, 59, 59, 0, time.UTC),
		time.Date(2028, time.December, 31, 0, 0, 0, 0, time.UTC),
		time.Date(2028, time.December, 31, 23, 59, 59, 0, time.UTC),
		time.Date(2029, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2029, time.January, 1, 0, 0, 1, 0, time.UTC),
	} {
		_, end := codexZonePeriod(instant, location)
		assert.True(t, end.IsZero() || end.After(instant), "the offset period must advance at %s", instant)
	}
}

func TestCodexTimezoneDeclinesAnAlternateFooterOutsideNativeYears(t *testing.T) {
	t.Parallel()
	transition := int32(0)
	first := codexTestTZData(t, []int32{0, 0}, &transition)
	first[4] = '2'
	second := append([]byte(nil), first[:44]...)
	second = binary.BigEndian.AppendUint64(second, math.MaxInt64)
	second = append(second, first[48:]...)
	data := append(append(first, second...), []byte("\nTST0DST,M3.2.0,M11.1.0\n")...)
	_, err := time.LoadLocationFromTZData("go-accepts", data)
	require.NoError(t, err)
	assert.Nil(t, codexTimezoneData(data, "outside-native-years"))
}

func TestCodexNativeNewYorkFileUsesTheCompleteLeapYear(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("/usr/share/zoneinfo/America/New_York")
	if os.IsNotExist(err) {
		t.Skip("the native New York timezone file is unavailable")
	}
	require.NoError(t, err)
	location := codexTimezoneData(data, "native-New-York")
	require.NotNil(t, location)
	now := time.Date(2040, time.December, 31, 18, 0, 0, 0, time.UTC)
	reset, ok := parseCodexRetryText("Try again at Dec 31st, 2040 3:00 PM.", codexRetryTextObservation{location: location}, now)
	require.True(t, ok)
	assert.Equal(t, time.Date(2040, time.December, 31, 20, 1, 0, 0, time.UTC), reset)
}
