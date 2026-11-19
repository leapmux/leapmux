package launch

import (
	"strings"

	"github.com/leapmux/leapmux/internal/util/shellquote"
)

func fishClearEnv(keys []string) string {
	var body strings.Builder
	for _, key := range keys {
		// An unexported global shadows a universal without changing its saved value.
		body.WriteString("set --global --unexport " + key + "; or exit $status; ")
	}
	return body.String()
}

func fishSetEnv(pins []environmentPin) string {
	var body strings.Builder
	for _, pin := range pins {
		body.WriteString("set --global --export " + pin.target + " \"$" + pin.source + "\"; or exit $status; ")
		body.WriteString("set --global --unexport " + pin.source + "; or exit $status; ")
	}
	return body.String()
}

func buildFishCommand(spec shellLaunchSpec, delimiter, metaPrefix string) string {
	quote := shellquote.POSIX.Quote
	arguments := make([]string, 0, len(spec.BaseArgs))
	for _, argument := range spec.BaseArgs {
		arguments = append(arguments, quote(argument))
	}
	command := "exec " + quote(spec.Launch.Program) + " " + strings.Join(arguments, " ")
	prefix := fishClearEnv(spec.StripEnvKeys) + fishSetEnv(spec.pins)
	ready := "echo " + quote(delimiter) + "; "
	if spec.EnvGated == nil {
		return prefix + ready + command
	}
	gate := spec.EnvGated
	conditions := make([]string, 0, len(gate.EnvVars))
	for _, variable := range gate.EnvVars {
		conditions = append(conditions, "test -n \"$"+variable+"\"")
	}
	extra := make([]string, 0, len(gate.Args))
	for _, argument := range gate.Args {
		extra = append(extra, quote(argument))
	}
	return prefix + "if " + strings.Join(conditions, "; or ") + "; " +
		"echo " + quote(metaPrefix+gate.MetaKey+"=false") + "; " + ready + command + "; else; " +
		"echo " + quote(metaPrefix+gate.MetaKey+"=true") + "; " + ready + command + " " + strings.Join(extra, " ") + "; end"
}
