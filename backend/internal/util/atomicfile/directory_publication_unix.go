//go:build unix

package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// currentAccountID reports the current account's numeric identifier, the
// Unix spelling of the identity a publication record pins.
func currentAccountID() string {
	return strconv.Itoa(os.Getuid())
}

// publishExclusive is the exclusive-rename publication step, overridable
// by tests that need to observe or fail the publish itself.
var publishExclusive = renameNoReplace

// publishStaged builds the missing chain below firstMissing inside a
// private staging directory beside it, flushes the staged subtree with
// its prepared record, publishes it with an exclusive rename, flushes
// the parent, and commits the record.
func publishStaged(firstMissing, target string) error {
	parent := filepath.Dir(firstMissing)

	// The staging directory is unique per call and created beside the
	// first missing prefix, so the publish is one rename on the same
	// filesystem and two concurrent invocations cannot share a stage.
	stage, err := os.MkdirTemp(parent, stagePrefix)
	if err != nil {
		return fmt.Errorf("atomicfile: create the staging directory beside %s: %w", firstMissing, err)
	}
	published := false
	// Cleanup removes only THIS invocation's unpublished staging tree. A
	// published destination and its record stay; another invocation's
	// staging tree is never touched.
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()

	// The remaining missing prefixes, the child of firstMissing down to
	// target, become directories inside the staging root, so the
	// published subtree arrives complete.
	chain := missingBelow(firstMissing, target)
	for _, element := range chain {
		p := filepath.Join(stage, element)
		if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("atomicfile: stage %s: %w", p, err)
		}
	}

	// The prepared record is written and flushed at the staging root
	// before the subtree becomes visible anywhere.
	rec := &publicationRecord{
		AccountID:       currentAccountID(),
		Directory:       firstMissing,
		TargetDirectory: target,
		Phase:           phasePrepared,
	}
	if err := writeRecord(recordPathAt(stage), rec); err != nil {
		return err
	}
	// Flush the new subtree from its leaves to the staging root, so the
	// published directory and everything inside it reached the disk
	// before the name that carries them becomes visible.
	for i := len(chain) - 1; i >= 0; i-- {
		if err := ops.syncDir(filepath.Join(stage, chain[i])); err != nil {
			return err
		}
	}
	if err := ops.syncDir(stage); err != nil {
		return err
	}

	// The exclusive publish: a concurrent winner leaves os.ErrExist, and
	// this invocation's staging tree is removed by the deferred cleanup.
	if err := publishExclusive(stage, firstMissing); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// The winner's record decides what this invocation does
			// next; re-walking reads it without publishing anything.
			return MkdirAllDurable(target)
		}
		return fmt.Errorf("atomicfile: publish %s: %w", firstMissing, err)
	}
	published = true

	// Publish the new name, then commit the record that proves it.
	if err := ops.syncDir(parent); err != nil {
		return err
	}
	rec.Phase = phaseCommitted
	if err := writeRecordCommitted(recordPathAt(firstMissing), rec); err != nil {
		return fmt.Errorf("atomicfile: commit the publication record of %s: %w", firstMissing, err)
	}
	return nil
}

// repairRecord completes a prepared record on an existing subtree: the
// same parent barrier, then the same committed record with the original
// provenance. Idempotent, and safe when several invocations repair at
// once: each performs the barrier and writes the same committed bytes.
func repairRecord(path string) error {
	rec, err := loadRecordAt(path)
	if err != nil {
		return err
	}
	if rec.Phase == phaseCommitted {
		return nil
	}
	if err := ops.syncDir(filepath.Dir(rec.Directory)); err != nil {
		return err
	}
	rec.Phase = phaseCommitted
	if err := writeRecordCommitted(path, rec); err != nil {
		return fmt.Errorf("atomicfile: commit the publication record of %s: %w", rec.Directory, err)
	}
	return nil
}

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
// a slash-separated path relative to firstMissing. target is below
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
