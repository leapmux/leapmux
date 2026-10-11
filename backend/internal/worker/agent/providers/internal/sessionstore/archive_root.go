package sessionstore

import "os"

// ArchiveRoot keeps a directory open while an archive reader checks its path.
// OpenChild preserves this interface so tests can inject a change at any depth.
type ArchiveRoot interface {
	RegularFileRoot
	Stat(string) (os.FileInfo, error)
	OpenChild(string) (ArchiveRoot, error)
	Close() error
}

type osArchiveRoot struct{ *os.Root }

func (r osArchiveRoot) OpenChild(name string) (ArchiveRoot, error) {
	child, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return osArchiveRoot{Root: child}, nil
}

// OpenArchiveRoot follows the configured root path once, then confines reads
// to the opened directory. Descendant symlinks get separate checks.
func OpenArchiveRoot(path string) (ArchiveRoot, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return osArchiveRoot{Root: root}, nil
}
