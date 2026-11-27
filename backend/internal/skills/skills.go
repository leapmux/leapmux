// Package skills embeds LeapMux's curated Agent Skills catalog and manages
// safe installation, status inspection, drift detection, and Worker startup
// synchronization across standard coding-agent skill directories.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"

	"github.com/leapmux/leapmux/internal/skills/generated/catalog"
	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/leapmux/leapmux/util/version"
)

// MarkerFileName records LeapMux ownership and the installed per-file hashes
// inside each managed skill directory so upgrades never overwrite user edits.
const MarkerFileName = ".leapmux-skill.json"

// Target identifies one standard coding-agent skill directory target.
type Target string

const (
	TargetAgents Target = "agents"
	TargetClaude Target = "claude"
	TargetCodex  Target = "codex"
	TargetCursor Target = "cursor"
	TargetGemini Target = "gemini"
)

type targetSpec struct {
	target  Target
	relDirs []string
}

var targetSpecs = []targetSpec{
	{target: TargetAgents, relDirs: []string{".agents", "skills"}},
	{target: TargetClaude, relDirs: []string{".claude", "skills"}},
	{target: TargetCodex, relDirs: []string{".codex", "skills"}},
	{target: TargetCursor, relDirs: []string{".cursor", "skills"}},
	{target: TargetGemini, relDirs: []string{".gemini", "skills"}},
}

// StandardTargets returns the supported standard skill targets in display order.
func StandardTargets() []Target {
	out := make([]Target, len(targetSpecs))
	for i, spec := range targetSpecs {
		out[i] = spec.target
	}
	return out
}

func lookupTargetSpec(target Target) (targetSpec, bool) {
	for _, spec := range targetSpecs {
		if spec.target == target {
			return spec, true
		}
	}
	return targetSpec{}, false
}

func standardTargetNames() string {
	names := make([]string, len(targetSpecs))
	for i, spec := range targetSpecs {
		names[i] = string(spec.target)
	}
	return strings.Join(names, ", ")
}

// Domain error sentinels returned by skill resolution, validation, and disk
// operations. Callers can inspect them with errors.Is.
var (
	ErrSkillModified  = errors.New("skill has local edits")
	ErrSkillUnmanaged = errors.New("skill is not managed by LeapMux")
	ErrUnsafeSymlink  = errors.New("symlink inside managed skill directory")
	ErrUnknownSkill   = errors.New("unknown skill")
	ErrInvalidTarget  = errors.New("invalid skill target")
)

// State describes the on-disk installation state of a bundled skill.
type State string

const (
	StateNotInstalled State = "not_installed"
	StateUpToDate     State = "up_to_date"
	StateOutdated     State = "outdated"
	StateModified     State = "modified"
	StateUnmanaged    State = "unmanaged"
)

// AggregateState rolls up per-target states for one skill by worst-case
// severity: unmanaged > modified > outdated > not_installed > up_to_date.
// An empty slice returns StateNotInstalled.
func AggregateState(states []State) State {
	if len(states) == 0 {
		return StateNotInstalled
	}
	worst := StateUpToDate
	worstRank := stateSeverity(worst)
	for _, st := range states {
		if r := stateSeverity(st); r > worstRank {
			worst = st
			worstRank = r
		}
	}
	return worst
}

func stateSeverity(st State) int {
	switch st {
	case StateUpToDate:
		return 0
	case StateNotInstalled:
		return 1
	case StateOutdated:
		return 2
	case StateModified:
		return 3
	case StateUnmanaged:
		return 4
	default:
		return 4
	}
}

// SkillFile is one file belonging to a bundled skill directory (for example
// SKILL.md, agents/openai.yaml, or references/review-schemas.md).
type SkillFile struct {
	RelPath     string `json:"rel_path"`
	Content     string `json:"-"`
	ContentHash string `json:"content_hash"`
}

// Skill is one bundled Agent Skill directory (SKILL.md plus optional metadata
// and companion reference files).
type Skill struct {
	Name          string      `json:"name"`
	Description   string      `json:"description"`
	ArgumentHint  string      `json:"argument_hint,omitempty"`
	UserInvocable bool        `json:"user_invocable,omitempty"`
	ContentHash   string      `json:"content_hash"`
	Files         []SkillFile `json:"-"`
	References    []string    `json:"references,omitempty"`
}

// Content returns the bundled SKILL.md document derived from Files.
func (s Skill) Content() string {
	for _, f := range s.Files {
		if f.RelPath == "SKILL.md" {
			return f.Content
		}
	}
	return ""
}

// Reference looks up one companion Markdown reference under references/.
// It accepts a bare name ("review-schemas"), filename ("review-schemas.md"),
// or relative path ("references/review-schemas.md").
func (s Skill) Reference(name string) (SkillFile, bool) {
	norm := strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	norm = strings.TrimPrefix(norm, "./")
	norm = strings.TrimPrefix(norm, "references/")
	norm = strings.TrimSuffix(norm, ".md")
	if norm == "" || norm == "." || norm == ".." || strings.Contains(norm, "/") {
		return SkillFile{}, false
	}
	want := "references/" + norm + ".md"
	for _, f := range s.Files {
		if f.RelPath == want {
			return f, true
		}
	}
	return SkillFile{}, false
}

