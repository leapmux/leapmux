package control

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/leapmux/leapmux/locallisten"
)

const localHubProbeTimeout = 150 * time.Millisecond

// DiscoverLocalHub probes local solo, dev, and standalone Hub IPC endpoints
// with a fast liveness check so commands work out of the box after `leapmux up`.
//
// On Unix, solo and dev Hubs bind `<config-parent>/{solo,dev}/hub/hub.sock`
// (and standalone Hub binds `<config-parent>/hub/hub.sock`), staying isolated
// inside any test's `LEAPMUX_CONTROL_CONFIG_DIR` temp directory. On Windows,
// local Hubs bind the per-user named pipe `npipe:leapmux-hub-<SID>`.
func DiscoverLocalHub() string {
	dir, err := ConfigDir()
	if err != nil {
		return ""
	}
	parent := filepath.Dir(dir)
	for _, candidate := range localHubCandidates(parent) {
		if isLocalIPCLive(candidate) {
			return candidate
		}
	}
	return ""
}

func localHubCandidates(configParent string) []string {
	if runtime.GOOS == "windows" {
		// Respect an explicit LEAPMUX_CONTROL_CONFIG_DIR override so isolated
		// runs and tests never escape to a host-level per-user named pipe.
		if os.Getenv("LEAPMUX_CONTROL_CONFIG_DIR") != "" {
			return nil
		}
		if u, err := user.Current(); err == nil && u.Uid != "" {
			return []string{"npipe:leapmux-hub-" + u.Uid}
		}
		return nil
	}
	roots := []string{configParent}
	// When XDG_CONFIG_HOME is set (without an explicit LEAPMUX_CONTROL_CONFIG_DIR
	// override), also probe ~/.config/leapmux because Hub/solo default data dirs
	// live under ~/.config/leapmux/{solo,dev,hub}.
	if os.Getenv("LEAPMUX_CONTROL_CONFIG_DIR") == "" && os.Getenv("XDG_CONFIG_HOME") != "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			fallback := filepath.Join(home, ".config", "leapmux")
			if fallback != configParent {
				roots = append(roots, fallback)
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, root := range roots {
		for _, sock := range []string{
			filepath.Join(root, "solo", "hub", "hub.sock"),
			filepath.Join(root, "dev", "hub", "hub.sock"),
			filepath.Join(root, "hub", "hub.sock"),
		} {
			if seen[sock] {
				continue
			}
			seen[sock] = true
			if _, err := os.Stat(sock); err == nil {
				out = append(out, "unix:"+sock)
			}
		}
	}
	return out
}

func isLocalIPCLive(addr string) bool {
	dial, err := locallisten.Dialer(addr)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), localHubProbeTimeout)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
