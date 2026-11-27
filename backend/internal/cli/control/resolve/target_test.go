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
	"github.com/leapmux/leapmux/internal/util/id"
)

// fakeListing serves a fixed set of objects, and counts the list calls so a
// test can pin which forms cost a call.
type fakeListing struct {
	tabs         []*leapmuxv1.WorkspaceTab
	titles       map[string]string
	workspaces   []*leapmuxv1.Workspace
	tiles        map[string]string
	workers      []*leapmuxv1.Worker
	workerNames  map[string]string
	located      *leapmuxv1.WorkspaceTab
	env          map[string]string
	selfIdentity func(context.Context) (resolve.SelfInfo, error)
	listErr      error
	calls        int
	titleCalls   int
	selfCalls    int
}

func (f *fakeListing) listing() resolve.Listing {
	l := resolve.Listing{
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
	if f.selfIdentity != nil {
		l.SelfIdentity = func(ctx context.Context) (resolve.SelfInfo, error) {
			f.selfCalls++
			return f.selfIdentity(ctx)
		}
	}
	return l
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

func TestCanonical_ShortBareMissReturnsNotFoundForNamedKindsAndUsageForTile(t *testing.T) {
	f := sampleListing()
	for _, input := range []string{"8", "87a"} {
		_, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindTab, input)
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeNotFound, e.Code, input)
		assert.Equal(t, cli.ExitNotFound, e.ExitCode(), input)
		assert.Contains(t, e.Message, "4-character id prefix minimum", input)
	}
	// A tile has no title fallback, so a short tile prefix still fails as usage before any call.
	fTile := sampleListing()
	_, err := resolve.Canonical(context.Background(), fTile.listing(), resolve.KindTile, "87")
	e := cli.FromError(err)
	assert.Equal(t, cli.CodeUsage, e.Code)
	assert.Equal(t, cli.ExitUsage, e.ExitCode())
	assert.Zero(t, fTile.calls, "a short tile prefix fails before any call")
}

func TestCanonical_ShortBareSlugResolvesDirectly(t *testing.T) {
	f := sampleListing()
	f.titles["87a19d5f-aaaa"] = "qa"
	got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, "qa")
	require.NoError(t, err)
	assert.Equal(t, "87a19d5f-aaaa", got.ID)

	f.workspaces[0].Title = "ops"
	got, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindWorkspace, "OPS")
	require.NoError(t, err)
	assert.Equal(t, "1aszUbNsWorkspaceOne", got.ID)

	f.workerNames["hQrS3iDtWorkerOne"] = "ci"
	got, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindWorker, "CI")
	require.NoError(t, err)
	assert.Equal(t, "hQrS3iDtWorkerOne", got.ID)
}

func TestKind_OutOfRangeFallsBackToDefaultSpec(t *testing.T) {
	for _, kind := range []resolve.Kind{resolve.Kind(-1), resolve.Kind(99)} {
		assert.Equal(t, "tab", kind.String())
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, kind.TabType())
		assert.False(t, kind.IsTab())
		assert.Equal(t,
			`A tab id, a unique prefix of 4 or more characters of one, "id:<id>" for a literal id, or "self" for the tab of the tab that runs this command.`,
			kind.Help())
		assert.Equal(t,
			`A tab id, a unique prefix of 4 or more characters of one, or "id:<id>" for a literal id.`,
			kind.HelpWithoutSelf())
		_, err := resolve.Canonical(t.Context(), sampleListing().listing(), kind, "self")
		require.ErrorContains(t, err, `no "self" for kind`)
	}
}

// The hint of a short prefix offers the named form of the kind, and only a
// form that the kind accepts: a tile has no title, and a worker has a name.
func TestCanonical_AShortPrefixHintsTheFormsOfItsKind(t *testing.T) {
	f := sampleListing()
	for _, tc := range []struct {
		kind     resolve.Kind
		wantCode cli.Code
		wantExit int
		wantHint string
	}{
		{resolve.KindTab, cli.CodeNotFound, cli.ExitNotFound, `use "self" for your own tab, or "title:<title>"`},
		{resolve.KindAgent, cli.CodeNotFound, cli.ExitNotFound, `use "self" for your own agent, or "title:<title>"`},
		{resolve.KindTerminal, cli.CodeNotFound, cli.ExitNotFound, `use "self" for your own terminal, or "title:<title>"`},
		{resolve.KindWorkspace, cli.CodeNotFound, cli.ExitNotFound, `use "self" for your own workspace, or "title:<title>"`},
		{resolve.KindWorker, cli.CodeNotFound, cli.ExitNotFound, `use "self" for your own worker, or "name:<name>"`},
		{resolve.KindTile, cli.CodeUsage, cli.ExitUsage, `use "self" for your own tile`},
	} {
		_, err := resolve.Canonical(context.Background(), f.listing(), tc.kind, "87")
		e := cli.FromError(err)
		assert.Equal(t, tc.wantCode, e.Code, "%s", tc.kind)
		assert.Equal(t, tc.wantExit, e.ExitCode(), "%s", tc.kind)
		assert.Equal(t, []string{`use "id:<id>" to select a literal id`, tc.wantHint}, e.Hints, "%s", tc.kind)
	}
}

