package resolve_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// fakeListing serves a fixed set of objects, and counts the list calls so a
// test can pin which forms cost a call.
type fakeListing struct {
	tabs        []*leapmuxv1.WorkspaceTab
	titles      map[string]string
	workspaces  []*leapmuxv1.Workspace
	tiles       map[string]string
	workers     []*leapmuxv1.Worker
	workerNames map[string]string
	located     *leapmuxv1.WorkspaceTab
	env         map[string]string
	listErr     error
	calls       int
	titleCalls  int
}

func (f *fakeListing) listing() resolve.Listing {
	return resolve.Listing{
		Tabs: func(context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
			f.calls++
			return f.tabs, f.listErr
		},
		TabTitles: func(_ context.Context, tabs []*leapmuxv1.WorkspaceTab) map[string]string {
			f.titleCalls++
			out := map[string]string{}
			for _, t := range tabs {
				out[t.GetTab().GetTabId()] = f.titles[t.GetTab().GetTabId()]
			}
			return out
		},
		Workspaces: func(context.Context) ([]*leapmuxv1.Workspace, error) {
			f.calls++
			return f.workspaces, f.listErr
		},
		Tiles: func(context.Context) (map[string]string, error) {
			f.calls++
			return f.tiles, f.listErr
		},
		Workers: func(context.Context) ([]*leapmuxv1.Worker, error) {
			f.calls++
			return f.workers, f.listErr
		},
		WorkerNames: func(context.Context, []*leapmuxv1.Worker) map[string]string {
			return f.workerNames
		},
		LocateTab: func(context.Context, string) (*leapmuxv1.WorkspaceTab, error) {
			f.calls++
			if f.located == nil {
				return nil, errors.New("no such tab")
			}
			return f.located, nil
		},
		Getenv: func(key string) string { return f.env[key] },
	}
}

func tab(id string, tt leapmuxv1.TabType) *leapmuxv1.WorkspaceTab {
	return &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabId: id, TabType: tt},
		WorkspaceId: "ws-1",
		TileId:      "tile-1",
		WorkerId:    "worker-1",
	}
}

func sampleListing() *fakeListing {
	return &fakeListing{
		tabs: []*leapmuxv1.WorkspaceTab{
			tab("87a19d5f-aaaa", leapmuxv1.TabType_TAB_TYPE_AGENT),
			tab("87a1ffff-bbbb", leapmuxv1.TabType_TAB_TYPE_TERMINAL),
			tab("4cbb3f34-cccc", leapmuxv1.TabType_TAB_TYPE_AGENT),
		},
		titles: map[string]string{
			"87a19d5f-aaaa": "CLI Design",
			"87a1ffff-bbbb": "Build",
			"4cbb3f34-cccc": "CLI Design",
		},
		workspaces: []*leapmuxv1.Workspace{
			{Id: "1aszUbNsWorkspaceOne", Title: "Release"},
			{Id: "1aszZZZZWorkspaceTwo", Title: "Scratch"},
		},
		tiles: map[string]string{"z4sx7DCAtile": "leaf", "gMCVBPMetile": "split"},
		workers: []*leapmuxv1.Worker{
			{Id: "hQrS3iDtWorkerOne", Online: true},
			{Id: "hQrSxxxxWorkerTwo", Online: true},
		},
		workerNames: map[string]string{"hQrS3iDtWorkerOne": "build-box", "hQrSxxxxWorkerTwo": "laptop"},
	}
}

func TestCanonical_AFullIDCostsNoCall(t *testing.T) {
	f := sampleListing()
	id := strings.Repeat("a", 48)
	got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTab, id)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.False(t, got.Self)
	assert.Zero(t, f.calls)
	uuid := "87a19d5f-4838-4e34-9f0f-56130f0a62b1"
	got, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, uuid)
	require.NoError(t, err)
	assert.Equal(t, uuid, got.ID)
	assert.Zero(t, f.calls)
}

func TestCanonical_ResolvesLongPrefixes(t *testing.T) {
	const generatedID = "Q0vNx01dbAo1BL3bTGB4uK6gGI3FmtgV6OlhMGB4YY6PRMhh"
	const uuid = "87a19d5f-4838-4e34-9f0f-56130f0a62b1"
	for _, tc := range []struct {
		name    string
		id      string
		lengths []int
	}{
		{name: "generated", id: generatedID, lengths: []int{31, 32, 35, 36, 47}},
		{name: "UUID", id: uuid, lengths: []int{31, 32, 35}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, length := range tc.lengths {
				f := &fakeListing{tabs: []*leapmuxv1.WorkspaceTab{tab(tc.id, leapmuxv1.TabType_TAB_TYPE_AGENT)}}
				input := tc.id[:length]
				got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindAgent, input)
				require.NoError(t, err, "%d-character prefix", length)
				assert.Equal(t, tc.id, got.ID, "%d-character prefix", length)
				assert.Equal(t, 1, f.calls, "%d-character prefix", length)
			}
		})
	}
}

