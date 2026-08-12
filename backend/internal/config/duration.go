package config

import (
	"flag"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/v2"
)

// UnitSyntax describes the spellings ParseDuration accepts. Put it in the help
// text of every duration flag, so the CLI states the syntax once per flag and
// the operator never has to find this package.
//
// The backquoted word is the placeholder that flag.UnquoteUsage lifts into the
// flag's help line, which would otherwise read the bare "value" that the flag
// package prints for every flag.Value.
const UnitSyntax = "Takes a `duration` suffix: ns, us, ms, s, m, h, d, w. A bare number is seconds."

// extendedUnits holds the two units that time.ParseDuration does not know.
// Everything else goes to the standard parser unchanged, so a duration this
// package accepts keeps the exact meaning the Go documentation gives it.
//
// A day is 24 hours and a week is 7 days, with no daylight-saving correction: a
// session lifetime and a connection lifetime are elapsed time, not calendar
// time, and the code that consumes them adds them to an instant.
var extendedUnits = map[string]time.Duration{
	"d": 24 * time.Hour,
	"w": 7 * 24 * time.Hour,
}

// ParseDuration parses a duration that a configuration source supplies: a
// config-file value, an environment variable, or a CLI flag.
//
// It accepts everything time.ParseDuration accepts ("500ms", "1h30m",
// "1.5h"), adds the d and w units that the standard parser rejects ("7d",
// "2w3d"), and reads a bare number as a count of seconds ("3600"), which is the
// spelling every one of these options carried before it took a unit.
//
// A number with no unit is only valid as the WHOLE value. "1d30" is an error
// rather than a day and thirty seconds, because a reader cannot tell that
// trailing number from a typing error in the unit.
//
// The result never overflows: a value past the range of a time.Duration
// (approximately 292 years) fails here rather than wrapping to a negative or a
// tiny duration at the multiplication.
func ParseDuration(s string) (time.Duration, error) {
	text := strings.TrimSpace(s)
	if text == "" {
		return 0, fmt.Errorf("parse duration %q: the value is empty", s)
	}

	negative := false
	switch text[0] {
	case '+':
		text = text[1:]
	case '-':
		negative = true
		text = text[1:]
	}
	if text == "" {
		return 0, fmt.Errorf("parse duration %q: the value has a sign and no number", s)
	}

	var total time.Duration
	var parts int
	for text != "" {
		number, rest := splitNumber(text)
		if number == "" {
			return 0, fmt.Errorf("parse duration %q: expected a number at %q (%s)", s, text, UnitSyntax)
		}
		unit, remainder := splitUnit(rest)

		var part time.Duration
		var err error
		switch {
		case unit == "":
			// A bare number is a count of seconds, and only as the whole value.
			if parts > 0 || remainder != "" {
				return 0, fmt.Errorf("parse duration %q: %q has no unit (%s)", s, number, UnitSyntax)
			}
			part, err = scale(number, time.Second)
		case extendedUnits[unit] != 0:
			part, err = scale(number, extendedUnits[unit])
		default:
			// The standard parser owns every remaining unit, so it also owns the
			// message for a unit that nothing knows.
			part, err = time.ParseDuration(number + unit)
			if err != nil {
				err = fmt.Errorf("%q is not a duration (%s)", number+unit, UnitSyntax)
			}
		}
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", s, err)
		}

		total, err = add(total, part)
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", s, err)
		}
		text = remainder
		parts++
	}

	if negative {
		return -total, nil
	}
	return total, nil
}

// FormatDuration writes d in the shortest exact spelling that ParseDuration
// reads back. It is what a CLI flag reports as its default and what the flag
// hands to the configuration loader, so an inexact form would change the value
// that the operator sees or the value that the Hub loads.
//
// Weeks are deliberately absent: "7d" states a session lifetime more plainly
// than "1w", and both parse.
func FormatDuration(d time.Duration) string {
	// Zero is "0" rather than "0s" so the flag package still recognizes it as a
	// zero default and leaves it out of the help text.
	if d == 0 {
		return "0"
	}
	// The most negative duration has no positive counterpart, so it cannot take
	// the sign-and-magnitude path below.
	if d == math.MinInt64 {
		return d.String()
	}

	sign := ""
	magnitude := d
	if magnitude < 0 {
		sign = "-"
		magnitude = -magnitude
	}
	for _, unit := range []struct {
		size   time.Duration
		suffix string
	}{
		{24 * time.Hour, "d"},
		{time.Hour, "h"},
		{time.Minute, "m"},
		{time.Second, "s"},
	} {
		if magnitude%unit.size == 0 {
			return sign + strconv.FormatInt(int64(magnitude/unit.size), 10) + unit.suffix
		}
	}
	return d.String()
}

// splitNumber cuts the leading number from s.
func splitNumber(s string) (number, rest string) {
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	return s[:i], s[i:]
}

