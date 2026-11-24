package cmd

import (
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/skills"
)

func newSkillCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("skill", "Inspect and install LeapMux agent skills locally or on a Worker",
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List bundled LeapMux agent skills and their install state",
			Examples: []cli.Example{
				{Command: "leapmux skill list"},
				{Comment: "Check installed skill state on a remote Worker.", Command: "leapmux skill list --worker name:build-box"},
				{Comment: "Inspect project-scoped skills in the current repository.", Command: "leapmux skill list --scope project"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addSkillScopeFlags(fs, false)
			},
			Run: runSkillList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "show <skill>",
			Short: "Print SKILL.md or companion reference documents for a bundled skill",
			Args:  map[string]string{"skill": "bundled skill name (see `leapmux skill list`)"},
			Examples: []cli.Example{
				{Command: "leapmux skill show leapmux"},
				{Command: "leapmux skill show leapmux-review"},
				{Comment: "List companion reference documents for a skill.", Command: "leapmux skill show leapmux-review --references"},
				{Comment: "Print one companion reference document.", Command: "leapmux skill show leapmux-review --reference review-schemas"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("references", false, "list companion reference names bundled with this skill")
				fs.String("reference", "", "print one companion reference document instead of SKILL.md")
			},
			Complete: completeSkillNames,
			Run:      runSkillShow,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "status",
			Short: "Show installed vs. bundled skill state and Worker auto-sync status",
			Examples: []cli.Example{
				{Command: "leapmux skill status"},
				{Command: "leapmux skill status --worker name:build-box --json"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addSkillScopeFlags(fs, false)
			},
			Run: runSkillList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "install [skill]...",
			Aliases: []string{"add"},
			Short:   "Install or refresh LeapMux agent skills locally or on a Worker",
			Mutates: true,
			Long: `Install bundled LeapMux SKILL.md playbooks (plus companion references and
Codex metadata) into standard coding-agent skill directories so agents (Claude
Code, Codex, OpenCode, Gemini CLI, Antigravity, Cursor, and others) can invoke
them directly.

By default, installs into ~/.agents/skills (universal user scope discovered by
Codex, OpenCode, Gemini CLI, Antigravity, and Cursor). Pass --target
claude|codex|cursor|gemini|dir:<path> or --scope project to target another
location. Pass --worker <worker> to enable and sync skills on a remote Worker
over E2EE (with optional --auto-sync to keep them updated on Worker startup).

Installing via the leapmux CLI always matches the installed binary offline;
installing via "npx skills add leapmux/leapmux" tracks the main branch.

Locally edited or unmanaged skill files are never overwritten unless --force
is passed.`,
			Args: map[string]string{"skill": "bundled skill names to install (or pass --all)"},
			Examples: []cli.Example{
				{Comment: "Install all bundled skills into ~/.agents/skills.", Command: "leapmux skill install --all"},
				{Comment: "Install specific skills into the current repo.", Command: "leapmux skill install leapmux leapmux-review --scope project"},
				{Comment: "Enable and auto-sync all skills on a remote Worker.", Command: "leapmux skill install --all --worker name:build-box --auto-sync"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addSkillScopeFlags(fs, true)
				fs.Bool("all", false, "select every bundled skill")
				fs.Bool("force", false, "overwrite locally modified or unmanaged skill files")
				fs.Bool("auto-sync", false, "with --worker, keep enabled skills refreshed automatically on Worker startup")
				fs.Bool("no-auto-sync", false, "with --worker, disable automatic skill refresh on Worker startup")
			},
			Complete: completeSkillNames,
			Run:      runSkillInstall,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "uninstall [skill]...",
			Aliases:     []string{"rm", "remove"},
			Short:       "Remove LeapMux-managed agent skills locally or from a Worker",
			Mutates:     true,
			Destructive: true,
			Args:        map[string]string{"skill": "bundled skill names to remove (or pass --all)"},
			Examples: []cli.Example{
				{Command: "leapmux skill uninstall leapmux-council"},
				{Command: "leapmux skill uninstall --all --worker name:build-box"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addSkillScopeFlags(fs, true)
				fs.Bool("all", false, "select every bundled skill")
				fs.Bool("force", false, "also remove locally modified or unmanaged skill files")
			},
			Complete: completeSkillNames,
			Run:      runSkillUninstall,
		}),
	), "skills")
}

func addSkillScopeFlags(fs *pflag.FlagSet, multiTarget bool) {
	if multiTarget {
		fs.StringArray("target", nil, `install target: agents (default, ~/.agents/skills), claude, codex, cursor, gemini, or dir:<path>; repeat for multiple`)
	} else {
		fs.String("target", skills.TargetAgents, `target directory: agents (~/.agents/skills), claude, codex, cursor, gemini, or dir:<path>`)
	}
	fs.String("scope", "user", "local install scope: user (home directory) or project (current working directory)")
	cli.SetEnum(fs, "scope", "user", "project")
	addWorkerFlag(fs)
}

func completeSkillNames(_ *cobra.Command, _ cli.ArgSpec, prefix string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, s := range skills.Catalog() {
		if strings.HasPrefix(s.Name, prefix) {
			out = append(out, s.Name+"\t"+s.Description)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

type skillRow struct {
	Name             string   `json:"name" table:"SKILL"`
	State            string   `json:"state" table:"STATE"`
	Enabled          *bool    `json:"enabled,omitempty" table:"ENABLED,optional"`
	InstalledVersion string   `json:"installed_version,omitempty" table:"INSTALLED,optional"`
	BundledVersion   string   `json:"bundled_version" table:"BUNDLED"`
	Description      string   `json:"description" table:"DESCRIPTION"`
	Path             string   `json:"path,omitempty"`
	References       []string `json:"references,omitempty"`
}

type skillListResult struct {
	Scope            string     `json:"scope"`
	WorkerID         string     `json:"worker_id,omitempty"`
	AutoSync         *bool      `json:"auto_sync,omitempty"`
	Targets          []string   `json:"targets,omitempty"`
	PrimarySkillsDir string     `json:"primary_skills_dir,omitempty"`
	Skills           []skillRow `json:"skills"`
}

func (r skillListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Skills))
	for _, s := range r.Skills {
		out = append(out, s.Name)
	}
	return out
}

type skillShowResult struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Reference   string   `json:"reference,omitempty"`
	References  []string `json:"references,omitempty"`
	Content     string   `json:"content"`
}

func (r skillShowResult) RenderText(w io.Writer) error {
	_, err := io.WriteString(w, r.Content)
	return err
}

func (r skillShowResult) QuietLines() []string {
	if r.Reference != "" {
		return []string{r.Reference}
	}
	return []string{r.Name}
}

type skillReferencesResult struct {
	Name       string   `json:"name"`
	References []string `json:"references"`
}

func (r skillReferencesResult) RenderText(w io.Writer) error {
	for _, ref := range r.References {
		if _, err := io.WriteString(w, ref+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func (r skillReferencesResult) QuietLines() []string { return r.References }

type skillMutationRow struct {
	Name   string `json:"name" table:"SKILL"`
	Action string `json:"action" table:"ACTION"`
	Path   string `json:"path,omitempty" table:"PATH"`
}

type skillMutationResult struct {
	Scope    string             `json:"scope"`
	WorkerID string             `json:"worker_id,omitempty"`
	AutoSync *bool              `json:"auto_sync,omitempty"`
	Targets  []string           `json:"targets,omitempty"`
	Results  []skillMutationRow `json:"results"`
}

func (r skillMutationResult) QuietLines() []string {
	out := make([]string, 0, len(r.Results))
	for _, row := range r.Results {
		out = append(out, row.Name)
	}
	return out
}

func resolveLocalSkillBaseDir(scope string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", cli.Wrap(cli.CodeInternal, err, "resolve home directory")
		}
		return home, nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", cli.Wrap(cli.CodeInternal, err, "resolve working directory")
		}
		return cwd, nil
	default:
		return "", cli.Usagef("unknown --scope %q (use user or project)", scope)
	}
}

func runSkillShow(inv *cli.Invocation) (any, error) {
	listRefs := boolFlag(inv, "references")
	refName := strings.TrimSpace(stringFlag(inv, "reference"))
	if listRefs && refName != "" {
		return nil, cli.Usagef("--references and --reference exclude each other")
	}
	name := strings.TrimSpace(inv.Arg("skill"))
	s, ok := skills.Lookup(name)
	if !ok {
		return nil, cli.Newf(cli.CodeNotFound, "unknown skill %q", name).
			WithHint("run `leapmux skill list` to see bundled skills")
	}
	if listRefs {
		refs := s.References
		if refs == nil {
			refs = []string{}
		}
		return skillReferencesResult{Name: s.Name, References: refs}, nil
	}
	if refName != "" {
		ref, found := s.Reference(refName)
		if !found {
			hint := "this skill bundles no companion references"
			if len(s.References) > 0 {
				hint = "available references: " + strings.Join(s.References, ", ")
			}
			return nil, cli.Newf(cli.CodeNotFound, "unknown reference %q for skill %q", refName, s.Name).
				WithHint("%s", hint)
		}
		return skillShowResult{
			Name:        s.Name,
			Description: s.Description,
			Reference:   ref.RelPath,
			References:  s.References,
			Content:     ref.Content,
		}, nil
	}
	return skillShowResult{
		Name:        s.Name,
		Description: s.Description,
		References:  s.References,
		Content:     s.Content,
	}, nil
}

func runSkillList(inv *cli.Invocation) (any, error) {
	if inv.Changed(workerFlag) {
		if inv.Changed("scope") {
			return nil, cli.Usagef("--scope applies only to local skill inspection (omit it with --worker)")
		}
		if inv.Changed("target") {
			return nil, cli.Usagef("--target applies only to local skill inspection (omit it with --worker)")
		}
		s, err := openSession(inv)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		workerID, err := s.workerScope()
		if err != nil {
			return nil, err
		}
		var resp leapmuxv1.GetWorkerSkillsResponse
		if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure, &leapmuxv1.GetWorkerSkillsRequest{}, &resp); err != nil {
			return nil, err
		}
		return skillListResultFromProto(workerID, &resp), nil
	}
	scope := stringFlag(inv, "scope")
	baseDir, err := resolveLocalSkillBaseDir(scope)
	if err != nil {
		return nil, err
	}
	target := stringFlag(inv, "target")
	dir, err := skills.ResolveTargetDir(baseDir, target)
	if err != nil {
		return nil, cli.Usagef("%v", err)
	}
	statuses := skills.InspectAll(dir)
	rows := make([]skillRow, 0, len(statuses))
	for _, st := range statuses {
		rows = append(rows, skillRow{
			Name:             st.Name,
			State:            string(st.State),
			InstalledVersion: st.InstalledVersion,
			BundledVersion:   st.BundledVersion,
			Description:      st.Description,
			Path:             st.Path,
			References:       st.References,
		})
	}
	return skillListResult{
		Scope:            scope,
		Targets:          []string{target},
		PrimarySkillsDir: dir,
		Skills:           rows,
	}, nil
}

