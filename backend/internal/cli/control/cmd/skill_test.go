package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/skills"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestSkillCLI_LocalLifecycleAndDriftGuard(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "custom-skills")
	targetFlag := "dir:" + targetDir

	// List shows all bundled skills as not_installed and reports custom scope for dir:<path>.
	o := runControl(t, "skill", "list", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	var listEnv struct {
		Data skillListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &listEnv))
	assert.Equal(t, "custom", listEnv.Data.Scope)
	assert.Equal(t, []string{targetFlag}, listEnv.Data.Targets)
	require.Len(t, listEnv.Data.Skills, len(skills.Catalog()))
	assert.Equal(t, "not_installed", listEnv.Data.Skills[0].State)

	// Show prints the raw SKILL.md document.
	o = runControlText(t, "skill", "show", "leapmux-review")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "name: leapmux-review")
	assert.Contains(t, o.Stdout, "--output-schema")

	// Show --references lists bundled companion references.
	o = runControlText(t, "skill", "show", "leapmux-review", "--references")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Equal(t, "review-schemas\n", o.Stdout)

	// Show --reference prints the companion Markdown document.
	o = runControlText(t, "skill", "show", "leapmux-review", "--reference", "review-schemas")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "Code Diff Review Schema")

	// Unknown reference returns CodeNotFound with hint.
	o = runControl(t, "skill", "show", "leapmux-review", "--reference", "missing-ref")
	assert.Equal(t, cli.ExitNotFound, o.Code)
	assert.Equal(t, cli.CodeNotFound, o.Failure(t).Code)
	assert.Contains(t, o.Stdout, "review-schemas")

	// Mutual exclusion of --references and --reference.
	o = runControl(t, "skill", "show", "leapmux-review", "--references", "--reference", "review-schemas")
	assert.Equal(t, cli.ExitUsage, o.Code)

	// Install all skills via the plural alias `skills`.
	o = runControl(t, "skills", "install", "--all", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	var mutEnv struct {
		Data skillMutationResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &mutEnv))
	assert.Equal(t, "custom", mutEnv.Data.Scope)
	for _, s := range skills.Catalog() {
		assert.FileExists(t, filepath.Join(targetDir, s.Name, "SKILL.md"))
		assert.FileExists(t, filepath.Join(targetDir, s.Name, "agents", "openai.yaml"))
		for _, ref := range s.References {
			assert.FileExists(t, filepath.Join(targetDir, s.Name, "references", ref+".md"))
		}
	}

	// Status reports up_to_date with custom scope in JSON.
	o = runControl(t, "skill", "status", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &listEnv))
	assert.Equal(t, "custom", listEnv.Data.Scope)
	for _, s := range listEnv.Data.Skills {
		assert.Equal(t, "up_to_date", s.State)
	}

	// User edit triggers modified state and blocks overwrite without --force.
	skillFile := filepath.Join(targetDir, "leapmux", "SKILL.md")
	require.NoError(t, os.WriteFile(skillFile, []byte("# edited by user\n"), 0o644))
	o = runControl(t, "skill", "install", "leapmux", "--target", targetFlag)
	assert.Equal(t, cli.ExitFailure, o.Code)
	f := o.Failure(t)
	assert.Equal(t, cli.CodeConflict, f.Code)
	assert.Contains(t, f.Message, "local edits")
	assert.NotEmpty(t, f.Hints)
	assert.Contains(t, strings.Join(f.Hints, " "), "--force")

	// --force restores the bundled skill.
	o = runControl(t, "skill", "install", "leapmux", "--target", targetFlag, "--force")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)

	// Uninstall removes cleanly.
	o = runControl(t, "skill", "uninstall", "leapmux", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	_, statErr := os.Stat(skillFile)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestSkillCLI_ShowValidationAndHubGuard(t *testing.T) {
	// Blank --reference must fail with usage error instead of printing SKILL.md.
	o := runControl(t, "skill", "show", "leapmux-review", "--reference", "")
	f := o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--reference must not be empty")

	// Mutual exclusion still triggers when --reference is explicitly empty.
	o = runControl(t, "skill", "show", "leapmux-review", "--references", "--reference", "")
	o.requireFailure(t, cli.CodeUsage)

	// Inherited --hub is disallowed on offline `skill show`.
	o = runControl(t, "skill", "show", "leapmux", "--hub", "https://hub.example")
	o.requireFailure(t, cli.CodeUsage)
}

func TestSkillCLI_LocalTextRenderingAndColumnHiding(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "skills")
	targetFlag := "dir:" + targetDir

	// Local list prints only the table (no summary header), hides ENABLED and INSTALLED, and tones STATE.
	o := runControlText(t, "skill", "list", "--target", targetFlag, "--color", "always")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	lines := strings.Split(strings.TrimSpace(o.Stdout), "\n")
	require.Greater(t, len(lines), 1, "expected multi-line table, got:\n%s", o.Stdout)
	assert.NotContains(t, o.Stdout, "Scope:")
	assert.NotContains(t, o.Stdout, "Primary dir:")
	assert.Contains(t, lines[0], "SKILL")
	assert.Contains(t, lines[0], "STATE")
	assert.Contains(t, lines[0], "BUNDLED")
	assert.NotContains(t, lines[0], "ENABLED")
	assert.NotContains(t, lines[0], "INSTALLED")
	assert.Contains(t, o.Stdout, "\x1b[2mnot_installed\x1b[0m")

	// Single-target local install renders an aligned multi-line table with TARGET hidden.
	o = runControlText(t, "skill", "install", "leapmux", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	lines = strings.Split(strings.TrimSpace(o.Stdout), "\n")
	require.Len(t, lines, 2, "expected header + 1 row, got:\n%s", o.Stdout)
	assert.Contains(t, lines[0], "SKILL")
	assert.NotContains(t, lines[0], "TARGET")
	assert.Contains(t, lines[0], "ACTION")
	assert.Contains(t, lines[0], "PATH")
	assert.Contains(t, lines[1], "leapmux")
	assert.Contains(t, lines[1], "installed")

	// Multi-target local install shows TARGET column and deduplicates -q quiet lines.
	targetDir2 := filepath.Join(t.TempDir(), "skills-2")
	targetFlag2 := "dir:" + targetDir2
	o = runControlText(t, "skill", "install", "leapmux", "leapmux-review", "--target", targetFlag, "--target", targetFlag2)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	lines = strings.Split(strings.TrimSpace(o.Stdout), "\n")
	require.Len(t, lines, 5, "expected header + 4 rows, got:\n%s", o.Stdout)
	assert.Contains(t, lines[0], "TARGET")

	o = runControlText(t, "skill", "install", "leapmux", "leapmux-review", "--target", targetFlag, "--target", targetFlag2, "-q")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Equal(t, "leapmux\nleapmux-review\n", o.Stdout)

	// Status prints summary header lines above the table, shows INSTALLED once installed,
	// keeps ENABLED hidden, and colors up_to_date green, modified yellow, and unmanaged red.
	o = runControlText(t, "skill", "status", "--target", targetFlag, "--color", "always")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	lines = strings.Split(strings.TrimSpace(o.Stdout), "\n")
	require.Greater(t, len(lines), 5, "expected summary header + blank line + table, got:\n%s", o.Stdout)
	assert.Equal(t, "Scope: custom", lines[0])
	assert.Equal(t, "Targets: "+targetFlag, lines[1])
	assert.Equal(t, "Primary dir: "+targetDir, lines[2])
	assert.Empty(t, lines[3])
	assert.Contains(t, lines[4], "SKILL")
	assert.Contains(t, lines[4], "INSTALLED")
	assert.NotContains(t, lines[4], "ENABLED")
	assert.Contains(t, o.Stdout, "\x1b[32mup_to_date\x1b[0m")

	// Create modified and unmanaged states.
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "leapmux", "SKILL.md"), []byte("# edited\n"), 0o644))
	unmanagedDir := filepath.Join(targetDir, "leapmux-council")
	require.NoError(t, os.MkdirAll(unmanagedDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(unmanagedDir, "SKILL.md"), []byte("# manual\n"), 0o644))

	o = runControlText(t, "skill", "status", "--target", targetFlag, "--color", "always")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "\x1b[33mmodified\x1b[0m")
	assert.Contains(t, o.Stdout, "\x1b[31munmanaged\x1b[0m")

	// Restore and uninstall renders an aligned multi-line table.
	o = runControl(t, "skill", "install", "leapmux", "--target", targetFlag, "--force")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	o = runControlText(t, "skill", "uninstall", "leapmux", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	lines = strings.Split(strings.TrimSpace(o.Stdout), "\n")
	require.Len(t, lines, 2, "expected header + 1 row, got:\n%s", o.Stdout)
	assert.Contains(t, lines[0], "SKILL")
	assert.NotContains(t, lines[0], "TARGET")
	assert.Contains(t, lines[0], "ACTION")
	assert.Contains(t, lines[1], "removed")
}

