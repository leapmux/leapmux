package main

import (
	"bytes"
	"context"
	_ "embed"
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

Start a local instance with "leapmux up" (or "leapmux daemon solo start").
Drive workspaces, agents, terminals, files, and layouts directly with
"leapmux <command>" (or the short "lmx" alias).`
	cli.SetAbbreviationNote(root,
		"Any command name can be shortened as far as it stays unambiguous. A later\n"+
			"release can make an ad-hoc prefix ambiguous, so scripts use full names or\n"+
			"documented aliases (such as ag, term, ws, ls, rm, mv).")
	cmdcontrol.Attach(root, runners.version)
	localCmds := []*cobra.Command{
		newDaemonCommand(runners.modes),
		newUpCommand(runners.modes),
		newDownCommand(runners.modes),
		newRecoverCommand(),
		newCommandsCommand(root),
		newCompletionCommand(root),
	}
	for _, c := range localCmds {
		cli.DisallowInheritedFlags(c, "hub")
		cli.MarkLocalOnly(c)
	}
	root.AddCommand(localCmds...)
	assignRootCommandGroups(root)
	for _, child := range root.Commands() {
		if child.Name() == "version" || child.Name() == "help" {
			cli.MarkLocalOnly(child)
		}
	}
	return root
}

// assignRootCommandGroups assigns task-grouped help sections to top-level
// commands so `leapmux --help` reads cleanly by workflow rather than one flat
// list of 28 commands.
func assignRootCommandGroups(root *cobra.Command) {
	root.InitDefaultHelpCmd()
	groups := map[string]string{
		"tree":         "Daily Work",
		"agent":        "Daily Work",
		"terminal":     "Daily Work",
		"tab":          "Daily Work",
		"workspace":    "Daily Work",
		"section":      "Daily Work",
		"events":       "Daily Work",
		"notify":       "Daily Work",
		"notification": "Daily Work",
		"file":         "Files & Git",
		"git":          "Files & Git",
		"worktree":     "Files & Git",
		"tile":         "Layout & Windows",
		"window":       "Layout & Windows",
		"layout":       "Layout & Windows",
		"worker":       "Fleet & Auth",
		"tunnel":       "Fleet & Auth",
		"whoami":       "Fleet & Auth",
		"auth":         "Fleet & Auth",
		"context":      "Fleet & Auth",
		"settings":     "Fleet & Auth",
		"doctor":       "Fleet & Auth",
		"up":           "Local Daemon",
		"down":         "Local Daemon",
		"daemon":       "Local Daemon",
		"admin":        "Admin & Recovery",
		"recover":      "Admin & Recovery",
		"rpc":          "Admin & Recovery",
		"guide":        "Meta",
		"skill":        "Meta",
		"version":      "Meta",
		"commands":     "Meta",
		"completion":   "Meta",
		"help":         "Meta",
	}
	for _, child := range root.Commands() {
		if grp, ok := groups[child.Name()]; ok {
			cli.SetCommandGroup(child, grp)
		}
	}
}

// newCommandsCommand builds `leapmux commands [prefix...]`, which prints the
// whole command tree or a filtered subtree: in text, one summary per command;
// with --json, every command with its arguments, its flags and their types,
// its enum values and its examples (or compact summaries with --compact).
func newCommandsCommand(root *cobra.Command) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "commands [prefix...]",
		Short:            "List commands; with --json, include arguments, flags and examples",
		QuietUnsupported: true,
		Args: map[string]string{
			"prefix": "Optional command path prefix to inspect (for example: agent, or agent send).",
		},
		Examples: []cli.Example{
			{Command: "leapmux commands"},
			{Comment: "Inspect only the agent command subtree.", Command: "leapmux commands agent --json"},
			{Comment: "Print compact command summaries and required arguments.", Command: "leapmux commands --compact --json"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Int("depth", 0, "limit command path depth relative to the prefix (0 prints every depth)")
			fs.Bool("compact", false, "omit descriptions, examples, and optional flags for compact agent discovery")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			depth, err := inv.Command.Flags().GetInt("depth")
			if err != nil {
				return nil, err
			}
			if depth < 0 {
				return nil, cli.Usagef("--depth must be >= 0")
			}
			compact, err := inv.Command.Flags().GetBool("compact")
			if err != nil {
				return nil, err
			}
			return cli.DescribeFiltered(root, cli.DescribeOptions{
				Prefix:  strings.Join(inv.Variadic("prefix"), " "),
				Depth:   depth,
				Compact: compact,
			}), nil
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
		Use:              "completion <shell>",
		Short:            "Print the shell completion script",
		QuietUnsupported: true,
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
			shell := inv.Arg("shell")
			var buf bytes.Buffer
			if err := writeCompletion(root, shell, &buf); err != nil {
				return nil, err
			}
			return completionScript{Shell: shell, Script: buf.String()}, nil
		},
	})
}

type completionScript struct {
	Shell  string `json:"shell"`
	Script string `json:"script"`
}

func (c completionScript) RenderText(w io.Writer) error {
	_, err := io.WriteString(w, c.Script)
	return err
}

func writeCompletion(root *cobra.Command, shell string, w io.Writer) error {
	switch shell {
	case "bash":
		if err := root.GenBashCompletionV2(w, true); err != nil {
			return err
		}
		_, err := io.WriteString(w, "\nif [[ $(type -t compopt) = \"builtin\" ]]; then\n    complete -o default -F __start_leapmux lmx\nelse\n    complete -o default -o nospace -F __start_leapmux lmx\nfi\n")
		return err
	case "zsh":
		if err := root.GenZshCompletion(w); err != nil {
			return err
		}
		_, err := io.WriteString(w, "\ncompdef _leapmux lmx\n")
		return err
	case "fish":
		if err := root.GenFishCompletion(w, true); err != nil {
			return err
		}
		_, err := io.WriteString(w, "\ncomplete -c lmx -w leapmux\n")
		return err
	case "powershell":
		if err := root.GenPowerShellCompletionWithDesc(w); err != nil {
			return err
		}
		_, err := io.WriteString(w, "\nRegister-ArgumentCompleter -CommandName 'lmx' -ScriptBlock $__leapmuxCompleterBlock\n")
		return err
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
