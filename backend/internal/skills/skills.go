// Package skills embeds LeapMux's curated Agent Skills catalog and manages
// safe installation, status inspection, drift detection, and Worker startup
// synchronization across standard coding-agent skill directories.
package skills

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/version"
)

//go:embed catalog
var catalogFS embed.FS

// MarkerFileName records LeapMux ownership and the installed per-file hashes
// inside each managed skill directory so upgrades never overwrite user edits.
const MarkerFileName = ".leapmux-skill.json"

// Target identifiers supported by ResolveTargetDir and Worker sync configs.
const (
	TargetAgents = "agents"
	TargetClaude = "claude"
	TargetCodex  = "codex"
	TargetCursor = "cursor"
	TargetGemini = "gemini"
)

// AllStandardTargets lists the supported standard skill targets in display order.
var AllStandardTargets = []string{TargetAgents, TargetClaude, TargetCodex, TargetCursor, TargetGemini}

// State describes the on-disk installation state of a bundled skill.
type State string

const (
	StateNotInstalled State = "not_installed"
	StateUpToDate     State = "up_to_date"
	StateOutdated     State = "outdated"
	StateModified     State = "modified"
	StateUnmanaged    State = "unmanaged"
)

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
	Content       string      `json:"-"`
	ContentHash   string      `json:"content_hash"`
	Files         []SkillFile `json:"-"`
	References    []string    `json:"references,omitempty"`
}