func TestCanonical_ExactCustomIDWinsOverALongerMatch(t *testing.T) {
	f := &fakeListing{tabs: []*leapmuxv1.WorkspaceTab{
		tab("build-2", leapmuxv1.TabType_TAB_TYPE_AGENT),
		tab("build", leapmuxv1.TabType_TAB_TYPE_AGENT),
	}}
	target, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindAgent, "build")
	require.NoError(t, err)
	assert.Equal(t, "build", target.ID)
	assert.Equal(t, 1, f.calls)
	assert.Zero(t, f.titleCalls)
}

func TestCanonical_LiteralIDCanDisambiguateReservedWords(t *testing.T) {
	for _, literal := range []string{"a", "self"} {
		f := sampleListing()
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindAgent, "id:"+literal)
		require.NoError(t, err)
		assert.Equal(t, literal, got.ID)
		assert.False(t, got.Self)
		assert.Zero(t, f.calls)
	}
	f := sampleListing()
	_, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindAgent, "id:")
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Zero(t, f.calls)
}

func TestCanonical_AUniquePrefixResolvesWithItsRow(t *testing.T) {
	f := sampleListing()
	got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTab, "4cbb")
	require.NoError(t, err)
	assert.Equal(t, "4cbb3f34-cccc", got.ID)
	require.NotNil(t, got.Tab, "a prefix keeps the row it listed, so the command need not locate it again")
	assert.Equal(t, "ws-1", got.Tab.GetWorkspaceId())
	assert.Zero(t, f.titleCalls, "a unique prefix asks no worker for titles")
}

func TestCanonical_APrefixOnlyMatchesTheKindOfTheCommand(t *testing.T) {
	f := sampleListing()
	// "87a1" is ambiguous among all tabs, and unique among the agents.
	got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, "87a1")
	require.NoError(t, err)
	assert.Equal(t, "87a19d5f-aaaa", got.ID)
	got, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindTerminal, "87a1")
	require.NoError(t, err)
	assert.Equal(t, "87a1ffff-bbbb", got.ID)
}

func TestCanonical_AnAmbiguousPrefixListsTheCandidatesWithTheirTitles(t *testing.T) {
	f := sampleListing()
	_, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTab, "87a1")
	e := cli.FromError(err)
	require.Equal(t, cli.CodeAmbiguousTarget, e.Code)
	assert.Equal(t, cli.ExitNotFound, e.ExitCode())
	require.Len(t, e.Candidates, 2)
	assert.Equal(t, cli.Candidate{ID: "87a19d5f-aaaa", Kind: "agent", Title: "CLI Design"}, e.Candidates[0])
	assert.Equal(t, cli.Candidate{ID: "87a1ffff-bbbb", Kind: "terminal", Title: "Build"}, e.Candidates[1])
	assert.NotEmpty(t, e.Hints)
}

func TestCanonical_APrefixThatMatchesNothingIsNotFound(t *testing.T) {
	f := sampleListing()
	_, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTab, "ffff")
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
}

func TestCanonical_AShortPrefixIsAUsageError(t *testing.T) {
	f := sampleListing()
	for _, input := range []string{"8", "87a"} {
		_, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTab, input)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, input)
	}
	// A tile has no title fallback, so a short tile prefix still fails before any call.
	fTile := sampleListing()
	_, err := resolve.Canonical(context.Background(), fTile.listing(), resolve.KindTile, "87")
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Zero(t, fTile.calls, "a short tile prefix fails before any call")
}

func TestCanonical_ShortBareSlugResolvesDirectly(t *testing.T) {
	f := sampleListing()
	f.titles["87a19d5f-aaaa"] = "qa"
	got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, "qa")
	require.NoError(t, err)
	assert.Equal(t, "87a19d5f-aaaa", got.ID)
}

// The hint of a short prefix offers the named form of the kind, and only a
// form that the kind accepts: a tile has no title, and a worker has a name.
func TestCanonical_AShortPrefixHintsTheFormsOfItsKind(t *testing.T) {
	f := sampleListing()
	for kind, want := range map[resolve.Kind]string{
		resolve.KindTab:       `use "self" for your own tab, or "title:<title>"`,
		resolve.KindAgent:     `use "self" for your own agent, or "title:<title>"`,
		resolve.KindWorkspace: `use "self" for your own workspace, or "title:<title>"`,
		resolve.KindWorker:    `use "self" for your own worker, or "name:<name>"`,
		resolve.KindTile:      `use "self" for your own tile`,
	} {
		_, err := resolve.Canonical(context.Background(), f.listing(), kind, "87")
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeUsage, e.Code, "%s", kind)
		assert.Equal(t, []string{`use "id:<id>" to select a literal id`, want}, e.Hints, "%s", kind)
	}
}