func TestSkillStateClassifier_MapsSkillStates(t *testing.T) {
	assert.Equal(t, cli.StateToneGreen, leapmuxStateClassifier("up_to_date"))
	assert.Equal(t, cli.StateToneYellow, leapmuxStateClassifier("outdated"))
	assert.Equal(t, cli.StateToneYellow, leapmuxStateClassifier("modified"))
	assert.Equal(t, cli.StateToneRed, leapmuxStateClassifier("unmanaged"))
	assert.Equal(t, cli.StateToneDim, leapmuxStateClassifier("not_installed"))
}

func TestSkillCLI_LocalTargetValidationDedupAndErrorMapping(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)

	// Invalid second target must fail upfront before writing ~/.agents/skills.
	o := runControl(t, "skill", "install", "leapmux", "--target", "agents", "--target", "invalid")
	o.requireFailure(t, cli.CodeUsage)
	_, err := os.Stat(filepath.Join(fakeHome, ".agents", "skills", "leapmux"))
	assert.ErrorIs(t, err, os.ErrNotExist, "upfront target validation must prevent partial writes")

	// Duplicate targets and whitespace-padded duplicate skill names deduplicate cleanly.
	o = runControl(t, "skill", "install", "leapmux", " leapmux ", "--target", "agents", "--target", "agents")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	var mutEnv struct {
		Data skillMutationResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &mutEnv))
	assert.Equal(t, "user", mutEnv.Data.Scope)
	assert.Equal(t, []string{"agents"}, mutEnv.Data.Targets)
	assert.Len(t, mutEnv.Data.Results, 1)
	assert.Equal(t, "leapmux", mutEnv.Data.Results[0].Name)
	assert.Equal(t, "agents", mutEnv.Data.Results[0].Target)

	// I/O failure (targetDir blocked by regular file) maps to CodeInternal, not CodeConflict.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("blocker"), 0o644))
	o = runControl(t, "skill", "install", "leapmux", "--target", "dir:"+blocker)
	o.requireFailure(t, cli.CodeInternal)

	// Unsafe symlink returns CodeConflict without a --force hint.
	if runtime.GOOS != "windows" {
		symTargetDir := filepath.Join(t.TempDir(), "sym-skills")
		outside := t.TempDir()
		require.NoError(t, os.MkdirAll(symTargetDir, 0o755))
		require.NoError(t, os.Symlink(outside, filepath.Join(symTargetDir, "leapmux")))

		o = runControl(t, "skill", "install", "leapmux", "--target", "dir:"+symTargetDir, "--force")
		f := o.requireFailure(t, cli.CodeConflict)
		assert.Empty(t, f.Hints, "unsafe symlink error must not suggest --force")
	}

	// Uninstall conflict on modified skill suggests --force to remove.
	modDir := filepath.Join(t.TempDir(), "mod-skills")
	o = runControl(t, "skill", "install", "leapmux", "--target", "dir:"+modDir)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "leapmux", "SKILL.md"), []byte("# edited\n"), 0o644))
	o = runControl(t, "skill", "uninstall", "leapmux", "--target", "dir:"+modDir)
	f := o.requireFailure(t, cli.CodeConflict)
	assert.Contains(t, strings.Join(f.Hints, " "), "remove")
}

