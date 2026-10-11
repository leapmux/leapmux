package sessionstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ArchiveDirectoryChain owns each directory that a checked path opens. The
// initial root belongs to the caller.
type ArchiveDirectoryChain struct {
	root   ArchiveRoot
	opened []ArchiveRoot
}

// Root returns the deepest checked directory. The caller must close the chain
// after every read and listing through this root.
func (c *ArchiveDirectoryChain) Root() ArchiveRoot { return c.root }

// Close releases checked directories from deepest to shallowest.
func (c *ArchiveDirectoryChain) Close() error {
	var err error
	for index := len(c.opened) - 1; index >= 0; index-- {
		if closeErr := c.opened[index].Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close an archive directory: %w", closeErr))
		}
	}
	c.opened = nil
	return err
}

func validArchiveComponent(part string) bool {
	return part != "" && part != "." && part != ".." && filepath.Base(part) == part && !strings.ContainsAny(part, `/\`)
}

// OpenCheckedArchiveDirectoryChain anchors every path component in order.
// A symlink or a changed directory cannot redirect a later read or listing.
func OpenCheckedArchiveDirectoryChain(root ArchiveRoot, parts ...string) (*ArchiveDirectoryChain, error) {
	chain := &ArchiveDirectoryChain{root: root, opened: make([]ArchiveRoot, 0, len(parts))}
	fail := func(err error) (*ArchiveDirectoryChain, error) {
		return nil, errors.Join(err, chain.Close())
	}
	for _, part := range parts {
		if !validArchiveComponent(part) {
			return fail(errors.New("an archive path component is invalid"))
		}
		current := chain.root
		checked, statErr := current.Lstat(part)
		if statErr != nil {
			return fail(statErr)
		}
		if checked.Mode()&os.ModeSymlink != 0 {
			return fail(errors.New("an archive directory is a symlink"))
		}
		if !checked.IsDir() {
			return fail(errors.New("an archive path component is not a directory"))
		}
		next, openErr := current.OpenChild(part)
		if openErr != nil {
			return fail(openErr)
		}
		chain.opened = append(chain.opened, next)
		opened, statErr := next.Stat(".")
		if statErr != nil {
			return fail(statErr)
		}
		if !opened.IsDir() || !os.SameFile(checked, opened) {
			return fail(errors.New("an archive directory changed before it opened"))
		}
		chain.root = next
	}
	return chain, nil
}

// ReadRegularFileWithoutSymlinkAncestors reads a final file below checked
// directories. The shared final-file reader keeps its own identity and cap.
func ReadRegularFileWithoutSymlinkAncestors(root ArchiveRoot, limit int64, parts ...string) (data []byte, err error) {
	if len(parts) == 0 || !validArchiveComponent(parts[len(parts)-1]) {
		return nil, errors.New("an archive path component is invalid")
	}
	chain, err := OpenCheckedArchiveDirectoryChain(root, parts[:len(parts)-1]...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			data, err = nil, errors.Join(err, closeErr)
		}
	}()
	return ReadRegularFile(chain.Root(), parts[len(parts)-1], limit)
}
