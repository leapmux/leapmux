package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// The argument names in each leaf's Use line identify the target kinds for
// help, validation, and completion.
var targetArguments = map[string]resolve.Kind{
	"tab":       resolve.KindTab,
	"agent":     resolve.KindAgent,
	"terminal":  resolve.KindTerminal,
	"tile":      resolve.KindTile,
	"workspace": resolve.KindWorkspace,
	"worker":    resolve.KindWorker,
}

// extraTargetArguments lists positional target arguments that do not share a
// canonical resolve.Kind name in targetArguments but must still reject blank
// values locally before dialing.
var extraTargetArguments = map[string]struct{}{
	"input":   {},
	"tile-a":  {},
	"tile-b":  {},
	"window":  {},
	"section": {},
}

func isTargetArgument(name string) bool {
	if _, ok := targetArguments[name]; ok {
		return true
	}
	_, ok := extraTargetArguments[name]
	return ok
}

type scopeTargetFlag struct {
	name    string
	kind    resolve.Kind
	hasKind bool
}

// scopeTargetFlags lists every flag that selects a target object.
// rejectBlankTargets rejects an explicit blank value for every entry, and
// registerScopeCompletions registers target completion for each entry that has
// a resolve.Kind.
var scopeTargetFlags = []scopeTargetFlag{
	{name: "hub"},
	{name: "workspace", kind: resolve.KindWorkspace, hasKind: true},
	{name: "worker", kind: resolve.KindWorker, hasKind: true},
	{name: "tile", kind: resolve.KindTile, hasKind: true},
	{name: "tab", kind: resolve.KindTab, hasKind: true},
	{name: "parent", kind: resolve.KindAgent, hasKind: true},
	{name: "section"},
	{name: "before"},
	{name: "after"},
}

func configureTargetArguments(root *cobra.Command) {
	help := map[string]string{"text": `The text, or "-" to read it from standard input.`}
	for name, kind := range targetArguments {
		help[name] = kind.Help()
	}
	cli.DescribeArgs(root, help)
	configureControlTree(root)
}

func configureControlTree(cmd *cobra.Command) {
	if cmd.HasSubCommands() {
		cli.SetFooter(cmd, guideFooter)
	}
	registerScopeCompletions(cmd)
	if validate := cmd.Args; validate != nil {
		specs := cli.ParseArgSpecs(cmd.Use)
		cmd.Args = func(cmd *cobra.Command, args []string) error {
			if err := validate(cmd, args); err != nil {
				return err
			}
			return rejectBlankTargets(cmd, specs, args)
		}
	}
	for _, child := range cmd.Commands() {
		configureControlTree(child)
	}
}

// An omitted selector can have a contextual default. A supplied blank value
// must fail before any request, even if another selector comes first.
func rejectBlankTargets(cmd *cobra.Command, specs []cli.ArgSpec, args []string) error {
	for i, value := range args {
		// The argument count was validated. Extra values belong to the last
		// argument, which is variadic in that case.
		spec := specs[min(i, len(specs)-1)]
		if isTargetArgument(spec.Name) && strings.TrimSpace(value) == "" {
			return cli.Usagef("the %s is empty", spec.Name)
		}
	}
	for _, entry := range scopeTargetFlags {
		if blankTargetFlag(cmd, entry.name) {
			return cli.Usagef("--%s is empty", entry.name)
		}
	}
	return nil
}

func blankTargetFlag(cmd *cobra.Command, name string) bool {
	flag := cmd.Flag(name)
	return flag != nil && flag.Changed && strings.TrimSpace(flag.Value.String()) == ""
}
