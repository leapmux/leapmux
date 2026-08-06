// Package memlimit answers one question: how much memory may this process
// assume it is allowed to use?
//
// It exists because a fixed byte constant is wrong at both ends of the hardware
// range a LeapMux Hub runs on. The same default has to behave on a 512 MiB
// container -- where a generous queue budget IS the OOM -- and on a 256 GiB
// host, where a conservative one drops connections while gigabytes sit idle.
// Neither is a tuning problem the operator should have to discover from a
// crash.
package memlimit

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
)

// Source names where a Basis came from, for the startup log. An operator
// reading "512.0 MiB (source=fallback)" learns something a bare number does
// not: that nothing on this machine could be probed and the figure is a guess.
type Source string

const (
	// SourceGoMemLimit: GOMEMLIMIT is set. The most authoritative answer there
	// is -- somebody stated the budget explicitly.
	SourceGoMemLimit Source = "gomemlimit"
	// SourceCgroup: a cgroup memory limit applies. The right answer inside a
	// container, where physical memory is the HOST's and wildly too large.
	SourceCgroup Source = "cgroup"
	// SourcePhysical: total physical memory.
	SourcePhysical Source = "physical"
	// SourceFallback: nothing could be probed.
	SourceFallback Source = "fallback"
)

// FallbackBytes is used when every probe fails. Deliberately modest: an
// under-estimate costs throughput on a big machine, an over-estimate costs the
// process.
const FallbackBytes int64 = 512 << 20 // 512 MiB

// errNoLimit is returned by a probe that ran fine and found no limit in force
// (an unlimited cgroup, an unset GOMEMLIMIT). Distinct from a real failure so
// Detect does not report "fallback" for a machine it read successfully.
var errNoLimit = errors.New("memlimit: no limit configured")

// sanitizePhysicalMemory narrows a kernel-reported total to the int64 every
// budget downstream is expressed in, refusing anything that would not survive
// the trip.
//
// Both the darwin sysctl and the Windows status struct hand back a uint64, and
// on both a zero or a value past the signed range is a broken answer rather
// than a very large machine -- it would land NEGATIVE as an int64 and size
// every pool off it. They report it as errNoLimit rather than a failure,
// because the probe itself ran fine; the precedence chain simply has nothing
// usable from it. Untagged so both GOOS files reach it, and so the rejection
// can be tested on a platform that has neither.
//
// The Linux probe does NOT come through here: it parses /proc/meminfo as a
// decimal int64 in KiB, so its guard has a different type, a different
// threshold (sized for the KiB-to-bytes multiply), and real parse errors to
// report. See parseMeminfoTotal.
func sanitizePhysicalMemory(total uint64) (int64, error) {
	if total == 0 || total > 1<<62 {
		return 0, errNoLimit
	}
	return int64(total), nil
}

// Basis is the memory figure a process should size itself against.
type Basis struct {
	Bytes  int64
	Source Source

	// CgroupErr is the cgroup probe's failure when the precedence had to step
	// over it -- the machine may be confined by a limit this figure does not
	// reflect, which is the one way Detect can be badly wrong rather than merely
	// imprecise.
	//
	// Nil is the normal case and covers BOTH "the cgroup limit is the figure
	// above" and "the probe ran fine and no cgroup limit applies": an
	// unconfined host must not log a warning every time it starts, or the
	// warning stops meaning anything. It is set only for a diagnosis that names
	// something wrong -- a limit file that could not be read, or one whose
	// contents did not parse. See probeFailure.
	CgroupErr error
}

// Figure renders WHICH figure is in force and where it came from -- "5.8 GiB
// (source=physical)" -- and nothing else.
//
// This is the rendering to embed in a line that reports CgroupErr on its own,
// which anything logging several derived numbers should: the basis is a
// property of the PROCESS, probed once, so a failure repeated per derived
// number is the same diagnosis printed N times. The Hub's three queue budgets
// each carried String() and printed one probe failure three times inside a
// single log line, burying the thing it exists to surface.
func (b Basis) Figure() string {
	return fmt.Sprintf("%s (source=%s)", HumanBytes(b.Bytes), b.Source)
}