func TestSkillCLI_LocalFlagGuards(t *testing.T) {
	// --auto-sync=false and --no-auto-sync=false must still be rejected without --worker.
	o := runControl(t, "skill", "install", "leapmux", "--auto-sync=false")
	f := o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "require --worker")

	o = runControl(t, "skill", "install", "leapmux", "--no-auto-sync=false")
	f = o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "require --worker")

	o = runControl(t, "skill", "install", "leapmux", "--worker", "worker-A", "--auto-sync=false", "--no-auto-sync=false")
	f = o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "exclude each other")

	o = runControl(t, "skill", "uninstall", "leapmux", "--no-auto-sync")
	f = o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "requires --worker")

	// --hub without --worker fails with usage error on list, status, install, uninstall.
	for _, args := range [][]string{
		{"skill", "list", "--hub", "https://hub.example"},
		{"skill", "status", "--hub", "https://hub.example"},
		{"skill", "install", "leapmux", "--hub", "https://hub.example"},
		{"skill", "uninstall", "leapmux", "--hub", "https://hub.example"},
	} {
		o = runControl(t, args...)
		f = o.requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--hub requires --worker", "args=%v", args)
	}

	// Explicit --scope combined with dir:<path> fails with CodeUsage across all local subcommands.
	customTarget := "dir:" + filepath.Join(t.TempDir(), "custom-guard")
	for _, args := range [][]string{
		{"skill", "list", "--scope", "project", "--target", customTarget},
		{"skill", "list", "--scope", "user", "--target", customTarget},
		{"skill", "status", "--scope", "project", "--target", customTarget},
		{"skill", "diff", "--scope", "project", "--target", customTarget},
		{"skill", "install", "leapmux", "--scope", "project", "--target", customTarget},
		{"skill", "install", "leapmux", "--scope", "project", "--target", "agents", "--target", customTarget},
		{"skill", "uninstall", "leapmux", "--scope", "project", "--target", customTarget},
		{"skill", "uninstall", "leapmux", "--scope", "project", "--target", "agents", "--target", customTarget},
	} {
		o = runControl(t, args...)
		f = o.requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--scope cannot be combined with dir:<path> targets", "args=%v", args)
	}

	// Remote --worker flag guards (invalid target, auto target, --all + args, empty skill name, --scope).
	for _, args := range [][]string{
		{"skill", "install", "leapmux", "--worker", "worker-A", "--target", customTarget},
		{"skill", "install", "leapmux", "--worker", "worker-A", "--target", "auto"},
		{"skill", "install", "--all", "leapmux", "--worker", "worker-A"},
		{"skill", "install", "   ", "--worker", "worker-A"},
		{"skill", "install", "leapmux", "--worker", "worker-A", "--scope", "project"},
		{"skill", "uninstall", "--all", "leapmux", "--worker", "worker-A"},
		{"skill", "uninstall", "   ", "--worker", "worker-A"},
		{"skill", "uninstall", "leapmux", "--worker", "worker-A", "--target", customTarget},
		{"skill", "uninstall", "leapmux", "--worker", "worker-A", "--target", "auto"},
		{"skill", "uninstall", "leapmux", "--worker", "worker-A", "--scope", "project"},
		{"skill", "diff", "--hub", "https://hub.example"},
		{"skill", "diff", "--all", "leapmux"},
	} {
		o = runControl(t, args...)
		o.requireFailure(t, cli.CodeUsage)
	}

	// LEAPMUX_CONTROL_WORKER_ID must not silently route `skill list` to a remote Worker.
	t.Setenv(controlenv.WorkerIDVariable, "worker-env-should-be-ignored")
	o = runControl(t, "skill", "list", "--target", "dir:"+filepath.Join(t.TempDir(), "local"))
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	var listEnv struct {
		Data skillListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &listEnv))
	assert.Equal(t, "custom", listEnv.Data.Scope)
	assert.Empty(t, listEnv.Data.WorkerID)
}

type fakeWorkerSkill struct {
	name        string
	description string
	references  []string
}

type skillWorkerDispatcher struct {
	mu      sync.Mutex
	methods []string
	updates []*leapmuxv1.UpdateWorkerSkillsRequest

	autoSync       bool
	targets        []string
	enabled        []string
	catalog        []fakeWorkerSkill
	targetOverride map[string]map[string]string // skill -> target -> state

	nextErrCode codes.Code
	nextErrMsg  string
}

func (d *skillWorkerDispatcher) catalogLocked() []fakeWorkerSkill {
	if len(d.catalog) > 0 {
		return d.catalog
	}
	cat := skills.Catalog()
	out := make([]fakeWorkerSkill, 0, len(cat))
	for _, s := range cat {
		out = append(out, fakeWorkerSkill{
			name:        s.Name,
			description: s.Description,
			references:  slices.Clone(s.References),
		})
	}
	return out
}

func (d *skillWorkerDispatcher) targetDir(target, skillName string) string {
	dir := ".agents"
	switch target {
	case "claude":
		dir = ".claude"
	case "codex":
		dir = ".codex"
	case "cursor":
		dir = ".cursor"
	case "gemini":
		dir = ".gemini"
	}
	return "/home/dev/" + dir + "/skills/" + skillName + "/SKILL.md"
}

