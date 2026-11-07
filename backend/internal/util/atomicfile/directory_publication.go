package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/leapmux/leapmux/internal/util/canonicalpath"
	"github.com/leapmux/leapmux/util/useronly"
)

// publicationRecordName is the fixed file name of a directory
// publication record. The record sits at the root of the first directory
// a MkdirAllDurable call created, and its presence tells a later command
// whether LeapMux confirmed the publication of that subtree. A
// pre-existing directory with no record is an externally provisioned
// publication boundary: this helper neither opens nor flushes it.
const publicationRecordName = ".leapmux-directory-publication.yaml"

// ErrDirectoryPublicationUncertain reports a directory whose publication
// a previous invocation started but could not confirm. On Windows a
// prepared record cannot be repaired locally, so the next command must
// stop before any application state or credential access and name the
// unconfirmed prefix for operator inspection or a different data
// location.
var ErrDirectoryPublicationUncertain = errors.New(
	"a directory publication from an earlier command was not confirmed; inspect the named directory or choose another data location")

// publicationPhase states where a directory publication stands.
type publicationPhase string

const (
	phasePrepared  publicationPhase = "prepared"
	phaseCommitted publicationPhase = "committed"
)

// publicationRecord is the durable marker of one MkdirAllDurable
// publication. directory is the canonical first missing prefix that
// became visible; target_directory is the original requested directory
// below it. The three provenance fields never change when the record is
// committed or repaired, including when another requested path shares
// its ancestor.
type publicationRecord struct {
	AccountID       string           `yaml:"account_id"`
	Directory       string           `yaml:"directory"`
	TargetDirectory string           `yaml:"target_directory"`
	Phase           publicationPhase `yaml:"phase"`
}

// stagePrefix is the prefix of the private staging directory a
// publication builds beside the first missing prefix.
const stagePrefix = ".leapmux-stage-"

// MkdirAllDurable creates target and every missing parent, and reports
// success only after the created subtree's publication reached the disk
// through the platform's documented operations.
//
// It never opens or synchronizes pre-existing ancestors: an existing
// searchable path may legitimately contain execute-only directories,
// and a pre-existing directory without an outstanding LeapMux record is
// an externally provisioned boundary.
//
// The protocol, in order:
//
//  1. Walk the existing prefixes without listing any directory's
//     contents, looking for the fixed publication record after checking
//     each prefix's existence.
//  2. Build the entire missing chain inside a unique private staging
//     directory beside the first missing prefix, on the same
//     filesystem, then write and flush a prepared record at the staging
//     root and flush the new subtree from its leaves to its root.
//  3. Publish the staging root onto the first missing prefix with an
//     exclusive rename that refuses an existing destination. A losing
//     concurrent publisher removes only its own unpublished staging
//     tree and then inspects the winner's record. Every handled
//     pre-publication failure removes only that invocation's staging
//     tree; once published, failure cleanup leaves the destination and
//     its record intact.
//  4. Flush the published prefix's containing directory, then durably
//     replace the record with the same provenance and phase committed.
//     The committed record stays as publication evidence.
//
// A retry that finds a prepared record repairs it first: the same
// parent barrier, then the same committed record, before any pending
// publication, application state, or registration. Unix repairs are
// idempotent. On Windows a prepared record cannot be repaired locally,
// so MkdirAllDurable reports ErrDirectoryPublicationUncertain instead of
// renaming, removing, or guessing the persistence of the visible
// subtree.
func MkdirAllDurable(target string) error {
	target = filepath.Clean(target)
	if !filepath.IsAbs(target) {
		return fmt.Errorf("atomicfile: MkdirAllDurable needs an absolute path, got %s", target)
	}
	canonical, err := canonicalpath.Directory(target)
	if err != nil {
		return err
	}
	target = canonical

	firstMissing, records, err := walkExistingPrefixes(target)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := confirmExistingRecord(record); err != nil {
			return err
		}
	}
	if firstMissing == "" {
		return nil
	}
	return publishStaged(firstMissing, target)
}

