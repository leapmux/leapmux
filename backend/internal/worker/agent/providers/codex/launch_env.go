package codex

import (
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

func codexNativeEnvMetadata() *launch.EnvMetadata {
	if runtime.GOOS == "windows" {
		return nil
	}
	executable, err := os.Executable()
	// An unavailable helper leaves clock text untrusted. Structured resets still work.
	if err != nil {
		return nil
	}
	return &launch.EnvMetadata{Program: executable, Variables: []string{"TZ"}}
}

func (a *Agent) loadRetryTextLocation() {
	var location *time.Location
	if runtime.GOOS == "windows" {
		// Chrono reads the Windows timezone API rather than Unix TZ rules.
		location = time.Local
	} else if snapshot, err := envprobe.DecodeMetadata(a.PreambleMetaValue(envprobe.MetadataKey)); err == nil {
		location = codexUnixRetryTextLocation(snapshot, func(path string) (io.ReadCloser, error) { return os.Open(path) })
	}
	a.Mu.Lock()
	a.retryTextLocation = location
	a.Mu.Unlock()
}

type codexTimezoneOpener func(string) (io.ReadCloser, error)

// codexUnixRetryTextLocation follows Chrono's file precedence. A missing or
// unsupported configuration remains unknown rather than using the worker zone.
func codexUnixRetryTextLocation(snapshot envprobe.Snapshot, open codexTimezoneOpener) *time.Location {
	value, hasValue := snapshot.Values["TZ"]
	present, known := snapshot.Present["TZ"]
	if !hasValue || !known {
		return nil
	}
	if !present || value == "localtime" {
		location, _ := readCodexTimezoneFile("/etc/localtime", "native-local", open)
		return location
	}
	if value == "" {
		return time.UTC
	}
	fileOnly := strings.HasPrefix(value, ":")
	name := value
	if fileOnly {
		name = name[1:]
	}
	if strings.HasPrefix(name, "/") {
		location, _ := readCodexTimezoneFile(name, name, open)
		return location
	}
	for _, directory := range []string{"/usr/share/zoneinfo", "/share/zoneinfo", "/etc/zoneinfo", "/usr/share/lib/zoneinfo"} {
		// Rust preserves symlink/.. semantics here. filepath.Join would remove them.
		location, opened := readCodexTimezoneFile(directory+"/"+name, name, open)
		if opened {
			return location
		}
	}
	if fileOnly {
		return nil
	}
	return codexFixedPOSIXTimezone(strings.Trim(value, " \t\n\r\v\f"))
}

func readCodexTimezoneFile(path, name string, open codexTimezoneOpener) (*time.Location, bool) {
	file, err := open(path)
	if err != nil {
		return nil, false
	}
	// The read owns all relevant data; closing a read-only file changes none.
	defer func() { _ = file.Close() }()
	const maximumTimezoneBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, maximumTimezoneBytes+1))
	if err != nil || len(data) > maximumTimezoneBytes {
		return nil, true
	}
	return codexTimezoneData(data, name), true
}

// POSIX offsets count west of UTC. A rule with daylight-saving transitions
// requires native TZif data; this parser accepts fixed offsets only.
var codexFixedPOSIXTimezonePattern = regexp.MustCompile(`^([A-Za-z]{3,7}|<[A-Za-z0-9+-]{3,7}>)([+-]?)([0-9]{1,2})(?::([0-9]{2})(?::([0-9]{2}))?)?$`)

var codexTZAlternatePattern = regexp.MustCompile(`^([A-Za-z]{3,7}|<[A-Za-z0-9+-]{3,7}>)([+-]?[0-9]{1,2}(?::[0-9]{2}(?::[0-9]{2})?)?)([A-Za-z]{3,7}|<[A-Za-z0-9+-]{3,7}>)([+-]?[0-9]{1,2}(?::[0-9]{2}(?::[0-9]{2})?)?)?$`)

func codexFixedPOSIXTimezone(value string) *time.Location {
	match := codexFixedPOSIXTimezonePattern.FindStringSubmatch(value)
	if match == nil {
		return nil
	}
	hours, _ := strconv.Atoi(match[3])
	minutes, _ := strconv.Atoi(match[4])
	seconds, _ := strconv.Atoi(match[5])
	if hours > 23 || minutes > 59 || seconds > 59 {
		return nil
	}
	offset := hours*3600 + minutes*60 + seconds
	if match[2] != "-" {
		offset = -offset
	}
	return time.FixedZone(strings.Trim(match[1], "<>"), offset)
}
