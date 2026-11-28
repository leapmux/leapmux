package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
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
			FlagComplete: map[string]cobra.CompletionFunc{
				"before": completeSectionPositionFlag,
				"after":  completeSectionPositionFlag,
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
	if n, ok := parseEnumToken(leapmuxv1.Sidebar_SIDEBAR_UNSPECIFIED, strings.TrimSpace(raw)); ok {
		return leapmuxv1.Sidebar(n), nil
	}
	return leapmuxv1.Sidebar_SIDEBAR_UNSPECIFIED, cli.Usagef(
		"--sidebar must be one of %s; got %q",
		strings.Join(enumTokens(leapmuxv1.Sidebar_SIDEBAR_UNSPECIFIED), ", "),
		raw,
	)
}

// resolveSection finds one section by exact ID, unique ID prefix (>= 4 chars),
// "id:<id>" (exact ID or unique ID prefix, never matching names), or
// case-insensitive name.
func resolveSection(sections []*leapmuxv1.Section, raw string) (*leapmuxv1.Section, error) {
	q := strings.TrimSpace(raw)
	if q == "" {
		return nil, cli.Usagef("section target is empty")
	}
	if rest, ok := strings.CutPrefix(q, "id:"); ok {
		want := strings.TrimSpace(rest)
		if want == "" {
			return nil, cli.Usagef(`"id:" needs a section id after the colon`)
		}
		match, ambiguous, ok := lookupByIDPrefixMin(sections, want, resolve.MinPrefixLength, (*leapmuxv1.Section).GetId)
		if ok {
			return match, nil
		}
		if len(ambiguous) > 1 {
			return nil, ambiguousSectionsError(want, ambiguous)
		}
		return nil, cli.Newf(cli.CodeNotFound, "no section with id %q", want).
			WithHint(`list sections with "leapmux section list"`)
	}
	if rest, ok := strings.CutPrefix(q, "name:"); ok {
		want := strings.TrimSpace(rest)
		if want == "" {
			return nil, cli.Usagef(`"name:" needs a section name after the colon`)
		}
		return matchSectionByName(sections, want)
	}
	if rest, ok := strings.CutPrefix(q, "title:"); ok {
		want := strings.TrimSpace(rest)
		if want == "" {
			return nil, cli.Usagef(`"title:" needs a section name after the colon`)
		}
		return matchSectionByName(sections, want)
	}
	match, ambiguous, ok := lookupByIDPrefixMin(sections, q, resolve.MinPrefixLength, func(sec *leapmuxv1.Section) string {
		return sec.GetId()
	})
	if ok {
		return match, nil
	}
	if len(ambiguous) > 1 {
		return nil, ambiguousSectionsError(q, ambiguous)
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
	return s.listSectionsWithPrefix("")
}

func (s *session) listSectionsWithPrefix(idPrefix string) (*leapmuxv1.ListSectionsResponse, error) {
	var resp leapmuxv1.ListSectionsResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.SectionServiceListSectionsProcedure, &leapmuxv1.ListSectionsRequest{
		IdPrefix: idPrefix,
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// resolveSectionFromHub resolves raw against the Hub using a server-side
// id_prefix query for "id:" selectors and bare ID-prefix candidates, falling
// back to a full ListSections call only when name matching is needed.
func (s *session) resolveSectionFromHub(raw string) (*leapmuxv1.ListSectionsResponse, *leapmuxv1.Section, error) {
	q := strings.TrimSpace(raw)
	if q == "" {
		return nil, nil, cli.Usagef("section target is empty")
	}
	if rest, ok := strings.CutPrefix(q, "id:"); ok {
		want := strings.TrimSpace(rest)
		if want == "" {
			return nil, nil, cli.Usagef(`"id:" needs a section id after the colon`)
		}
		list, err := s.listSectionsWithPrefix(want)
		if err != nil {
			return nil, nil, err
		}
		match, ambiguous, ok := lookupByIDPrefixMin(list.GetSections(), want, resolve.MinPrefixLength, (*leapmuxv1.Section).GetId)
		if ok {
			return list, match, nil
		}
		if len(ambiguous) > 1 {
			return nil, nil, ambiguousSectionsError(want, ambiguous)
		}
		return nil, nil, cli.Newf(cli.CodeNotFound, "no section with id %q", want).
			WithHint(`list sections with "leapmux section list"`)
	}
	if rest, ok := strings.CutPrefix(q, "name:"); ok {
		want := strings.TrimSpace(rest)
		if want == "" {
			return nil, nil, cli.Usagef(`"name:" needs a section name after the colon`)
		}
		list, err := s.listSections()
		if err != nil {
			return nil, nil, err
		}
		sec, err := matchSectionByName(list.GetSections(), want)
		return list, sec, err
	}
	if rest, ok := strings.CutPrefix(q, "title:"); ok {
		want := strings.TrimSpace(rest)
		if want == "" {
			return nil, nil, cli.Usagef(`"title:" needs a section name after the colon`)
		}
		list, err := s.listSections()
		if err != nil {
			return nil, nil, err
		}
		sec, err := matchSectionByName(list.GetSections(), want)
		return list, sec, err
	}
	if len(q) >= resolve.MinPrefixLength && resolve.IsBareIDPrefixCandidate(q) {
		list, err := s.listSectionsWithPrefix(q)
		if err != nil {
			return nil, nil, err
		}
		match, ambiguous, ok := lookupByIDPrefixMin(list.GetSections(), q, resolve.MinPrefixLength, (*leapmuxv1.Section).GetId)
		if ok {
			return list, match, nil
		}
		if len(ambiguous) > 1 {
			return nil, nil, ambiguousSectionsError(q, ambiguous)
		}
	}
	list, err := s.listSections()
	if err != nil {
		return nil, nil, err
	}
	sec, err := resolveSection(list.GetSections(), q)
	return list, sec, err
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

// openSectionSession opens a CLI session and resolves the target section using
// server-side id_prefix queries when possible. It closes the session on error.
func openSectionSession(inv *cli.Invocation) (*session, *leapmuxv1.ListSectionsResponse, *leapmuxv1.Section, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, nil, nil, err
	}
	list, sec, err := s.resolveSectionFromHub(inv.Arg("section"))
	if err != nil {
		s.Close()
		return nil, nil, nil, err
	}
	return s, list, sec, nil
}

// openFullSectionSession opens a CLI session, loads every sidebar section (for
// commands such as `section move` that need peer sections in the target
// sidebar), and resolves the target section argument. It closes the session on
// error.
func openFullSectionSession(inv *cli.Invocation) (*session, *leapmuxv1.ListSectionsResponse, *leapmuxv1.Section, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, nil, nil, err
	}
	list, err := s.listSections()
	if err != nil {
		s.Close()
		return nil, nil, nil, err
	}
	sec, err := resolveSection(list.GetSections(), inv.Arg("section"))
	if err != nil {
		s.Close()
		return nil, nil, nil, err
	}
	return s, list, sec, nil
}

func runSectionRename(inv *cli.Invocation) (any, error) {
	name := strings.TrimSpace(inv.Arg("name"))
	if name == "" {
		return nil, cli.Usagef("section name is empty")
	}
	s, list, sec, err := openSectionSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
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
	s, _, sec, err := openSectionSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
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
	if inv.Changed("before") && beforeRaw == "" {
		return nil, cli.Usagef("--before must not be empty")
	}
	if inv.Changed("after") && afterRaw == "" {
		return nil, cli.Usagef("--after must not be empty")
	}
	hasPosition := first || last || beforeRaw != "" || afterRaw != ""
	spec, err := parsePositionSpec(first, last, beforeRaw, afterRaw)
	if err != nil {
		return nil, cli.Usagef("%s", err.Error())
	}
	if !inv.Changed("sidebar") && !hasPosition {
		return nil, cli.Usagef("give --sidebar, --first, --last, --before, or --after")
	}
	var explicitSidebar leapmuxv1.Sidebar
	if inv.Changed("sidebar") {
		explicitSidebar, err = parseSidebarFlag(stringFlag(inv, "sidebar"))
		if err != nil {
			return nil, err
		}
	}

	s, list, sec, err := openFullSectionSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	sidebar := sec.GetSidebar()
	if inv.Changed("sidebar") {
		sidebar = explicitSidebar
	}
	pos := sec.GetPosition()
	if hasPosition || sidebar != sec.GetSidebar() {
		if spec.refID != "" {
			ref, err := resolveSection(list.GetSections(), spec.refID)
			if err != nil {
				return nil, err
			}
			spec.refID = ref.GetId()
		}
		peers := make([]rankedItem, 0, len(list.GetSections()))
		for _, other := range list.GetSections() {
			if other.GetSidebar() == sidebar && other.GetId() != sec.GetId() {
				peers = append(peers, rankedItem{ID: other.GetId(), Position: other.GetPosition()})
			}
		}
		pos, err = computeRankedPosition(peers, sec.GetId(), spec, "section")
		if err != nil {
			return nil, err
		}
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

func completeSections(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	if spec.Name != "section" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext(cmd)
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
			out = append(out, formatCompletionCandidate(name, cli.ShortID(sec.GetId())+" ("+enumToken(sec.GetSidebar())+")"))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