// walkExistingPrefixes inspects target's prefixes from the root down.
// It validates every publication record before any repair or creation and returns
// those records in ancestor order. Looking up a fixed record does not list a directory.
func walkExistingPrefixes(target string) (firstMissing string, records []string, err error) {
	var prefixes []string
	for p := target; ; p = filepath.Dir(p) {
		prefixes = append(prefixes, p)
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	for i := len(prefixes) - 1; i >= 0; i-- {
		p := prefixes[i]
		info, statErr := os.Stat(p)
		if statErr == nil {
			if !info.IsDir() {
				return "", nil, fmt.Errorf("atomicfile: %s exists and is not a directory", p)
			}
			rec := filepath.Join(p, publicationRecordName)
			if _, lerr := os.Lstat(rec); lerr == nil {
				if _, recordErr := loadRecordAt(rec); recordErr != nil {
					return "", nil, recordErr
				}
				records = append(records, rec)
			} else if !errors.Is(lerr, fs.ErrNotExist) {
				return "", nil, lerr
			}
			continue
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return "", nil, statErr
		}
		return p, records, nil
	}
	return "", records, nil
}

// loadRecordAt reads and validates the record at path. A record that
// belongs to another account, identifies another directory, escapes its own
// directory with its target, holds malformed fields, or is not a
// regular file is a conflict, reported rather than overwritten.
func loadRecordAt(path string) (*publicationRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("atomicfile: %s is not a regular publication record", path)
	}
	data, err := useronly.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec publicationRecord
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("atomicfile: parse %s: %w", path, err)
	}
	if rec.AccountID == "" || rec.Directory == "" || rec.TargetDirectory == "" {
		return nil, fmt.Errorf("atomicfile: %s misses a required field", path)
	}
	if rec.Phase != phasePrepared && rec.Phase != phaseCommitted {
		return nil, fmt.Errorf("atomicfile: %s holds unknown phase %q", path, rec.Phase)
	}
	if rec.AccountID != currentAccountID() {
		return nil, fmt.Errorf("atomicfile: %s belongs to account %q, not this account", path, rec.AccountID)
	}
	if dir := filepath.Clean(filepath.Dir(path)); rec.Directory != dir {
		return nil, fmt.Errorf("atomicfile: %s identifies directory %q, not %q", path, rec.Directory, dir)
	}
	rel, relErr := filepath.Rel(rec.Directory, filepath.Clean(rec.TargetDirectory))
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("atomicfile: %s targets %q, outside %q", path, rec.TargetDirectory, rec.Directory)
	}
	return &rec, nil
}

// confirmExistingRecord finishes the record an earlier invocation left
// on an existing subtree: committed is confirmation, prepared needs the
// repair this platform allows.
func confirmExistingRecord(path string) error {
	rec, err := loadRecordAt(path)
	if err != nil {
		return err
	}
	if rec.Phase == phaseCommitted {
		return nil
	}
	return repairRecord(path)
}

// writeRecord writes a record through the shared write sequence and
// flushes it, at mode 0600: the record is private and carries no
// credentials, but its provenance is what the next command trusts.
func writeRecord(path string, rec *publicationRecord) error {
	data, err := yamlMarshalRecord(rec)
	if err != nil {
		return err
	}
	if err := writeFileSequenced(path, data, 0o600, ops.rename); err != nil {
		return fmt.Errorf("atomicfile: write %s: %w", path, err)
	}
	return nil
}

// yamlMarshalRecord renders a record.
func yamlMarshalRecord(rec *publicationRecord) ([]byte, error) {
	return yaml.Marshal(rec)
}

// recordPathAt returns the record path inside dir.
func recordPathAt(dir string) string { return filepath.Join(dir, publicationRecordName) }

// writeRecordCommitted replaces a record with its committed form
// durably.
func writeRecordCommitted(path string, rec *publicationRecord) error {
	data, err := yamlMarshalRecord(rec)
	if err != nil {
		return err
	}
	return WriteFileDurable(path, data, 0o600)
}

// missingBelow returns the chain of directories between firstMissing
// and target (exclusive of firstMissing, inclusive of target), each as
// a path relative to firstMissing, with native separators. target is below
// firstMissing by construction.
func missingBelow(firstMissing, target string) []string {
	rel, err := filepath.Rel(firstMissing, target)
	if err != nil || rel == "." {
		return nil
	}
	segments := strings.Split(rel, string(filepath.Separator))
	chain := make([]string, 0, len(segments))
	for i := 1; i <= len(segments); i++ {
		chain = append(chain, filepath.Join(segments[:i]...))
	}
	return chain
}