// SkillStatus reports a bundled skill's installation state in one directory.
type SkillStatus struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	State            State    `json:"state"`
	InstalledVersion string   `json:"installed_version,omitempty"`
	BundledVersion   string   `json:"bundled_version"`
	Path             string   `json:"path"`
	References       []string `json:"references,omitempty"`
}

type skillMarker struct {
	Name        string            `json:"name"`
	ManagedBy   string            `json:"managed_by"`
	Version     string            `json:"version"`
	ContentHash string            `json:"content_hash"`
	Files       map[string]string `json:"files,omitempty"`
}

type frontmatter struct {
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	ArgumentHint  string `yaml:"argument-hint"`
	UserInvocable bool   `yaml:"user-invocable"`
}

var loadBundledSkills = sync.OnceValue(loadCatalog)

func cloneSkill(s Skill) Skill {
	s.Files = slices.Clone(s.Files)
	s.References = slices.Clone(s.References)
	return s
}

// Catalog returns a copy of all bundled skills in canonical order (`leapmux`
// first, then alphabetical).
func Catalog() []Skill {
	src := loadBundledSkills()
	out := make([]Skill, len(src))
	for i, s := range src {
		out[i] = cloneSkill(s)
	}
	return out
}

// Lookup finds one bundled skill by exact name.
func Lookup(name string) (Skill, bool) {
	want := strings.TrimSpace(name)
	for _, s := range loadBundledSkills() {
		if s.Name == want {
			return cloneSkill(s), true
		}
	}
	return Skill{}, false
}

// Names returns the canonical list of bundled skill names.
func Names() []string {
	src := loadBundledSkills()
	out := make([]string, len(src))
	for i, s := range src {
		out[i] = s.Name
	}
	return out
}

// BundledVersion returns the CLI/Worker version stamped into skill markers.
func BundledVersion() string {
	v := strings.TrimSpace(version.Value)
	if v == "" {
		return "dev"
	}
	return v
}

func walkSkillFiles(fsys fs.FS, root string, fn func(rel string, data []byte) error) error {
	return fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel := p
		if root != "." {
			rel = strings.TrimPrefix(p, root+"/")
		}
		data, rErr := fs.ReadFile(fsys, p)
		if rErr != nil {
			return rErr
		}
		return fn(rel, data)
	})
}

