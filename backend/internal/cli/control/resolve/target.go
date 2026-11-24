package resolve

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// Kind is what a target identifies.
type Kind int

const (
	// KindTab is a tab of any type.
	KindTab Kind = iota
	// KindAgent is an agent tab.
	KindAgent
	// KindTerminal is a terminal tab, or the quake terminal that the caller
	// runs inside.
	KindTerminal
	// KindTile is a tile of a workspace layout.
	KindTile
	// KindWorkspace is a workspace.
	KindWorkspace
	// KindWorker is a Worker.
	KindWorker
)

// String returns the noun that a message uses for the kind.
func (k Kind) String() string {
	switch k {
	case KindAgent:
		return "agent"
	case KindTerminal:
		return "terminal"
	case KindTile:
		return "tile"
	case KindWorkspace:
		return "workspace"
	case KindWorker:
		return "worker"
	default:
		return "tab"
	}
}

// TabType returns the tab type that the kind requires, or UNSPECIFIED for a
// kind that accepts every tab or is not a tab.
func (k Kind) TabType() leapmuxv1.TabType {
	switch k {
	case KindAgent:
		return leapmuxv1.TabType_TAB_TYPE_AGENT
	case KindTerminal:
		return leapmuxv1.TabType_TAB_TYPE_TERMINAL
	default:
		return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED
	}
}

// IsTab reports whether the kind identifies a tab.
func (k Kind) IsTab() bool {
	return k == KindTab || k == KindAgent || k == KindTerminal
}

// namedForm is the form that selects an object of the kind by its title or
// its name, or "" for a kind that has neither.
func (k Kind) namedForm() string {
	switch {
	case k.IsTab(), k == KindWorkspace:
		return titlePrefix + "<title>"
	case k == KindWorker:
		return namePrefix + "<name>"
	default:
		return ""
	}
}

// Help is one sentence about the forms that a target of the kind accepts,
// for the help of an argument. Canonical reads exactly these forms.
func (k Kind) Help() string {
	return joinWithOr(append(k.forms(), fmt.Sprintf("%q for %s", SelfTarget, k.selfMeaning()))) + "."
}

// HelpWithoutSelf is Help for a command that refuses the caller's own
// object, such as a wait on the calling agent, so the help offers no "self".
func (k Kind) HelpWithoutSelf() string {
	return joinWithOr(k.forms()) + "."
}

// forms lists the forms of a target of the kind, except "self".
func (k Kind) forms() []string {
	article := "A"
	if k == KindAgent {
		article = "An"
	}
	forms := []string{
		fmt.Sprintf("%s %s id", article, k),
		fmt.Sprintf("a unique prefix of %d or more characters of one", MinPrefixLength),
		`"id:<id>" for a literal id`,
	}
	switch {
	case k == KindAgent:
		forms = append(forms, `"title:<title>" or a bare case-insensitive title or --name slug`, `"parent" for the calling agent's parent`)
	case k.IsTab():
		forms = append(forms, `"title:<title>" or a bare case-insensitive title`)
	case k == KindWorkspace:
		forms = append(forms, `"title:<title>" or a bare case-insensitive title`)
	case k == KindWorker:
		forms = append(forms, `"name:<name>" or a bare case-insensitive worker name`)
	}
	return forms
}

// joinWithOr joins the available selector forms with commas and a final "or".
func joinWithOr(items []string) string {
	last := len(items) - 1
	return strings.Join(items[:last], ", ") + ", or " + items[last]
}

// selfMeaning states what "self" selects for the kind.
func (k Kind) selfMeaning() string {
	switch k {
	case KindTab:
		return "the tab that runs this command"
	case KindAgent:
		return "the agent tab that runs this command"
	case KindTerminal:
		return "the terminal that runs this command"
	default:
		return fmt.Sprintf("the %s of the tab that runs this command", k)
	}
}

// The forms of a target that are not an id.
const (
	// SelfTarget selects the caller's own object: the tab, tile, workspace
	// or Worker of the LeapMux tab that runs the command.
	SelfTarget = "self"
	// literalIDPrefix selects a full id, including one that spells a reserved word.
	literalIDPrefix = "id:"
	// titlePrefix selects a tab or a workspace by its exact title.
	titlePrefix = "title:"
	// namePrefix selects a Worker by its exact name.
	namePrefix = "name:"
)

