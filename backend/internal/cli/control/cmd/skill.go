package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/skills"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type skillTargetMode int

const (
	skillTargetModeInspect skillTargetMode = iota
	skillTargetModeInstall
	skillTargetModeUninstall
)

func newSkillCommand() *cobra.Command {
	showCmd := cli.NewLeaf(cli.Leaf{
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
	})
	cli.DisallowInheritedFlags(showCmd, "hub")

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
				addSkillScopeFlags(fs, skillTargetModeInspect)
			},
			Run: runSkillList,
		}),
		showCmd,
		cli.NewLeaf(cli.Leaf{
			Use:   "status",
			Short: "Show installed vs. bundled skill state and Worker auto-sync status",
			Examples: []cli.Example{
				{Command: "leapmux skill status"},
				{Command: "leapmux skill status --worker name:build-box --json"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addSkillScopeFlags(fs, skillTargetModeInspect)
			},
			Run: runSkillStatus,
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
location (dir:<path> applies to local installation only). Pass --worker <worker>
to enable and sync skills on a remote Worker over E2EE (with optional
--auto-sync to keep them updated on Worker startup).

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
				addSkillScopeFlags(fs, skillTargetModeInstall)
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
				addSkillScopeFlags(fs, skillTargetModeUninstall)
				fs.Bool("all", false, "select every bundled skill")
				fs.Bool("force", false, "also remove locally modified or unmanaged skill files")
				fs.Bool("no-auto-sync", false, "with --worker, disable automatic skill refresh on Worker startup")
			},
			Complete: completeSkillNames,
			Run:      runSkillUninstall,
		}),
	), "skills")
}

func addSkillScopeFlags(fs *pflag.FlagSet, mode skillTargetMode) {
	switch mode {
	case skillTargetModeInspect:
		fs.String("target", string(skills.TargetAgents), `local target directory: agents (~/.agents/skills), claude, codex, cursor, gemini, or dir:<path>`)
	case skillTargetModeInstall:
		fs.StringArray("target", nil, `target directory: agents (default, ~/.agents/skills), claude, codex, cursor, gemini, or dir:<path> (local only); repeat for multiple`)
	case skillTargetModeUninstall:
		fs.StringArray("target", nil, `local target directory: agents (default, ~/.agents/skills), claude, codex, cursor, gemini, or dir:<path>; repeat for multiple (omit with --worker)`)
	}
	fs.String("scope", "user", "local install scope: user (home directory) or project (current working directory)")
	cli.SetEnum(fs, "scope", "user", "project")
	// Register --worker without an environment default so commands inside a
	// LeapMux tab default to the local machine unless the caller passes --worker.
	fs.String(workerFlag, "", `the Worker to inspect or manage skills on: an id, a unique id prefix, "name:<name>" or "self"; omit for local machine`)
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

type skillTargetStatusRow struct {
	Target           string `json:"target"`
	State            string `json:"state"`
	InstalledVersion string `json:"installed_version,omitempty"`
	Path             string `json:"path,omitempty"`
}

type skillRow struct {
	Name             string                 `json:"name" table:"SKILL"`
	State            string                 `json:"state" table:"STATE,state"`
	Enabled          *bool                  `json:"enabled,omitempty" table:"ENABLED"`
	InstalledVersion string                 `json:"installed_version,omitempty" table:"INSTALLED"`
	BundledVersion   string                 `json:"bundled_version" table:"BUNDLED"`
	Description      string                 `json:"description" table:"DESCRIPTION"`
	Path             string                 `json:"path,omitempty"`
	References       []string               `json:"references,omitempty"`
	Targets          []skillTargetStatusRow `json:"targets,omitempty"`
}

type skillRows []skillRow

func (rows skillRows) DefaultHiddenColumns() []string {
	hasEnabled := false
	hasInstalled := false
	for _, row := range rows {
		if row.Enabled != nil {
			hasEnabled = true
		}
		if row.InstalledVersion != "" {
			hasInstalled = true
		}
	}
	var hidden []string
	if !hasEnabled {
		hidden = append(hidden, "enabled")
	}
	if !hasInstalled {
		hidden = append(hidden, "installed_version")
	}
	return hidden
}

type skillListResult struct {
	Scope            string     `json:"scope"`
	WorkerID         string     `json:"worker_id,omitempty"`
	AutoSync         *bool      `json:"auto_sync,omitempty"`
	Targets          []string   `json:"targets,omitempty"`
	PrimarySkillsDir string     `json:"primary_skills_dir,omitempty"`
	Skills           []skillRow `json:"skills"`
}

func (r skillListResult) DefaultHiddenColumns() []string {
	return skillRows(r.Skills).DefaultHiddenColumns()
}

func (r skillListResult) RenderTextWithOptions(w io.Writer, opts cli.TableOptions) error {
	return cli.WriteTableWithOptions(w, skillRows(r.Skills), opts)
}

func (r skillListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Skills))
	for _, s := range r.Skills {
		out = append(out, s.Name)
	}
	return out
}

