package cmd

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// completionTimeout caps the hub calls that one completion makes. A shell
// waits for the completion before it shows anything, so a slow hub must not
// freeze the prompt.
const completionTimeout = 2 * time.Second

// kindOfArg maps the name of a positional argument to the kind of object it
// takes. The Use line gives each argument its name, so the name is the one
// declaration that completion reads.
var kindOfArg = map[string]resolve.Kind{
	"tab":       resolve.KindTab,
	"agent":     resolve.KindAgent,
	"terminal":  resolve.KindTerminal,
	"tile":      resolve.KindTile,
	"workspace": resolve.KindWorkspace,
	"worker":    resolve.KindWorker,
}

// completeTargets completes a positional target with "self" and the ids of
// the live objects of its kind. Each candidate carries its title as the
// description that zsh and fish show.
func completeTargets(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	kind, ok := kindOfArg[spec.Name]
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	hub := ""
	if f := cmd.Flag("hub"); f != nil && f.Changed {
		hub = f.Value.String()
	}
	c, err := control.NewClientFromEnv(hub)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	listing := targetListing(c, resolveDeps(c))
	candidates := []string{resolve.SelfTarget}
	add := func(id, title string) {
		if !strings.HasPrefix(id, toComplete) {
			return
		}
		if title != "" {
			id += "\t" + title
		}
		candidates = append(candidates, id)
	}
	switch {
	case kind.IsTab():
		tabs, err := listing.Tabs(ctx)
		if err != nil {
			break
		}
		want := kind.TabType()
		var matching []*leapmuxv1.WorkspaceTab
		for _, t := range tabs {
			if want == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED || t.GetTab().GetTabType() == want {
				matching = append(matching, t)
			}
		}
		titles := listing.TabTitles(ctx, matching)
		for _, t := range matching {
			add(t.GetTab().GetTabId(), titles[t.GetTab().GetTabId()])
		}
	case kind == resolve.KindWorkspace:
		workspaces, err := listing.Workspaces(ctx)
		if err != nil {
			break
		}
		for _, w := range workspaces {
			add(w.GetId(), w.GetTitle())
		}
	case kind == resolve.KindTile:
		tiles, err := listing.Tiles(ctx)
		if err != nil {
			break
		}
		for id, nodeKind := range tiles {
			add(id, nodeKind)
		}
	case kind == resolve.KindWorker:
		workers, err := listing.Workers(ctx)
		if err != nil {
			break
		}
		names := listing.WorkerNames(ctx, workers)
		for _, w := range workers {
			add(w.GetId(), names[w.GetId()])
		}
	}
	if os.Getenv(controlenv.TabIDVariable) == "" && kind != resolve.KindWorker {
		// "self" works only inside a LeapMux tab.
		candidates = candidates[1:]
	}
	return candidates, cobra.ShellCompDirectiveNoFileComp
}