// Kind.Help states exactly the forms that Canonical reads, for the help of a
// target argument.
func TestKind_HelpStatesTheFormsThatCanonicalReads(t *testing.T) {
	assert.Equal(t, `A tab id, a unique prefix of 4 or more characters of one, "id:<id>" for a literal id, "title:<title>" or a bare case-insensitive title, or "self" for the tab that runs this command.`,
		resolve.KindTab.Help())
	assert.Equal(t, `An agent id, a unique prefix of 4 or more characters of one, "id:<id>" for a literal id, "title:<title>" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command.`,
		resolve.KindAgent.Help())
	assert.Equal(t, `A terminal id, a unique prefix of 4 or more characters of one, "id:<id>" for a literal id, "title:<title>" or a bare case-insensitive title, or "self" for the terminal that runs this command.`,
		resolve.KindTerminal.Help())
	assert.Equal(t, `A workspace id, a unique prefix of 4 or more characters of one, "id:<id>" for a literal id, "title:<title>" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command.`,
		resolve.KindWorkspace.Help())
	assert.Equal(t, `A tile id, a unique prefix of 4 or more characters of one, "id:<id>" for a literal id, or "self" for the tile of the tab that runs this command.`,
		resolve.KindTile.Help(), "a tile has no title, so its help offers none")
	assert.Equal(t, `A worker id, a unique prefix of 4 or more characters of one, "id:<id>" for a literal id, "name:<name>" or a bare case-insensitive worker name, or "self" for the worker of the tab that runs this command.`,
		resolve.KindWorker.Help())
}

// Kind.HelpWithoutSelf states the same forms as Help except "self", for a
// command that refuses the caller's own object.
func TestKind_HelpWithoutSelfOffersNoSelf(t *testing.T) {
	assert.Equal(t, `An agent id, a unique prefix of 4 or more characters of one, "id:<id>" for a literal id, "title:<title>" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent.`,
		resolve.KindAgent.HelpWithoutSelf())
	assert.Equal(t, `A tile id, a unique prefix of 4 or more characters of one, or "id:<id>" for a literal id.`,
		resolve.KindTile.HelpWithoutSelf())
}

func TestCanonical_AnEmptyTargetIsAUsageError(t *testing.T) {
	_, err := resolve.Canonical(context.Background(), sampleListing().listing(), resolve.KindTab, "  ")
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestCanonical_ATitleSelectsATab(t *testing.T) {
	f := sampleListing()
	got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTerminal, "title:Build")
	require.NoError(t, err)
	assert.Equal(t, "87a1ffff-bbbb", got.ID)

	_, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, "title:CLI Design")
	e := cli.FromError(err)
	require.Equal(t, cli.CodeAmbiguousTarget, e.Code, "two agents share the title")
	assert.Len(t, e.Candidates, 2)

	_, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindTab, "title:Nope")
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	_, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindTab, "title:")
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestCanonical_ATitleSelectsAWorkspace(t *testing.T) {
	got, err := resolve.Canonical(context.Background(), sampleListing().listing(), resolve.KindWorkspace, "title:Scratch")
	require.NoError(t, err)
	assert.Equal(t, "1aszZZZZWorkspaceTwo", got.ID)
}

func TestCanonical_RequiresCompleteMetadataBeforeSelectingByTitleOrName(t *testing.T) {
	t.Run("tab title", func(t *testing.T) {
		f := sampleListing()
		listing := f.listing()
		listing.TabTitles = func(context.Context, []*leapmuxv1.WorkspaceTab) map[string]string {
			return map[string]string{"87a19d5f-aaaa": "CLI Design"}
		}
		got, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "title:CLI Design")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
		assert.Empty(t, got.ID, "another visible agent could have the same title")
	})
	t.Run("worker name", func(t *testing.T) {
		f := sampleListing()
		delete(f.workerNames, "hQrSxxxxWorkerTwo")
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "name:build-box")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
		assert.Empty(t, got.ID, "another visible Worker could have the same name")
	})
	t.Run("two known titles prove ambiguity despite missing metadata", func(t *testing.T) {
		f := sampleListing()
		f.tabs = append(f.tabs, tab("unread-agent", leapmuxv1.TabType_TAB_TYPE_AGENT))
		listing := f.listing()
		listing.TabTitles = func(context.Context, []*leapmuxv1.WorkspaceTab) map[string]string {
			return f.titles
		}
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "title:CLI Design")
		require.Error(t, err)
		failure := cli.FromError(err)
		assert.Equal(t, cli.CodeAmbiguousTarget, failure.Code)
		assert.Len(t, failure.Candidates, 2)
	})
}

