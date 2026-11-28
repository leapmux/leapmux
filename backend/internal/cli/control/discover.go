package control

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/leapmux/leapmux/locallisten"
)

const localHubProbeTimeout = 150 * time.Millisecond

// DiscoverLocalHub probes local solo, dev, and standalone Hub IPC endpoints
// concurrently under one shared deadline so commands work out of the box after
// `leapmux up`. Priority order is deterministic: solo, then dev, then
// standalone.
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
	candidates := localHubCandidates(parent)
	if len(candidates) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), localHubProbeTimeout)
	defer cancel()
	if len(candidates) == 1 {
		if isLocalIPCLive(ctx, candidates[0]) {
			return candidates[0]
		}
		return ""
	}

	type probeResult struct {
		index int
		live  bool
	}
	results := make(chan probeResult, len(candidates))
	var wg sync.WaitGroup
	for i, candidate := range candidates {
		wg.Go(func() {
			results <- probeResult{index: i, live: isLocalIPCLive(ctx, candidate)}
		})
	}
	defer func() {
		cancel()
		wg.Wait()
	}()

	done := make([]bool, len(candidates))
	live := make([]bool, len(candidates))
	next := 0
	for range candidates {
		r := <-results
		done[r.index] = true
		live[r.index] = r.live
		for next < len(candidates) && done[next] {
			if live[next] {
				return candidates[next]
			}
			next++
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
		if addr, err := locallisten.DefaultHubAddress(""); err == nil && addr != "" {
			return []string{addr}
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
		for _, dataDir := range []string{
			filepath.Join(root, "solo", "hub"),
			filepath.Join(root, "dev", "hub"),
			filepath.Join(root, "hub"),
		} {
			sock := locallisten.DefaultHubSocketPath(dataDir)
			if seen[sock] {
				continue
			}
			seen[sock] = true
			if _, err := os.Stat(sock); err == nil {
				if addr, err := locallisten.DefaultHubAddress(dataDir); err == nil {
					out = append(out, addr)
				}
			}
		}
	}
	return out
}

func isLocalIPCLive(ctx context.Context, addr string) bool {
	dial, err := locallisten.Dialer(addr)
	if err != nil {
		return false
	}
	conn, err := dial(ctx)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
