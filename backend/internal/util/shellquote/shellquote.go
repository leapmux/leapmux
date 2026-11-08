// Package shellquote renders command lines that a person can paste into a
// shell and run unchanged: a POSIX shell (sh, bash, zsh) or PowerShell.
package shellquote

import (
	"strings"
)

// Shell identifies the syntax of a command line.
type Shell int

const (
	// POSIX is the syntax of sh, bash, and zsh.
	POSIX Shell = iota
	// PowerShell is the syntax of Windows PowerShell and PowerShell 7.
	PowerShell
)

// ForOS returns the shell that a person most likely types commands into on
// the operating system goos (a runtime.GOOS value).
func ForOS(goos string) Shell {
	if goos == "windows" {
		return PowerShell
	}
	return POSIX
}

// Quote renders one argument in single quotes, whatever it holds. In a
// POSIX shell, each single quote in the value closes the quoted segment,
// emits an escaped quote, and opens another segment. PowerShell doubles each
// single-quote delimiter instead.
func (s Shell) Quote(value string) string {
	if s == PowerShell {
		return "'" + powerShellQuoteReplacer.Replace(value) + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// Arg renders one argument: as it is when the shell reads every character
// of it literally, and quoted otherwise. An empty argument is quoted, so it
// stays an argument.
func (s Shell) Arg(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool { return !s.literal(r) }) < 0 {
		return value
	}
	return s.Quote(value)
}

// Command renders a command line from a program and its arguments. In
// PowerShell, a quoted program needs the call operator, because a quoted
// string alone is a value and not a command.
func (s Shell) Command(program string, args ...string) string {
	words := make([]string, 0, len(args)+2)
	quoted := s.Arg(program)
	if s == PowerShell && quoted != program {
		words = append(words, "&")
	}
	words = append(words, quoted)
	for _, arg := range args {
		words = append(words, s.Arg(arg))
	}
	return strings.Join(words, " ")
}

// literal reports whether the shell reads r as itself in an unquoted word.
// Only ASCII qualifies; every other rune is quoted.
func (s Shell) literal(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return true
	case r == '_', r == '.', r == '/', r == ':', r == '-':
		return true
	case s == POSIX:
		// A backslash escapes in a POSIX word, so it is not literal there.
		return r == ',' || r == '+' || r == '=' || r == '@' || r == '%'
	default:
		// PowerShell reads a comma as an array separator, @ as splatting,
		// and = and + as operators in places, but a backslash literally.
		return r == '\\'
	}
}

// powerShellQuoteReplacer doubles every single-quote delimiter. PowerShell
// accepts the typographic single quotes as delimiters too.
var powerShellQuoteReplacer = strings.NewReplacer(
	"'", "''",
	"‘", "‘‘",
	"’", "’’",
	"‚", "‚‚",
	"‛", "‛‛",
)