func (d *skillWorkerDispatcher) stateLocked() *leapmuxv1.GetWorkerSkillsResponse {
	enabledSet := make(map[string]bool, len(d.enabled))
	for _, name := range d.enabled {
		enabledSet[name] = true
	}
	cat := d.catalogLocked()
	rows := make([]*leapmuxv1.WorkerSkillStatus, 0, len(cat))
	for _, s := range cat {
		state := string(skills.StateNotInstalled)
		installedVer := ""
		if enabledSet[s.name] {
			state = string(skills.StateUpToDate)
			installedVer = skills.BundledVersion()
		}
		targetStatuses := make([]*leapmuxv1.WorkerSkillTargetStatus, 0, len(d.targets))
		targetStates := make([]skills.State, 0, len(d.targets))
		for _, t := range d.targets {
			tState := state
			if byTarget, ok := d.targetOverride[s.name]; ok {
				if ov, ok := byTarget[t]; ok {
					tState = ov
				}
			}
			targetStates = append(targetStates, skills.State(tState))
			targetStatuses = append(targetStatuses, &leapmuxv1.WorkerSkillTargetStatus{
				Target:           t,
				State:            tState,
				InstalledVersion: installedVer,
				Path:             d.targetDir(t, s.name),
			})
		}
		aggState := state
		if len(targetStates) > 0 {
			aggState = string(skills.AggregateState(targetStates))
		}
		primaryTarget := "agents"
		if len(d.targets) > 0 {
			primaryTarget = d.targets[0]
		}
		rows = append(rows, &leapmuxv1.WorkerSkillStatus{
			Name:             s.name,
			Description:      s.description,
			State:            aggState,
			Enabled:          enabledSet[s.name],
			InstalledVersion: installedVer,
			BundledVersion:   skills.BundledVersion(),
			Path:             d.targetDir(primaryTarget, s.name),
			References:       slices.Clone(s.references),
			Targets:          targetStatuses,
		})
	}
	return &leapmuxv1.GetWorkerSkillsResponse{
		AutoSync:         d.autoSync,
		Targets:          append([]string(nil), d.targets...),
		PrimarySkillsDir: "/home/dev/.agents/skills",
		Skills:           rows,
	}
}

func (d *skillWorkerDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.methods = append(d.methods, req.GetMethod())
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure:
		writeProtoResponse(w, d.stateLocked())
	case leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure:
		var in leapmuxv1.UpdateWorkerSkillsRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.updates = append(d.updates, proto.Clone(&in).(*leapmuxv1.UpdateWorkerSkillsRequest))
		if d.nextErrCode != codes.OK {
			code, msg := d.nextErrCode, d.nextErrMsg
			d.nextErrCode = codes.OK
			d.nextErrMsg = ""
			_ = w.SendError(int32(code), msg)
			return
		}
		if in.AutoSync != nil {
			d.autoSync = in.GetAutoSync()
		}
		for _, t := range in.GetAddTargets() {
			if !slices.Contains(d.targets, t) {
				d.targets = append(d.targets, t)
			}
		}
		if len(in.GetRemoveTargets()) > 0 {
			filtered := make([]string, 0, len(d.targets))
			for _, t := range d.targets {
				if !slices.Contains(in.GetRemoveTargets(), t) {
					filtered = append(filtered, t)
				}
			}
			d.targets = filtered
		}
		var outcomes []*leapmuxv1.WorkerSkillOutcome
		if in.GetAddAllSkills() {
			for _, s := range d.catalogLocked() {
				if !slices.Contains(d.enabled, s.name) {
					d.enabled = append(d.enabled, s.name)
				}
				for _, t := range d.targets {
					outcomes = append(outcomes, &leapmuxv1.WorkerSkillOutcome{
						Name:   s.name,
						Target: t,
						Action: string(skills.ActionInstalled),
						Path:   d.targetDir(t, s.name),
					})
				}
			}
		}
		for _, name := range in.GetAddSkills() {
			if !slices.Contains(d.enabled, name) {
				d.enabled = append(d.enabled, name)
			}
			for _, t := range d.targets {
				outcomes = append(outcomes, &leapmuxv1.WorkerSkillOutcome{
					Name:   name,
					Target: t,
					Action: string(skills.ActionInstalled),
					Path:   d.targetDir(t, name),
				})
			}
		}
		if in.GetRemoveAllSkills() {
			for _, s := range d.catalogLocked() {
				for _, t := range d.targets {
					outcomes = append(outcomes, &leapmuxv1.WorkerSkillOutcome{
						Name:   s.name,
						Target: t,
						Action: string(skills.ActionRemoved),
						Path:   d.targetDir(t, s.name),
					})
				}
			}
			d.enabled = nil
		}
		if len(in.GetRemoveSkills()) > 0 {
			filtered := make([]string, 0, len(d.enabled))
			for _, name := range d.enabled {
				if !slices.Contains(in.GetRemoveSkills(), name) {
					filtered = append(filtered, name)
				}
			}
			d.enabled = filtered
			for _, name := range in.GetRemoveSkills() {
				for _, t := range d.targets {
					outcomes = append(outcomes, &leapmuxv1.WorkerSkillOutcome{
						Name:   name,
						Target: t,
						Action: string(skills.ActionRemoved),
						Path:   d.targetDir(t, name),
					})
				}
			}
		}
		writeProtoResponse(w, &leapmuxv1.UpdateWorkerSkillsResponse{
			State:    d.stateLocked(),
			Outcomes: outcomes,
		})
	default:
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func (d *skillWorkerDispatcher) setNextError(code codes.Code, msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextErrCode = code
	d.nextErrMsg = msg
}

func (d *skillWorkerDispatcher) resetCalls() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.methods = nil
	d.updates = nil
}

func (d *skillWorkerDispatcher) recorded() (methods []string, updates []*leapmuxv1.UpdateWorkerSkillsRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...), append([]*leapmuxv1.UpdateWorkerSkillsRequest(nil), d.updates...)
}