func skillListResultFromProto(workerID string, resp *leapmuxv1.GetWorkerSkillsResponse) skillListResult {
	autoSync := resp.GetAutoSync()
	rows := make([]skillRow, 0, len(resp.GetSkills()))
	for _, s := range resp.GetSkills() {
		enabled := s.GetEnabled()
		var refs []string
		if bundled, ok := skills.Lookup(s.GetName()); ok {
			refs = bundled.References
		}
		rows = append(rows, skillRow{
			Name:             s.GetName(),
			State:            s.GetState(),
			Enabled:          &enabled,
			InstalledVersion: s.GetInstalledVersion(),
			BundledVersion:   s.GetBundledVersion(),
			Description:      s.GetDescription(),
			Path:             s.GetPath(),
			References:       refs,
		})
	}
	return skillListResult{
		Scope:            "worker",
		WorkerID:         workerID,
		AutoSync:         &autoSync,
		Targets:          resp.GetTargets(),
		PrimarySkillsDir: resp.GetPrimarySkillsDir(),
		Skills:           rows,
	}
}

func selectedSkillNames(inv *cli.Invocation) ([]string, error) {
	all := boolFlag(inv, "all")
	args := inv.Variadic("skill")
	if all && len(args) > 0 {
		return nil, cli.Usagef("pass skill names or --all, not both")
	}
	if all {
		return skills.Names(), nil
	}
	if len(args) == 0 {
		return nil, cli.Usagef("specify one or more skill names, or pass --all").
			WithHint("run `leapmux skill list` to see bundled skills")
	}
	for _, name := range args {
		if _, ok := skills.Lookup(name); !ok {
			return nil, cli.Newf(cli.CodeNotFound, "unknown skill %q", name).
				WithHint("available skills: %s", strings.Join(skills.Names(), ", "))
		}
	}
	return args, nil
}