type skillStatusResult struct {
	skillListResult
}

func (r skillStatusResult) RenderTextWithOptions(w io.Writer, opts cli.TableOptions) error {
	if err := writeSkillSummary(w, r.Scope, r.WorkerID, r.AutoSync, r.Targets, r.PrimarySkillsDir); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	return cli.WriteTableWithOptions(w, skillRows(r.Skills), opts)
}

func writeSkillSummary(w io.Writer, scope, workerID string, autoSync *bool, targets []string, primaryDir string) error {
	if scope != "" {
		if _, err := fmt.Fprintf(w, "Scope: %s\n", cli.SanitizeSingleLine(scope)); err != nil {
			return err
		}
	}
	if workerID != "" {
		if _, err := fmt.Fprintf(w, "Worker: %s\n", cli.SanitizeSingleLine(workerID)); err != nil {
			return err
		}
	}
	if autoSync != nil {
		state := "disabled"
		if *autoSync {
			state = "enabled"
		}
		if _, err := fmt.Fprintf(w, "Auto-sync: %s\n", state); err != nil {
			return err
		}
	}
	if len(targets) > 0 {
		clean := make([]string, len(targets))
		for i, t := range targets {
			clean[i] = cli.SanitizeSingleLine(t)
		}
		if _, err := fmt.Fprintf(w, "Targets: %s\n", strings.Join(clean, ", ")); err != nil {
			return err
		}
	}
	if primaryDir != "" {
		if _, err := fmt.Fprintf(w, "Primary dir: %s\n", cli.SanitizeSingleLine(primaryDir)); err != nil {
			return err
		}
	}
	return nil
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
	Target string `json:"target,omitempty" table:"TARGET"`
	Action string `json:"action" table:"ACTION"`
	Path   string `json:"path,omitempty" table:"PATH"`
}

type skillMutationRows []skillMutationRow

func (rows skillMutationRows) DefaultHiddenColumns() []string {
	seenTarget := ""
	multiTarget := false
	for _, row := range rows {
		if row.Target == "" {
			continue
		}
		if seenTarget == "" {
			seenTarget = row.Target
		} else if row.Target != seenTarget {
			multiTarget = true
			break
		}
	}
	if !multiTarget {
		return []string{"target"}
	}
	return nil
}

type skillMutationResult struct {
	Scope    string             `json:"scope"`
	WorkerID string             `json:"worker_id,omitempty"`
	AutoSync *bool              `json:"auto_sync,omitempty"`
	Targets  []string           `json:"targets,omitempty"`
	Results  []skillMutationRow `json:"results"`
}

func (r skillMutationResult) DefaultHiddenColumns() []string {
	return skillMutationRows(r.Results).DefaultHiddenColumns()
}

