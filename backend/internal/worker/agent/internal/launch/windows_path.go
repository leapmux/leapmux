package launch

import (
	"slices"
	"strings"
	"sync"
)

// windowsPathRefresh tracks the Windows account PATH from when the process
// started so later reads can distinguish saved entries from launcher additions.
type windowsPathRefresh struct {
	read func() ([]string, error)

	mu              sync.Mutex
	initial         []string
	current         []string
	initialCaptured bool
}

func newWindowsPathRefresh(read func() ([]string, error)) *windowsPathRefresh {
	refresher := &windowsPathRefresh{read: read}
	if initial, err := read(); err == nil {
		refresher.initial = initial
		refresher.current = initial
		refresher.initialCaptured = true
	}
	return refresher
}

func (r *windowsPathRefresh) readSavedPath(inherited []string) (initial, current []string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path, err := r.read()
	if err == nil {
		if !r.initialCaptured {
			// After a startup read failure, the first successful read
			// can identify only saved entries still in the inherited PATH.
			inheritedCounts := windowsPathCounts(inherited)
			for _, directory := range path {
				key := windowsPathKey(directory)
				if inheritedCounts[key] > 0 {
					r.initial = append(r.initial, directory)
					inheritedCounts[key]--
				}
			}
			r.initialCaptured = true
		}
		r.current = path
	}
	return r.initial, r.current, err
}

func (r *windowsPathRefresh) directories(inherited []string) ([]string, error) {
	initial, current, err := r.readSavedPath(inherited)
	if slices.Equal(initial, current) {
		return inherited, err
	}

	// Most launchers prepend or append directories to the saved PATH.
	// Replace that entire saved block and preserve its exact surroundings.
	if index := savedWindowsPathIndex(inherited, initial); index >= 0 {
		return slices.Concat(inherited[:index], current, inherited[index+len(initial):]), err
	}

	// A customized PATH can reorder or filter saved entries. Apply the saved
	// additions and removals without reordering retained inherited entries.
	initialCounts := windowsPathCounts(initial)
	currentCounts := windowsPathCounts(current)
	reverse := make([]string, 0, len(inherited))
	for index := len(inherited) - 1; index >= 0; index-- {
		directory := inherited[index]
		key := windowsPathKey(directory)
		if initialCounts[key] > currentCounts[key] {
			initialCounts[key]--
			continue
		}
		reverse = append(reverse, directory)
	}
	slices.Reverse(reverse)
	retainedCounts := windowsPathCounts(reverse)
	for _, directory := range current {
		key := windowsPathKey(directory)
		if max(initialCounts[key], retainedCounts[key]) < currentCounts[key] {
			reverse = append(reverse, directory)
			retainedCounts[key]++
		}
	}
	return reverse, err
}

// NUL cannot occur in a PATH entry. Delimit each entry so string search can
// match a whole contiguous block without repeatedly comparing long prefixes.
func savedWindowsPathIndex(inherited, saved []string) int {
	if len(saved) == 0 || len(saved) > len(inherited) {
		return -1
	}
	const separator = "\x00"
	var inheritedPath, savedPath strings.Builder
	inheritedPath.WriteString(separator)
	for _, directory := range inherited {
		inheritedPath.WriteString(windowsPathKey(directory))
		inheritedPath.WriteString(separator)
	}
	savedPath.WriteString(separator)
	for _, directory := range saved {
		savedPath.WriteString(windowsPathKey(directory))
		savedPath.WriteString(separator)
	}
	index := strings.Index(inheritedPath.String(), savedPath.String())
	if index < 0 {
		return -1
	}
	return strings.Count(inheritedPath.String()[:index], separator)
}

func windowsPathCounts(directories []string) map[string]int {
	keys := make(map[string]int, len(directories))
	for _, directory := range directories {
		keys[windowsPathKey(directory)]++
	}
	return keys
}

func windowsPathKey(directory string) string {
	directory = strings.ToLower(strings.ReplaceAll(directory, "/", "\\"))
	trimmed := strings.TrimRight(directory, "\\")
	if trimmed != directory && (trimmed == "" || (len(trimmed) == 2 && trimmed[1] == ':')) {
		return trimmed + "\\"
	}
	return trimmed
}