func runSkillInstall(inv *cli.Invocation) (any, error) {
	autoOn := boolFlag(inv, "auto-sync")
	autoOff := boolFlag(inv, "no-auto-sync")
	if autoOn && autoOff {
		return nil, cli.Usagef("--auto-sync and --no-auto-sync exclude each other")
	}
	if (autoOn || autoOff) && !inv.Changed(workerFlag) {
		return nil, cli.Usagef("--auto-sync and --no-auto-sync require --worker")
	}
	names, err := selectedSkillNames(inv)
	if err != nil {
		return nil, err
	}
	targets := inv.StringArray("target")
	if len(targets) == 0 {
		targets = []string{skills.TargetAgents}
	}
	force := boolFlag(inv, "force")

	if inv.Changed(workerFlag) {
		if inv.Changed("scope") {
			return nil, cli.Usagef("--scope applies only to local installation (omit it with --worker)")
		}
		var normTargets []string
		if inv.Changed("target") {
			for _, raw := range targets {
				t, tErr := skills.NormalizeTarget(raw)
				if tErr != nil {
					return nil, cli.Usagef("%v", tErr)
				}
				normTargets = append(normTargets, t)
			}
		}
		s, err := openSession(inv)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		workerID, err := s.workerScope()
		if err != nil {
			return nil, err
		}
		var cur leapmuxv1.GetWorkerSkillsResponse
		if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure, &leapmuxv1.GetWorkerSkillsRequest{}, &cur); err != nil {
			return nil, err
		}
		enabled := make([]string, 0, len(cur.GetSkills()))
		for _, row := range cur.GetSkills() {
			if row.GetEnabled() {
				enabled = append(enabled, row.GetName())
			}
		}
		for _, n := range names {
			if !slices.Contains(enabled, n) {
				enabled = append(enabled, n)
			}
		}
		req := &leapmuxv1.UpdateWorkerSkillsRequest{
			UpdateEnabledSkills: true,
			EnabledSkills:       enabled,
			Targets:             normTargets,
			SyncNow:             true,
			Force:               force,
		}
		if autoOn {
			v := true
			req.AutoSync = &v
		} else if autoOff {
			v := false
			req.AutoSync = &v
		}
		var resp leapmuxv1.UpdateWorkerSkillsResponse
		if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, req, &resp); err != nil {
			return nil, err
		}
		return skillListResultFromProto(workerID, resp.GetState()), nil
	}

	scope := stringFlag(inv, "scope")
	baseDir, err := resolveLocalSkillBaseDir(scope)
	if err != nil {
		return nil, err
	}
	var rows []skillMutationRow
	for _, target := range targets {
		dir, err := skills.ResolveTargetDir(baseDir, target)
		if err != nil {
			return nil, cli.Usagef("%v", err)
		}
		outcomes, err := skills.Install(dir, names, skills.InstallOptions{Force: force})
		if err != nil {
			return nil, cli.Newf(cli.CodeConflict, "%v", err)
		}
		for _, o := range outcomes {
			rows = append(rows, skillMutationRow{Name: o.Name, Action: o.Action, Path: o.Path})
		}
	}
	return skillMutationResult{Scope: scope, Targets: targets, Results: rows}, nil
}

