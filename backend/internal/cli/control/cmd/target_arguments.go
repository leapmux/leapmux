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

var scopeTargetFlags = []string{"workspace", "worker", "tile"}

func configureTargetArguments(root *cobra.Command) {
	help := map[string]string{"text": `The text, or "-" to read it from standard input.`}
	for name, kind := range targetArguments {
		help[name] = kind.Help()
	}
	cli.DescribeArgs(root, help)
	configureTargetValidation(root)
}

func configureTargetValidation(cmd *cobra.Command) {
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
		configureTargetValidation(child)
	}
}

// An omitted selector can have a contextual default. A supplied blank value
// must fail before any request, even if another selector comes first.
func rejectBlankTargets(cmd *cobra.Command, specs []cli.ArgSpec, args []string) error {
	for i, value := range args {
		// The argument count was validated. Extra values belong to the last
		// argument, which is variadic in that case.
		spec := specs[min(i, len(specs)-1)]
		if _, isTarget := targetArguments[spec.Name]; isTarget && strings.TrimSpace(value) == "" {
			return cli.Usagef("the %s is empty", spec.Name)
		}
	}
	for _, name := range scopeTargetFlags {
		if flag := cmd.Flag(name); flag != nil && flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
			return cli.Usagef("--%s is empty", name)
		}
	}
	return nil
}
