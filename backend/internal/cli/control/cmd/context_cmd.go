package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
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
		Short:            "Save persistent default hub, worker, workspace, or provider",
		Mutates:          true,
		QuietUnsupported: true,
		Long: `Save persistent default hub, worker, workspace, or provider in context.json.

Inside a LeapMux tab, hub, worker, and workspace come from the tab environment
($LEAPMUX_CONTROL_SOCK, $LEAPMUX_CONTROL_WORKER_ID, and
$LEAPMUX_CONTROL_WORKSPACE_ID), which supersede saved context.json defaults.
The saved provider still serves as a fallback in terminal tabs where
$LEAPMUX_CONTROL_AGENT_PROVIDER is unset.`,
		Examples: []cli.Example{
			{Command: `leapmux context use --worker build-box --workspace "Release 1.4"`},
			{Command: "leapmux context use --hub https://hub.example.com --provider claude"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("worker", "", "default worker id or name")
			fs.String("workspace", "", "default workspace id or title")
			fs.String("provider", "", "default agent provider")
		},
		// --hub inherits completion from root; --worker and --workspace receive
		// scope completions automatically via registerScopeCompletions.
		FlagComplete: map[string]cobra.CompletionFunc{
			"provider": completeProviderFlag,
		},
		Run: runContextUse,
	})

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
	Hub             string `json:"hub,omitempty"`
	Worker          string `json:"worker,omitempty"`
	Workspace       string `json:"workspace,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Path            string `json:"path,omitempty"`
	ActiveTabSocket bool   `json:"active_tab_socket,omitempty"`
	EnvHub          string `json:"env_hub,omitempty"`
}

func (r contextShowResult) RenderText(w io.Writer) error {
	if r.Hub == "" && r.Worker == "" && r.Workspace == "" && r.Provider == "" {
		if _, err := fmt.Fprintln(w, "no saved context defaults"); err != nil {
			return err
		}
	} else {
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
	}
	if r.ActiveTabSocket {
		if _, err := fmt.Fprintln(w, "note: inside a LeapMux tab ($LEAPMUX_CONTROL_SOCK is set); tab environment supersedes saved hub, worker, and workspace defaults"); err != nil {
			return err
		}
	}
	if r.EnvHub != "" {
		if r.ActiveTabSocket {
			if _, err := fmt.Fprintf(w, "note: $LEAPMUX_HUB (%s) is set, but $LEAPMUX_CONTROL_SOCK takes precedence inside a tab\n", cli.SanitizeSingleLine(r.EnvHub)); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(w, "note: $LEAPMUX_HUB (%s) supersedes the saved hub default\n", cli.SanitizeSingleLine(r.EnvHub)); err != nil {
				return err
			}
		}
	}
	return nil
}

func contextShowResultOf(cfg control.ContextConfig) contextShowResult {
	path, _ := control.ContextPath()
	return contextShowResult{
		Hub:       cfg.Hub,
		Worker:    cfg.Worker,
		Workspace: cfg.Workspace,
		Provider:  cfg.Provider,
		Path:      path,
	}
}

func runContextShow(_ *cli.Invocation) (any, error) {
	cfg, err := control.LoadContext()
	if err != nil {
		return nil, err
	}
	res := contextShowResultOf(cfg)
	res.ActiveTabSocket = strings.TrimSpace(os.Getenv(controlenv.SocketVariable)) != ""
	res.EnvHub = strings.TrimSpace(os.Getenv("LEAPMUX_HUB"))
	return res, nil
}

func runContextUse(inv *cli.Invocation) (any, error) {
	hubFlag := inv.Command.Flags().Lookup("hub")
	hubChanged := hubFlag != nil && hubFlag.Changed
	if !hubChanged && !inv.Changed("worker") && !inv.Changed("workspace") && !inv.Changed("provider") {
		return nil, cli.Usagef("pass at least one of --hub, --worker, --workspace, or --provider")
	}
	cfg, err := control.LoadContext()
	if err != nil {
		return nil, err
	}
	if hubChanged {
		rawHub := explicitHub(inv.Command)
		if rawHub == "" {
			return nil, cli.Usagef("--hub is empty")
		}
		canonicalHub, err := validateDefaultHub(rawHub)
		if err != nil {
			return nil, err
		}
		cfg.Hub = canonicalHub
	}
	if inv.Changed("worker") {
		v := strings.TrimSpace(stringFlag(inv, "worker"))
		if v == "" {
			return nil, cli.Usagef("--worker is empty")
		}
		cfg.Worker = v
	}
	if inv.Changed("workspace") {
		v := strings.TrimSpace(stringFlag(inv, "workspace"))
		if v == "" {
			return nil, cli.Usagef("--workspace is empty")
		}
		cfg.Workspace = v
	}
	if inv.Changed("provider") {
		v := strings.TrimSpace(stringFlag(inv, "provider"))
		if v == "" {
			return nil, cli.Usagef("--provider is empty")
		}
		cfg.Provider = v
	}
	if err := control.SaveContext(cfg); err != nil {
		return nil, err
	}
	return contextShowResultOf(cfg), nil
}

func runContextClear(inv *cli.Invocation) (any, error) {
	fields := inv.StringArray("field")
	if len(fields) == 0 {
		if err := control.ClearContext(); err != nil {
			return nil, err
		}
		return contextShowResultOf(control.ContextConfig{}), nil
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
	return contextShowResultOf(cfg), nil
}