// String renders the basis STANDALONE -- for a caller that reports the figure
// and nothing else about it, and so has no other place to surface a failed
// probe.
//
// A stepped-over cgroup failure is appended rather than replacing the source,
// because both halves matter: WHICH figure is being used, and that a tighter
// one may exist and could not be read. Without it, `source=physical` on a
// confined machine reads exactly like `source=physical` on an unconfined one.
//
// A caller that surfaces CgroupErr itself wants Figure instead, or the failure
// is reported twice.
func (b Basis) String() string {
	if b.CgroupErr == nil {
		return b.Figure()
	}
	return fmt.Sprintf("%s (source=%s; cgroup probe failed: %v)", HumanBytes(b.Bytes), b.Source, b.CgroupErr)
}

// Detect returns the tightest credible memory budget for this process.
//
// Precedence is deliberate. GOMEMLIMIT wins because it is a statement of
// intent, and it is what the Go runtime itself will hold the heap to -- sizing
// above it would only mean the GC thrashes before the bound is ever reached. A
// cgroup limit wins over physical memory because in a container the physical
// figure is the host's and can be two orders of magnitude too large. Physical
// memory is the bare-metal answer.
//
// Every probe degrades to the next rather than failing: a hub that refused to
// start because /proc was not mounted would be trading a tuning default for an
// outage.
func Detect() Basis {
	if limit := goMemLimit(); limit > 0 {
		return Basis{Bytes: limit, Source: SourceGoMemLimit}
	}
	return detectFrom()
}

// cgroupProbe and physicalProbe are the platform implementations, as variables
// so a test can state the machine it means to describe. Every branch of the
// precedence below is unreachable on any single host -- a Linux CI box has no
// darwin path and a laptop has no cgroups -- so without a seam the logic that
// decides which limit binds would only ever be exercised in one shape.
var (
	cgroupProbe   = cgroupLimit
	physicalProbe = physicalMemory
)

// setMemoryLimit wraps debug.SetMemoryLimit so a test can install a GOMEMLIMIT
// without an env var and a subprocess.
var setMemoryLimit = debug.SetMemoryLimit

// detectFrom resolves the basis from the two machine probes, GOMEMLIMIT having
// already been ruled out.
//
// A failed cgroup probe is carried out on the Basis rather than discarded. It
// used to be dropped the moment physical memory answered, so a process that IS
// confined but whose limit could not be read reported `source=physical` --
// indistinguishable from a genuinely unconfined host, and the only place that
// difference would ever have surfaced is the OOM kill.
func detectFrom() Basis {
	cgroup, cgroupErr := cgroupProbe()
	physical, physicalErr := physicalProbe()
	basis := Basis{Bytes: FallbackBytes, Source: SourceFallback}
	switch {
	case cgroupErr == nil && physicalErr == nil:
		// A cgroup limit above physical memory is not a limit; report the
		// figure that actually binds, and name it honestly.
		if cgroup < physical {
			basis = Basis{Bytes: cgroup, Source: SourceCgroup}
		} else {
			basis = Basis{Bytes: physical, Source: SourcePhysical}
		}
	case cgroupErr == nil:
		basis = Basis{Bytes: cgroup, Source: SourceCgroup}
	case physicalErr == nil:
		basis = Basis{Bytes: physical, Source: SourcePhysical}
	}
	// Unconditional: probeFailure is nil whenever the cgroup answer was used
	// (that arm's error is nil) and whenever the probe merely found no limit,
	// so this cannot annotate a basis that has nothing wrong with it.
	basis.CgroupErr = probeFailure(cgroupErr)
	return basis
}

// probeFailure separates a probe's two kinds of non-nil error: one that names
// something WRONG, and errNoLimit, which reports a machine read successfully on
// which nothing applies. Only the first is worth an operator's attention.
func probeFailure(err error) error {
	if err == nil || errors.Is(err, errNoLimit) {
		return nil
	}
	return err
}

// goMemLimit reports GOMEMLIMIT in bytes, or 0 when it is unset.
//
// SetMemoryLimit(-1) is a READ -- a negative argument leaves the limit
// untouched and returns it -- and math.MaxInt64 is the documented "no limit"
// value, not a 8-exabyte machine.
func goMemLimit() int64 {
	limit := setMemoryLimit(-1)
	if limit <= 0 || limit == math.MaxInt64 {
		return 0
	}
	return limit
}

