package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

func newContextCommand() *cobra.Command {
	showCmd := cli.NewLeaf(cli.Leaf{
		Use:              "show",
		Short:            "Show the saved CLI defaults",
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux context show"},
		},
		Run: runContextShow,
	})
	cli.DisallowInheritedFlags(showCmd, "hub")

	useCmd := cli.NewLeaf(cli.Leaf{
		Use:              "use",
		Short:            "Save default hub, worker, workspace, or provider for commands outside a tab",
		Mutates:          true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux context use --worker build-box --workspace \"Release 1.4\""},
			{Comment: "Clear a single saved default by passing an empty string.", Command: `leapmux context use --provider ""`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("hub", "", "default hub URL (pass \"\" to clear)")
			fs.String("worker", "", "default worker id or name (pass \"\" to clear)")
			fs.String("workspace", "", "default workspace id or title (pass \"\" to clear)")
			fs.String("provider", "", "default agent provider (pass \"\" to clear)")
		},
		Run: runContextUse,
	})
	if useCmd.Annotations == nil {
		useCmd.Annotations = map[string]string{}
	}
	useCmd.Annotations[allowBlankScopeFlagsAnnotation] = "true"

	clearCmd := cli.NewLeaf(cli.Leaf{
		Use:              "clear",
		Short:            "Remove saved CLI defaults (all by default, or specific fields with --field)",
		Mutates:          true,
		Destructive:      true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux context clear"},
			{Comment: "Clear only the saved default worker.", Command: "leapmux context clear --field worker"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringArray("field", nil, "clear only this saved default; repeat for more than one")
			cli.SetEnum(fs, "field", "hub", "worker", "workspace", "provider")
		},
		Run: runContextClear,
	})
	cli.DisallowInheritedFlags(clearCmd, "hub")

	return cli.SetAliases(cli.Group("context", "Show, set, or clear persistent default hub, worker, workspace, and provider outside a tab",
		showCmd,
		useCmd,
		clearCmd,
	), "ctx")
}

type contextShowResult struct {
	Hub       string `json:"hub,omitempty"`
	Worker    string `json:"worker,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Path      string `json:"path,omitempty"`
}

func (r contextShowResult) RenderText(w io.Writer) error {
	if r.Hub == "" && r.Worker == "" && r.Workspace == "" && r.Provider == "" {
		_, err := fmt.Fprintln(w, "no saved context defaults")
		return err
	}
	write := func(k, v string) error {
		if v == "" {
			return nil
		}
		_, err := fmt.Fprintf(w, "%-10s %s\n", k+":", cli.SanitizeSingleLine(v))
		return err
	}
	for _, kv := range [][2]string{
		{"hub", r.Hub},
		{"worker", r.Worker},
		{"workspace", r.Workspace},
		{"provider", r.Provider},
	} {
		if err := write(kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

func runContextShow(_ *cli.Invocation) (any, error) {
	cfg, err := control.LoadContext()
	if err != nil {
		return nil, err
	}
	path, _ := control.ContextPath()
	return contextShowResult{
		Hub:       cfg.Hub,
		Worker:    cfg.Worker,
		Workspace: cfg.Workspace,
		Provider:  cfg.Provider,
		Path:      path,
	}, nil
}

func runContextUse(inv *cli.Invocation) (any, error) {
	if !anyLocalFlagChanged(inv) {
		return nil, cli.Usagef("pass at least one of --hub, --worker, --workspace, or --provider")
	}
	cfg, err := control.LoadContext()
	if err != nil {
		return nil, err
	}
	if f := inv.Command.LocalNonPersistentFlags().Lookup("hub"); f != nil && f.Changed {
		cfg.Hub = strings.TrimSpace(f.Value.String())
	}
	if inv.Changed("worker") {
		cfg.Worker = strings.TrimSpace(stringFlag(inv, "worker"))
	}
	if inv.Changed("workspace") {
		cfg.Workspace = strings.TrimSpace(stringFlag(inv, "workspace"))
	}
	if inv.Changed("provider") {
		cfg.Provider = strings.TrimSpace(stringFlag(inv, "provider"))
	}
	if err := control.SaveContext(cfg); err != nil {
		return nil, err
	}
	path, _ := control.ContextPath()
	return contextShowResult{
		Hub:       cfg.Hub,
		Worker:    cfg.Worker,
		Workspace: cfg.Workspace,
		Provider:  cfg.Provider,
		Path:      path,
	}, nil
}

func runContextClear(inv *cli.Invocation) (any, error) {
	fields := inv.StringArray("field")
	if len(fields) == 0 {
		if err := control.ClearContext(); err != nil {
			return nil, err
		}
		path, _ := control.ContextPath()
		return contextShowResult{Path: path}, nil
	}
	cfg, err := control.LoadContext()
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		switch strings.TrimSpace(f) {
		case "hub":
			cfg.Hub = ""
		case "worker":
			cfg.Worker = ""
		case "workspace":
			cfg.Workspace = ""
		case "provider":
			cfg.Provider = ""
		default:
			return nil, cli.Usagef("unknown --field %q; expected hub, worker, workspace, or provider", f)
		}
	}
	if err := control.SaveContext(cfg); err != nil {
		return nil, err
	}
	path, _ := control.ContextPath()
	return contextShowResult{
		Hub:       cfg.Hub,
		Worker:    cfg.Worker,
		Workspace: cfg.Workspace,
		Provider:  cfg.Provider,
		Path:      path,
	}, nil
}
