package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// completionTimeout limits the time a shell waits on Hub or pin-store reads.
const completionTimeout = 2 * time.Second

func registerScopeCompletions(cmd *cobra.Command) {
	for _, name := range []string{"workspace", "worker", "tile", "tab"} {
		if cmd.Flags().Lookup(name) == nil {
			continue
		}
		kind := targetArguments[name]
		err := cmd.RegisterFlagCompletionFunc(name, func(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeTargetsOfKind(cmd, kind, word, true)
		})
		if err != nil {
			panic(fmt.Sprintf("complete scope flag %q of %q: %v", name, cmd.Use, err))
		}
	}
}

// completeTargets completes a positional target with "self" and the ids of
// the live objects of its kind. Each candidate carries its title as the
// description that zsh and fish show.
func completeTargets(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	kind, ok := targetArguments[spec.Name]
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeTargetsOfKind(cmd, kind, toComplete, true)
}

func completeOtherAgents(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeTargetsOfKind(cmd, resolve.KindAgent, toComplete, false)
}

func completeTargetsOfKind(cmd *cobra.Command, kind resolve.Kind, toComplete string, allowSelf bool) ([]string, cobra.ShellCompDirective) {
	// Cobra completes flags and arguments without running Args validation.
	if blankTargetFlag(cmd, "hub") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	self := completionSelfID(kind)
	var candidates []string
	if allowSelf && self != "" && strings.HasPrefix(resolve.SelfTarget, toComplete) {
		candidates = append(candidates, resolve.SelfTarget)
	}
	c, err := control.NewClientFromEnv(explicitHub(cmd))
	if err != nil {
		return candidates, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	listing := targetListing(c, resolveDeps(c))
	add := func(id, title string) {
		if !strings.HasPrefix(id, toComplete) || (!allowSelf && id == self) {
			return
		}
		if title != "" {
			id += "\t" + strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return ' '
				}
				return r
			}, title)
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
	slices.Sort(candidates)
	return candidates, cobra.ShellCompDirectiveNoFileComp
}

func completionSelfID(kind resolve.Kind) string {
	if kind == resolve.KindWorker {
		return strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	}
	return (resolve.Listing{}).SelfTabID(kind)
}

func completePinnedWorkers(cmd *cobra.Command, _ cli.ArgSpec, word string) ([]string, cobra.ShellCompDirective) {
	if blankTargetFlag(cmd, "hub") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	pins, err := openPinStore(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	entries, err := pins.List(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var ids []string
	for _, pin := range entries {
		if strings.HasPrefix(pin.WorkerID, word) {
			ids = append(ids, pin.WorkerID)
		}
	}
	return ids, cobra.ShellCompDirectiveNoFileComp
}