func (r skillMutationResult) RenderTextWithOptions(w io.Writer, opts cli.TableOptions) error {
	if len(r.Results) == 0 {
		return writeSkillSummary(w, r.Scope, r.WorkerID, r.AutoSync, r.Targets, "")
	}
	return cli.WriteTableWithOptions(w, skillMutationRows(r.Results), opts)
}

func (r skillMutationResult) QuietLines() []string {
	out := make([]string, 0, len(r.Results))
	for _, row := range r.Results {
		if !slices.Contains(out, row.Name) {
			out = append(out, row.Name)
		}
	}
	return out
}

func resolveLocalSkillBaseDir(scope string) (baseDir string, normScope string, err error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", cli.Wrap(cli.CodeInternal, err, "resolve home directory")
		}
		return home, "user", nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", cli.Wrap(cli.CodeInternal, err, "resolve working directory")
		}
		return cwd, "project", nil
	default:
		return "", "", cli.Usagef("unknown --scope %q (use user or project)", scope)
	}
}

func resolveLocalSkillTargets(inv *cli.Invocation, targets []string, forceVerb string) (scope string, dirs []string, labels []string, err error) {
	if len(targets) == 0 {
		targets = []string{string(skills.TargetAgents)}
	}
	hasCustom := false
	hasStandard := false
	for _, t := range targets {
		if skills.IsCustomTarget(t) {
			hasCustom = true
		} else {
			hasStandard = true
		}
	}
	if inv.Changed("scope") && hasCustom {
		return "", nil, nil, cli.Usagef("--scope cannot be combined with dir:<path> targets")
	}

	var baseDir string
	if hasStandard {
		baseDir, scope, err = resolveLocalSkillBaseDir(stringFlag(inv, "scope"))
		if err != nil {
			return "", nil, nil, err
		}
	} else {
		scope = "custom"
	}

	dirs, labels, err = skills.ResolveLocalTargetDirs(baseDir, targets)
	if err != nil {
		return "", nil, nil, mapSkillDiskError(err, forceVerb)
	}
	return scope, dirs, labels, nil
}

func mapSkillDiskError(err error, forceVerb string) error {
	switch {
	case errors.Is(err, skills.ErrSkillModified), errors.Is(err, skills.ErrSkillUnmanaged):
		return cli.Newf(cli.CodeConflict, "%v", err).WithHint("pass --force to %s", forceVerb)
	case errors.Is(err, skills.ErrUnsafeSymlink):
		return cli.Newf(cli.CodeConflict, "%v", err)
	case errors.Is(err, skills.ErrInvalidTarget):
		return cli.Usagef("%v", err)
	case errors.Is(err, skills.ErrUnknownSkill):
		return cli.Newf(cli.CodeNotFound, "%v", err)
	default:
		return cli.Wrap(cli.CodeInternal, err, "manage local skills")
	}
}

func mapWorkerSkillRPCError(err error, forceVerb string) error {
	var cErr *connect.Error
	if errors.As(err, &cErr) && cErr.Code() == connect.CodeAborted {
		msg := cErr.Message()
		if msg == "" {
			msg = err.Error()
		}
		cliErr := cli.Newf(cli.CodeConflict, "%s", msg).WithCause(err)
		if forceVerb != "" && !strings.Contains(strings.ToLower(msg), "symlink") {
			cliErr = cliErr.WithHint("pass --force to %s", forceVerb)
		}
		return cliErr
	}
	return err
}