// parseMeminfoTotal pulls MemTotal out of /proc/meminfo content. The value is
// in kibibytes ("MemTotal:       16311456 kB"), which is why this cannot just
// be a strconv on the field.
func parseMeminfoTotal(content string) (int64, error) {
	for line := range strings.SplitSeq(content, "\n") {
		rest, found := strings.CutPrefix(line, "MemTotal:")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, errors.New("memlimit: MemTotal has no value")
		}
		kib, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("memlimit: parse MemTotal: %w", err)
		}
		if kib <= 0 {
			return 0, errors.New("memlimit: MemTotal is not positive")
		}
		// The overflow rejection sanitizePhysicalMemory makes for the other two
		// probes, at the threshold THIS one needs: their uint64 is already in
		// bytes, whereas this value still has a KiB->bytes multiply ahead of it.
		// Absent, a corrupt or hostile /proc/meminfo with a huge MemTotal
		// wrapped that multiply into a NEGATIVE basis, which every share derived
		// from it inherits.
		if kib > math.MaxInt64/1024 {
			return 0, errors.New("memlimit: MemTotal is implausibly large")
		}
		return kib * 1024, nil
	}
	return 0, errors.New("memlimit: MemTotal not found")
}

// parseCgroupLimit reads one cgroup memory-limit file's content.
//
// Both hierarchies spell "unlimited" as a value rather than an absent file:
// v2 writes the literal "max", and v1 writes a sentinel so large it is really
// PAGE_COUNTER_MAX rounded to the page size. Treating either as a real limit
// would hand the caller a number in the exabytes.
func parseCgroupLimit(content string) (int64, error) {
	text := strings.TrimSpace(content)
	if text == "" || text == "max" {
		return 0, errNoLimit
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("memlimit: parse cgroup limit %q: %w", text, err)
	}
	if value <= 0 {
		return 0, errNoLimit
	}
	// cgroup v1's "no limit" is PAGE_COUNTER_MAX * PAGE_SIZE, which lands just
	// under 2^63. Anything at that scale is the sentinel, not a machine.
	if value >= cgroupV1Unlimited {
		return 0, errNoLimit
	}
	return value, nil
}

// selfCgroupPaths extracts the cgroup-relative paths for the unified (v2) and
// the memory (v1) controllers from /proc/self/cgroup content.
//
// The format is `hierarchy-ID:controller-list:cgroup-path` per line. v2 is the
// line whose controller list is empty (`0::/foo`); v1's memory controller is
// the line listing `memory` among its comma-separated controllers.
func selfCgroupPaths(content string) (v2, v1 string) {
	v2, v1 = "/", "/"
	for line := range strings.SplitSeq(content, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		switch {
		case parts[1] == "":
			v2 = parts[2]
		case slices.Contains(strings.Split(parts[1], ","), "memory"):
			v1 = parts[2]
		}
	}
	return v2, v1
}

// cgroupMount is one mounted cgroup hierarchy: the directory it is mounted on,
// and which part of the hierarchy that mount exposes.
//
// root is "/" for a normal mount of a whole hierarchy, and something else when
// the mount shows only a SUBTREE of it -- a nested container is the usual way
// to see one, its runtime bind-mounting the outer container's own cgroup as the
// root of what the inner one sees. That distinction cannot be ignored, because
// /proc/self/cgroup keeps naming this process's cgroup by its path in the whole
// hierarchy: joining that path straight onto the mount point would repeat the
// prefix the mount already stands for and address a directory that does not
// exist.
type cgroupMount struct {
	point string
	root  string
}

