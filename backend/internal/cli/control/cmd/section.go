package cmd

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/lexorank"
)

func newSectionCommand() *cobra.Command {
	return cli.Group("section", "List, create, rename, move, and delete sidebar sections",
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List your sidebar sections and how many workspaces each holds",
			Examples: []cli.Example{
				{Command: "leapmux section list"},
				{Command: "leapmux section list --json"},
			},
			Run: runSectionList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "create <name>",
			Short:   "Create a custom workspace section",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux section create "Client Work"`},
				{Command: `leapmux section create "Monitoring" --sidebar right`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("sidebar", "left", "sidebar to place the section in")
				cli.SetEnum(fs, "sidebar", "left", "right")
			},
			Run: runSectionCreate,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "rename <section> <name>",
			Short:   "Rename a custom section",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux section rename "Client Work" "Active Clients"`},
			},
			Complete: completeSections,
			Run:      runSectionRename,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "move <section>",
			Aliases: []string{"mv"},
			Short:   "Move a section to another sidebar or reorder it",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux section move "Client Work" --first`},
				{Command: `leapmux section move "Client Work" --sidebar right --last`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("sidebar", "", "destination sidebar (default: keep current sidebar)")
				cli.SetEnum(fs, "sidebar", "left", "right")
				fs.Bool("first", false, "put the section first in the sidebar")
				fs.Bool("last", false, "put the section last in the sidebar")
				fs.String("before", "", "put the section before this section (id, prefix, or name)")
				fs.String("after", "", "put the section after this section (id, prefix, or name)")
			},
			Complete: completeSections,
			Run:      runSectionMove,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "delete <section>",
			Aliases:     []string{"rm"},
			Short:       "Delete a custom section (moves its workspaces to In progress)",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: `leapmux section delete "Client Work"`},
			},
			Complete: completeSections,
			Run:      runSectionDelete,
		}),
	)
}

type sectionRow struct {
	ID         string `json:"id" table:"ID,short"`
	Name       string `json:"name" table:"NAME"`
	Type       string `json:"type" table:"TYPE"`
	Sidebar    string `json:"sidebar" table:"SIDEBAR"`
	Position   string `json:"position" table:"POSITION"`
	Workspaces int    `json:"workspaces" table:"WORKSPACES"`
}

type sectionListResult struct {
	Sections []sectionRow `json:"sections"`
}

func (r sectionListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Sections))
	for _, s := range r.Sections {
		out = append(out, s.ID)
	}
	return out
}

func sectionRowOf(sec *leapmuxv1.Section, count int) sectionRow {
	return sectionRow{
		ID:         sec.GetId(),
		Name:       sec.GetName(),
		Type:       enumToken(sec.GetSectionType()),
		Sidebar:    enumToken(sec.GetSidebar()),
		Position:   sec.GetPosition(),
		Workspaces: count,
	}
}

func parseSidebarFlag(raw string) (leapmuxv1.Sidebar, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "left":
		return leapmuxv1.Sidebar_SIDEBAR_LEFT, nil
	case "right":
		return leapmuxv1.Sidebar_SIDEBAR_RIGHT, nil
	default:
		return leapmuxv1.Sidebar_SIDEBAR_UNSPECIFIED, cli.Usagef(`--sidebar must be "left" or "right"; got %q`, raw)
	}
}

// resolveSection finds one section by exact ID, unique ID prefix (>= 4 chars),
// or case-insensitive name.
func resolveSection(sections []*leapmuxv1.Section, raw string) (*leapmuxv1.Section, error) {
	q := strings.TrimSpace(raw)
	if q == "" {
		return nil, cli.Usagef("section target is empty")
	}
	if strings.HasPrefix(q, "id:") {
		want := strings.TrimPrefix(q, "id:")
		for _, sec := range sections {
			if sec.GetId() == want {
				return sec, nil
			}
		}
		return nil, cli.Newf(cli.CodeNotFound, "no section with id %q", want).
			WithHint(`list sections with "leapmux section list"`)
	}
	if strings.HasPrefix(q, "name:") || strings.HasPrefix(q, "title:") {
		want := q[strings.IndexByte(q, ':')+1:]
		return matchSectionByName(sections, want)
	}
	for _, sec := range sections {
		if sec.GetId() == q {
			return sec, nil
		}
	}
	if len(q) >= resolve.MinPrefixLength {
		var matches []*leapmuxv1.Section
		for _, sec := range sections {
			if strings.HasPrefix(sec.GetId(), q) {
				matches = append(matches, sec)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return nil, ambiguousSectionsError(q, matches)
		}
	}
	return matchSectionByName(sections, q)
}

func matchSectionByName(sections []*leapmuxv1.Section, q string) (*leapmuxv1.Section, error) {
	var matches []*leapmuxv1.Section
	for _, sec := range sections {
		if strings.EqualFold(sec.GetName(), q) {
			matches = append(matches, sec)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, cli.Newf(cli.CodeNotFound, "no section matches %q", q).
			WithHint(`list sections with "leapmux section list"`)
	default:
		return nil, ambiguousSectionsError(q, matches)
	}
}

func ambiguousSectionsError(q string, matches []*leapmuxv1.Section) error {
	cands := make([]cli.Candidate, 0, len(matches))
	for _, m := range matches {
		cands = append(cands, cli.Candidate{ID: m.GetId(), Kind: "section", Title: m.GetName()})
	}
	err := cli.Newf(cli.CodeAmbiguousTarget, "%d sections match %q", len(matches), q)
	err.Candidates = cands
	return err
}

func (s *session) listSections() (*leapmuxv1.ListSectionsResponse, error) {
	var resp leapmuxv1.ListSectionsResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.SectionServiceListSectionsProcedure, &leapmuxv1.ListSectionsRequest{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func runSectionList(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	resp, err := s.listSections()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, item := range resp.GetItems() {
		counts[item.GetSectionId()]++
	}
	rows := make([]sectionRow, 0, len(resp.GetSections()))
	for _, sec := range resp.GetSections() {
		rows = append(rows, sectionRowOf(sec, counts[sec.GetId()]))
	}
	return sectionListResult{Sections: rows}, nil
}

func runSectionCreate(inv *cli.Invocation) (any, error) {
	name := strings.TrimSpace(inv.Arg("name"))
	if name == "" {
		return nil, cli.Usagef("section name is empty")
	}
	sidebar, err := parseSidebarFlag(stringFlag(inv, "sidebar"))
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var resp leapmuxv1.CreateSectionResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.SectionServiceCreateSectionProcedure, &leapmuxv1.CreateSectionRequest{
		Name:    name,
		Sidebar: sidebar,
	}, &resp); err != nil {
		return nil, err
	}
	return sectionRowOf(resp.GetSection(), 0), nil
}

func sectionWorkspaceCount(items []*leapmuxv1.SectionItem, sectionID string) int {
	n := 0
	for _, item := range items {
		if item.GetSectionId() == sectionID {
			n++
		}
	}
	return n
}

func runSectionRename(inv *cli.Invocation) (any, error) {
	name := strings.TrimSpace(inv.Arg("name"))
	if name == "" {
		return nil, cli.Usagef("section name is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	list, err := s.listSections()
	if err != nil {
		return nil, err
	}
	sec, err := resolveSection(list.GetSections(), inv.Arg("section"))
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.RenameSectionResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.SectionServiceRenameSectionProcedure, &leapmuxv1.RenameSectionRequest{
		SectionId: sec.GetId(),
		Name:      name,
	}, &resp); err != nil {
		return nil, err
	}
	row := sectionRowOf(sec, sectionWorkspaceCount(list.GetItems(), sec.GetId()))
	row.Name = name
	return row, nil
}

func runSectionDelete(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	list, err := s.listSections()
	if err != nil {
		return nil, err
	}
	sec, err := resolveSection(list.GetSections(), inv.Arg("section"))
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.DeleteSectionResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.SectionServiceDeleteSectionProcedure, &leapmuxv1.DeleteSectionRequest{
		SectionId: sec.GetId(),
	}, &resp); err != nil {
		return nil, err
	}
	return struct {
		ID      string `json:"id"`
		Name    string `json:"name,omitempty"`
		Deleted bool   `json:"deleted"`
	}{ID: sec.GetId(), Name: sec.GetName(), Deleted: true}, nil
}

func runSectionMove(inv *cli.Invocation) (any, error) {
	first := boolFlag(inv, "first")
	last := boolFlag(inv, "last")
	beforeRaw := strings.TrimSpace(stringFlag(inv, "before"))
	afterRaw := strings.TrimSpace(stringFlag(inv, "after"))
	placeCount := 0
	for _, set := range []bool{first, last, inv.Changed("before"), inv.Changed("after")} {
		if set {
			placeCount++
		}
	}
	if placeCount > 1 {
		return nil, cli.Usagef("--first, --last, --before, and --after exclude each other")
	}
	if !inv.Changed("sidebar") && placeCount == 0 {
		return nil, cli.Usagef("give --sidebar, --first, --last, --before, or --after")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	list, err := s.listSections()
	if err != nil {
		return nil, err
	}
	sec, err := resolveSection(list.GetSections(), inv.Arg("section"))
	if err != nil {
		return nil, err
	}
	sidebar := sec.GetSidebar()
	if inv.Changed("sidebar") {
		sidebar, err = parseSidebarFlag(stringFlag(inv, "sidebar"))
		if err != nil {
			return nil, err
		}
	}
	peers := make([]rankedItem, 0, len(list.GetSections()))
	for _, other := range list.GetSections() {
		if other.GetSidebar() == sidebar && other.GetId() != sec.GetId() {
			peers = append(peers, rankedItem{ID: other.GetId(), Position: other.GetPosition()})
		}
	}
	var refID string
	if inv.Changed("before") {
		ref, err := resolveSection(list.GetSections(), beforeRaw)
		if err != nil {
			return nil, err
		}
		refID = ref.GetId()
	} else if inv.Changed("after") {
		ref, err := resolveSection(list.GetSections(), afterRaw)
		if err != nil {
			return nil, err
		}
		refID = ref.GetId()
	}
	if sidebar != sec.GetSidebar() && !first && !last && !inv.Changed("before") && !inv.Changed("after") {
		last = true
	}
	pos, err := computeLexoRankPosition(peers, first, last, inv.Changed("before"), inv.Changed("after"), refID, sec.GetPosition())
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.MoveSectionResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.SectionServiceMoveSectionProcedure, &leapmuxv1.MoveSectionRequest{
		SectionId: sec.GetId(),
		Sidebar:   sidebar,
		Position:  pos,
	}, &resp); err != nil {
		return nil, err
	}
	row := sectionRowOf(sec, sectionWorkspaceCount(list.GetItems(), sec.GetId()))
	row.Sidebar = enumToken(sidebar)
	row.Position = pos
	return row, nil
}

type rankedItem struct {
	ID       string
	Position string
}

// computeLexoRankPosition calculates the destination LexoRank for an ordered
// list of peers given mutually exclusive first/last/before/after selectors.
func computeLexoRankPosition(peers []rankedItem, first, last, before, after bool, refID, fallback string) (string, error) {
	slices.SortFunc(peers, func(a, b rankedItem) int {
		if a.Position != b.Position {
			return strings.Compare(a.Position, b.Position)
		}
		return strings.Compare(a.ID, b.ID)
	})
	switch {
	case first:
		if len(peers) == 0 {
			return lexorank.First(), nil
		}
		return lexorank.Mid("", peers[0].Position), nil
	case last:
		if len(peers) == 0 {
			return lexorank.First(), nil
		}
		return lexorank.After(peers[len(peers)-1].Position), nil
	case before:
		idx := slices.IndexFunc(peers, func(r rankedItem) bool { return r.ID == refID })
		if idx < 0 {
			return "", cli.Newf(cli.CodeNotFound, "reference item %s is not in the destination container", refID)
		}
		prev := ""
		if idx > 0 {
			prev = peers[idx-1].Position
		}
		return lexorank.Mid(prev, peers[idx].Position), nil
	case after:
		idx := slices.IndexFunc(peers, func(r rankedItem) bool { return r.ID == refID })
		if idx < 0 {
			return "", cli.Newf(cli.CodeNotFound, "reference item %s is not in the destination container", refID)
		}
		next := ""
		if idx+1 < len(peers) {
			next = peers[idx+1].Position
		}
		return lexorank.Mid(peers[idx].Position, next), nil
	default:
		return fallback, nil
	}
}

func completeSections(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	if spec.Name != "section" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	var resp leapmuxv1.ListSectionsResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.SectionServiceListSectionsProcedure, &leapmuxv1.ListSectionsRequest{}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	lower := strings.ToLower(toComplete)
	for _, sec := range resp.GetSections() {
		name := sec.GetName()
		if lower == "" || strings.HasPrefix(strings.ToLower(name), lower) || strings.HasPrefix(sec.GetId(), toComplete) {
			out = append(out, name+"\t"+cli.ShortID(sec.GetId())+" ("+enumToken(sec.GetSidebar())+")")
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
