package codex

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// codexTimezoneData validates the native Chrono subset before Go interprets it.
// Go accepts malformed records that Chrono rejects, ignores leap seconds, and
// evaluates some POSIX rules differently. Such files cannot prove a native zone.
func codexTimezoneData(data []byte, name string) *time.Location {
	block, remaining, ok := readCodexTZBlock(data, 4)
	if !ok {
		return nil
	}
	var footerLocation *time.Location
	if block.version != 0 {
		version := block.version
		block, remaining, ok = readCodexTZBlock(remaining, 8)
		if !ok || block.version != version {
			return nil
		}
		if len(remaining) < 2 || remaining[0] != '\n' || remaining[len(remaining)-1] != '\n' || !utf8.Valid(remaining) {
			return nil
		}
		footer := strings.Trim(string(remaining), " \t\n\r\v\f")
		if string(remaining) != "\n"+footer+"\n" || footer != "" && !codexTZFooterKnown(footer) {
			return nil
		}
		if footer != "" {
			if len(block.indices) != 0 && strings.Contains(footer, ",") {
				year := time.Unix(block.transition(len(block.indices)-1), 0).UTC().Year()
				// Chrono's alternate-rule evaluator rejects extreme years that
				// Go accepts. Human calendar years are the supported subset here.
				if year < 1 || year > 9999 {
					return nil
				}
			}
			footerLocation = codexTZFooterLocation(footer)
			if footerLocation == nil {
				return nil
			}
		}
	} else if len(remaining) != 0 {
		return nil
	}
	if !block.valid() {
		return nil
	}
	location, err := time.LoadLocationFromTZData(name, data)
	if err != nil {
		return nil
	}
	// Chrono selects type zero before its first transition. Go can select a
	// different type when the first type was used later or denotes daylight time.
	if len(block.transitions) != 0 && block.transition(0) != math.MinInt64 {
		if !block.matches(location, time.Unix(block.transition(0)-1, 0), 0) {
			return nil
		}
	}
	// Chrono requires a footer to agree with the final explicit transition.
	if count := len(block.indices); footerLocation != nil && count != 0 && !block.matches(footerLocation, time.Unix(block.transition(count-1), 0), int(block.indices[count-1])) {
		return nil
	}
	return location
}

// codexTZFooterLocation evaluates only the validated rule. The original
// transition table must not supply the value used to validate that table.
func codexTZFooterLocation(footer string) *time.Location {
	header := make([]byte, 44)
	copy(header, "TZif2")
	binary.BigEndian.PutUint32(header[36:40], 1)
	binary.BigEndian.PutUint32(header[40:44], 4)
	block := append(header, 0, 0, 0, 0, 0, 0, 'U', 'T', 'C', 0)
	data := append(append([]byte(nil), block...), block...)
	data = append(data, []byte("\n"+footer+"\n")...)
	location, err := time.LoadLocationFromTZData("native-rule", data)
	if err != nil {
		return nil
	}
	return location
}

type codexTZBlock struct {
	version                byte
	timeSize               int
	transitions, indices   []byte
	types, names, standard []byte
	universal              []byte
}

func readCodexTZBlock(data []byte, timeSize int) (codexTZBlock, []byte, bool) {
	var block codexTZBlock
	if len(data) < 44 || !bytes.Equal(data[:4], []byte("TZif")) || (data[4] != 0 && data[4] != '2' && data[4] != '3') {
		return block, nil, false
	}
	counts := [6]int{}
	for index := range counts {
		value := binary.BigEndian.Uint32(data[20+index*4:])
		if uint64(value) > uint64(len(data)) {
			return block, nil, false
		}
		counts[index] = int(value)
	}
	if counts[2] != 0 || counts[4] == 0 || counts[4] > 256 || counts[5] == 0 || (counts[0] != 0 && counts[0] != counts[4]) || (counts[1] != 0 && counts[1] != counts[4]) {
		return block, nil, false
	}
	remaining := data[44:]
	take := func(size int) []byte {
		if size > len(remaining) {
			ok := remaining[:0]
			remaining = nil
			return ok
		}
		value := remaining[:size]
		remaining = remaining[size:]
		return value
	}
	block = codexTZBlock{version: data[4], timeSize: timeSize}
	block.transitions = take(counts[3] * timeSize)
	block.indices = take(counts[3])
	block.types = take(counts[4] * 6)
	block.names = take(counts[5])
	block.standard = take(counts[1])
	block.universal = take(counts[0])
	if len(block.transitions) != counts[3]*timeSize || len(block.indices) != counts[3] || len(block.types) != counts[4]*6 || len(block.names) != counts[5] || len(block.standard) != counts[1] || len(block.universal) != counts[0] {
		return codexTZBlock{}, nil, false
	}
	return block, remaining, true
}

func (block codexTZBlock) transition(index int) int64 {
	data := block.transitions[index*block.timeSize:]
	if block.timeSize == 4 {
		return int64(int32(binary.BigEndian.Uint32(data)))
	}
	return int64(binary.BigEndian.Uint64(data))
}