func TestSkillCLI_WorkerRemoteLifecycle(t *testing.T) {
	disp := &skillWorkerDispatcher{
		autoSync: true,
		targets:  []string{"agents"},
		enabled:  []string{"leapmux"},
		catalog: []fakeWorkerSkill{
			{name: "leapmux", description: "Core LeapMux skill", references: []string{"worker-owned-ref"}},
			{name: "worker-only-skill", description: "Only bundled on newer Worker", references: []string{"remote-guide"}},
		},
	}
	hub := &recordingHub{workers: workerRows("worker-A")}
	startSpawnIPC(t, hub, disp)

	// 1. skill list --worker runs preflight, renders only the table with ENABLED, and uses Worker references/targets.
	o := runControlText(t, "skill", "list", "--worker", "worker-A")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	lines := strings.Split(strings.TrimSpace(o.Stdout), "\n")
	require.Greater(t, len(lines), 1)
	assert.NotContains(t, o.Stdout, "Scope:")
	assert.NotContains(t, o.Stdout, "Auto-sync:")
	assert.Contains(t, lines[0], "ENABLED")
	assert.Contains(t, hub.called(), leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure,
		"remote skill list must call maybePreflightWorker")
	methods, _ := disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure}, methods)

	// Verify JSON uses Worker-owned references (not local CLI catalog) and exposes per-target status.
	o = runControl(t, "skill", "list", "--worker", "worker-A")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	var listEnv struct {
		Data skillListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &listEnv))
	require.Len(t, listEnv.Data.Skills, 2)
	assert.Equal(t, []string{"worker-owned-ref"}, listEnv.Data.Skills[0].References)
	assert.Equal(t, "worker-only-skill", listEnv.Data.Skills[1].Name)
	assert.Equal(t, []string{"remote-guide"}, listEnv.Data.Skills[1].References)
	require.Len(t, listEnv.Data.Skills[0].Targets, 1)
	assert.Equal(t, "agents", listEnv.Data.Skills[0].Targets[0].Target)
	assert.Equal(t, "up_to_date", listEnv.Data.Skills[0].Targets[0].State)

	// 1b. skill status --worker renders summary lines (Scope, Worker, Auto-sync, Targets, Primary dir) above table.
	o = runControlText(t, "skill", "status", "--worker", "worker-A")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	lines = strings.Split(strings.TrimSpace(o.Stdout), "\n")
	require.Greater(t, len(lines), 6, "expected summary header + blank line + table, got:\n%s", o.Stdout)
	assert.Equal(t, "Scope: worker", lines[0])
	assert.Equal(t, "Worker: worker-A", lines[1])
	assert.Equal(t, "Auto-sync: enabled", lines[2])
	assert.Equal(t, "Targets: agents", lines[3])
	assert.Equal(t, "Primary dir: /home/dev/.agents/skills", lines[4])
	assert.Empty(t, lines[5])
	assert.Contains(t, lines[6], "SKILL")
	assert.Contains(t, lines[6], "ENABLED")

	// 2. skill install sends ONE single UpdateWorkerSkills RPC with atomic AddSkills + AddTargets + StrictConflicts,
	// accepts a Worker-only skill not in local CLI catalog, and renders unified skillMutationResult with TARGET column on multi-target.
	disp.resetCalls()
	o = runControlText(t, "skill", "install", " worker-only-skill ", "worker-only-skill", "--worker", "worker-A", "--target", "claude")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	lines = strings.Split(strings.TrimSpace(o.Stdout), "\n")
	require.Len(t, lines, 3, "expected header + 2 target rows, got:\n%s", o.Stdout)
	assert.Contains(t, lines[0], "SKILL")
	assert.Contains(t, lines[0], "TARGET")
	assert.Contains(t, lines[0], "ACTION")
	assert.Contains(t, lines[0], "PATH")
	methods, updates := disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure}, methods)
	require.Len(t, updates, 1)
	assert.False(t, updates[0].GetUpdateEnabledSkills())
	assert.Equal(t, []string{"worker-only-skill"}, updates[0].GetAddSkills())
	assert.Equal(t, []string{"claude"}, updates[0].GetAddTargets())
	assert.True(t, updates[0].GetSyncNow())
	assert.False(t, updates[0].GetForce())
	assert.True(t, updates[0].GetStrictConflicts())

	// Multi-target remote install returns unified skillMutationResult JSON and deduplicated -q quiet lines.
	disp.resetCalls()
	o = runControl(t, "skill", "install", "worker-only-skill", "--worker", "worker-A")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	var mutEnv struct {
		Data skillMutationResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &mutEnv))
	assert.Equal(t, "worker", mutEnv.Data.Scope)
	assert.Equal(t, "worker-A", mutEnv.Data.WorkerID)
	require.NotNil(t, mutEnv.Data.AutoSync)
	assert.True(t, *mutEnv.Data.AutoSync)
	assert.Equal(t, []string{"agents", "claude"}, mutEnv.Data.Targets)
	require.Len(t, mutEnv.Data.Results, 2)
	assert.Equal(t, "worker-only-skill", mutEnv.Data.Results[0].Name)
	assert.Equal(t, "agents", mutEnv.Data.Results[0].Target)
	assert.Equal(t, "claude", mutEnv.Data.Results[1].Target)

	o = runControlText(t, "skill", "install", "worker-only-skill", "--worker", "worker-A", "-q")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Equal(t, "worker-only-skill\n", o.Stdout)

	// 3. skill install --all sends AddAllSkills=true in a single RPC (never expands local CLI catalog).
	disp.resetCalls()
	o = runControl(t, "skill", "install", "--all", "--worker", "worker-A", "--force")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	methods, updates = disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure}, methods)
	require.Len(t, updates, 1)
	assert.True(t, updates[0].GetAddAllSkills())
	assert.Empty(t, updates[0].GetAddSkills())
	assert.False(t, updates[0].GetUpdateEnabledSkills())
	assert.True(t, updates[0].GetSyncNow())
	assert.True(t, updates[0].GetForce())
	assert.False(t, updates[0].GetStrictConflicts())

	// 4. Standalone --no-auto-sync sends SyncNow=false and renders concise summary lines in text mode.
	disp.resetCalls()
	o = runControlText(t, "skill", "install", "--worker", "worker-A", "--no-auto-sync")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "Scope: worker\n")
	assert.Contains(t, o.Stdout, "Worker: worker-A\n")
	assert.Contains(t, o.Stdout, "Auto-sync: disabled\n")
	methods, updates = disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure}, methods)
	require.Len(t, updates, 1)
	assert.False(t, updates[0].GetUpdateEnabledSkills())
	assert.False(t, updates[0].GetSyncNow())
	require.NotNil(t, updates[0].AutoSync)
	assert.False(t, updates[0].GetAutoSync())

	// 4b. --auto-sync with --target sends ONE RPC with AddTargets and SyncNow=true (never calls GetWorkerSkills).
	disp.mu.Lock()
	disp.targets = []string{"agents"}
	disp.mu.Unlock()
	disp.resetCalls()
	o = runControl(t, "skill", "install", "--worker", "worker-A", "--auto-sync", "--target", "codex")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	methods, updates = disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure}, methods)
	require.Len(t, updates, 1)
	assert.False(t, updates[0].GetUpdateEnabledSkills())
	assert.True(t, updates[0].GetSyncNow())
	assert.Equal(t, []string{"codex"}, updates[0].GetAddTargets())
	require.NotNil(t, updates[0].AutoSync)
	assert.True(t, updates[0].GetAutoSync())

	// 5. --no-auto-sync=false enables auto-sync; --auto-sync=false disables it.
	disp.resetCalls()
	o = runControl(t, "skill", "install", "--worker", "worker-A", "--no-auto-sync=false")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	_, updates = disp.recorded()
	require.Len(t, updates, 1)
	require.NotNil(t, updates[0].AutoSync)
	assert.True(t, updates[0].GetAutoSync())

	disp.resetCalls()
	o = runControl(t, "skill", "install", "--worker", "worker-A", "--auto-sync=false")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	_, updates = disp.recorded()
	require.Len(t, updates, 1)
	require.NotNil(t, updates[0].AutoSync)
	assert.False(t, updates[0].GetAutoSync())

	// 6. skill uninstall sends ONE RPC with trimmed & deduplicated RemoveSkills (including Worker-only skills).
	disp.resetCalls()
	o = runControl(t, "skill", "uninstall", " worker-only-skill ", "worker-only-skill", "--worker", "worker-A")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	methods, updates = disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure}, methods)
	require.Len(t, updates, 1)
	assert.False(t, updates[0].GetUpdateEnabledSkills())
	assert.Equal(t, []string{"worker-only-skill"}, updates[0].GetRemoveSkills())
	assert.True(t, updates[0].GetStrictConflicts())
	assert.False(t, updates[0].GetForce())

	// 7. skill uninstall --all --no-auto-sync sends RemoveAllSkills=true and AutoSync=false in a single RPC.
	disp.resetCalls()
	o = runControl(t, "skill", "uninstall", "--all", "--worker", "worker-A", "--no-auto-sync")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	methods, updates = disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure}, methods)
	require.Len(t, updates, 1)
	assert.True(t, updates[0].GetRemoveAllSkills())
	assert.Empty(t, updates[0].GetRemoveSkills())
	assert.False(t, updates[0].GetUpdateEnabledSkills())
	require.NotNil(t, updates[0].AutoSync)
	assert.False(t, updates[0].GetAutoSync())
	assert.True(t, updates[0].GetStrictConflicts())

	// 8. Multi-target Worker status displays TARGETS column with per-target drift.
	disp.mu.Lock()
	disp.targets = []string{"agents", "claude"}
	disp.enabled = []string{"leapmux"}
	disp.targetOverride = map[string]map[string]string{
		"leapmux": {"claude": string(skills.StateOutdated)},
	}
	disp.mu.Unlock()
	o = runControlText(t, "skill", "status", "--worker", "worker-A")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "Targets: agents, claude\n")
	assert.Contains(t, o.Stdout, "TARGETS")
	assert.Contains(t, o.Stdout, "agents:up_to_date, claude:outdated")

	// 9. Standalone target removal on Worker uninstall sends RemoveTargets without requiring skill names.
	disp.resetCalls()
	o = runControl(t, "skill", "uninstall", "--worker", "worker-A", "--target", "claude", "--target", "claude")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	methods, updates = disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure}, methods)
	require.Len(t, updates, 1)
	assert.Equal(t, []string{"claude"}, updates[0].GetRemoveTargets())
	assert.Empty(t, updates[0].GetRemoveSkills())
	assert.False(t, updates[0].GetRemoveAllSkills())
	assert.Nil(t, updates[0].AutoSync)
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &mutEnv))
	assert.Equal(t, []string{"agents"}, mutEnv.Data.Targets)

	// 10. Standalone --no-auto-sync on Worker uninstall succeeds without skill names or targets.
	disp.resetCalls()
	o = runControl(t, "skill", "uninstall", "--worker", "worker-A", "--no-auto-sync")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	methods, updates = disp.recorded()
	assert.Equal(t, []string{leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure}, methods)
	require.Len(t, updates, 1)
	assert.Empty(t, updates[0].GetRemoveSkills())
	assert.Empty(t, updates[0].GetRemoveTargets())
	require.NotNil(t, updates[0].AutoSync)
	assert.False(t, updates[0].GetAutoSync())
}