// MinPrefixLength is the shortest id prefix that a target accepts. A shorter
// one matches too many ids to be useful.
const MinPrefixLength = 4

// Target is a target that Canonical resolved to a full id.
type Target struct {
	ID string
	// Self reports that the target is the caller's own object, selected by
	// the word "self" or by a command that defaulted to it.
	Self bool
	// Tab is the hub's row for a tab target, when the resolution read it.
	// It is nil for a full id, which costs no call, and for a quake
	// terminal, which has no row.
	Tab *leapmuxv1.WorkspaceTab
}

// Listing holds what a short form needs to find its object. Each function
// lists everything of one kind that the caller can see.
type Listing struct {
	// Tabs lists every tab of every workspace.
	Tabs func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error)
	// TabTitles returns known titles keyed by tab id. An absent key means
	// the caller could not read that title.
	TabTitles func(ctx context.Context, tabs []*leapmuxv1.WorkspaceTab) map[string]string
	// Workspaces lists every workspace.
	Workspaces func(ctx context.Context) ([]*leapmuxv1.Workspace, error)
	// Tiles lists every live tile, keyed by id, with the kind of the tile
	// ("leaf", "split", "grid") as the value.
	Tiles func(ctx context.Context) (map[string]string, error)
	// Workers lists every Worker.
	Workers func(ctx context.Context) ([]*leapmuxv1.Worker, error)
	// WorkerNames returns known names keyed by id. An absent key means the
	// caller could not read that name.
	WorkerNames func(ctx context.Context, workers []*leapmuxv1.Worker) map[string]string
	// LocateTab finds the hub's row for one tab, for "self" on a tile or a
	// workspace.
	LocateTab func(ctx context.Context, tabID string) (*leapmuxv1.WorkspaceTab, error)
	// Getenv reads the environment. Nil reads os.Getenv.
	Getenv func(string) string
}

func (l Listing) getenv(key string) string {
	if l.Getenv != nil {
		return strings.TrimSpace(l.Getenv(key))
	}
	return strings.TrimSpace(os.Getenv(key))
}

// SelfTabID returns the id of the tab that runs the command, as kind sees
// it, or "" outside a LeapMux tab of that kind.
//
// An agent command sees the tab only when the tab is an agent. A terminal
// command prefers LEAPMUX_CONTROL_TERMINAL_ID, which a quake panel sets and
// LEAPMUX_CONTROL_TAB_ID does not: in a quake panel, the tab id identifies a
// neighbouring tab.
func (l Listing) SelfTabID(kind Kind) string {
	if kind == KindTerminal {
		if id := l.getenv(controlenv.TerminalIDVariable); id != "" {
			return id
		}
	}
	id := l.getenv(controlenv.TabIDVariable)
	if want := kind.TabType(); want != leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		got, _ := ParseTabType(l.getenv(controlenv.TabTypeVariable))
		if got != want {
			return ""
		}
	}
	return id
}

// Canonical resolves the target input of kind to a full id.
//
// The input takes one of these forms:
//
//   - A full id. It costs no call; the command that uses it reports an id
//     that does not exist.
//   - A unique prefix of an id, of at least MinPrefixLength characters.
//   - "id:<id>" for a literal id that may be short or spell a reserved word.
//   - "title:<exact title>" for a tab or a workspace.
//   - "name:<exact name>" for a Worker.
//   - "self" for the caller's own object.
//
// A short form that matches nothing fails with not_found, and one that
// matches more than one object fails with ambiguous_target and lists every
// candidate.
func Canonical(ctx context.Context, l Listing, kind Kind, input string) (Target, error) {
	selector := strings.TrimLeftFunc(input, unicode.IsSpace)
	input = strings.TrimSpace(selector)
	if exact := ExplicitID(kind, input); exact != "" {
		return Target{ID: exact}, nil
	}
	switch {
	case input == "":
		return Target{}, cli.Usagef("the %s is empty", kind)
	case input == SelfTarget:
		return l.self(ctx, kind)
	case strings.HasPrefix(input, literalIDPrefix):
		return Target{}, cli.Usagef(`"id:" needs an id after the colon`)
	case strings.HasPrefix(selector, titlePrefix):
		return l.byTitle(ctx, kind, strings.TrimPrefix(selector, titlePrefix))
	case strings.HasPrefix(selector, namePrefix):
		return l.byName(ctx, kind, strings.TrimPrefix(selector, namePrefix))
	case len(input) < MinPrefixLength:
		// Allow short (1-3 char) bare case-insensitive titles, agent --name
		// slugs (e.g. "qa", "api"), and worker names before rejecting the input
		// as too short for an ID prefix.
		if got, handled, err := l.byShortBareName(ctx, kind, input); handled {
			return got, err
		}
		e := cli.Usagef("%q is too short for a %s id prefix; type at least %d characters", input, kind, MinPrefixLength).
			WithHint(`use "id:<id>" to select a literal id`)
		if named := kind.namedForm(); named != "" {
			return Target{}, e.WithHint(`use "self" for your own %s, or %q`, kind, named)
		}
		return Target{}, e.WithHint(`use "self" for your own %s`, kind)
	}
	return l.byPrefix(ctx, kind, input)
}