// rel maps a cgroup path as /proc/self/cgroup spells it -- relative to the
// hierarchy root -- onto a path relative to this MOUNT, which is what can be
// joined onto point.
//
// It reports false for a cgroup OUTSIDE the subtree this mount exposes, which
// cannot be addressed through the mount at all. Answering with the mount's own
// root instead would be worse than finding nothing: every limit reachable under
// it belongs to some OTHER cgroup, and one likelier to be tighter than this
// process's own -- a mount that exposes a subtree exists to point at one
// particular service -- so the hub would size its queues against a bound that
// was never its own.
func (m cgroupMount) rel(cgroupPath string) (string, bool) {
	root := path.Clean("/" + m.root)
	full := path.Clean("/" + cgroupPath)
	switch {
	case root == "/":
		return full, true
	case full == root:
		return "/", true
	default:
		// The trailing separator carries the test: without it /docker/abcdef
		// reads as a child of /docker/abc, and the walk climbs a chain of
		// directories that never existed -- or worse, one that exists and
		// belongs to somebody else.
		rest, found := strings.CutPrefix(full, root+"/")
		if !found {
			return "", false
		}
		return "/" + rest, true
	}
}

// mountinfoFixedFields is how many fields every /proc/self/mountinfo line has
// before its optional ones: mount ID, parent ID, major:minor, root, mount
// point, mount options.
const mountinfoFixedFields = 6

// cgroupMounts lists, out of /proc/self/mountinfo content, the mounts carrying
// each hierarchy: the cgroup2 filesystems for v2, and for v1 the cgroup
// filesystems whose super options name the memory controller (v1 mounts one
// controller set per mount, and only that one has memory.limit_in_bytes).
//
// Reading mountinfo rather than assuming /sys/fs/cgroup is the same class of
// /proc read the cgroup path itself comes from, and it is the half that
// assumption gets wrong: a runtime free to mount cgroup2 anywhere -- a rootless
// or nested setup, a minimal image, a deliberately relocated cgroup root --
// leaves the probe reading a directory that holds no limits, which is the exact
// shape of the bug where a confined process sized itself off the host's RAM.
//
// Every match is kept, in mountinfo order, rather than only the first: a mount
// can expose a subtree that this process's cgroup is not in (see cgroupMount),
// and the caller has to be able to pass over one of those for a mount that can
// actually address it. Either list is empty when mountinfo names no such mount,
// which the caller answers by falling back to the conventional location.
func cgroupMounts(content string) (v2, v1 []cgroupMount) {
	for line := range strings.SplitSeq(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < mountinfoFixedFields {
			continue
		}
		// Between the fixed fields and the filesystem type sits an arbitrary
		// number of optional fields ("shared:2 master:1"), terminated by a "-".
		// So the type is the field AFTER that separator, never a fixed index --
		// and the search starts at the first optional field, so that a mount
		// point or an option spelled "-" cannot be taken for the terminator.
		offset := slices.Index(fields[mountinfoFixedFields:], "-")
		if offset < 0 || len(fields) <= mountinfoFixedFields+offset+1 {
			continue
		}
		separator := mountinfoFixedFields + offset
		// Mount roots and mount points are escaped, so they must be decoded
		// before they are used as paths.
		mount := cgroupMount{
			point: unescapeMountinfoPath(fields[4]),
			root:  unescapeMountinfoPath(fields[3]),
		}
		// The v1 arm reaches two fields further along, past the mount source, so
		// it has to bounds-check separately -- a truncated line must be skipped,
		// not panic the process that was only trying to size a queue.
		switch fsType := fields[separator+1]; {
		case fsType == "cgroup2":
			v2 = append(v2, mount)
		case fsType == "cgroup" && len(fields) > separator+3 &&
			slices.Contains(strings.Split(fields[separator+3], ","), "memory"):
			v1 = append(v1, mount)
		}
	}
	return v2, v1
}