func TestIsGeneratedID(t *testing.T) {
	generated := id.Generate()
	require.Len(t, generated, id.GeneratedLength)

	for _, tc := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "real generated id", input: generated, want: true},
		{name: "all allowed ASCII characters", input: "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg-_xyz", want: true},
		{name: "empty", input: "", want: false},
		{name: "too short", input: strings.Repeat("a", id.GeneratedLength-1), want: false},
		{name: "too long", input: strings.Repeat("a", id.GeneratedLength+1), want: false},
		{name: "UUID", input: "87a19d5f-4838-4e34-9f0f-56130f0a62b1", want: false},
		{name: "48 chars with space", input: strings.Repeat("a", 23) + " " + strings.Repeat("b", 24), want: false},
		{name: "48 chars with punctuation", input: strings.Repeat("a", 47) + ".", want: false},
		{name: "48 bytes with non-ASCII UTF-8", input: strings.Repeat("a", 46) + "é", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolve.IsGeneratedID(tc.input))
		})
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

func TestCanonical_ANameSelectsAWorkerOrAnAgent(t *testing.T) {
	f := sampleListing()
	got, err := resolve.Canonical(context.Background(), f.listing(), resolve.KindWorker, "name:laptop")
	require.NoError(t, err)
	assert.Equal(t, "hQrSxxxxWorkerTwo", got.ID)

	_, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindWorker, "name:nope")
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	_, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindWorker, "name:")
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

	f.titles["87a19d5f-aaaa"] = "reviewer"
	got, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, "name:reviewer")
	require.NoError(t, err)
	assert.Equal(t, "87a19d5f-aaaa", got.ID)

	_, err = resolve.Canonical(context.Background(), f.listing(), resolve.KindAgent, "name:")
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

	for _, kind := range []resolve.Kind{resolve.KindTab, resolve.KindTerminal, resolve.KindTile, resolve.KindWorkspace} {
		_, err = resolve.Canonical(context.Background(), f.listing(), kind, "name:build-box")
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, kind.String())
	}
}

func TestCanonical_NilMetadataCallbacksDoNotPanic(t *testing.T) {
	t.Run("byTitle with nil TabTitles", func(t *testing.T) {
		f := sampleListing()
		listing := f.listing()
		listing.TabTitles = nil
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "title:CLI Design")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
	})
	t.Run("byName with nil WorkerNames", func(t *testing.T) {
		f := sampleListing()
		listing := f.listing()
		listing.WorkerNames = nil
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindWorker, "name:build-box")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
	})
	t.Run("ambiguous tab prefix with nil TabTitles", func(t *testing.T) {
		f := sampleListing()
		listing := f.listing()
		listing.TabTitles = nil
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindTab, "87a1")
		e := cli.FromError(err)
		require.Equal(t, cli.CodeAmbiguousTarget, e.Code)
		require.Len(t, e.Candidates, 2)
		assert.Empty(t, e.Candidates[0].Title)
	})
}