func TestSkillCLI_WorkerRemoteErrorMapping(t *testing.T) {
	disp := &skillWorkerDispatcher{
		autoSync: false,
		targets:  []string{"agents"},
		enabled:  []string{"leapmux"},
	}
	hub := &recordingHub{workers: workerRows("worker-A")}
	startSpawnIPC(t, hub, disp)

	// Install conflict (codes.Aborted) maps to CodeConflict with --force overwrite hint.
	disp.setNextError(codes.Aborted, "skill has local edits: leapmux")
	o := runControl(t, "skill", "install", "leapmux", "--worker", "worker-A")
	assert.Equal(t, cli.ExitFailure, o.Code)
	f := o.requireFailure(t, cli.CodeConflict)
	assert.Contains(t, f.Message, "local edits")
	assert.Contains(t, strings.Join(f.Hints, " "), "pass --force to overwrite")

	// Uninstall conflict (codes.Aborted) maps to CodeConflict with --force remove hint.
	disp.setNextError(codes.Aborted, "skill is not managed by LeapMux: leapmux")
	o = runControl(t, "skill", "uninstall", "leapmux", "--worker", "worker-A")
	assert.Equal(t, cli.ExitFailure, o.Code)
	f = o.requireFailure(t, cli.CodeConflict)
	assert.Contains(t, strings.Join(f.Hints, " "), "pass --force to remove")

	// Symlink conflict (codes.Aborted) maps to CodeConflict without --force hint.
	disp.setNextError(codes.Aborted, "symlink inside managed skill directory: /home/dev/.agents/skills/leapmux")
	o = runControl(t, "skill", "install", "leapmux", "--worker", "worker-A")
	assert.Equal(t, cli.ExitFailure, o.Code)
	f = o.requireFailure(t, cli.CodeConflict)
	assert.Empty(t, f.Hints, "symlink conflict must not suggest --force")

	// Remote unknown skill (codes.NotFound) maps to CodeNotFound (exit 4).
	disp.setNextError(codes.NotFound, `unknown skill "missing-skill"`)
	o = runControl(t, "skill", "install", "missing-skill", "--worker", "worker-A")
	assert.Equal(t, cli.ExitNotFound, o.Code)
	o.requireFailure(t, cli.CodeNotFound)

	// Remote invalid argument maps to CodeInvalidArgument.
	disp.setNextError(codes.InvalidArgument, "invalid worker skills update")
	o = runControl(t, "skill", "install", "leapmux", "--worker", "worker-A")
	o.requireFailure(t, cli.CodeInvalidArgument)

	// Remote validation edge cases fail before dialing the Worker.
	disp.resetCalls()
	o = runControl(t, "skill", "install", "   ", "--worker", "worker-A")
	assert.Equal(t, cli.ExitUsage, o.Code)
	assert.Contains(t, o.Failure(t).Message, "must not be empty")

	o = runControl(t, "skill", "install", "--worker", "worker-A")
	assert.Equal(t, cli.ExitUsage, o.Code)

	o = runControl(t, "skill", "uninstall", "--worker", "worker-A")
	assert.Equal(t, cli.ExitUsage, o.Code)

	methods, _ := disp.recorded()
	assert.Empty(t, methods, "invalid remote invocations must fail before sending RPCs")

	// --no-auto-sync=false on uninstall sends AutoSync=true.
	disp.resetCalls()
	o = runControl(t, "skill", "uninstall", "--all", "--worker", "worker-A", "--no-auto-sync=false")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	_, updates := disp.recorded()
	require.Len(t, updates, 1)
	require.NotNil(t, updates[0].AutoSync)
	assert.True(t, updates[0].GetAutoSync())
}