// unescapeMountinfoPath decodes the octal escapes the kernel writes into
// mountinfo's path fields. mangle_path() escapes exactly the four bytes that
// would otherwise make a space-separated line unparseable -- space (\040), tab
// (\011), newline (\012) and the backslash itself (\134) -- so a hierarchy
// mounted on "/var/lib/my cgroups" arrives as "/var/lib/my\040cgroups", and
// joining that literal onto a cgroup path names a directory that does not
// exist.
func unescapeMountinfoPath(field string) string {
	if !strings.Contains(field, `\`) {
		return field
	}
	var out strings.Builder
	out.Grow(len(field))
	for i := 0; i < len(field); {
		// A backslash not followed by three octal digits is a literal one: the
		// kernel would have escaped it, so this is a hand-written or a
		// future-kernel spelling, and dropping the character would corrupt the
		// path rather than decode it.
		if field[i] == '\\' && i+4 <= len(field) {
			if b, err := strconv.ParseUint(field[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(b))
				i += 4
				continue
			}
		}
		out.WriteByte(field[i])
		i++
	}
	return out.String()
}

// ancestorLimitFiles lists `<root>/<rel>/<file>` for rel and each of its
// ancestors, LEAF FIRST. Order is presentational only -- the walk takes the
// tightest limit it finds, not the first -- but leaf-first keeps the more
// specific path in front for anything that logs the candidates.
func ancestorLimitFiles(root, rel, file string) []string {
	rel = path.Clean("/" + rel)
	var out []string
	for {
		out = append(out, path.Join(root, rel, file))
		if rel == "/" {
			return out
		}
		rel = path.Dir(rel)
	}
}

// readFile is a seam so the walk below -- and the /proc reads that decide which
// files it walks -- can be tested on any host, including the macOS laptops this
// is developed on, where none of these paths exist. Tests substitute it through
// stubReadFile in memlimit_linux_test.go.
var readFile = os.ReadFile

// cgroupLimit reports the memory limit the process's cgroup imposes, or
// errNoLimit where the concept does not apply. Platforms supply the candidate
// paths (empty on the ones with no cgroups); the walk itself is shared, so its
// rules are exercised by the same tests everywhere rather than only on a Linux
// developer's machine.
func cgroupLimit() (int64, error) { return readCgroupLimit(cgroupPaths(), readFile) }

// readCgroupLimit walks candidate cgroup limit files and returns the TIGHTEST
// real limit any of them imposes.
//
// Tightest, not first: the candidates span both hierarchies AND every ancestor
// of this process's own cgroup, and a limit anywhere on that chain binds. A
// hybrid host mounts both hierarchies and the process is usually unconstrained
// on one of them, so a file that reads fine and imposes nothing is not an error
// and must not stop the walk.
//
// The error it returns when no limit was found is a DIAGNOSIS: errNoLimit when
// every candidate answered "nothing applies here", and otherwise the first
// candidate that failed for a reason worth reporting.
func readCgroupLimit(paths []string, read func(string) ([]byte, error)) (int64, error) {
	var (
		firstErr error
		best     int64
	)
	// noteErr keeps the FIRST error that names something wrong, and ignores the
	// two that do not: errNoLimit (the file read fine and imposes nothing) and
	// an absent file, which says exactly the same thing -- the hierarchy is not
	// mounted, or this level of it has no memory limit file, which on v2's root
	// cgroup is every unconstrained Linux host in existence. Reporting either as
	// a failure would make the "cgroup probe failed" signal fire on machines
	// with nothing wrong with them, and a warning that is always on is not one.
	//
	// Ignoring rather than recording them is also what keeps the informative
	// diagnosis: firstErr used to be whichever error came first in candidate
	// order, so a permission failure on the v1 hierarchy was masked by the "max"
	// the v2 leaf had already reported.
	noteErr := func(err error) {
		if errors.Is(err, errNoLimit) || errors.Is(err, fs.ErrNotExist) {
			return
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, path := range paths {
		content, err := read(path)
		if err != nil {
			noteErr(err)
			continue
		}
		limit, err := parseCgroupLimit(string(content))
		if err != nil {
			// errNoLimit means this file read fine and imposes nothing; keep
			// looking rather than reporting a failure that never happened.
			noteErr(err)
			continue
		}
		if best == 0 || limit < best {
			best = limit
		}
	}
	if best > 0 {
		return best, nil
	}
	if firstErr == nil {
		firstErr = errNoLimit
	}
	return 0, firstErr
}

// cgroupV1Unlimited is the threshold above which a v1 limit is the "unlimited"
// sentinel. The exact sentinel varies with page size, so this compares against
// a bound no real machine reaches (8 EiB / 2).
const cgroupV1Unlimited int64 = math.MaxInt64 / 2

// HumanBytes renders a byte count the way an operator reads one. Used in the
// startup log and in config validation messages, so a misconfigured budget is
// legible without counting digits.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit && exp < 4; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