func TestCanonical_BareFallbackMetadataUnavailability(t *testing.T) {
	t.Run("unique bare tab match fails unavailable when another tab's metadata is unread", func(t *testing.T) {
		f := sampleListing()
		listing := f.listing()
		listing.TabTitles = func(context.Context, []*leapmuxv1.WorkspaceTab) map[string]string {
			return map[string]string{"87a19d5f-aaaa": "CLI Design"}
		}
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "cli design")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
	})
	t.Run("zero bare tab matches still yield not_found when unread metadata exists", func(t *testing.T) {
		f := sampleListing()
		listing := f.listing()
		listing.TabTitles = func(context.Context, []*leapmuxv1.WorkspaceTab) map[string]string {
			return map[string]string{"87a19d5f-aaaa": "CLI Design"}
		}
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "UnreachableTitle")
		require.Error(t, err)
		assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
	})
	t.Run("explicit title selector fails unavailable on zero matches when metadata is unread", func(t *testing.T) {
		f := sampleListing()
		listing := f.listing()
		listing.TabTitles = func(context.Context, []*leapmuxv1.WorkspaceTab) map[string]string {
			return map[string]string{"87a19d5f-aaaa": "CLI Design"}
		}
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "title:UnreachableTitle")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
	})
	t.Run("unique bare worker match fails unavailable when another online worker's metadata is unread", func(t *testing.T) {
		f := sampleListing()
		delete(f.workerNames, "hQrSxxxxWorkerTwo")
		_, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "build-box")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
	})
	t.Run("zero bare worker matches still yield not_found when unread metadata exists", func(t *testing.T) {
		f := sampleListing()
		delete(f.workerNames, "hQrSxxxxWorkerTwo")
		_, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "missing-box")
		require.Error(t, err)
		assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
	})
	t.Run("explicit name selector fails unavailable on zero matches when online worker metadata is unread", func(t *testing.T) {
		f := sampleListing()
		delete(f.workerNames, "hQrSxxxxWorkerTwo")
		_, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "name:missing-box")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
	})
}

func TestCanonical_OfflineWorkersDoNotBlockOnlineWorkerNameResolution(t *testing.T) {
	f := &fakeListing{
		workers: []*leapmuxv1.Worker{
			{Id: "hQrS3iDtWorkerOne", Online: true},
			{Id: "hQrSxxxxOfflineTwo", Online: false},
		},
		workerNames: map[string]string{"hQrS3iDtWorkerOne": "build-box"},
	}

	got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "name:build-box")
	require.NoError(t, err)
	assert.Equal(t, "hQrS3iDtWorkerOne", got.ID)

	got, err = resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "BUILD-BOX")
	require.NoError(t, err)
	assert.Equal(t, "hQrS3iDtWorkerOne", got.ID)

	_, err = resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "missing-box")
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
}