// Reference looks up one companion Markdown reference under references/.
// It accepts a bare name ("review-schemas"), filename ("review-schemas.md"),
// or relative path ("references/review-schemas.md").
func (s Skill) Reference(name string) (SkillFile, bool) {
	norm := strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	norm = strings.TrimPrefix(norm, "./")
	norm = strings.TrimPrefix(norm, "references/")
	norm = strings.TrimSuffix(norm, ".md")
	if norm == "" || strings.Contains(norm, "/") || strings.Contains(norm, "..") {
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

var bundledSkills = loadCatalog()

// Catalog returns a copy of all bundled skills in canonical order (`leapmux`
// first, then alphabetical).
func Catalog() []Skill {
	out := make([]Skill, len(bundledSkills))
	copy(out, bundledSkills)
	return out
}

// Lookup finds one bundled skill by exact name.
func Lookup(name string) (Skill, bool) {
	want := strings.TrimSpace(name)
	for _, s := range bundledSkills {
		if s.Name == want {
			return s, true
		}
	}
	return Skill{}, false
}

// Names returns the canonical list of bundled skill names.
func Names() []string {
	out := make([]string, len(bundledSkills))
	for i, s := range bundledSkills {
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

func loadCatalog() []Skill {
	entries, err := fs.ReadDir(catalogFS, "catalog")
	if err != nil {
		panic(fmt.Sprintf("skills: read catalog dir: %v", err))
	}
	out := make([]Skill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirName := e.Name()
		skillRoot := path.Join("catalog", dirName)
		skillMDPath := path.Join(skillRoot, "SKILL.md")
		rawSkillMD, err := catalogFS.ReadFile(skillMDPath)
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
		err = fs.WalkDir(catalogFS, skillRoot, func(p string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			if d.IsDir() {
				if p != skillRoot && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel := strings.TrimPrefix(p, skillRoot+"/")
			data, rErr := catalogFS.ReadFile(p)
			if rErr != nil {
				return rErr
			}
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
			Content:       string(rawSkillMD),
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
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return frontmatter{}, errors.New("missing YAML frontmatter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return frontmatter{}, errors.New("unterminated YAML frontmatter")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
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
// "codex", "cursor", "gemini"). Empty input defaults to "agents".
func NormalizeTarget(raw string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" || t == "auto" {
		return TargetAgents, nil
	}
	if !slices.Contains(AllStandardTargets, t) {
		return "", fmt.Errorf("unknown skill target %q (expected one of: %s)", raw, strings.Join(AllStandardTargets, ", "))
	}
	return t, nil
}

// ResolveTargetDir resolves a target ("agents", "claude", "codex", "cursor",
// "gemini", or "dir:<path>") relative to baseDir (the user's home directory
// for user scope, or project root for project scope).
func ResolveTargetDir(baseDir, target string) (string, error) {
	raw := strings.TrimSpace(target)
	if custom, ok := strings.CutPrefix(raw, "dir:"); ok {
		custom = strings.TrimSpace(custom)
		if custom == "" {
			return "", errors.New(`"dir:" target needs a directory path`)
		}
		if strings.HasPrefix(custom, "~/") && baseDir != "" {
			custom = filepath.Join(baseDir, custom[2:])
		}
		return filepath.Clean(custom), nil
	}
	norm, err := NormalizeTarget(raw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(baseDir) == "" {
		return "", errors.New("base directory is empty")
	}
	switch norm {
	case TargetAgents:
		return filepath.Join(baseDir, ".agents", "skills"), nil
	case TargetClaude:
		return filepath.Join(baseDir, ".claude", "skills"), nil
	case TargetCodex:
		return filepath.Join(baseDir, ".codex", "skills"), nil
	case TargetCursor:
		return filepath.Join(baseDir, ".cursor", "skills"), nil
	case TargetGemini:
		return filepath.Join(baseDir, ".gemini", "skills"), nil
	default:
		return "", fmt.Errorf("unsupported skill target %q", norm)
	}
}

var errUnsafeSymlink = errors.New("symlink inside managed skill directory")

// checkSafeManagedFile verifies that rel is a clean relative path inside
// skillDir and that neither skillDir, any intermediate directory, nor the
// final file (when present) is a symlink.
func checkSafeManagedFile(skillDir, rel string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("invalid managed skill path %q", rel)
	}
	if fi, err := os.Lstat(skillDir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return "", fmt.Errorf("%w: %s", errUnsafeSymlink, skillDir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parts := strings.Split(clean, "/")
	cur := skillDir
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %s", errUnsafeSymlink, cur)
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return "", fmt.Errorf("expected directory at %s", cur)
		}
		if i == len(parts)-1 && fi.IsDir() {
			return "", fmt.Errorf("expected file at %s", cur)
		}
	}
	return filepath.Join(skillDir, filepath.FromSlash(clean)), nil
}

func readSafeManagedFile(skillDir, rel string) ([]byte, error) {
	full, err := checkSafeManagedFile(skillDir, rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

func readSkillMarker(skillDir string) (*skillMarker, error) {
	raw, err := readSafeManagedFile(skillDir, MarkerFileName)
	if err != nil {
		return nil, err
	}
	var m skillMarker
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.ManagedBy != "leapmux" {
		return nil, errors.New("unmanaged skill marker")
	}
	return &m, nil
}

// InspectOne returns the installation status of one bundled skill in targetDir.
func InspectOne(targetDir string, s Skill) SkillStatus {
	skillDir := filepath.Join(targetDir, s.Name)
	skillFile := filepath.Join(skillDir, "SKILL.md")
	st := SkillStatus{
		Name:           s.Name,
		Description:    s.Description,
		State:          StateNotInstalled,
		BundledVersion: BundledVersion(),
		Path:           skillFile,
		References:     slices.Clone(s.References),
	}

	dirInfo, dirErr := os.Lstat(skillDir)
	if dirErr != nil {
		if errors.Is(dirErr, os.ErrNotExist) {
			return st
		}
		st.State = StateUnmanaged
		return st
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		st.State = StateUnmanaged
		return st
	}

	skillMDBytes, skillMDErr := readSafeManagedFile(skillDir, "SKILL.md")
	marker, markerErr := readSkillMarker(skillDir)
	if errors.Is(skillMDErr, os.ErrNotExist) && errors.Is(markerErr, os.ErrNotExist) {
		return st
	}
	if errors.Is(skillMDErr, errUnsafeSymlink) || errors.Is(markerErr, errUnsafeSymlink) {
		st.State = StateUnmanaged
		return st
	}

	bundleMatches := true
	for _, f := range s.Files {
		diskBytes, err := readSafeManagedFile(skillDir, f.RelPath)
		if err != nil {
			if errors.Is(err, errUnsafeSymlink) {
				st.State = StateUnmanaged
				return st
			}
			bundleMatches = false
			break
		}
		if sha256Hex(diskBytes) != f.ContentHash {
			bundleMatches = false
			break
		}
	}

	if markerErr != nil {
		if bundleMatches {
			st.State = StateUpToDate
		} else {
			st.State = StateUnmanaged
		}
		return st
	}

	st.InstalledVersion = marker.Version
	if bundleMatches {
		st.State = StateUpToDate
		return st
	}

	// Distinguish local edits from upstream bundle updates.
	if len(marker.Files) > 0 {
		for rel, wantHash := range marker.Files {
			diskBytes, err := readSafeManagedFile(skillDir, rel)
			if err != nil || sha256Hex(diskBytes) != wantHash {
				st.State = StateModified
				return st
			}
		}
		st.State = StateOutdated
		return st
	}

	// Backwards compatibility with v1 markers that hashed SKILL.md alone.
	if skillMDErr != nil {
		st.State = StateModified
		return st
	}
	if marker.ContentHash != "" && sha256Hex(skillMDBytes) != marker.ContentHash {
		st.State = StateModified
		return st
	}
	st.State = StateOutdated
	return st
}

// InspectAll returns the status of every bundled skill in targetDir.
func InspectAll(targetDir string) []SkillStatus {
	out := make([]SkillStatus, 0, len(bundledSkills))
	for _, s := range bundledSkills {
		out = append(out, InspectOne(targetDir, s))
	}
	return out
}

// InstallOptions controls Install behavior.
type InstallOptions struct {
	Force bool
}

// InstallOutcome reports what Install did for one skill.
type InstallOutcome struct {
	Name   string `json:"name"`
	Action string `json:"action"` // installed, updated, unchanged, skipped_modified, skipped_unmanaged
	Path   string `json:"path"`
}

// Install writes the requested bundled skills into targetDir. If force is false,
// locally modified or unmanaged skills return an error (or are skipped when
// SyncEnabled is used).
func Install(targetDir string, names []string, opts InstallOptions) ([]InstallOutcome, error) {
	selected, err := resolveSkillList(names)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("create skills directory %s: %w", targetDir, err)
	}
	outcomes := make([]InstallOutcome, 0, len(selected))
	for _, s := range selected {
		st := InspectOne(targetDir, s)
		switch st.State {
		case StateUpToDate:
			// Ensure marker exists if the files matched byte-for-byte without one.
			if st.InstalledVersion == "" {
				if err := writeSkillFiles(targetDir, s); err != nil {
					return nil, err
				}
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "unchanged", Path: st.Path})
		case StateModified:
			if !opts.Force {
				return nil, fmt.Errorf("skill %q at %s has local edits (pass --force to overwrite)", s.Name, st.Path)
			}
			if err := writeSkillFiles(targetDir, s); err != nil {
				return nil, err
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "updated", Path: st.Path})
		case StateUnmanaged:
			if !opts.Force {
				return nil, fmt.Errorf("skill %q at %s is not managed by LeapMux (pass --force to overwrite)", s.Name, st.Path)
			}
			if err := writeSkillFiles(targetDir, s); err != nil {
				return nil, err
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "updated", Path: st.Path})
		case StateOutdated:
			if err := writeSkillFiles(targetDir, s); err != nil {
				return nil, err
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "updated", Path: st.Path})
		case StateNotInstalled:
			if err := writeSkillFiles(targetDir, s); err != nil {
				return nil, err
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "installed", Path: st.Path})
		}
	}
	return outcomes, nil
}

// Uninstall removes the requested LeapMux-managed skills from targetDir.
func Uninstall(targetDir string, names []string, opts InstallOptions) ([]InstallOutcome, error) {
	selected, err := resolveSkillList(names)
	if err != nil {
		return nil, err
	}
	outcomes := make([]InstallOutcome, 0, len(selected))
	for _, s := range selected {
		skillDir := filepath.Join(targetDir, s.Name)
		if fi, lErr := os.Lstat(skillDir); lErr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing to remove symlinked skill directory %s", skillDir)
		}
		st := InspectOne(targetDir, s)
		if st.State == StateNotInstalled {
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "not_installed", Path: st.Path})
			continue
		}
		if (st.State == StateModified || st.State == StateUnmanaged) && !opts.Force {
			return nil, fmt.Errorf("skill %q at %s has local edits or is unmanaged (pass --force to remove)", s.Name, st.Path)
		}
		if err := os.RemoveAll(skillDir); err != nil {
			return nil, fmt.Errorf("remove skill %q: %w", s.Name, err)
		}
		outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "removed", Path: st.Path})
	}
	return outcomes, nil
}