func runSkillUninstall(inv *cli.Invocation) (any, error) {
	names, err := selectedSkillNames(inv)
	if err != nil {
		return nil, err
	}
	targets := inv.StringArray("target")
	if len(targets) == 0 {
		targets = []string{skills.TargetAgents}
	}
	force := boolFlag(inv, "force")

	if inv.Changed(workerFlag) {
		if inv.Changed("scope") || inv.Changed("target") {
			return nil, cli.Usagef("--scope and --target apply only to local uninstallation (omit them with --worker)")
		}
		s, err := openSession(inv)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		workerID, err := s.workerScope()
		if err != nil {
			return nil, err
		}
		var cur leapmuxv1.GetWorkerSkillsResponse
		if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure, &leapmuxv1.GetWorkerSkillsRequest{}, &cur); err != nil {
			return nil, err
		}
		var remaining []string
		for _, row := range cur.GetSkills() {
			if row.GetEnabled() && !slices.Contains(names, row.GetName()) {
				remaining = append(remaining, row.GetName())
			}
		}
		var resp leapmuxv1.UpdateWorkerSkillsResponse
		if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
			UpdateEnabledSkills: true,
			EnabledSkills:       remaining,
			Force:               force,
		}, &resp); err != nil {
			return nil, err
		}
		return skillListResultFromProto(workerID, resp.GetState()), nil
	}

	scope := stringFlag(inv, "scope")
	baseDir, err := resolveLocalSkillBaseDir(scope)
	if err != nil {
		return nil, err
	}
	var rows []skillMutationRow
	for _, target := range targets {
		dir, err := skills.ResolveTargetDir(baseDir, target)
		if err != nil {
			return nil, cli.Usagef("%v", err)
		}
		outcomes, err := skills.Uninstall(dir, names, skills.InstallOptions{Force: force})
		if err != nil {
			return nil, cli.Newf(cli.CodeConflict, "%v", err)
		}
		for _, o := range outcomes {
			rows = append(rows, skillMutationRow{Name: o.Name, Action: o.Action, Path: o.Path})
		}
	}
	return skillMutationResult{Scope: scope, Targets: targets, Results: rows}, nil
}