func (l Listing) byShortBareName(ctx context.Context, kind Kind, input string) (Target, bool, error) {
	switch {
	case kind.IsTab():
		if l.TabTitles == nil {
			return Target{}, false, nil
		}
		tabs, err := l.tabsOf(ctx, kind)
		if err != nil {
			return Target{}, true, err
		}
		return l.bareTabTitleMatch(ctx, kind, tabs, input)
	case kind == KindWorkspace:
		if l.Workspaces == nil {
			return Target{}, false, nil
		}
		workspaces, err := l.Workspaces(ctx)
		if err != nil {
			return Target{}, true, err
		}
		var matches []cli.Candidate
		for _, w := range workspaces {
			if strings.EqualFold(w.GetTitle(), input) {
				matches = append(matches, cli.Candidate{ID: w.GetId(), Kind: "workspace", Title: w.GetTitle()})
			}
		}
		if len(matches) == 0 {
			return Target{}, false, nil
		}
		got, err := pick(kind, fmt.Sprintf("title %q", input), matches)
		return got, true, err
	case kind == KindWorker:
		if l.Workers == nil || l.WorkerNames == nil {
			return Target{}, false, nil
		}
		workers, err := l.Workers(ctx)
		if err != nil {
			return Target{}, true, err
		}
		return l.bareWorkerNameMatch(ctx, workers, input)
	default:
		return Target{}, false, nil
	}
}

// ExplicitID returns an ID supplied in full or with the literal ID prefix.
func ExplicitID(kind Kind, selector string) string {
	input := strings.TrimSpace(selector)
	if literal, ok := strings.CutPrefix(input, literalIDPrefix); ok {
		return strings.TrimSpace(literal)
	}
	if strings.HasPrefix(input, titlePrefix) || strings.HasPrefix(input, namePrefix) {
		return ""
	}
	if isCompleteTargetID(kind, input) {
		return input
	}
	return ""
}

// isCompleteTargetID recognizes the direct forms. A UUID-shaped tab id is
// distinct from a 36-character prefix of a generated id, and a 48-character
// string only qualifies as a generated id when every byte is an ASCII
// identifier character (alphanumeric, '-', or '_'), so a 48-character title
// with spaces or punctuation still resolves by title.
func isCompleteTargetID(kind Kind, input string) bool {
	if len(input) == id.GeneratedLength && isGeneratedIDAlphabet(input) {
		return true
	}
	return kind.IsTab() && len(input) == 36 && uuid.Validate(input) == nil
}

func isGeneratedIDByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '-' || c == '_'
}

func isGeneratedIDAlphabet(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isGeneratedIDByte(s[i]) {
			return false
		}
	}
	return len(s) > 0
}

func (l Listing) self(ctx context.Context, kind Kind) (Target, error) {
	switch kind {
	case KindWorker:
		if id := l.getenv(controlenv.WorkerIDVariable); id != "" {
			return Target{ID: id, Self: true}, nil
		}
		return Target{}, notInsideTab(kind)
	case KindTile, KindWorkspace:
		tabID := l.SelfTabID(KindTab)
		if tabID == "" {
			return Target{}, notInsideTab(kind)
		}
		tab, err := l.LocateTab(ctx, tabID)
		if err != nil {
			return Target{}, fmt.Errorf("locate the calling tab %s: %w", tabID, err)
		}
		if kind == KindTile {
			return Target{ID: tab.GetTileId(), Self: true}, nil
		}
		return Target{ID: tab.GetWorkspaceId(), Self: true}, nil
	case KindTab, KindAgent, KindTerminal:
		id := l.SelfTabID(kind)
		if id == "" {
			return Target{}, notInsideTab(kind)
		}
		return Target{ID: id, Self: true}, nil
	}
	return Target{}, fmt.Errorf("resolve: no %q for kind %d", SelfTarget, kind)
}