// SyncEnabled ensures every enabled skill is installed and up to date in
// targetDir without overwriting locally modified or unmanaged skills (unless
// force is true). It never removes disabled skills on its own — callers that
// disable skills explicitly pass newlyDisabled to Uninstall.
func SyncEnabled(targetDir string, enabled []string, force bool) ([]InstallOutcome, error) {
	if len(enabled) == 0 {
		return nil, nil
	}
	selected, err := resolveSkillList(enabled)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("create skills directory %s: %w", targetDir, err)
	}
	outcomes := make([]InstallOutcome, 0, len(selected))
	for _, s := range selected {
		st := InspectOne(targetDir, s)
		switch st.State {
		case StateUpToDate:
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "unchanged", Path: st.Path})
		case StateModified:
			if !force {
				outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "skipped_modified", Path: st.Path})
				continue
			}
			if err := writeSkillFiles(targetDir, s); err != nil {
				return nil, err
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "updated", Path: st.Path})
		case StateUnmanaged:
			if !force {
				outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "skipped_unmanaged", Path: st.Path})
				continue
			}
			if err := writeSkillFiles(targetDir, s); err != nil {
				return nil, err
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "updated", Path: st.Path})
		case StateOutdated:
			if err := writeSkillFiles(targetDir, s); err != nil {
				return nil, err
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "updated", Path: st.Path})
		case StateNotInstalled:
			if err := writeSkillFiles(targetDir, s); err != nil {
				return nil, err
			}
			outcomes = append(outcomes, InstallOutcome{Name: s.Name, Action: "installed", Path: st.Path})
		}
	}
	return outcomes, nil
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
			return nil, fmt.Errorf("unknown skill %q (available: %s)", name, strings.Join(Names(), ", "))
		}
		seen[name] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one skill name is required")
	}
	return out, nil
}

