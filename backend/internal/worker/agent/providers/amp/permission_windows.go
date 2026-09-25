//go:build windows

package amp

// restrictSocket does nothing on Windows. The socket takes the access list of
// its agent directory, which admits the owner alone: agentdir creates the
// parent of every agent directory with that list, and each directory and file
// under the parent takes it.
func restrictSocket(string) error { return nil }

// checkPrivateSocket does nothing on Windows. The socket lies in an agent
// directory whose access list admits the owner alone (see restrictSocket), and
// Windows has no Unix owner or mode bits that the helper could check.
func checkPrivateSocket(string) error { return nil }