func notInsideTab(kind Kind) error {
	return cli.Usagef(`"self" selects your own %s, and this shell does not run inside a LeapMux %s`, kind, selfHost(kind)).
		WithHint("give the %s's id instead", kind)
}

// selfHost is the kind of tab that "self" of kind needs.
func selfHost(kind Kind) string {
	switch kind {
	case KindAgent:
		return "agent tab"
	case KindTerminal:
		return "terminal"
	default:
		return "tab"
	}
}

func (l Listing) byPrefix(ctx context.Context, kind Kind, prefix string) (Target, error) {
	switch {
	case kind.IsTab():
		tabs, err := l.tabsOf(ctx, kind)
		if err != nil {
			return Target{}, err
		}
		var matches []*leapmuxv1.WorkspaceTab
		for _, t := range tabs {
			if t.GetTab().GetTabId() == prefix {
				return Target{ID: prefix, Tab: t}, nil
			}
			if strings.HasPrefix(t.GetTab().GetTabId(), prefix) {
				matches = append(matches, t)
			}
		}
		if len(matches) == 1 {
			return Target{ID: matches[0].GetTab().GetTabId(), Tab: matches[0]}, nil
		}
		if len(matches) == 0 && l.TabTitles != nil {
			if got, handled, err := l.bareTabTitleMatch(ctx, kind, tabs, prefix); handled {
				return got, err
			}
		}
		return Target{}, l.tabMatchError(ctx, kind, "id prefix or title "+prefix, matches)
	case kind == KindWorkspace:
		workspaces, err := l.Workspaces(ctx)
		if err != nil {
			return Target{}, err
		}
		var matches []cli.Candidate
		for _, w := range workspaces {
			if w.GetId() == prefix {
				return Target{ID: prefix}, nil
			}
			if strings.HasPrefix(w.GetId(), prefix) {
				matches = append(matches, cli.Candidate{ID: w.GetId(), Kind: "workspace", Title: w.GetTitle()})
			}
		}
		if len(matches) == 0 {
			for _, w := range workspaces {
				if strings.EqualFold(w.GetTitle(), prefix) {
					matches = append(matches, cli.Candidate{ID: w.GetId(), Kind: "workspace", Title: w.GetTitle()})
				}
			}
			if len(matches) > 0 {
				return pick(kind, fmt.Sprintf("title %q", prefix), matches)
			}
		}
		return pick(kind, "id prefix or title "+prefix, matches)
	case kind == KindTile:
		tiles, err := l.Tiles(ctx)
		if err != nil {
			return Target{}, err
		}
		if _, exists := tiles[prefix]; exists {
			return Target{ID: prefix}, nil
		}
		var matches []cli.Candidate
		for id, nodeKind := range tiles {
			if strings.HasPrefix(id, prefix) {
				matches = append(matches, cli.Candidate{ID: id, Kind: nodeKind})
			}
		}
		return pick(kind, "id prefix "+prefix, matches)
	default:
		workers, err := l.Workers(ctx)
		if err != nil {
			return Target{}, err
		}
		var matches []cli.Candidate
		for _, w := range workers {
			if w.GetId() == prefix {
				return Target{ID: prefix}, nil
			}
			if strings.HasPrefix(w.GetId(), prefix) {
				matches = append(matches, cli.Candidate{ID: w.GetId(), Kind: "worker"})
			}
		}
		if len(matches) == 0 && l.WorkerNames != nil {
			if got, handled, err := l.bareWorkerNameMatch(ctx, workers, prefix); handled {
				return got, err
			}
		}
		if len(matches) > 1 && l.WorkerNames != nil {
			names := l.WorkerNames(ctx, workers)
			for i := range matches {
				matches[i].Title = names[matches[i].ID]
			}
		}
		return pick(kind, "id prefix or name "+prefix, matches)
	}
}