func TestCanonical_AmbiguousWorkerPrefixQueriesOnlyMatchedWorkers(t *testing.T) {
	f := sampleListing()
	f.workers = append(f.workers, &leapmuxv1.Worker{Id: "zzzzUnmatchedWorker", Online: true})
	var queried []string
	listing := f.listing()
	listing.WorkerNames = func(_ context.Context, workers []*leapmuxv1.Worker) map[string]string {
		for _, w := range workers {
			queried = append(queried, w.GetId())
		}
		return f.workerNames
	}

	_, err := resolve.Canonical(t.Context(), listing, resolve.KindWorker, "hQrS")
	require.Equal(t, cli.CodeAmbiguousTarget, cli.FromError(err).Code)
	assert.ElementsMatch(t, []string{"hQrS3iDtWorkerOne", "hQrSxxxxWorkerTwo"}, queried)
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

func TestCanonical_Parent(t *testing.T) {
	t.Run("resolves the calling agent's parent for agent and tab kinds", func(t *testing.T) {
		for _, tc := range []struct {
			kind  resolve.Kind
			input string
		}{
			{resolve.KindAgent, "parent"},
			{resolve.KindAgent, "PARENT"},
			{resolve.KindAgent, " parent "},
			{resolve.KindTab, "parent"},
			{resolve.KindTab, "\tPaReNt\n"},
		} {
			f := sampleListing()
			f.env = map[string]string{
				"LEAPMUX_CONTROL_TAB_ID":    "child-1",
				"LEAPMUX_CONTROL_TAB_TYPE":  "agent",
				"LEAPMUX_CONTROL_WORKER_ID": "worker-A",
			}
			listing := f.listing()
			listing.ParentAgent = func(_ context.Context, workerID, selfAgentID string) (string, error) {
				assert.Equal(t, "worker-A", workerID)
				assert.Equal(t, "child-1", selfAgentID)
				return "parent-1", nil
			}
			got, err := resolve.Canonical(t.Context(), listing, tc.kind, tc.input)
			require.NoError(t, err)
			assert.Equal(t, "parent-1", got.ID)
			assert.True(t, got.Parent)
			assert.False(t, got.Self)
		}
	})
	t.Run("is a usage error outside an agent tab", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			env  map[string]string
		}{
			{
				name: "terminal tab",
				env:  map[string]string{"LEAPMUX_CONTROL_TAB_ID": "term-1", "LEAPMUX_CONTROL_TAB_TYPE": "terminal", "LEAPMUX_CONTROL_WORKER_ID": "worker-A"},
			},
			{
				name: "missing worker id",
				env:  map[string]string{"LEAPMUX_CONTROL_TAB_ID": "child-1", "LEAPMUX_CONTROL_TAB_TYPE": "agent"},
			},
			{
				name: "empty environment",
				env:  map[string]string{},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := sampleListing()
				f.env = tc.env
				listing := f.listing()
				listing.ParentAgent = func(context.Context, string, string) (string, error) {
					t.Fatal("ParentAgent must not run without calling agent and worker env")
					return "", nil
				}
				_, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "parent")
				assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
			})
		}
	})
	t.Run("is unavailable when ParentAgent is nil", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{"LEAPMUX_CONTROL_TAB_ID": "child-1", "LEAPMUX_CONTROL_TAB_TYPE": "agent", "LEAPMUX_CONTROL_WORKER_ID": "worker-A"}
		_, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindAgent, "parent")
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
	})
	t.Run("is not_found when the calling agent has no parent", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{"LEAPMUX_CONTROL_TAB_ID": "root-1", "LEAPMUX_CONTROL_TAB_TYPE": "agent", "LEAPMUX_CONTROL_WORKER_ID": "worker-A"}
		listing := f.listing()
		listing.ParentAgent = func(context.Context, string, string) (string, error) { return "", nil }
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "parent")
		assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
	})
	t.Run("propagates lookup error", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{"LEAPMUX_CONTROL_TAB_ID": "child-1", "LEAPMUX_CONTROL_TAB_TYPE": "agent", "LEAPMUX_CONTROL_WORKER_ID": "worker-A"}
		listing := f.listing()
		listing.ParentAgent = func(context.Context, string, string) (string, error) { return "", errors.New("worker down") }
		_, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "parent")
		require.ErrorContains(t, err, "worker down")
	})
	t.Run("does not treat parent as relational for non-agent kinds", func(t *testing.T) {
		f := sampleListing()
		f.workerNames["hQrS3iDtWorkerOne"] = "parent"
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "parent")
		require.NoError(t, err)
		assert.Equal(t, "hQrS3iDtWorkerOne", got.ID)
		assert.False(t, got.Parent)

		f.titles["87a1ffff-bbbb"] = "parent"
		got, err = resolve.Canonical(t.Context(), f.listing(), resolve.KindTerminal, "parent")
		require.NoError(t, err)
		assert.Equal(t, "87a1ffff-bbbb", got.ID)
		assert.False(t, got.Parent)
	})
}

func TestCanonical_SelfCarriesEnvTabTypeAndWorkerID(t *testing.T) {
	t.Run("agent tab populates TabType and WorkerID from env", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{
			"LEAPMUX_CONTROL_TAB_ID":    "agent-1",
			"LEAPMUX_CONTROL_TAB_TYPE":  "agent",
			"LEAPMUX_CONTROL_WORKER_ID": "worker-A",
		}
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindTab, "self")
		require.NoError(t, err)
		assert.Equal(t, "agent-1", got.ID)
		assert.True(t, got.Self)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, got.TabType)
		assert.Equal(t, "worker-A", got.WorkerID)
		assert.Zero(t, f.calls)
	})
	t.Run("quake terminal populates TAB_TYPE_TERMINAL and WorkerID from env", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{
			"LEAPMUX_CONTROL_TERMINAL_ID": "quake-1",
			"LEAPMUX_CONTROL_WORKER_ID":   "worker-A",
		}
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindTerminal, "self")
		require.NoError(t, err)
		assert.Equal(t, "quake-1", got.ID)
		assert.True(t, got.Self)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, got.TabType)
		assert.Equal(t, "worker-A", got.WorkerID)
		assert.Zero(t, f.calls)
	})
}