func TestSkillCLI_FlagHelpAndWorkerEnvDefault(t *testing.T) {
	root := NewCommand()
	for _, tc := range []struct {
		path     []string
		contains string
		hasWork  bool
	}{
		{[]string{"skill", "list"}, "local target directory: auto", true},
		{[]string{"skill", "status"}, "local target directory: auto", true},
		{[]string{"skill", "diff"}, "local target directory: auto", false},
		{[]string{"skill", "install"}, "target directory: auto", true},
		{[]string{"skill", "uninstall"}, "target directory: auto", true},
	} {
		cmd, _, err := root.Find(tc.path)
		require.NoError(t, err)
		targetFlag := cmd.Flags().Lookup("target")
		require.NotNil(t, targetFlag)
		assert.Contains(t, targetFlag.Usage, tc.contains, "path=%v", tc.path)
		assert.Equal(t, "stringArray", targetFlag.Value.Type(), "path=%v --target must be repeatable", tc.path)

		wFlag := cmd.Flags().Lookup(workerFlag)
		if tc.hasWork {
			require.NotNil(t, wFlag)
			assert.Empty(t, wFlag.Annotations["leapmux_env_default"], "skill %v --worker must not bind env default", tc.path)
		} else {
			assert.Nil(t, wFlag, "local-only skill %v must not declare --worker", tc.path)
		}
	}
}

func TestSkillCLI_AutoTargetDetectionAndMultiTargetStatus(t *testing.T) {
	t.Run("clean home defaults to agents only", func(t *testing.T) {
		fakeHome := t.TempDir()
		t.Setenv("HOME", fakeHome)
		t.Setenv("USERPROFILE", fakeHome)

		o := runControl(t, "skill", "install", "leapmux")
		require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
		var mutEnv struct {
			Data skillMutationResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(o.Stdout), &mutEnv))
		assert.Equal(t, "user", mutEnv.Data.Scope)
		assert.Equal(t, []string{"agents"}, mutEnv.Data.Targets)
		assert.FileExists(t, filepath.Join(fakeHome, ".agents", "skills", "leapmux", "SKILL.md"))
		_, claudeErr := os.Stat(filepath.Join(fakeHome, ".claude", "skills", "leapmux", "SKILL.md"))
		assert.ErrorIs(t, claudeErr, os.ErrNotExist)

		// Single-target status keeps TARGETS column hidden in text output.
		o = runControlText(t, "skill", "status")
		require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
		assert.Contains(t, o.Stdout, "Targets: agents\n")
		assert.NotContains(t, o.Stdout, "TARGETS")
	})

	t.Run("home with .claude and .codex/skills auto-detects and surfaces per-target drift", func(t *testing.T) {
		fakeHome := t.TempDir()
		t.Setenv("HOME", fakeHome)
		t.Setenv("USERPROFILE", fakeHome)
		require.NoError(t, os.MkdirAll(filepath.Join(fakeHome, ".claude"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(fakeHome, ".codex", "skills"), 0o755))

		// Default install hits agents, claude, and codex.
		o := runControl(t, "skill", "install", "leapmux")
		require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
		var mutEnv struct {
			Data skillMutationResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(o.Stdout), &mutEnv))
		assert.Equal(t, []string{"agents", "claude", "codex"}, mutEnv.Data.Targets)
		assert.FileExists(t, filepath.Join(fakeHome, ".agents", "skills", "leapmux", "SKILL.md"))
		assert.FileExists(t, filepath.Join(fakeHome, ".claude", "skills", "leapmux", "SKILL.md"))
		assert.FileExists(t, filepath.Join(fakeHome, ".codex", "skills", "leapmux", "SKILL.md"))

		// Introduce local edit only in claude target to verify worst-case aggregation and TARGETS column.
		claudeSkill := filepath.Join(fakeHome, ".claude", "skills", "leapmux", "SKILL.md")
		require.NoError(t, os.WriteFile(claudeSkill, []byte("# local claude edit\n"), 0o644))

		// JSON list populates per-target rows and aggregates worst-case state.
		o = runControl(t, "skill", "list")
		require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
		var listEnv struct {
			Data skillListResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(o.Stdout), &listEnv))
		assert.Equal(t, []string{"agents", "claude", "codex"}, listEnv.Data.Targets)
		require.NotEmpty(t, listEnv.Data.Skills)
		assert.Equal(t, "leapmux", listEnv.Data.Skills[0].Name)
		assert.Equal(t, "modified", listEnv.Data.Skills[0].State)
		require.Len(t, listEnv.Data.Skills[0].Targets, 3)
		assert.Equal(t, "up_to_date", listEnv.Data.Skills[0].Targets[0].State)
		assert.Equal(t, "modified", listEnv.Data.Skills[0].Targets[1].State)
		assert.Equal(t, "up_to_date", listEnv.Data.Skills[0].Targets[2].State)

		// Text status and list show TARGETS column with per-target drift.
		o = runControlText(t, "skill", "status")
		require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
		assert.Contains(t, o.Stdout, "Targets: agents, claude, codex\n")
		assert.Contains(t, o.Stdout, "TARGETS")
		assert.Contains(t, o.Stdout, "agents:up_to_date, claude:modified, codex:up_to_date")

		o = runControlText(t, "skill", "list", "--target", "agents", "--target", "claude")
		require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
		assert.Contains(t, o.Stdout, "TARGETS")
		assert.Contains(t, o.Stdout, "agents:up_to_date, claude:modified")
	})
}

