package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// File status values reported in SkillFileDiff.Status.
const (
	FileDiffAdded     = "added"
	FileDiffRemoved   = "removed"
	FileDiffModified  = "modified"
	FileDiffUntracked = "untracked"
)

// SkillFileDiff describes the difference between the on-disk copy and the
// bundled catalog for one file inside a skill directory.
type SkillFileDiff struct {
	RelPath string `json:"rel_path"`
	Status  string `json:"status"`
	Diff    string `json:"diff,omitempty"`
}

// SkillDiff describes the on-disk state and per-file unified diffs of one
// bundled skill in a target directory.
type SkillDiff struct {
	Name   string          `json:"name"`
	Target string          `json:"target,omitempty"`
	State  State           `json:"state"`
	Path   string          `json:"path"`
	Files  []SkillFileDiff `json:"files,omitempty"`
}

// DiffOptions controls DiffAll behavior.
type DiffOptions struct {
	// SkipNotInstalled omits skills whose on-disk state is StateNotInstalled
	// from the returned slice without generating synthetic /dev/null diffs.
	SkipNotInstalled bool
}

// DiffOne inspects one bundled skill in targetDir under *os.Root confinement
// and produces unified diffs comparing the on-disk files (a/) against the
// bundled catalog (b/). Unknown skills return ErrUnknownSkill and symlinks
// return ErrUnsafeSymlink.
func DiffOne(targetDir, name string) (SkillDiff, error) {
	if strings.TrimSpace(name) == "" {
		return SkillDiff{}, fmt.Errorf("%w %q (available: %s)", ErrUnknownSkill, "", strings.Join(Names(), ", "))
	}
	diffs, err := DiffAll(targetDir, []string{name}, DiffOptions{})
	if err != nil {
		return SkillDiff{}, err
	}
	return diffs[0], nil
}

// DiffAll computes diffs for the requested bundled skills in targetDir. When
// names is empty, every bundled skill in Catalog() is diffed.
func DiffAll(targetDir string, names []string, opts DiffOptions) ([]SkillDiff, error) {
	var selected []Skill
	if len(names) == 0 {
		selected = Catalog()
	} else {
		var err error
		selected, err = resolveSkillList(names)
		if err != nil {
			return nil, err
		}
	}

	targetRoot, openErr := os.OpenRoot(targetDir)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			if opts.SkipNotInstalled {
				return nil, nil
			}
			out := make([]SkillDiff, len(selected))
			for i, s := range selected {
				out[i] = diffNotInstalled(targetDir, s)
			}
			return out, nil
		}
		return nil, fmt.Errorf("open skills directory %s: %w", targetDir, openErr)
	}
	defer func() { _ = targetRoot.Close() }()

	out := make([]SkillDiff, 0, len(selected))
	for _, s := range selected {
		d, include, err := diffOneInTargetRoot(targetRoot, targetDir, s, opts.SkipNotInstalled)
		if err != nil {
			return nil, err
		}
		if include {
			out = append(out, d)
		}
	}
	return out, nil
}

func diffNotInstalled(targetDir string, s Skill) SkillDiff {
	files := make([]SkillFileDiff, 0, len(s.Files))
	for _, f := range s.Files {
		files = append(files, SkillFileDiff{
			RelPath: f.RelPath,
			Status:  FileDiffAdded,
			Diff:    unifiedDiff("/dev/null", "b/"+f.RelPath, "", f.Content),
		})
	}
	return SkillDiff{
		Name:  s.Name,
		State: StateNotInstalled,
		Path:  filepath.Join(targetDir, s.Name, "SKILL.md"),
		Files: files,
	}
}

func diffTooLargeSummary(fromName, toName string) string {
	return fmt.Sprintf("--- %s\n+++ %s\n(file exceeds %d-byte diff limit; unified diff omitted)\n", fromName, toName, maxManagedFileBytes)
}