func TestCanonical_PreservesExactTitleWhitespace(t *testing.T) {
	f := sampleListing()
	f.titles["87a19d5f-aaaa"] = "CLI Design "
	got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindAgent, "title:CLI Design ")
	require.NoError(t, err)
	assert.Equal(t, "87a19d5f-aaaa", got.ID)
}

func TestCanonical_ATitleDoesNotSelectATileOrAWorker(t *testing.T) {
	for _, kind := range []resolve.Kind{resolve.KindTile, resolve.KindWorker} {
		_, err := resolve.Canonical(context.Background(), sampleListing().listing(), kind, "title:x")
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, kind.String())
	}
}

func TestCanonical_ANameSelectsAWorkerOnly(t *testing.T) {
	got, err := resolve.Canonical(context.Background(), sampleListing().listing(), resolve.KindWorker, "name:laptop")
	require.NoError(t, err)
	assert.Equal(t, "hQrSxxxxWorkerTwo", got.ID)

	_, err = resolve.Canonical(context.Background(), sampleListing().listing(), resolve.KindWorker, "name:nope")
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	_, err = resolve.Canonical(context.Background(), sampleListing().listing(), resolve.KindTab, "name:build-box")
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestCanonical_AWorkerPrefixListsItsNamesWhenAmbiguous(t *testing.T) {
	_, err := resolve.Canonical(context.Background(), sampleListing().listing(), resolve.KindWorker, "hQrS")
	e := cli.FromError(err)
	require.Equal(t, cli.CodeAmbiguousTarget, e.Code)
	require.Len(t, e.Candidates, 2)
	assert.Equal(t, "build-box", e.Candidates[0].Title)
}

func TestCanonical_ATilePrefixResolves(t *testing.T) {
	got, err := resolve.Canonical(context.Background(), sampleListing().listing(), resolve.KindTile, "z4sx")
	require.NoError(t, err)
	assert.Equal(t, "z4sx7DCAtile", got.ID)
}

func TestCanonical_AListFailureIsReturned(t *testing.T) {
	f := sampleListing()
	f.listErr = errors.New("hub down")
	_, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindWorkspace, "1asz")
	require.ErrorContains(t, err, "hub down")
}

func TestCanonical_Self(t *testing.T) {
	t.Run("selects the calling tab", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{"LEAPMUX_CONTROL_TAB_ID": "87a19d5f-aaaa", "LEAPMUX_CONTROL_TAB_TYPE": "agent"}
		got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, "self")
		require.NoError(t, err)
		assert.Equal(t, "87a19d5f-aaaa", got.ID)
		assert.True(t, got.Self)
		assert.Zero(t, f.calls, `"self" of a tab costs no call`)
	})
	t.Run("selects the calling worker", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{"LEAPMUX_CONTROL_WORKER_ID": "hQrS3iDtWorkerOne"}
		got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindWorker, "self")
		require.NoError(t, err)
		assert.Equal(t, "hQrS3iDtWorkerOne", got.ID)
	})
	t.Run("locates the calling tab for its tile and workspace", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{"LEAPMUX_CONTROL_TAB_ID": "87a19d5f-aaaa", "LEAPMUX_CONTROL_TAB_TYPE": "agent"}
		f.located = tab("87a19d5f-aaaa", leapmuxv1.TabType_TAB_TYPE_AGENT)
		got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTile, "self")
		require.NoError(t, err)
		assert.Equal(t, "tile-1", got.ID)
		got, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindWorkspace, "self")
		require.NoError(t, err)
		assert.Equal(t, "ws-1", got.ID)
	})
	t.Run("fails when the tile of the calling tab cannot be located", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{"LEAPMUX_CONTROL_TAB_ID": "gone"}
		_, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTile, "self")
		require.ErrorContains(t, err, "locate the calling tab")
	})
	t.Run("is a usage error outside a tab", func(t *testing.T) {
		for _, kind := range []resolve.Kind{resolve.KindTab, resolve.KindAgent, resolve.KindTerminal, resolve.KindTile, resolve.KindWorkspace, resolve.KindWorker} {
			_, err := resolve.Canonical(context.Background(), sampleListing().listing(), kind, "self")
			assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, kind.String())
		}
	})
	t.Run("of an agent is a usage error inside a terminal tab", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{"LEAPMUX_CONTROL_TAB_ID": "term-1", "LEAPMUX_CONTROL_TAB_TYPE": "terminal"}
		_, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, "self")
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	})
}