func writeSkillFiles(targetDir string, s Skill) error {
	skillDir := filepath.Join(targetDir, s.Name)
	if fi, err := os.Lstat(skillDir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("refusing to write into symlinked or non-directory skill path %s", skillDir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// Capture any existing valid LeapMux marker before writing so retired
	// managed files can be cleaned up without touching user-edited files.
	prevMarker, _ := readSkillMarker(skillDir)

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create skill directory %s: %w", skillDir, err)
	}

	nextHashes := make(map[string]string, len(s.Files))
	for _, f := range s.Files {
		fullPath, err := checkSafeManagedFile(skillDir, f.RelPath)
		if err != nil {
			return fmt.Errorf("unsafe path for skill %s (%s): %w", s.Name, f.RelPath, err)
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return fmt.Errorf("create parent directory for %s/%s: %w", s.Name, f.RelPath, err)
		}
		if err := atomicfile.WriteFile(fullPath, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("write %s for %s: %w", f.RelPath, s.Name, err)
		}
		nextHashes[f.RelPath] = f.ContentHash
	}

	if prevMarker != nil && len(prevMarker.Files) > 0 {
		for rel, prevHash := range prevMarker.Files {
			if _, keep := nextHashes[rel]; keep {
				continue
			}
			fullPath, err := checkSafeManagedFile(skillDir, rel)
			if err != nil {
				continue
			}
			diskBytes, err := os.ReadFile(fullPath)
			if err != nil || sha256Hex(diskBytes) != prevHash {
				continue
			}
			if err := os.Remove(fullPath); err == nil {
				pruneEmptyParents(skillDir, filepath.Dir(fullPath))
			}
		}
	}

	markerPath, err := checkSafeManagedFile(skillDir, MarkerFileName)
	if err != nil {
		return fmt.Errorf("unsafe marker path for %s: %w", s.Name, err)
	}
	marker, err := json.MarshalIndent(skillMarker{
		Name:        s.Name,
		ManagedBy:   "leapmux",
		Version:     BundledVersion(),
		ContentHash: s.ContentHash,
		Files:       nextHashes,
	}, "", "  ")
	if err != nil {
		return err
	}
	marker = append(marker, '\n')
	if err := atomicfile.WriteFile(markerPath, marker, 0o644); err != nil {
		return fmt.Errorf("write marker for %s: %w", s.Name, err)
	}
	return nil
}

func pruneEmptyParents(stopDir, dir string) {
	stopClean := filepath.Clean(stopDir)
	for cur := filepath.Clean(dir); cur != stopClean && strings.HasPrefix(cur, stopClean+string(filepath.Separator)); cur = filepath.Dir(cur) {
		if err := os.Remove(cur); err != nil {
			return
		}
	}
}

// VerifyDirectoryMatchesCatalog compares every file under repoSkillsDir against
// the embedded catalog, returning an error if any skill or file is missing,
// extra, or differs in bytes.
func VerifyDirectoryMatchesCatalog(repoSkillsDir string) error {
	catalog := Catalog()
	seenSkills := make(map[string]bool, len(catalog))
	for _, s := range catalog {
		seenSkills[s.Name] = true
		skillRoot := filepath.Join(repoSkillsDir, s.Name)
		expected := make(map[string][]byte, len(s.Files))
		for _, f := range s.Files {
			expected[f.RelPath] = []byte(f.Content)
		}
		found := make(map[string]bool, len(s.Files))
		err := filepath.WalkDir(skillRoot, func(p string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			if d.IsDir() {
				if p != skillRoot && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel, rErr := filepath.Rel(skillRoot, p)
			if rErr != nil {
				return rErr
			}
			rel = filepath.ToSlash(rel)
			want, ok := expected[rel]
			if !ok {
				return fmt.Errorf("repo skill %q has extra file %s missing from embedded catalog", s.Name, rel)
			}
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return fmt.Errorf("read repo skill file %s: %w", p, readErr)
			}
			if !bytes.Equal(raw, want) {
				return fmt.Errorf("embedded catalog for %q (%s) differs from %s", s.Name, rel, p)
			}
			found[rel] = true
			return nil
		})
		if err != nil {
			return err
		}
		for rel := range expected {
			if !found[rel] {
				return fmt.Errorf("embedded catalog file %q/%s is missing from %s", s.Name, rel, skillRoot)
			}
		}
	}
	entries, err := os.ReadDir(repoSkillsDir)
	if err != nil {
		return fmt.Errorf("read repo skills dir %s: %w", repoSkillsDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !seenSkills[e.Name()] {
			return fmt.Errorf("repo skill %q is missing from embedded backend/internal/skills/catalog", e.Name())
		}
	}
	return nil
}
