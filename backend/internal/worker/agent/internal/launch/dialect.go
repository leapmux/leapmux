package launch

import (
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// shellDialect encapsulates syntax, probe rendering, invocation packaging, and
// environment-pin allocation for one shell family.
type shellDialect interface {
	buildCommand(spec shellLaunchSpec, prepared *PreparedLaunch) (string, error)
	renderProbe(nonce string, names []string, nativeOnly, loginShell bool) string
	protocolLine(line string, first bool) string
	prepareProbeScript(script string) (wrapped string, cleanup func(), err error)
	pinSource(base string, used map[string]bool) string
}

// baseDialect supplies the default probe-script pass-through and collision
// suffixing shared by every dialect other than C shell.
type baseDialect struct{}

func (baseDialect) prepareProbeScript(script string) (string, func(), error) {
	return script, nil, nil
}

func (baseDialect) pinSource(base string, used map[string]bool) string {
	alias := base
	for used[strings.ToUpper(alias)] {
		alias += "_"
	}
	return alias
}

type posixDialect struct {
	baseDialect
	shell string
}

type pwshDialect struct{ baseDialect }
type nuDialect struct{ baseDialect }
type fishDialect struct{ baseDialect }
type elvishDialect struct{ baseDialect }
type xonshDialect struct{ baseDialect }
type cshDialect struct{}

var (
	_ shellDialect = posixDialect{}
	_ shellDialect = pwshDialect{}
	_ shellDialect = nuDialect{}
	_ shellDialect = fishDialect{}
	_ shellDialect = elvishDialect{}
	_ shellDialect = xonshDialect{}
	_ shellDialect = cshDialect{}
)

// resolveDialect returns the dialect for a canonical shell basename returned
// by terminal.ShellBaseName.
func resolveDialect(shell string) shellDialect {
	switch {
	case terminal.IsPwsh(shell):
		return pwshDialect{}
	case shell == "nu":
		return nuDialect{}
	case shell == "fish":
		return fishDialect{}
	case shell == "elvish":
		return elvishDialect{}
	case shell == "xonsh":
		return xonshDialect{}
	case shell == "csh" || shell == "tcsh":
		return cshDialect{}
	default:
		return posixDialect{shell: shell}
	}
}

// renderCommandProbe runs each lookup in the same profile and working directory.
// Metadata is line-framed separately from the profile's output.
func renderCommandProbe(shell, nonce string, names []string, nativeOnly, loginShell bool) string {
	return resolveDialect(shell).renderProbe(nonce, names, nativeOnly, loginShell)
}

// renderStandardProbe emits setup, start record, per-command results, and
// completion record for dialects that emit one record per command.
func renderStandardProbe(setup string, d shellDialect, probeResult func(name, present, absent string, nativeOnly bool) string, nonce string, names []string, nativeOnly bool) string {
	var script strings.Builder
	script.WriteString(setup)
	script.WriteString(d.protocolLine(nonce+":start", true))
	for index, name := range names {
		prefix := nonce + ":" + strconv.Itoa(index) + ":"
		script.WriteString(probeResult(name, prefix+"yes", prefix+"no", nativeOnly))
	}
	script.WriteString(d.protocolLine(nonce+":complete", false))
	return script.String()
}
