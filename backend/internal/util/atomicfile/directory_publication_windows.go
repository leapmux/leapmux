//go:build windows

package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/util/useronly"
)

// publishExclusive is the exclusive write-through publication step,
// overridable by tests that need to observe or fail the publish itself.
var publishExclusive = renameNoReplace

// publishStaged builds the missing chain below firstMissing inside a
// private staging directory beside it, writes the prepared record,
// publishes the staging root through the write-through exclusive move,
// and commits the record only after that publication succeeded.
func publishStaged(firstMissing, target string) error {
	parent := filepath.Dir(firstMissing)

	stage, err := useronly.MkdirTemp(parent, stagePrefix)
	if err != nil {
		return fmt.Errorf("atomicfile: create the staging directory beside %s: %w", firstMissing, err)
	}
	published := false
	// Only THIS invocation's unpublished staging tree is removed. A
	// published destination and its record stay for the next command to
	// confirm; another invocation's staging tree is never touched.
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()

	for _, element := range missingBelow(firstMissing, target) {
		p := filepath.Join(stage, element)
		if err := useronly.Mkdir(p); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("atomicfile: stage %s: %w", p, err)
		}
	}

	rec := &publicationRecord{
		AccountID:       currentAccountID(),
		Directory:       firstMissing,
		TargetDirectory: target,
		Phase:           phasePrepared,
	}
	if err := writeRecord(recordPathAt(stage), rec); err != nil {
		return err
	}

	// The write-through exclusive move is the native publication
	// contract: the system returns only after the subtree's new name
	// reached the disk.
	if err := publishExclusive(stage, firstMissing); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// The winner published first. Its record decides what this
			// invocation does next; re-walking reads it without
			// publishing anything.
			return MkdirAllDurable(target)
		}
		return fmt.Errorf("atomicfile: publish %s: %w", firstMissing, err)
	}
	published = true

	// Committed only after the write-through publication succeeded. A
	// crash between the two leaves a prepared record, which the next
	// invocation reports as uncertain rather than guessing.
	rec.Phase = phaseCommitted
	if err := writeRecordCommitted(recordPathAt(firstMissing), rec); err != nil {
		return fmt.Errorf("atomicfile: commit the publication record of %s: %w", firstMissing, err)
	}
	return nil
}

// repairRecord has no local repair on Windows: a prepared record means
// the write-through publication of an earlier invocation was never
// confirmed to have completed its record, and nothing here may rename,
// remove, or guess the persistence of the already-visible subtree. The
// error identifies the unconfirmed prefix for operator inspection or a
// different data location.
func repairRecord(path string) error {
	rec, err := loadRecordAt(path)
	if err != nil {
		return err
	}
	if rec.Phase == phaseCommitted {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrDirectoryPublicationUncertain, rec.Directory)
}