func loadCatalog() []Skill {
	entries, err := fs.ReadDir(catalog.FS, "files")
	if err != nil {
		panic(fmt.Sprintf("skills: read catalog dir: %v", err))
	}
	out := make([]Skill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirName := e.Name()
		skillRoot := path.Join("files", dirName)
		skillMDPath := path.Join(skillRoot, "SKILL.md")
		rawSkillMD, err := catalog.FS.ReadFile(skillMDPath)
		if err != nil {
			panic(fmt.Sprintf("skills: read %s: %v", skillMDPath, err))
		}
		fm, err := parseFrontmatter(rawSkillMD)
		if err != nil {
			panic(fmt.Sprintf("skills: parse %s: %v", skillMDPath, err))
		}
		if fm.Name != dirName {
			panic(fmt.Sprintf("skills: %s frontmatter name %q != dir %q", skillMDPath, fm.Name, dirName))
		}

		var files []SkillFile
		var refs []string
		err = walkSkillFiles(catalog.FS, skillRoot, func(rel string, data []byte) error {
			files = append(files, SkillFile{
				RelPath:     rel,
				Content:     string(data),
				ContentHash: sha256Hex(data),
			})
			if strings.HasPrefix(rel, "references/") && strings.HasSuffix(rel, ".md") {
				refName := strings.TrimSuffix(strings.TrimPrefix(rel, "references/"), ".md")
				if !strings.Contains(refName, "/") {
					refs = append(refs, refName)
				}
			}
			return nil
		})
		if err != nil {
			panic(fmt.Sprintf("skills: walk %s: %v", skillRoot, err))
		}
		slices.SortFunc(files, func(a, b SkillFile) int {
			return strings.Compare(a.RelPath, b.RelPath)
		})
		slices.Sort(refs)

		out = append(out, Skill{
			Name:          fm.Name,
			Description:   strings.TrimSpace(fm.Description),
			ArgumentHint:  strings.TrimSpace(fm.ArgumentHint),
			UserInvocable: fm.UserInvocable,
			ContentHash:   bundleHash(files),
			Files:         files,
			References:    refs,
		})
	}
	slices.SortFunc(out, func(a, b Skill) int {
		if a.Name == "leapmux" && b.Name != "leapmux" {
			return -1
		}
		if b.Name == "leapmux" && a.Name != "leapmux" {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func bundleHash(files []SkillFile) string {
	h := sha256.New()
	for _, f := range files {
		_, _ = h.Write([]byte(f.RelPath))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(f.ContentHash))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func parseFrontmatter(raw []byte) (frontmatter, error) {
	text := strings.TrimPrefix(string(raw), "\xef\xbb\xbf")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t") != "---" {
		return frontmatter{}, errors.New("missing YAML frontmatter")
	}
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return frontmatter{}, errors.New("unterminated YAML frontmatter")
	}
	body := strings.Join(lines[1:endIdx], "\n")
	var fm frontmatter
	dec := yaml.NewDecoder(strings.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil {
		return frontmatter{}, err
	}
	if strings.TrimSpace(fm.Name) == "" {
		return frontmatter{}, errors.New("missing frontmatter name")
	}
	if strings.TrimSpace(fm.Description) == "" {
		return frontmatter{}, errors.New("missing frontmatter description")
	}
	return fm, nil
}

// NormalizeTarget validates a standard target token ("agents", "claude",
// "codex", "cursor", "gemini").
func NormalizeTarget(raw string) (Target, error) {
	t := Target(strings.ToLower(strings.TrimSpace(raw)))
	spec, ok := lookupTargetSpec(t)
	if !ok {
		return "", fmt.Errorf("%w: unknown skill target %q (expected one of: %s)", ErrInvalidTarget, raw, standardTargetNames())
	}
	return spec.target, nil
}

// NormalizeTargets validates and deduplicates standard targets in first-seen order.
func NormalizeTargets(raw []string) ([]Target, error) {
	out := make([]Target, 0, len(raw))
	for _, item := range raw {
		norm, err := NormalizeTarget(item)
		if err != nil {
			return nil, err
		}
		out = appendUnique(out, norm)
	}
	return out, nil
}

// ResolveStandardTargetDir resolves one validated standard Target against a
// non-empty baseDir via the target registry.
func ResolveStandardTargetDir(baseDir string, target Target) (string, error) {
	spec, ok := lookupTargetSpec(target)
	if !ok {
		return "", fmt.Errorf("%w: unknown skill target %q (expected one of: %s)", ErrInvalidTarget, target, standardTargetNames())
	}
	if strings.TrimSpace(baseDir) == "" {
		return "", errors.New("base directory is empty")
	}
	parts := make([]string, 0, 1+len(spec.relDirs))
	parts = append(parts, baseDir)
	parts = append(parts, spec.relDirs...)
	return filepath.Join(parts...), nil
}

// IsCustomTarget reports whether raw uses the local "dir:<path>" target prefix.
func IsCustomTarget(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "dir:")
}

// ResolveLocalTargetDir resolves a local target ("agents", "claude", "codex",
// "cursor", "gemini", or "dir:<path>") relative to baseDir for standard targets
// or CWD/home for "dir:<path>". It returns the resolved directory path and a
// normalized display label.
func ResolveLocalTargetDir(baseDir, raw string) (dir string, label string, err error) {
	trimmed := strings.TrimSpace(raw)
	if custom, ok := strings.CutPrefix(trimmed, "dir:"); ok {
		custom = strings.TrimSpace(custom)
		if custom == "" {
			return "", "", fmt.Errorf(`%w: "dir:" target needs a directory path`, ErrInvalidTarget)
		}
		if custom == "~" || strings.HasPrefix(custom, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(custom, `~\`)) {
			home, hErr := os.UserHomeDir()
			if hErr != nil {
				return "", "", fmt.Errorf("resolve home directory for %q: %w", raw, hErr)
			}
			custom = pathutil.ExpandHome(custom, home)
		}
		dir = filepath.Clean(custom)
		return dir, "dir:" + dir, nil
	}
	norm, err := NormalizeTarget(trimmed)
	if err != nil {
		return "", "", err
	}
	dir, err = ResolveStandardTargetDir(baseDir, norm)
	if err != nil {
		return "", "", err
	}
	return dir, string(norm), nil
}

// ResolveLocalTargetDirs validates every local target in raw upfront, resolves
// each directory path, and deduplicates resolved paths using pathutil.SamePath
// while preserving first-seen order.
func ResolveLocalTargetDirs(baseDir string, raw []string) (dirs []string, labels []string, err error) {
	dirs = make([]string, 0, len(raw))
	labels = make([]string, 0, len(raw))
	for _, item := range raw {
		dir, label, rErr := ResolveLocalTargetDir(baseDir, item)
		if rErr != nil {
			return nil, nil, rErr
		}
		duplicate := false
		for _, existing := range dirs {
			if pathutil.SamePath(existing, dir) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		dirs = append(dirs, dir)
		labels = append(labels, label)
	}
	return dirs, labels, nil
}

func validateTargetDir(targetDir string) error {
	fi, err := os.Stat(targetDir)
	if err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("skills directory %s is not a directory", targetDir)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect skills directory %s: %w", targetDir, err)
	}
	return nil
}

func openSkillRoot(targetRoot *os.Root, skillName string) (skillRoot *os.Root, exists bool, err error) {
	fi, err := targetRoot.Lstat(skillName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	fullPath := filepath.Join(targetRoot.Name(), skillName)
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, true, fmt.Errorf("%w: %s", ErrUnsafeSymlink, fullPath)
	}
	if !fi.IsDir() {
		return nil, true, fmt.Errorf("%w: non-directory skill path %s", ErrSkillUnmanaged, fullPath)
	}
	skillRoot, err = targetRoot.OpenRoot(skillName)
	if err != nil {
		return nil, true, err
	}
	return skillRoot, true, nil
}

func cleanManagedRelPath(rel string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if clean == "." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("invalid managed skill path %q", rel)
	}
	if !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("invalid managed skill path %q", rel)
	}
	return clean, nil
}

// checkSafeManagedInRoot verifies that rel is a clean local relative path
// inside skillRoot and that no intermediate directory or leaf entry (when
// present) is a symlink (including in-root relative symlinks) or has the wrong
// file type. It returns the clean OS-native relative path inside skillRoot.
func checkSafeManagedInRoot(skillRoot *os.Root, rel string) (string, error) {
	clean, err := cleanManagedRelPath(rel)
	if err != nil {
		return "", err
	}
	parts := strings.Split(clean, "/")
	cur := ""
	for i, part := range parts {
		if i == 0 {
			cur = part
		} else {
			cur = filepath.Join(cur, part)
		}
		fi, err := skillRoot.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			return "", err
		}
		fullPath := filepath.Join(skillRoot.Name(), cur)
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %s", ErrUnsafeSymlink, fullPath)
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return "", fmt.Errorf("%w: expected directory at %s", ErrSkillUnmanaged, fullPath)
		}
		if i == len(parts)-1 && !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%w: expected regular file at %s", ErrSkillUnmanaged, fullPath)
		}
	}
	return filepath.FromSlash(clean), nil
}

func readSafeManagedInRoot(skillRoot *os.Root, rel string) ([]byte, error) {
	osRel, err := checkSafeManagedInRoot(skillRoot, rel)
	if err != nil {
		return nil, err
	}
	return skillRoot.ReadFile(osRel)
}

func readSkillMarkerInRoot(skillRoot *os.Root, wantName string) (*skillMarker, error) {
	raw, err := readSafeManagedInRoot(skillRoot, MarkerFileName)
	if err != nil {
		return nil, err
	}
	var m skillMarker
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.ManagedBy != "leapmux" {
		return nil, fmt.Errorf("%w: invalid marker owner", ErrSkillUnmanaged)
	}
	if m.Name == "" || (wantName != "" && m.Name != wantName) {
		return nil, fmt.Errorf("%w: mismatched marker skill name %q", ErrSkillUnmanaged, m.Name)
	}
	if len(m.Files) == 0 {
		return nil, fmt.Errorf("%w: empty skill marker manifest", ErrSkillUnmanaged)
	}
	for rel := range m.Files {
		clean, cleanErr := cleanManagedRelPath(rel)
		if cleanErr != nil || clean == MarkerFileName {
			return nil, fmt.Errorf("%w: invalid marker file path %q", ErrSkillUnmanaged, rel)
		}
	}
	return &m, nil
}

func defaultSkillStatus(targetDir string, s Skill) SkillStatus {
	return SkillStatus{
		Name:           s.Name,
		Description:    s.Description,
		State:          StateNotInstalled,
		BundledVersion: BundledVersion(),
		Path:           filepath.Join(targetDir, s.Name, "SKILL.md"),
		References:     slices.Clone(s.References),
	}
}

func inspectSkillRoot(skillRoot *os.Root, s Skill, st SkillStatus) SkillStatus {
	entries, readDirErr := fs.ReadDir(skillRoot.FS(), ".")
	if readDirErr != nil {
		st.State = StateUnmanaged
		return st
	}
	if len(entries) == 0 {
		return st
	}

	marker, markerErr := readSkillMarkerInRoot(skillRoot, s.Name)
	if markerErr != nil {
		st.State = StateUnmanaged
		return st
	}

	tracked := make(map[string]bool, len(marker.Files))
	for rel := range marker.Files {
		if clean, err := cleanManagedRelPath(rel); err == nil {
			tracked[clean] = true
		}
	}

	// Scan every bundled file without returning early on hash/missing mismatch
	// so any unsafe symlink or non-regular file always classifies as unmanaged.
	bundleMatches := true
	newFileConflict := false
	diskHashes := make(map[string]string, len(s.Files))
	for _, f := range s.Files {
		diskBytes, err := readSafeManagedInRoot(skillRoot, f.RelPath)
		if err != nil {
			if errors.Is(err, ErrUnsafeSymlink) || errors.Is(err, ErrSkillUnmanaged) {
				st.State = StateUnmanaged
				return st
			}
			bundleMatches = false
			continue
		}
		h := sha256Hex(diskBytes)
		diskHashes[f.RelPath] = h
		if h != f.ContentHash {
			bundleMatches = false
			if !tracked[f.RelPath] {
				newFileConflict = true
			}
		}
	}

	st.InstalledVersion = marker.Version
	if bundleMatches {
		st.State = StateUpToDate
		return st
	}

	// Distinguish local edits from upstream bundle updates across every tracked
	// file. Scan all entries so unsafe paths deterministically return unmanaged.
	modified := false
	for rel, wantHash := range marker.Files {
		cleanRel, cleanErr := cleanManagedRelPath(rel)
		if cleanErr != nil {
			st.State = StateUnmanaged
			return st
		}
		if h, ok := diskHashes[cleanRel]; ok {
			if h != wantHash {
				modified = true
			}
			continue
		}
		diskBytes, err := readSafeManagedInRoot(skillRoot, cleanRel)
		if err != nil {
			if errors.Is(err, ErrUnsafeSymlink) || errors.Is(err, ErrSkillUnmanaged) {
				st.State = StateUnmanaged
				return st
			}
			modified = true
			continue
		}
		if sha256Hex(diskBytes) != wantHash {
			modified = true
		}
	}
	if modified || newFileConflict {
		st.State = StateModified
		return st
	}
	st.State = StateOutdated
	return st
}

func inspectInTargetRoot(targetRoot *os.Root, targetDir string, s Skill) SkillStatus {
	st := defaultSkillStatus(targetDir, s)
	if targetRoot == nil {
		return st
	}
	skillRoot, exists, dirErr := openSkillRoot(targetRoot, s.Name)
	if dirErr != nil {
		st.State = StateUnmanaged
		return st
	}
	if !exists {
		return st
	}
	defer func() { _ = skillRoot.Close() }()
	return inspectSkillRoot(skillRoot, s, st)
}

// InspectOne returns the installation status of one bundled skill in targetDir.
func InspectOne(targetDir string, s Skill) SkillStatus {
	targetRoot, err := os.OpenRoot(targetDir)
	if err != nil {
		st := defaultSkillStatus(targetDir, s)
		if !errors.Is(err, os.ErrNotExist) {
			st.State = StateUnmanaged
		}
		return st
	}
	defer func() { _ = targetRoot.Close() }()
	return inspectInTargetRoot(targetRoot, targetDir, s)
}

// InspectAll returns the status of every bundled skill in targetDir.
func InspectAll(targetDir string) []SkillStatus {
	src := loadBundledSkills()
	out := make([]SkillStatus, 0, len(src))
	targetRoot, err := os.OpenRoot(targetDir)
	if err != nil {
		state := StateNotInstalled
		if !errors.Is(err, os.ErrNotExist) {
			state = StateUnmanaged
		}
		for _, s := range src {
			st := defaultSkillStatus(targetDir, s)
			st.State = state
			out = append(out, st)
		}
		return out
	}
	defer func() { _ = targetRoot.Close() }()
	for _, s := range src {
		out = append(out, inspectInTargetRoot(targetRoot, targetDir, s))
	}
	return out
}

// Options controls Install and Uninstall behavior.
type Options struct {
	Force         bool
	SkipProtected bool
}

// Action describes the per-skill result of Install, Uninstall, or SyncEnabled.
type Action string

const (
	ActionInstalled        Action = "installed"
	ActionUpdated          Action = "updated"
	ActionUnchanged        Action = "unchanged"
	ActionSkippedModified  Action = "skipped_modified"
	ActionSkippedUnmanaged Action = "skipped_unmanaged"
	ActionRemoved          Action = "removed"
	ActionNotInstalled     Action = "not_installed"
)

// InstallOutcome reports what Install, Uninstall, or SyncEnabled did for one skill.
type InstallOutcome struct {
	Name   string `json:"name"`
	Action Action `json:"action"`
	Path   string `json:"path"`
}

type installPlan struct {
	skill      Skill
	action     Action
	path       string
	needsWrite bool
}

func planInstall(targetDir string, names []string, opts Options) ([]installPlan, bool, error) {
	selected, err := resolveSkillList(names)
	if err != nil {
		return nil, false, err
	}
	if err := validateTargetDir(targetDir); err != nil {
		return nil, false, err
	}

	var targetRoot *os.Root
	openedRoot, openErr := os.OpenRoot(targetDir)
	if openErr == nil {
		targetRoot = openedRoot
		defer func() { _ = targetRoot.Close() }()
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return nil, false, fmt.Errorf("open skills directory %s: %w", targetDir, openErr)
	}

	plans := make([]installPlan, 0, len(selected))
	anyWrite := false
	for _, s := range selected {
		st := inspectInTargetRoot(targetRoot, targetDir, s)
		p := installPlan{
			skill: s,
			path:  st.Path,
		}
		switch st.State {
		case StateUpToDate:
			p.action = ActionUnchanged
		case StateModified:
			if opts.Force {
				p.action = ActionUpdated
				p.needsWrite = true
			} else if opts.SkipProtected {
				p.action = ActionSkippedModified
			} else {
				return nil, false, fmt.Errorf("skill %q at %s: %w", s.Name, st.Path, ErrSkillModified)
			}
		case StateUnmanaged:
			if opts.Force {
				p.action = ActionUpdated
				p.needsWrite = true
			} else if opts.SkipProtected {
				p.action = ActionSkippedUnmanaged
			} else {
				return nil, false, fmt.Errorf("skill %q at %s: %w", s.Name, st.Path, ErrSkillUnmanaged)
			}
		case StateOutdated:
			p.action = ActionUpdated
			p.needsWrite = true
		case StateNotInstalled:
			p.action = ActionInstalled
			p.needsWrite = true
		}
		if p.needsWrite {
			if err := preflightWriteSkillInTargetRoot(targetRoot, targetDir, s); err != nil {
				return nil, false, err
			}
			anyWrite = true
		}
		plans = append(plans, p)
	}
	return plans, anyWrite, nil
}

func executeInstall(targetDir string, plans []installPlan, anyWrite bool) ([]InstallOutcome, error) {
	if anyWrite {
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return nil, fmt.Errorf("create skills directory %s: %w", targetDir, err)
		}
	}
	outcomes := make([]InstallOutcome, 0, len(plans))
	for _, p := range plans {
		if p.needsWrite {
			if err := writeSkillFiles(targetDir, p.skill); err != nil {
				return nil, err
			}
		}
		outcomes = append(outcomes, InstallOutcome{Name: p.skill.Name, Action: p.action, Path: p.path})
	}
	return outcomes, nil
}

// Install writes the requested bundled skills into targetDir. When opts.Force
// is false, locally modified or unmanaged skills return an error unless
// opts.SkipProtected is true, in which case they are skipped. All skills in the
// batch pass preflight validation before any disk mutation runs.
func Install(targetDir string, names []string, opts Options) ([]InstallOutcome, error) {
	plans, anyWrite, err := planInstall(targetDir, names, opts)
	if err != nil {
		return nil, err
	}
	return executeInstall(targetDir, plans, anyWrite)
}

type uninstallPlan struct {
	skill       Skill
	action      Action
	path        string
	remove      bool
	forceRemove bool
	managedRels []string
}

func planUninstallOne(targetRoot *os.Root, targetDir string, s Skill, opts Options) (uninstallPlan, error) {
	skillDir := filepath.Join(targetDir, s.Name)
	st := defaultSkillStatus(targetDir, s)

	skillRoot, exists, statErr := openSkillRoot(targetRoot, s.Name)
	if statErr != nil {
		if opts.SkipProtected && !opts.Force {
			return uninstallPlan{skill: s, action: ActionSkippedUnmanaged, path: st.Path}, nil
		}
		return uninstallPlan{}, fmt.Errorf("refusing to remove %s: %w", skillDir, statErr)
	}
	if !exists {
		return uninstallPlan{skill: s, action: ActionNotInstalled, path: st.Path}, nil
	}
	defer func() { _ = skillRoot.Close() }()

	st = inspectSkillRoot(skillRoot, s, st)
	switch st.State {
	case StateNotInstalled:
		return uninstallPlan{skill: s, action: ActionNotInstalled, path: st.Path}, nil
	case StateModified:
		if !opts.Force {
			if opts.SkipProtected {
				return uninstallPlan{skill: s, action: ActionSkippedModified, path: st.Path}, nil
			}
			return uninstallPlan{}, fmt.Errorf("skill %q at %s: %w", s.Name, st.Path, ErrSkillModified)
		}
		return uninstallPlan{skill: s, action: ActionRemoved, path: st.Path, remove: true, forceRemove: true}, nil
	case StateUnmanaged:
		if !opts.Force {
			if opts.SkipProtected {
				return uninstallPlan{skill: s, action: ActionSkippedUnmanaged, path: st.Path}, nil
			}
			return uninstallPlan{}, fmt.Errorf("skill %q at %s: %w", s.Name, st.Path, ErrSkillUnmanaged)
		}
		return uninstallPlan{skill: s, action: ActionRemoved, path: st.Path, remove: true, forceRemove: true}, nil
	case StateUpToDate, StateOutdated:
		if opts.Force {
			return uninstallPlan{skill: s, action: ActionRemoved, path: st.Path, remove: true, forceRemove: true}, nil
		}
		marker, mErr := readSkillMarkerInRoot(skillRoot, s.Name)
		if mErr != nil {
			return uninstallPlan{}, fmt.Errorf("read marker for %q: %w", s.Name, mErr)
		}
		if _, mPathErr := checkSafeManagedInRoot(skillRoot, MarkerFileName); mPathErr != nil {
			return uninstallPlan{}, fmt.Errorf("unsafe marker path for %q: %w", s.Name, mPathErr)
		}
		managedRels := make([]string, 0, len(marker.Files))
		for rel := range marker.Files {
			osRel, fErr := checkSafeManagedInRoot(skillRoot, rel)
			if fErr != nil {
				return uninstallPlan{}, fmt.Errorf("unsafe managed file %q for %q: %w", rel, s.Name, fErr)
			}
			managedRels = appendUnique(managedRels, osRel)
		}
		return uninstallPlan{
			skill:       s,
			action:      ActionRemoved,
			path:        st.Path,
			remove:      true,
			managedRels: managedRels,
		}, nil
	default:
		return uninstallPlan{}, fmt.Errorf("unexpected skill state %q for %q", st.State, s.Name)
	}
}

func planUninstall(targetDir string, names []string, opts Options) ([]uninstallPlan, error) {
	selected, err := resolveSkillList(names)
	if err != nil {
		return nil, err
	}

	targetRoot, openErr := os.OpenRoot(targetDir)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			plans := make([]uninstallPlan, 0, len(selected))
			for _, s := range selected {
				plans = append(plans, uninstallPlan{
					skill:  s,
					action: ActionNotInstalled,
					path:   filepath.Join(targetDir, s.Name, "SKILL.md"),
				})
			}
			return plans, nil
		}
		return nil, fmt.Errorf("open skills directory %s: %w", targetDir, openErr)
	}
	defer func() { _ = targetRoot.Close() }()

	plans := make([]uninstallPlan, 0, len(selected))
	for _, s := range selected {
		p, pErr := planUninstallOne(targetRoot, targetDir, s, opts)
		if pErr != nil {
			return nil, pErr
		}
		plans = append(plans, p)
	}
	return plans, nil
}

func executeUninstall(targetDir string, plans []uninstallPlan) ([]InstallOutcome, error) {
	anyRemove := false
	for _, p := range plans {
		if p.remove {
			anyRemove = true
			break
		}
	}

	var targetRoot *os.Root
	if anyRemove {
		var openErr error
		targetRoot, openErr = os.OpenRoot(targetDir)
		if openErr != nil {
			return nil, fmt.Errorf("open skills directory %s: %w", targetDir, openErr)
		}
		defer func() { _ = targetRoot.Close() }()
	}

	outcomes := make([]InstallOutcome, 0, len(plans))
	for _, p := range plans {
		if p.remove {
			if p.forceRemove {
				if err := removeAllForceInRoot(targetRoot, p.skill.Name); err != nil {
					return nil, fmt.Errorf("remove skill %q: %w", p.skill.Name, err)
				}
			} else if err := removeManagedSkillInRoot(targetRoot, p); err != nil {
				return nil, err
			}
		}
		outcomes = append(outcomes, InstallOutcome{Name: p.skill.Name, Action: p.action, Path: p.path})
	}
	return outcomes, nil
}

func removeManagedSkillInRoot(targetRoot *os.Root, p uninstallPlan) error {
	skillRoot, exists, err := openSkillRoot(targetRoot, p.skill.Name)
	if err != nil {
		return fmt.Errorf("open skill %q for removal: %w", p.skill.Name, err)
	}
	if !exists {
		return nil
	}
	for _, osRel := range p.managedRels {
		if rmErr := skillRoot.Remove(osRel); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			_ = skillRoot.Close()
			return fmt.Errorf("remove managed file %s for %q: %w", osRel, p.skill.Name, rmErr)
		}
		pruneEmptyParentsInRoot(skillRoot, filepath.Dir(osRel))
	}
	if rmErr := skillRoot.Remove(MarkerFileName); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		_ = skillRoot.Close()
		return fmt.Errorf("remove marker for %q: %w", p.skill.Name, rmErr)
	}
	_ = skillRoot.Close()

	if rmErr := targetRoot.Remove(p.skill.Name); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		// Keep the directory intact if user-added files remain inside it.
		checkRoot, checkExists, checkErr := openSkillRoot(targetRoot, p.skill.Name)
		if checkErr != nil {
			return fmt.Errorf("remove skill directory %s: %w", filepath.Join(targetRoot.Name(), p.skill.Name), rmErr)
		}
		if checkExists {
			entries, readErr := fs.ReadDir(checkRoot.FS(), ".")
			_ = checkRoot.Close()
			if readErr != nil || len(entries) == 0 {
				return fmt.Errorf("remove skill directory %s: %w", filepath.Join(targetRoot.Name(), p.skill.Name), rmErr)
			}
		}
	}
	return nil
}

// Uninstall removes the requested LeapMux-managed skills from targetDir. When
// opts.Force is false, only files tracked in the skill's manifest (plus the
// marker and empty subdirectories) are removed so user-added files survive. All
// skills in the batch pass preflight validation before any disk mutation runs.
func Uninstall(targetDir string, names []string, opts Options) ([]InstallOutcome, error) {
	plans, err := planUninstall(targetDir, names, opts)
	if err != nil {
		return nil, err
	}
	return executeUninstall(targetDir, plans)
}

// SyncEnabled ensures every enabled skill is installed and up to date in
// targetDir without overwriting locally modified or unmanaged skills (unless
// force is true). It never removes disabled skills on its own — callers that
// disable skills explicitly pass newlyDisabled to Uninstall.
func SyncEnabled(targetDir string, enabled []string, force bool) ([]InstallOutcome, error) {
	if len(enabled) == 0 {
		return nil, nil
	}
	return Install(targetDir, enabled, Options{Force: force, SkipProtected: !force})
}

func resolveSkillList(names []string) ([]Skill, error) {
	if len(names) == 0 {
		return nil, errors.New("at least one skill name is required")
	}
	seen := make(map[string]bool, len(names))
	out := make([]Skill, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		s, ok := Lookup(name)
		if !ok {
			return nil, fmt.Errorf("%w %q (available: %s)", ErrUnknownSkill, name, strings.Join(Names(), ", "))
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one skill name is required")
	}
	return out, nil
}

func preflightWriteSkillInTargetRoot(targetRoot *os.Root, targetDir string, s Skill) error {
	for _, f := range s.Files {
		if _, err := cleanManagedRelPath(f.RelPath); err != nil {
			return fmt.Errorf("unsafe path for skill %s (%s): %w", s.Name, f.RelPath, err)
		}
	}
	if targetRoot == nil {
		return nil
	}
	skillRoot, exists, err := openSkillRoot(targetRoot, s.Name)
	if err != nil {
		return fmt.Errorf("refusing to write into %s: %w", filepath.Join(targetDir, s.Name), err)
	}
	if !exists {
		return nil
	}
	defer func() { _ = skillRoot.Close() }()
	_, err = preflightWriteSkillInRoot(skillRoot, s)
	return err
}

func preflightWriteSkillInRoot(skillRoot *os.Root, s Skill) ([]string, error) {
	relPaths := make([]string, len(s.Files))
	for i, f := range s.Files {
		osRel, fErr := checkSafeManagedInRoot(skillRoot, f.RelPath)
		if fErr != nil {
			return nil, fmt.Errorf("unsafe path for skill %s (%s): %w", s.Name, f.RelPath, fErr)
		}
		relPaths[i] = osRel
	}
	if _, err := checkSafeManagedInRoot(skillRoot, MarkerFileName); err != nil {
		return nil, fmt.Errorf("unsafe marker path for %s: %w", s.Name, err)
	}
	return relPaths, nil
}

func writeSkillFiles(targetDir string, s Skill) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create skills directory %s: %w", targetDir, err)
	}
	targetRoot, err := os.OpenRoot(targetDir)
	if err != nil {
		return fmt.Errorf("open skills directory %s: %w", targetDir, err)
	}
	defer func() { _ = targetRoot.Close() }()

	skillDir := filepath.Join(targetDir, s.Name)
	skillRoot, exists, err := openSkillRoot(targetRoot, s.Name)
	if err != nil {
		return fmt.Errorf("refusing to write into %s: %w", skillDir, err)
	}
	if !exists {
		if err := targetRoot.Mkdir(s.Name, 0o755); err != nil {
			return fmt.Errorf("create skill directory %s: %w", skillDir, err)
		}
		skillRoot, err = targetRoot.OpenRoot(s.Name)
		if err != nil {
			return fmt.Errorf("open skill directory %s: %w", skillDir, err)
		}
	}
	defer func() { _ = skillRoot.Close() }()

	relPaths, err := preflightWriteSkillInRoot(skillRoot, s)
	if err != nil {
		return err
	}

	// Capture any existing valid LeapMux marker before writing so retired
	// managed files can be cleaned up without touching user-edited files.
	prevMarker, _ := readSkillMarkerInRoot(skillRoot, s.Name)

	nextHashes := make(map[string]string, len(s.Files))
	for i, f := range s.Files {
		osRel := relPaths[i]
		nextHashes[f.RelPath] = f.ContentHash
		if existing, rErr := skillRoot.ReadFile(osRel); rErr == nil && sha256Hex(existing) == f.ContentHash {
			continue
		}
		if parent := filepath.Dir(osRel); parent != "." {
			if err := skillRoot.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("create parent directory for %s/%s: %w", s.Name, f.RelPath, err)
			}
		}
		if err := atomicfile.WriteFileInRoot(skillRoot, osRel, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("write %s for %s: %w", f.RelPath, s.Name, err)
		}
	}

	// Commit the new marker before pruning retired files so any failure during
	// cleanup never leaves a stale manifest that classifies the skill as modified.
	if err := writeJSONAtomicInRoot(skillRoot, MarkerFileName, skillMarker{
		Name:        s.Name,
		ManagedBy:   "leapmux",
		Version:     BundledVersion(),
		ContentHash: s.ContentHash,
		Files:       nextHashes,
	}, 0o644); err != nil {
		return fmt.Errorf("write marker for %s: %w", s.Name, err)
	}

	if prevMarker != nil && len(prevMarker.Files) > 0 {
		for rel, prevHash := range prevMarker.Files {
			cleanRel, cleanErr := cleanManagedRelPath(rel)
			if cleanErr != nil {
				continue
			}
			if _, keep := nextHashes[cleanRel]; keep {
				continue
			}
			osRel, fErr := checkSafeManagedInRoot(skillRoot, cleanRel)
			if fErr != nil {
				continue
			}
			diskBytes, rErr := skillRoot.ReadFile(osRel)
			if rErr != nil || sha256Hex(diskBytes) != prevHash {
				continue
			}
			if err := skillRoot.Remove(osRel); err == nil {
				pruneEmptyParentsInRoot(skillRoot, filepath.Dir(osRel))
			}
		}
	}
	return nil
}

func writeJSONAtomic(filePath string, v any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicfile.WriteFile(filePath, raw, mode)
}

func writeJSONAtomicInRoot(root *os.Root, rel string, v any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicfile.WriteFileInRoot(root, rel, raw, mode)
}

func appendUnique[T comparable](dst []T, item T) []T {
	if slices.Contains(dst, item) {
		return dst
	}
	return append(dst, item)
}

func mergeUnique[T comparable](lists ...[]T) []T {
	total := 0
	for _, list := range lists {
		total += len(list)
	}
	out := make([]T, 0, total)
	for _, list := range lists {
		for _, item := range list {
			out = appendUnique(out, item)
		}
	}
	return out
}

func removeAllForceInRoot(targetRoot *os.Root, name string) error {
	if err := targetRoot.RemoveAll(name); err == nil {
		return nil
	}
	// Make non-symlink files and directories writable (needed on Windows for
	// read-only files and on POSIX for read-only directories) before retrying.
	if fi, lErr := targetRoot.Lstat(name); lErr == nil && fi.Mode()&os.ModeSymlink == 0 && fi.IsDir() {
		_ = targetRoot.Chmod(name, fi.Mode()|0o700)
	}
	if skillRoot, err := targetRoot.OpenRoot(name); err == nil {
		_ = fs.WalkDir(skillRoot.FS(), ".", func(p string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				if p != "." {
					osRel := filepath.FromSlash(p)
					if fi, lErr := skillRoot.Lstat(osRel); lErr == nil && fi.Mode()&os.ModeSymlink == 0 && fi.IsDir() {
						_ = skillRoot.Chmod(osRel, fi.Mode()|0o700)
					}
				}
				return nil
			}
			if p == "." || d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			osRel := filepath.FromSlash(p)
			fi, lErr := skillRoot.Lstat(osRel)
			if lErr != nil || fi.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			if fi.IsDir() {
				_ = skillRoot.Chmod(osRel, fi.Mode()|0o700)
			} else {
				_ = skillRoot.Chmod(osRel, fi.Mode()|0o200)
			}
			return nil
		})
		_ = skillRoot.Close()
	}
	return targetRoot.RemoveAll(name)
}

func pruneEmptyParentsInRoot(skillRoot *os.Root, dir string) {
	for cur := filepath.Clean(dir); cur != "." && cur != "" && filepath.IsLocal(cur); cur = filepath.Dir(cur) {
		if err := skillRoot.Remove(cur); err != nil {
			return
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return
		}
	}
}