func TestCanonical_SelfIdentity(t *testing.T) {
	t.Run("resolves self via SelfIdentity when env is unset and LocateTab fails", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			kind resolve.Kind
			info resolve.SelfInfo
		}{
			{
				name: "KindTab with agent",
				kind: resolve.KindTab,
				info: resolve.SelfInfo{TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: "worker-A"},
			},
			{
				name: "KindAgent with agent",
				kind: resolve.KindAgent,
				info: resolve.SelfInfo{TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: "worker-A"},
			},
			{
				name: "KindTerminal with terminal",
				kind: resolve.KindTerminal,
				info: resolve.SelfInfo{TabID: "term-1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, WorkerID: "worker-B"},
			},
			{
				name: "KindTab with file tab",
				kind: resolve.KindTab,
				info: resolve.SelfInfo{TabID: "file-1", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, WorkerID: "worker-C"},
			},
			{
				name: "KindTab with image tab",
				kind: resolve.KindTab,
				info: resolve.SelfInfo{TabID: "img-1", TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, WorkerID: "worker-C"},
			},
			{
				name: "trims whitespace on TabID and WorkerID",
				kind: resolve.KindAgent,
				info: resolve.SelfInfo{TabID: "  agent-2\n", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: "\tworker-D "},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := sampleListing()
				// Leave f.located nil so LocateTab fails if called.
				f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
					return tc.info, nil
				}
				got, err := resolve.Canonical(t.Context(), f.listing(), tc.kind, "self")
				require.NoError(t, err)
				assert.Equal(t, strings.TrimSpace(tc.info.TabID), got.ID)
				assert.True(t, got.Self)
				assert.Equal(t, tc.info.TabType, got.TabType)
				assert.Equal(t, strings.TrimSpace(tc.info.WorkerID), got.WorkerID)
				assert.Equal(t, 1, f.selfCalls)
				assert.Zero(t, f.calls, "Hub RPCs must not run when SelfIdentity answers")
			})
		}
	})

	t.Run("fails cleanly on kind mismatch or missing identity from SelfIdentity", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			kind resolve.Kind
			info resolve.SelfInfo
		}{
			{
				name: "KindAgent when SelfIdentity returns terminal",
				kind: resolve.KindAgent,
				info: resolve.SelfInfo{TabID: "term-1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, WorkerID: "worker-A"},
			},
			{
				name: "KindTerminal when SelfIdentity returns agent",
				kind: resolve.KindTerminal,
				info: resolve.SelfInfo{TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: "worker-A"},
			},
			{
				name: "KindTab when SelfIdentity returns unspecified tab type",
				kind: resolve.KindTab,
				info: resolve.SelfInfo{TabID: "tab-1", TabType: leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, WorkerID: "worker-A"},
			},
			{
				name: "KindTab when SelfIdentity returns empty tab id",
				kind: resolve.KindTab,
				info: resolve.SelfInfo{TabID: "", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: "worker-A"},
			},
			{
				name: "KindWorker when SelfIdentity returns empty worker id",
				kind: resolve.KindWorker,
				info: resolve.SelfInfo{TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: ""},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := sampleListing()
				f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
					return tc.info, nil
				}
				_, err := resolve.Canonical(t.Context(), f.listing(), tc.kind, "self")
				require.Error(t, err)
				assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
				assert.Equal(t, 1, f.selfCalls)
				assert.Zero(t, f.calls)
			})
		}
	})

	t.Run("propagates SelfIdentity errors", func(t *testing.T) {
		f := sampleListing()
		f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			return resolve.SelfInfo{}, errors.New("ipc socket closed")
		}
		_, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindTab, "self")
		require.ErrorContains(t, err, "ipc socket closed")
	})

	t.Run("queries SelfIdentity when env vars are incomplete", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{
			"LEAPMUX_CONTROL_TAB_ID": "agent-1",
		}
		f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			return resolve.SelfInfo{
				TabID:    "agent-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
				WorkerID: "worker-A",
			}, nil
		}
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindTab, "self")
		require.NoError(t, err)
		assert.Equal(t, "agent-1", got.ID)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, got.TabType)
		assert.Equal(t, "worker-A", got.WorkerID)
		assert.Equal(t, 1, f.selfCalls)
	})

	t.Run("preserves env WorkerID if SelfIdentity leaves WorkerID empty", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{
			"LEAPMUX_CONTROL_TAB_ID":    "agent-1",
			"LEAPMUX_CONTROL_WORKER_ID": "worker-env",
		}
		f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			return resolve.SelfInfo{
				TabID:   "agent-1",
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
			}, nil
		}
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindTab, "self")
		require.NoError(t, err)
		assert.Equal(t, "agent-1", got.ID)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, got.TabType)
		assert.Equal(t, "worker-env", got.WorkerID)
		assert.Equal(t, 1, f.selfCalls)
	})

	t.Run("env tab id takes precedence over a differing SelfIdentity tab id", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{
			"LEAPMUX_CONTROL_TERMINAL_ID": "quake-1",
		}
		f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			return resolve.SelfInfo{
				TabID:    "agent-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
				WorkerID: "worker-A",
			}, nil
		}
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindTerminal, "self")
		require.NoError(t, err)
		assert.Equal(t, "quake-1", got.ID)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, got.TabType)
		assert.Equal(t, "worker-A", got.WorkerID)
	})

	t.Run("skips SelfIdentity when env vars already give the complete identity", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{
			"LEAPMUX_CONTROL_TAB_ID":    "agent-1",
			"LEAPMUX_CONTROL_TAB_TYPE":  "agent",
			"LEAPMUX_CONTROL_WORKER_ID": "worker-A",
		}
		f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			t.Fatal("SelfIdentity must not run when env vars are complete")
			return resolve.SelfInfo{}, nil
		}
		got, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindTab, "self")
		require.NoError(t, err)
		assert.Equal(t, "agent-1", got.ID)
		assert.True(t, got.Self)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, got.TabType)
		assert.Equal(t, "worker-A", got.WorkerID)
		assert.Zero(t, f.selfCalls)
	})

	t.Run("rejects explicit env type mismatch without calling SelfIdentity", func(t *testing.T) {
		f := sampleListing()
		f.env = map[string]string{
			"LEAPMUX_CONTROL_TAB_ID":   "term-1",
			"LEAPMUX_CONTROL_TAB_TYPE": "terminal",
		}
		f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			t.Fatal("SelfIdentity must not run when env already proves a kind mismatch")
			return resolve.SelfInfo{}, nil
		}
		_, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindAgent, "self")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
		assert.Zero(t, f.selfCalls)
	})

	t.Run("resolves KindWorker, KindTile, and KindWorkspace when env is unset", func(t *testing.T) {
		f := sampleListing()
		f.located = tab("agent-1", leapmuxv1.TabType_TAB_TYPE_AGENT)
		f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			return resolve.SelfInfo{
				TabID:    "agent-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
				WorkerID: "worker-A",
			}, nil
		}

		gotWorker, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindWorker, "self")
		require.NoError(t, err)
		assert.Equal(t, "worker-A", gotWorker.ID)
		assert.Equal(t, "worker-A", gotWorker.WorkerID)
		assert.True(t, gotWorker.Self)
		assert.Zero(t, f.calls)

		gotTile, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindTile, "self")
		require.NoError(t, err)
		assert.Equal(t, "tile-1", gotTile.ID)
		assert.True(t, gotTile.Self)

		gotWS, err := resolve.Canonical(t.Context(), f.listing(), resolve.KindWorkspace, "self")
		require.NoError(t, err)
		assert.Equal(t, "ws-1", gotWS.ID)
		assert.True(t, gotWS.Self)
	})

	t.Run("resolves parent via SelfIdentity when env is unset and rejects non-agent", func(t *testing.T) {
		f := sampleListing()
		f.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			return resolve.SelfInfo{
				TabID:    "child-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
				WorkerID: "worker-A",
			}, nil
		}
		listing := f.listing()
		listing.ParentAgent = func(_ context.Context, workerID, selfAgentID string) (string, error) {
			assert.Equal(t, "worker-A", workerID)
			assert.Equal(t, "child-1", selfAgentID)
			return "parent-1", nil
		}
		got, err := resolve.Canonical(t.Context(), listing, resolve.KindAgent, "parent")
		require.NoError(t, err)
		assert.Equal(t, "parent-1", got.ID)
		assert.True(t, got.Parent)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, got.TabType)
		assert.Equal(t, "worker-A", got.WorkerID)

		fTerm := sampleListing()
		fTerm.selfIdentity = func(context.Context) (resolve.SelfInfo, error) {
			return resolve.SelfInfo{
				TabID:    "term-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
				WorkerID: "worker-A",
			}, nil
		}
		termListing := fTerm.listing()
		termListing.ParentAgent = func(context.Context, string, string) (string, error) {
			t.Fatal("ParentAgent must not run for a terminal tab")
			return "", nil
		}
		_, err = resolve.Canonical(t.Context(), termListing, resolve.KindAgent, "parent")
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	})
}