func (block codexTZBlock) designation(index int) []byte {
	start := int(block.types[index*6+5])
	if start >= len(block.names) {
		return nil
	}
	if end := bytes.IndexByte(block.names[start:], 0); end >= 0 {
		return block.names[start : start+end]
	}
	return nil
}

func (block codexTZBlock) valid() bool {
	for index, kind := range block.indices {
		if int(kind) >= len(block.types)/6 || (index > 0 && block.transition(index) <= block.transition(index-1)) {
			return false
		}
	}
	for index := range len(block.types) / 6 {
		record := block.types[index*6:]
		offset := int32(binary.BigEndian.Uint32(record))
		if offset <= -86400 || offset >= 86400 || record[4] > 1 {
			return false
		}
		name := block.designation(index)
		if name == nil || (len(name) != 0 && !codexTZNameValid(string(name))) {
			return false
		}
		standard, universal := byte(0), byte(0)
		if len(block.standard) != 0 {
			standard = block.standard[index]
		}
		if len(block.universal) != 0 {
			universal = block.universal[index]
		}
		if standard > 1 || universal > 1 || (standard == 0 && universal == 1) {
			return false
		}
	}
	return true
}

func (block codexTZBlock) matches(location *time.Location, instant time.Time, index int) bool {
	local := instant.In(location)
	name, offset := local.Zone()
	record := block.types[index*6:]
	return name == string(block.designation(index)) && offset == int(int32(binary.BigEndian.Uint32(record))) && local.IsDST() == (record[4] == 1)
}

func codexTZNameValid(name string) bool {
	if len(name) < 3 || len(name) > 7 {
		return false
	}
	for _, char := range []byte(name) {
		if (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '+' && char != '-' {
			return false
		}
	}
	return true
}

// codexTZFooterKnown accepts rules whose Go and Chrono interpretations agree.
// Year-crossing UTC transitions are unknown because Go considers only the
// current UTC year, whereas Chrono also checks adjacent years.
func codexTZFooterKnown(value string) bool {
	parts := strings.Split(value, ",")
	if len(parts) == 1 {
		return codexFixedPOSIXTimezone(value) != nil
	}
	if len(parts) != 3 {
		return false
	}
	match := codexTZAlternatePattern.FindStringSubmatch(parts[0])
	if match == nil {
		return false
	}
	standard := codexFixedPOSIXTimezone(match[1] + match[2])
	if standard == nil {
		return false
	}
	_, standardOffset := time.Unix(0, 0).In(standard).Zone()
	dstOffset := standardOffset + 3600
	if match[4] != "" {
		dst := codexFixedPOSIXTimezone(match[3] + match[4])
		if dst == nil {
			return false
		}
		_, dstOffset = time.Unix(0, 0).In(dst).Zone()
	}
	if dstOffset <= -86400 || dstOffset >= 86400 {
		return false
	}
	for year := 2000; year < 2400; year++ {
		for index, offset := range []int{standardOffset, dstOffset} {
			transition, ok := codexTZRuleTime(parts[index+1], year, offset)
			if !ok || transition.Year() != year {
				return false
			}
		}
	}
	return true
}

func codexTZRuleTime(value string, year, offset int) (time.Time, bool) {
	parts := strings.Split(value, "/")
	if len(parts) > 2 || parts[0] == "" {
		return time.Time{}, false
	}
	seconds := 2 * 3600
	if len(parts) == 2 {
		zone := codexFixedPOSIXTimezone("UTC" + parts[1])
		if zone == nil || strings.HasPrefix(parts[1], "+") || strings.HasPrefix(parts[1], "-") {
			return time.Time{}, false
		}
		_, west := time.Unix(0, 0).In(zone).Zone()
		seconds = -west
	}
	day := parts[0]
	date := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	if strings.HasPrefix(day, "M") {
		fields := strings.Split(day[1:], ".")
		if len(fields) != 3 {
			return time.Time{}, false
		}
		month, okMonth := codexTZNumber(fields[0], 1, 12)
		week, okWeek := codexTZNumber(fields[1], 1, 5)
		weekday, okDay := codexTZNumber(fields[2], 0, 6)
		if !okMonth || !okWeek || !okDay {
			return time.Time{}, false
		}
		date = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		monthDay := 1 + (weekday-int(date.Weekday())+7)%7 + (week-1)*7
		if monthDay > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
			monthDay -= 7
		}
		date = date.AddDate(0, 0, monthDay-1)
	} else {
		minimum := 0
		julian := strings.HasPrefix(day, "J")
		if julian {
			day, minimum = day[1:], 1
		}
		number, ok := codexTZNumber(day, minimum, 365)
		if !ok {
			return time.Time{}, false
		}
		if julian {
			number--
			if number >= 59 && time.Date(year, time.March, 0, 0, 0, 0, 0, time.UTC).Day() == 29 {
				number++
			}
		}
		date = date.AddDate(0, 0, number)
	}
	return date.Add(time.Duration(seconds-offset) * time.Second), true
}

func codexTZNumber(value string, minimum, maximum int) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, char := range []byte(value) {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(value)
	return number, err == nil && number >= minimum && number <= maximum
}