// bareTabTitleMatch falls back to a case-insensitive title match when no ID
// prefix matched. It returns handled=true when one or more tabs matched (or
// when missing Worker metadata blocks a unique match).
func (l Listing) bareTabTitleMatch(ctx context.Context, kind Kind, tabs []*leapmuxv1.WorkspaceTab, input string) (Target, bool, error) {
	if len(tabs) == 0 {
		return Target{}, false, nil
	}
	titles := l.TabTitles(ctx, tabs)
	var matches []*leapmuxv1.WorkspaceTab
	unread := 0
	for _, t := range tabs {
		actual, known := titles[t.GetTab().GetTabId()]
		if !known {
			unread++
		} else if strings.EqualFold(actual, input) {
			matches = append(matches, t)
		}
	}
	if len(matches) == 0 {
		return Target{}, false, nil
	}
	what := fmt.Sprintf("title %q", input)
	if unread > 0 && len(matches) < 2 {
		return Target{}, true, incompleteMetadataError(what, kind, unread)
	}
	if len(matches) == 1 {
		return Target{ID: matches[0].GetTab().GetTabId(), Tab: matches[0]}, true, nil
	}
	return Target{}, true, tabMatchErrorWithTitles(kind, what, matches, titles)
}

// bareWorkerNameMatch falls back to a case-insensitive Worker name match when
// no ID prefix matched.
func (l Listing) bareWorkerNameMatch(ctx context.Context, workers []*leapmuxv1.Worker, input string) (Target, bool, error) {
	if len(workers) == 0 {
		return Target{}, false, nil
	}
	names := l.WorkerNames(ctx, workers)
	var matches []cli.Candidate
	unread := 0
	for _, w := range workers {
		actual, known := names[w.GetId()]
		if !known {
			unread++
		} else if strings.EqualFold(actual, input) {
			matches = append(matches, cli.Candidate{ID: w.GetId(), Kind: "worker", Title: actual})
		}
	}
	if len(matches) == 0 {
		return Target{}, false, nil
	}
	what := fmt.Sprintf("name %q", input)
	if unread > 0 && len(matches) < 2 {
		return Target{}, true, incompleteMetadataError(what, KindWorker, unread)
	}
	got, err := pick(KindWorker, what, matches)
	return got, true, err
}

func (l Listing) byTitle(ctx context.Context, kind Kind, title string) (Target, error) {
	if strings.TrimSpace(title) == "" {
		return Target{}, cli.Usagef(`"title:" needs a title after the colon`)
	}
	switch {
	case kind.IsTab():
		tabs, err := l.tabsOf(ctx, kind)
		if err != nil {
			return Target{}, err
		}
		titles := l.TabTitles(ctx, tabs)
		var matches []*leapmuxv1.WorkspaceTab
		unread := 0
		for _, t := range tabs {
			actual, known := titles[t.GetTab().GetTabId()]
			if !known {
				unread++
			} else if actual == title {
				matches = append(matches, t)
			}
		}
		what := fmt.Sprintf("title %q", title)
		if unread > 0 && len(matches) < 2 {
			return Target{}, incompleteMetadataError(what, kind, unread)
		}
		if len(matches) == 1 {
			return Target{ID: matches[0].GetTab().GetTabId(), Tab: matches[0]}, nil
		}
		return Target{}, tabMatchErrorWithTitles(kind, what, matches, titles)
	case kind == KindWorkspace:
		workspaces, err := l.Workspaces(ctx)
		if err != nil {
			return Target{}, err
		}
		var matches []cli.Candidate
		for _, w := range workspaces {
			if w.GetTitle() == title {
				matches = append(matches, cli.Candidate{ID: w.GetId(), Kind: "workspace", Title: w.GetTitle()})
			}
		}
		return pick(kind, fmt.Sprintf("title %q", title), matches)
	}
	return Target{}, cli.Usagef(`a %s has no title; "title:" selects a tab or a workspace`, kind)
}