func diffOneInTargetRoot(targetRoot *os.Root, targetDir string, s Skill, skipNotInstalled bool) (SkillDiff, bool, error) {
	skillRoot, exists, openErr := openSkillRoot(targetRoot, s.Name)
	if openErr != nil {
		return SkillDiff{}, false, openErr
	}
	if !exists {
		if skipNotInstalled {
			return SkillDiff{}, false, nil
		}
		return diffNotInstalled(targetDir, s), true, nil
	}
	defer func() { _ = skillRoot.Close() }()

	// Verify the marker path is not a symlink before inspecting state.
	if _, mPathErr := checkSafeManagedInRoot(skillRoot, MarkerFileName); mPathErr != nil && errors.Is(mPathErr, ErrUnsafeSymlink) {
		return SkillDiff{}, false, mPathErr
	}

	// Walk the skill directory under *os.Root confinement to reject any symlink
	// and collect regular non-dot files on disk.
	diskFiles, walkErr := collectSkillDiskFiles(skillRoot)
	if walkErr != nil {
		return SkillDiff{}, false, walkErr
	}

	insp := inspectSkillRootDetailed(skillRoot, s, defaultSkillStatus(targetDir, s), true)
	st := insp.status
	if st.State == StateNotInstalled {
		if skipNotInstalled {
			return SkillDiff{}, false, nil
		}
		return diffNotInstalled(targetDir, s), true, nil
	}

	// Fast path: every bundled file already matched its catalog hash during
	// inspection. When no extra non-dot files or extra marker entries exist,
	// return immediately without re-reading the marker, re-lstatting tracked
	// files, or re-reading bundled files.
	if st.State == StateUpToDate && len(diskFiles) == len(s.Files) && insp.marker != nil && len(insp.marker.Files) == len(s.Files) {
		return SkillDiff{
			Name:  s.Name,
			State: st.State,
			Path:  st.Path,
		}, true, nil
	}

	bundledByPath := make(map[string]SkillFile, len(s.Files))
	candidateSet := make(map[string]struct{}, len(s.Files)+len(diskFiles))
	for _, f := range s.Files {
		bundledByPath[f.RelPath] = f
		candidateSet[f.RelPath] = struct{}{}
	}

	trackedSet := make(map[string]bool)
	if insp.marker != nil {
		for rel := range insp.marker.Files {
			cleanRel, cleanErr := cleanManagedRelPath(rel)
			if cleanErr != nil {
				continue
			}
			if _, ok := insp.diskBytes[cleanRel]; !ok {
				if _, safeErr := checkSafeManagedInRoot(skillRoot, cleanRel); safeErr != nil && errors.Is(safeErr, ErrUnsafeSymlink) {
					return SkillDiff{}, false, safeErr
				}
			}
			trackedSet[cleanRel] = true
			candidateSet[cleanRel] = struct{}{}
		}
	}

	for rel := range diskFiles {
		candidateSet[rel] = struct{}{}
	}

	candidates := make([]string, 0, len(candidateSet))
	for rel := range candidateSet {
		candidates = append(candidates, rel)
	}
	slices.Sort(candidates)

	readCached := func(rel string) ([]byte, error) {
		if raw, ok := insp.diskBytes[rel]; ok {
			return raw, nil
		}
		return readSafeManagedInRoot(skillRoot, rel)
	}

	var fileDiffs []SkillFileDiff
	for _, rel := range candidates {
		if bundled, inBundled := bundledByPath[rel]; inBundled {
			diskBytes, readErr := readCached(rel)
			if readErr != nil {
				if errors.Is(readErr, ErrUnsafeSymlink) {
					return SkillDiff{}, false, readErr
				}
				if errors.Is(readErr, os.ErrNotExist) {
					fileDiffs = append(fileDiffs, SkillFileDiff{
						RelPath: rel,
						Status:  FileDiffAdded,
						Diff:    unifiedDiff("/dev/null", "b/"+rel, "", bundled.Content),
					})
					continue
				}
				if errors.Is(readErr, ErrManagedFileTooLarge) {
					fileDiffs = append(fileDiffs, SkillFileDiff{
						RelPath: rel,
						Status:  FileDiffModified,
						Diff:    diffTooLargeSummary("a/"+rel, "b/"+rel),
					})
					continue
				}
				return SkillDiff{}, false, readErr
			}
			if bytes.Equal(diskBytes, []byte(bundled.Content)) {
				continue
			}
			fileDiffs = append(fileDiffs, SkillFileDiff{
				RelPath: rel,
				Status:  FileDiffModified,
				Diff:    unifiedDiff("a/"+rel, "b/"+rel, string(diskBytes), bundled.Content),
			})
			continue
		}

		if trackedSet[rel] {
			diskBytes, readErr := readCached(rel)
			if readErr != nil {
				if errors.Is(readErr, ErrUnsafeSymlink) {
					return SkillDiff{}, false, readErr
				}
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				if errors.Is(readErr, ErrManagedFileTooLarge) {
					fileDiffs = append(fileDiffs, SkillFileDiff{
						RelPath: rel,
						Status:  FileDiffRemoved,
						Diff:    diffTooLargeSummary("a/"+rel, "/dev/null"),
					})
					continue
				}
				return SkillDiff{}, false, readErr
			}
			fileDiffs = append(fileDiffs, SkillFileDiff{
				RelPath: rel,
				Status:  FileDiffRemoved,
				Diff:    unifiedDiff("a/"+rel, "/dev/null", string(diskBytes), ""),
			})
			continue
		}

		if _, onDisk := diskFiles[rel]; onDisk {
			fileDiffs = append(fileDiffs, SkillFileDiff{
				RelPath: rel,
				Status:  FileDiffUntracked,
			})
		}
	}

	return SkillDiff{
		Name:  s.Name,
		State: st.State,
		Path:  st.Path,
		Files: fileDiffs,
	}, true, nil
}

func collectSkillDiskFiles(skillRoot *os.Root) (map[string]struct{}, error) {
	files := make(map[string]struct{})
	err := fs.WalkDir(skillRoot.FS(), ".", func(p string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if p == "." {
			return nil
		}
		osRel := filepath.FromSlash(p)
		fi, lErr := skillRoot.Lstat(osRel)
		if lErr != nil {
			return lErr
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrUnsafeSymlink, filepath.Join(skillRoot.Name(), osRel))
		}
		if fi.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if fi.Mode().IsRegular() {
			files[p] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