func runSkillShow(inv *cli.Invocation) (any, error) {
	listRefs := boolFlag(inv, "references")
	refChanged := inv.Changed("reference")
	refName := strings.TrimSpace(stringFlag(inv, "reference"))
	if listRefs && refChanged {
		return nil, cli.Usagef("--references and --reference exclude each other")
	}
	if refChanged && refName == "" {
		return nil, cli.Usagef("--reference must not be empty")
	}
	name := strings.TrimSpace(inv.Arg("skill"))
	s, ok := skills.Lookup(name)
	if !ok {
		return nil, cli.Newf(cli.CodeNotFound, "unknown skill %q", name).
			WithHint("run `leapmux skill list` to see bundled skills")
	}
	if listRefs {
		return skillReferencesResult{Name: s.Name, References: ptrconv.NonNil(s.References)}, nil
	}
	if refChanged {
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
		Content:     s.Content(),
	}, nil
}

func fetchSkillList(inv *cli.Invocation) (skillListResult, error) {
	if inv.Changed(workerFlag) {
		if inv.Changed("scope") {
			return skillListResult{}, cli.Usagef("--scope applies only to local skill inspection (omit it with --worker)")
		}
		if inv.Changed("target") {
			return skillListResult{}, cli.Usagef("--target applies only to local skill inspection (omit it with --worker)")
		}
		s, err := openSession(inv)
		if err != nil {
			return skillListResult{}, err
		}
		defer s.Close()
		workerID, err := s.workerScope()
		if err != nil {
			return skillListResult{}, err
		}
		if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
			return skillListResult{}, err
		}
		var resp leapmuxv1.GetWorkerSkillsResponse
		if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure, &leapmuxv1.GetWorkerSkillsRequest{}, &resp); err != nil {
			return skillListResult{}, err
		}
		return skillListResultFromProto(workerID, &resp), nil
	}
	if inv.Changed("hub") {
		return skillListResult{}, cli.Usagef("--hub requires --worker")
	}
	target := stringFlag(inv, "target")
	scope, dirs, normTargets, err := resolveLocalSkillTargets(inv, []string{target}, "")
	if err != nil {
		return skillListResult{}, err
	}
	dir := dirs[0]
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
		Targets:          normTargets,
		PrimarySkillsDir: dir,
		Skills:           rows,
	}, nil
}

func runSkillList(inv *cli.Invocation) (any, error) {
	return fetchSkillList(inv)
}

func runSkillStatus(inv *cli.Invocation) (any, error) {
	res, err := fetchSkillList(inv)
	if err != nil {
		return nil, err
	}
	return skillStatusResult{skillListResult: res}, nil
}

func skillListResultFromProto(workerID string, resp *leapmuxv1.GetWorkerSkillsResponse) skillListResult {
	rows := make([]skillRow, 0, len(resp.GetSkills()))
	for _, s := range resp.GetSkills() {
		var targetRows []skillTargetStatusRow
		if protoTargets := s.GetTargets(); len(protoTargets) > 0 {
			targetRows = make([]skillTargetStatusRow, 0, len(protoTargets))
			for _, t := range protoTargets {
				targetRows = append(targetRows, skillTargetStatusRow{
					Target:           t.GetTarget(),
					State:            t.GetState(),
					InstalledVersion: t.GetInstalledVersion(),
					Path:             t.GetPath(),
				})
			}
		}
		rows = append(rows, skillRow{
			Name:             s.GetName(),
			State:            s.GetState(),
			Enabled:          ptrconv.Ptr(s.GetEnabled()),
			InstalledVersion: s.GetInstalledVersion(),
			BundledVersion:   s.GetBundledVersion(),
			Description:      s.GetDescription(),
			Path:             s.GetPath(),
			References:       slices.Clone(s.GetReferences()),
			Targets:          targetRows,
		})
	}
	return skillListResult{
		Scope:            "worker",
		WorkerID:         workerID,
		AutoSync:         ptrconv.Ptr(resp.GetAutoSync()),
		Targets:          resp.GetTargets(),
		PrimarySkillsDir: resp.GetPrimarySkillsDir(),
		Skills:           rows,
	}
}