func TestSkillCLI_CrossTargetAtomicPreflight(t *testing.T) {
	dir1 := filepath.Join(t.TempDir(), "target-1")
	dir2 := filepath.Join(t.TempDir(), "target-2")
	t1 := "dir:" + dir1
	t2 := "dir:" + dir2

	// Create an unmanaged conflict in target #2 before installing across both targets.
	require.NoError(t, os.MkdirAll(filepath.Join(dir2, "leapmux"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "leapmux", "SKILL.md"), []byte("# unmanaged\n"), 0o644))

	o := runControl(t, "skill", "install", "leapmux", "--target", t1, "--target", t2)
	o.requireFailure(t, cli.CodeConflict)
	_, err := os.Stat(filepath.Join(dir1, "leapmux"))
	assert.ErrorIs(t, err, os.ErrNotExist, "conflict in target #2 must leave target #1 untouched on install")

	// Install cleanly into both targets (with --force to replace unmanaged in target #2),
	// then locally modify target #2 and verify uninstall preflight leaves target #1 intact.
	o = runControl(t, "skill", "install", "leapmux", "--target", t1, "--target", t2, "--force")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "leapmux", "SKILL.md"), []byte("# edited\n"), 0o644))

	o = runControl(t, "skill", "uninstall", "leapmux", "--target", t1, "--target", t2)
	o.requireFailure(t, cli.CodeConflict)
	assert.FileExists(t, filepath.Join(dir1, "leapmux", "SKILL.md"), "conflict in target #2 must leave target #1 untouched on uninstall")
}

func TestSkillCLI_Diff(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "diff-skills")
	targetFlag := "dir:" + targetDir

	// Install one skill cleanly; default diff reports clean even when other bundled skills are uninstalled.
	o := runControl(t, "skill", "install", "leapmux", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)

	o = runControlText(t, "skill", "diff", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Equal(t, "All installed skills match the bundled catalog.\n", o.Stdout)

	o = runControl(t, "skill", "diff", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	var diffEnv struct {
		Data skillDiffResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &diffEnv))
	assert.Equal(t, "custom", diffEnv.Data.Scope)
	assert.Equal(t, []string{targetFlag}, diffEnv.Data.Targets)
	assert.Empty(t, diffEnv.Data.Diffs)

	// Locally edit SKILL.md and add an untracked file.
	skillMD := filepath.Join(targetDir, "leapmux", "SKILL.md")
	orig, err := os.ReadFile(skillMD)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(skillMD, append(orig, []byte("\n# local note\n")...), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "leapmux", "notes.md"), []byte("extra\n"), 0o644))

	// Text diff prints header, unified diff, and untracked file entry.
	o = runControlText(t, "skill", "diff", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "=== leapmux ("+targetFlag+", modified) ===\n")
	assert.Contains(t, o.Stdout, "--- a/SKILL.md\n+++ b/SKILL.md\n")
	assert.Contains(t, o.Stdout, "-# local note\n")
	assert.Contains(t, o.Stdout, "untracked: notes.md\n")

	// Quiet mode lists differing skill names.
	o = runControlText(t, "skill", "diff", "--target", targetFlag, "-q")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Equal(t, "leapmux\n", o.Stdout)

	// Explicit uninstalled skill shows concise not_installed status in text and full details in JSON.
	o = runControlText(t, "skill", "diff", "leapmux-council", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "=== leapmux-council ("+targetFlag+", not_installed) ===\n")
	assert.Contains(t, o.Stdout, "not installed (")

	// --all includes every bundled skill in JSON.
	o = runControl(t, "skill", "diff", "--all", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &diffEnv))
	assert.Len(t, diffEnv.Data.Diffs, len(skills.Catalog()))

	// Unknown skill returns CodeNotFound.
	o = runControl(t, "skill", "diff", "no-such-skill", "--target", targetFlag)
	o.requireFailure(t, cli.CodeNotFound)

	// Auto-detected multi-target diff reports only the target that drifted.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)
	require.NoError(t, os.MkdirAll(filepath.Join(fakeHome, ".claude"), 0o755))

	o = runControl(t, "skill", "install", "leapmux")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	require.NoError(t, os.WriteFile(filepath.Join(fakeHome, ".claude", "skills", "leapmux", "SKILL.md"), []byte("# claude edit\n"), 0o644))

	o = runControlText(t, "skill", "diff")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "=== leapmux (claude, modified) ===\n")
	assert.NotContains(t, o.Stdout, "=== leapmux (agents,")

	// Unsafe symlink fails with CodeConflict and no --force hint.
	if runtime.GOOS != "windows" {
		symDir := filepath.Join(t.TempDir(), "sym-diff")
		outside := t.TempDir()
		require.NoError(t, os.MkdirAll(symDir, 0o755))
		require.NoError(t, os.Symlink(outside, filepath.Join(symDir, "leapmux")))

		o = runControl(t, "skill", "diff", "leapmux", "--target", "dir:"+symDir)
		f := o.requireFailure(t, cli.CodeConflict)
		assert.Empty(t, f.Hints)
	}
}