func (l Listing) byName(ctx context.Context, kind Kind, name string) (Target, error) {
	if kind == KindAgent {
		if strings.TrimSpace(name) == "" {
			return Target{}, cli.Usagef(`"name:" needs a name after the colon`)
		}
		return l.byTitle(ctx, KindAgent, name)
	}
	if kind != KindWorker {
		return Target{}, cli.Usagef(`"name:" selects a worker or an agent, not %s`, indefiniteArticle(kind.String()))
	}
	if strings.TrimSpace(name) == "" {
		return Target{}, cli.Usagef(`"name:" needs a name after the colon`)
	}
	workers, err := l.Workers(ctx)
	if err != nil {
		return Target{}, err
	}
	names := l.WorkerNames(ctx, workers)
	var matches []cli.Candidate
	unread := 0
	for _, w := range workers {
		actual, known := names[w.GetId()]
		if !known {
			unread++
		} else if actual == name {
			matches = append(matches, cli.Candidate{ID: w.GetId(), Kind: "worker", Title: name})
		}
	}
	what := fmt.Sprintf("name %q", name)
	if unread > 0 && len(matches) < 2 {
		return Target{}, incompleteMetadataError(what, kind, unread)
	}
	return pick(kind, what, matches)
}

func indefiniteArticle(noun string) string {
	if len(noun) > 0 && strings.ContainsRune("aeiouAEIOU", rune(noun[0])) {
		return "an " + noun
	}
	return "a " + noun
}

func incompleteMetadataError(what string, kind Kind, unread int) error {
	return cli.Newf(cli.CodeUnavailable, "cannot resolve %s because metadata is unavailable for %d of the listed objects", what, unread).
		WithHint("use a full id or a unique id prefix to select the %s", kind)
}

// tabsOf lists the tabs that kind accepts.
func (l Listing) tabsOf(ctx context.Context, kind Kind) ([]*leapmuxv1.WorkspaceTab, error) {
	tabs, err := l.Tabs(ctx)
	if err != nil {
		return nil, err
	}
	want := kind.TabType()
	if want == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		return tabs, nil
	}
	out := tabs[:0:0]
	for _, t := range tabs {
		if t.GetTab().GetTabType() == want {
			out = append(out, t)
		}
	}
	return out, nil
}

// tabMatchError reports a short form that matched no tab or more than one.
// It asks the Workers for titles only when it has candidates to list.
func (l Listing) tabMatchError(ctx context.Context, kind Kind, what string, matches []*leapmuxv1.WorkspaceTab) error {
	var titles map[string]string
	if len(matches) > 1 {
		titles = l.TabTitles(ctx, matches)
	}
	return tabMatchErrorWithTitles(kind, what, matches, titles)
}

func tabMatchErrorWithTitles(kind Kind, what string, matches []*leapmuxv1.WorkspaceTab, titles map[string]string) error {
	candidates := make([]cli.Candidate, 0, len(matches))
	for _, t := range matches {
		id := t.GetTab().GetTabId()
		candidates = append(candidates, cli.Candidate{ID: id, Kind: TabTypeWireName(t.GetTab().GetTabType()), Title: titles[id]})
	}
	_, err := pick(kind, what, candidates)
	return err
}

// pick returns the one candidate, or the error for none or several.
func pick(kind Kind, what string, matches []cli.Candidate) (Target, error) {
	switch len(matches) {
	case 1:
		return Target{ID: matches[0].ID}, nil
	case 0:
		e := cli.Newf(cli.CodeNotFound, "no %s matches the %s", kind, what)
		if listCmd := kind.listCommand(); listCmd != "" {
			e = e.WithHint(`list available %ss with "%s"`, kind, listCmd)
		}
		if named := kind.namedForm(); named != "" {
			e = e.WithHint(`select by full id, unique prefix, "id:<id>", %q, or bare title/name`, named)
		} else {
			e = e.WithHint(`select by full id, unique prefix, or "id:<id>"`)
		}
		return Target{}, e
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	e := cli.Newf(cli.CodeAmbiguousTarget, "%d %ss match the %s", len(matches), kind, what).
		WithHint("type more characters of the id, or the full id")
	e.Candidates = matches
	return Target{}, e
}

func (k Kind) listCommand() string {
	switch k {
	case KindTab:
		return "leapmux tab list"
	case KindAgent:
		return "leapmux agent list"
	case KindTerminal:
		return "leapmux terminal list"
	case KindWorkspace:
		return "leapmux workspace list"
	case KindWorker:
		return "leapmux worker list"
	case KindTile:
		return "leapmux layout get"
	default:
		return ""
	}
}
