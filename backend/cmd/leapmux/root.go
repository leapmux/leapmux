package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	cmdcontrol "github.com/leapmux/leapmux/internal/cli/control/cmd"
)

// newRootCommand builds the whole `leapmux` command tree. Every command is
// declared once, in this tree, so the help, the abbreviation, the
// completion and `leapmux commands` all read the same declarations.
func newRootCommand(runners cliRunners) *cobra.Command {
	root := cli.Group("leapmux", "LeapMux: run coding agents and terminals in your browser")
	root.Version = runners.version()
	root.Long = `LeapMux runs coding agents and terminals on your machines and shows them in
your browser.

Start with "leapmux solo start" on one machine. "leapmux control" drives a
running LeapMux from a script or from an agent.`
	for _, mode := range []string{"solo", "hub", "worker", "dev"} {
		root.AddCommand(newModeCommand(mode, runners.modes))
	}
	root.AddCommand(
		newRecoverCommand(),
		cmdcontrol.NewCommand(),
		newVersionCommand(runners),
		newCommandsCommand(root),
		newCompletionCommand(root),
	)
	return root
}

func newVersionCommand(runners cliRunners) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "version",
		Short: "Print the version of this binary",
		Examples: []cli.Example{
			{Command: "leapmux version"},
		},
		Run: func(inv *cli.Invocation) (any, error) {
			_, err := fmt.Fprintln(inv.Printer.Out, runners.version())
			return nil, err
		},
	})
}

// newCommandsCommand builds `leapmux commands`, which prints the whole
// command tree: in text, one summary per command; with --json, every
// command with its arguments, its flags and their types, its enum values
// and its examples.
func newCommandsCommand(root *cobra.Command) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "commands",
		Short: "List every command; with --json, with its arguments, flags and examples",
		Examples: []cli.Example{
			{Command: "leapmux commands"},
			{Comment: "Read the arguments of every control command.", Command: `leapmux commands --json | jq '.data.commands[] | select(.path | startswith("control "))'`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("json", false, "print the tree as one JSON envelope")
		},
		Run: func(*cli.Invocation) (any, error) {
			return cli.Describe(root), nil
		},
	})
}

// shells lists the shells that `leapmux completion` writes a script for.
var shells = []string{"bash", "zsh", "fish", "powershell", "elvish", "nushell"}

//go:embed completion_elvish.elv
var elvishCompletion string

//go:embed completion_nushell.nu
var nushellCompletion string

// newCompletionCommand builds `leapmux completion <shell>`. The script asks
// the binary for each completion, so it completes the live ids of tabs,
// agents, workspaces and workers as well as the command names.
func newCompletionCommand(root *cobra.Command) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "completion <shell>",
		Short: "Print the shell completion script",
		Long: "Print the completion script for Bash, zsh, fish, PowerShell, Elvish,\n" +
			"or Nushell.\n" +
			"It completes commands, flags, and the IDs of your tabs, agents, workspaces, and Workers.",
		Examples: []cli.Example{
			{Comment: "Load completion into the current zsh.", Command: "source <(leapmux completion zsh)"},
			{Comment: "Install it for every new bash.", Command: "leapmux completion bash > ~/.local/share/bash-completion/completions/leapmux"},
			{Comment: "Load completion into the current Elvish shell.", Command: "eval (leapmux completion elvish | slurp)"},
			{Comment: "Save the Nushell module, then add use leapmux-completion.nu * to config.nu.", Command: "leapmux completion nushell | save ($nu.default-config-dir | path join leapmux-completion.nu)"},
		},
		Complete: func(_ *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
			var out []string
			for _, s := range shells {
				if strings.HasPrefix(s, toComplete) {
					out = append(out, s)
				}
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(inv *cli.Invocation) (any, error) {
			return nil, writeCompletion(root, inv.Arg("shell"), inv.Printer.Out)
		},
	})
}

func writeCompletion(root *cobra.Command, shell string, w io.Writer) error {
	switch shell {
	case "bash":
		return root.GenBashCompletionV2(w, true)
	case "zsh":
		return root.GenZshCompletion(w)
	case "fish":
		return root.GenFishCompletion(w, true)
	case "powershell":
		return root.GenPowerShellCompletionWithDesc(w)
	case "elvish":
		_, err := io.WriteString(w, elvishCompletion)
		return err
	case "nushell":
		_, err := io.WriteString(w, nushellCompletion)
		return err
	}
	return cli.Usagef("unknown shell %q; want one of %s", shell, strings.Join(shells, ", "))
}

// runCLI runs the command line args and returns the exit status.
func runCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, runners cliRunners) int {
	return cli.Execute(ctx, newRootCommand(runners), args, stdin, stdout, stderr)
}
