package agent

import (
	"strings"
	"unicode/utf8"
)

// ClipTailBytes keeps the LAST bytes of a tail, cut at a rune boundary.
//
// The last bytes are what a reader watches: a build prints its progress at the end.
// Cutting mid-rune would send a replacement character to the browser, so the cut
// advances to the next boundary. Shared, because the worker service and the Goose
// agent each spelled this loop and the next copy would have to get it right again.
//
// A limit of zero or less keeps nothing, and still reports the loss when the input
// holds bytes. Without that guard a negative limit computes a cut past the end of
// the string and panics there, so a caller that clamps its limit to a remaining
// budget can kill the worker with an arithmetic result it never inspects.
func ClipTailBytes(text string, limit int) (string, bool) {
	if limit <= 0 {
		return "", text != ""
	}
	if len(text) <= limit {
		return text, false
	}
	cut := len(text) - limit
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	// Clone rather than reslice. A Go string slice shares the backing array of the
	// whole input, so the caller that stores this tail for the length of a running
	// call would pin every byte the call printed -- the megabytes the clip exists
	// to release.
	return strings.Clone(text[cut:]), true
}