// splitUnit cuts the leading unit from s. The scan ends at the next digit
// rather than at the next ASCII letter, so a multi-byte unit such as "µs"
// stays whole.
func splitUnit(s string) (unit, rest string) {
	for i, r := range s {
		if r >= '0' && r <= '9' || r == '.' {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// scale multiplies a non-negative number by a unit and fails on overflow.
func scale(number string, unit time.Duration) (time.Duration, error) {
	if !strings.Contains(number, ".") {
		v, err := strconv.ParseInt(number, 10, 64)
		if err != nil || v > math.MaxInt64/int64(unit) {
			return 0, fmt.Errorf("%s%s is out of range (the maximum is about 292 years)", number, unitSuffix(unit))
		}
		return time.Duration(v) * unit, nil
	}
	v, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", number)
	}
	scaled := v * float64(unit)
	if scaled > math.MaxInt64 {
		return 0, fmt.Errorf("%s%s is out of range (the maximum is about 292 years)", number, unitSuffix(unit))
	}
	return time.Duration(scaled), nil
}

// unitSuffix gives a unit its spelling again, for an error message about the
// component that overflowed.
func unitSuffix(unit time.Duration) string {
	for suffix, size := range extendedUnits {
		if size == unit {
			return suffix
		}
	}
	return "s"
}

// add sums two non-negative durations and fails on overflow.
func add(a, b time.Duration) (time.Duration, error) {
	if a > math.MaxInt64-b {
		return 0, fmt.Errorf("the total is out of range (the maximum is about 292 years)")
	}
	return a + b, nil
}

// DurationFlag adapts a time.Duration to the flag package, so a CLI flag reads
// the same spellings as the config file and the environment variable. The
// standard flag.Duration accepts neither the bare seconds count nor the d and w
// units, so a flag registered with it would refuse a value that the same key
// takes in the config file.
type DurationFlag struct {
	target *time.Duration
}

// NewDurationFlag returns a flag.Value that writes into target, and sets target
// to def so the flag reports the default before Parse runs.
func NewDurationFlag(target *time.Duration, def time.Duration) *DurationFlag {
	*target = def
	return &DurationFlag{target: target}
}

// String reports the current value. The flag package builds a zero DurationFlag
// by reflection to recognize a zero default, so this must answer on a value
// that carries no target.
func (f *DurationFlag) String() string {
	if f == nil || f.target == nil {
		return "0"
	}
	return FormatDuration(*f.target)
}

// Set parses and stores one flag occurrence.
func (f *DurationFlag) Set(s string) error {
	d, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*f.target = d
	return nil
}

// Get satisfies flag.Getter, which the help printer uses to decide whether a
// default needs quotation marks.
func (f *DurationFlag) Get() any {
	if f == nil || f.target == nil {
		return time.Duration(0)
	}
	return *f.target
}

var _ flag.Getter = (*DurationFlag)(nil)

// toDurationHookFunc converts a configured value to a time.Duration.
//
// It replaces mapstructure's own StringToTimeDurationHookFunc, which calls
// time.ParseDuration and so rejects both the bare seconds count and the d and w
// units. Leaving that hook in place would make a duration key mean one thing
// from a CLI flag and another from a config file.
//
// A number needs the same treatment as a string, and for a reason that is easy
// to miss: a YAML parser reads `api_timeout: 10` as an int, so that value never
// reaches the string arm. Without the number arm mapstructure would convert it
// as a raw int64 count of NANOSECONDS, and every config file that spells a
// timeout the way this key has always taken would silently become a timeout of
// a few microseconds.
func toDurationHookFunc() mapstructure.DecodeHookFuncType {
	durationType := reflect.TypeOf(time.Duration(0))
	return func(from, to reflect.Type, data any) (any, error) {
		if to != durationType {
			return data, nil
		}
		switch from.Kind() {
		case reflect.String:
			return ParseDuration(data.(string))
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			// Route it through the same parser, so a number out of range fails
			// here rather than wrapping, exactly as the text form does.
			return ParseDuration(fmt.Sprintf("%v", data))
		default:
			return data, nil
		}
	}
}

// Unmarshal decodes the loaded configuration into out.
//
// Every config package goes through this rather than koanf's own Unmarshal, so
// one set of decode rules covers the Hub and the Worker. It keeps koanf's
// defaults that the configuration depends on -- the koanf struct tag and weakly
// typed input, which every string-valued source needs to reach an int or a bool
// field -- and replaces koanf's duration hook with ours.
func Unmarshal(k *koanf.Koanf, out any) error {
	return k.UnmarshalWithConf("", out, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				toDurationHookFunc(),
				mapstructure.TextUnmarshallerHookFunc(),
			),
			WeaklyTypedInput: true,
		},
	})
}