func skillMutationResultFromProto(workerID string, resp *leapmuxv1.UpdateWorkerSkillsResponse) skillMutationResult {
	outcomes := resp.GetOutcomes()
	rows := make([]skillMutationRow, 0, len(outcomes))
	for _, o := range outcomes {
		rows = append(rows, skillMutationRow{
			Name:   o.GetName(),
			Target: o.GetTarget(),
			Action: o.GetAction(),
			Path:   o.GetPath(),
		})
	}
	var autoSync *bool
	var targets []string
	if st := resp.GetState(); st != nil {
		autoSync = ptrconv.Ptr(st.GetAutoSync())
		targets = st.GetTargets()
	}
	return skillMutationResult{
		Scope:    "worker",
		WorkerID: workerID,
		AutoSync: autoSync,
		Targets:  targets,
		Results:  rows,
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
	canonical := make([]string, 0, len(args))
	for _, name := range args {
		s, ok := skills.Lookup(name)
		if !ok {
			return nil, cli.Newf(cli.CodeNotFound, "unknown skill %q", name).
				WithHint("available skills: %s", strings.Join(skills.Names(), ", "))
		}
		if !slices.Contains(canonical, s.Name) {
			canonical = append(canonical, s.Name)
		}
	}
	return canonical, nil
}

func parseRemoteSkillNames(inv *cli.Invocation) (all bool, names []string, err error) {
	all = boolFlag(inv, "all")
	args := inv.Variadic("skill")
	if all && len(args) > 0 {
		return false, nil, cli.Usagef("pass skill names or --all, not both")
	}
	if all || len(args) == 0 {
		return all, nil, nil
	}
	names = make([]string, 0, len(args))
	for _, raw := range args {
		name := strings.TrimSpace(raw)
		if name == "" {
			return false, nil, cli.Usagef("skill name must not be empty")
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return false, names, nil
}

func runLocalSkillMutation(
	inv *cli.Invocation,
	names []string,
	targets []string,
	force bool,
	forceVerb string,
	apply func(dir string, names []string, opts skills.Options) ([]skills.InstallOutcome, error),
) (skillMutationResult, error) {
	scope, dirs, normTargets, err := resolveLocalSkillTargets(inv, targets, forceVerb)
	if err != nil {
		return skillMutationResult{}, err
	}
	rows := make([]skillMutationRow, 0, len(dirs)*len(names))
	for i, dir := range dirs {
		outcomes, err := apply(dir, names, skills.Options{Force: force})
		if err != nil {
			return skillMutationResult{}, mapSkillDiskError(err, forceVerb)
		}
		for _, o := range outcomes {
			rows = append(rows, skillMutationRow{
				Name:   o.Name,
				Target: normTargets[i],
				Action: string(o.Action),
				Path:   o.Path,
			})
		}
	}
	return skillMutationResult{Scope: scope, Targets: normTargets, Results: rows}, nil
}

func mutateWorkerSkills(
	inv *cli.Invocation,
	req *leapmuxv1.UpdateWorkerSkillsRequest,
	forceVerb string,
) (skillMutationResult, error) {
	s, err := openSession(inv)
	if err != nil {
		return skillMutationResult{}, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return skillMutationResult{}, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return skillMutationResult{}, err
	}
	var resp leapmuxv1.UpdateWorkerSkillsResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, req, &resp); err != nil {
		return skillMutationResult{}, mapWorkerSkillRPCError(err, forceVerb)
	}
	return skillMutationResultFromProto(workerID, &resp), nil
}

func runSkillInstall(inv *cli.Invocation) (any, error) {
	autoChanged := inv.Changed("auto-sync")
	noAutoChanged := inv.Changed("no-auto-sync")
	if autoChanged && noAutoChanged {
		return nil, cli.Usagef("--auto-sync and --no-auto-sync exclude each other")
	}
	workerMode := inv.Changed(workerFlag)
	if (autoChanged || noAutoChanged) && !workerMode {
		return nil, cli.Usagef("--auto-sync and --no-auto-sync require --worker")
	}
	if !workerMode && inv.Changed("hub") {
		return nil, cli.Usagef("--hub requires --worker")
	}

	var autoSyncPtr *bool
	if autoChanged {
		autoSyncPtr = ptrconv.Ptr(boolFlag(inv, "auto-sync"))
	} else if noAutoChanged {
		autoSyncPtr = ptrconv.Ptr(!boolFlag(inv, "no-auto-sync"))
	}
	force := boolFlag(inv, "force")

	if workerMode {
		if inv.Changed("scope") {
			return nil, cli.Usagef("--scope applies only to local installation (omit it with --worker)")
		}
		all, rawNames, err := parseRemoteSkillNames(inv)
		if err != nil {
			return nil, err
		}
		targetChanged := inv.Changed("target")
		if !all && len(rawNames) == 0 && autoSyncPtr == nil && !targetChanged {
			return nil, cli.Usagef("specify one or more skill names, or pass --all").
				WithHint("run `leapmux skill list` to see bundled skills")
		}
		var addTargets []string
		if targetChanged {
			normTargets, tErr := skills.NormalizeTargets(inv.StringArray("target"))
			if tErr != nil {
				return nil, cli.Usagef("%v", tErr)
			}
			addTargets = make([]string, len(normTargets))
			for i, t := range normTargets {
				addTargets[i] = string(t)
			}
		}

		req := &leapmuxv1.UpdateWorkerSkillsRequest{
			AutoSync:        autoSyncPtr,
			AddAllSkills:    all,
			AddTargets:      addTargets,
			SyncNow:         all || len(rawNames) > 0 || targetChanged,
			Force:           force,
			StrictConflicts: !force,
		}
		if !all {
			req.AddSkills = rawNames
		}
		return mutateWorkerSkills(inv, req, "overwrite")
	}

	names, err := selectedSkillNames(inv)
	if err != nil {
		return nil, err
	}
	targets := inv.StringArray("target")
	if len(targets) == 0 {
		targets = []string{string(skills.TargetAgents)}
	}
	return runLocalSkillMutation(inv, names, targets, force, "overwrite", skills.Install)
}

func runSkillUninstall(inv *cli.Invocation) (any, error) {
	workerMode := inv.Changed(workerFlag)
	noAutoChanged := inv.Changed("no-auto-sync")
	if noAutoChanged && !workerMode {
		return nil, cli.Usagef("--no-auto-sync requires --worker")
	}
	if !workerMode && inv.Changed("hub") {
		return nil, cli.Usagef("--hub requires --worker")
	}
	var autoSyncPtr *bool
	if noAutoChanged {
		autoSyncPtr = ptrconv.Ptr(!boolFlag(inv, "no-auto-sync"))
	}
	force := boolFlag(inv, "force")

	if workerMode {
		if inv.Changed("scope") || inv.Changed("target") {
			return nil, cli.Usagef("--scope and --target apply only to local uninstallation (omit them with --worker)")
		}
		all, rawNames, err := parseRemoteSkillNames(inv)
		if err != nil {
			return nil, err
		}
		if !all && len(rawNames) == 0 {
			return nil, cli.Usagef("specify one or more skill names, or pass --all").
				WithHint("run `leapmux skill list` to see bundled skills")
		}
		req := &leapmuxv1.UpdateWorkerSkillsRequest{
			RemoveAllSkills: all,
			AutoSync:        autoSyncPtr,
			Force:           force,
			StrictConflicts: !force,
		}
		if !all {
			req.RemoveSkills = rawNames
		}
		return mutateWorkerSkills(inv, req, "remove")
	}

	names, err := selectedSkillNames(inv)
	if err != nil {
		return nil, err
	}
	targets := inv.StringArray("target")
	if len(targets) == 0 {
		targets = []string{string(skills.TargetAgents)}
	}
	return runLocalSkillMutation(inv, names, targets, force, "remove", skills.Uninstall)
}
