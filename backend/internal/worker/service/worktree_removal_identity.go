package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/id"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
)

// Git removes its per-worktree administrative directory with the worktree.
// Its path and .git link can be reused, so a saved random marker distinguishes
// the original registration from a replacement at the same filesystem path.
type worktreeRemovalIdentity struct {
	GitDir     string
	MarkerName string
	Token      string
}

func removalGitDir(ctx context.Context, wt db.Worktree) (string, error) {
	info, err := queryGitPathInfo(ctx, wt.WorktreePath)
	if err != nil {
		return "", err
	}
	if !info.IsWorktree || !pathutil.SamePath(info.TopLevel, pathutil.Canonicalize(wt.WorktreePath)) || !pathutil.SamePath(info.RepoRoot, pathutil.Canonicalize(wt.RepoRoot)) {
		return "", errors.New("the removal target is no longer the tracked linked worktree")
	}
	directory, err := gitutil.Output(ctx, wt.WorktreePath, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", err
	}
	return pathutil.Canonicalize(strings.TrimSpace(directory)), nil
}

func readRemovalMarker(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() != 48 {
		return "", errors.New("the worktree removal marker is invalid")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// captureWorktreeRemovalIdentity runs before resource retirement. Recovery with
// a saved snapshot only validates this identity and never creates a new marker.
func (svc *Service) captureWorktreeRemovalIdentity(ctx context.Context, wt db.Worktree, key ownedTabKey) (*worktreeRemovalIdentity, error) {
	defer svc.lockGitIndex(wt.RepoRoot)()
	directory, err := removalGitDir(ctx, wt)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s\x00%s", wt.ID, key.tabType, key.tabID, key.userID))
	marker := "leapmux-removal-" + hex.EncodeToString(digest[:]) + "-" + id.Generate()
	path := filepath.Join(directory, marker)
	// A unique destination prevents atomic replacement from changing a saved
	// marker or one owned by a concurrent ordinary close.
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("the fresh removal marker destination is unavailable")
	}
	token := id.Generate()
	if err := atomicfile.WriteFileDurable(path, []byte(token), 0o600); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &worktreeRemovalIdentity{GitDir: directory, MarkerName: marker, Token: token}, nil
}

// releaseWorktreeRemovalIdentity deletes only the marker this snapshot owns.
// A changed marker belongs to another filesystem state and must survive.
func releaseWorktreeRemovalIdentity(identity *worktreeRemovalIdentity) error {
	return releaseRemovalIdentity(identity, atomicfile.SyncDirectory)
}

func releaseRemovalIdentity(identity *worktreeRemovalIdentity, syncDirectory func(string) error) error {
	if identity == nil {
		return nil
	}
	if filepath.Base(identity.MarkerName) != identity.MarkerName || !strings.HasPrefix(identity.MarkerName, "leapmux-removal-") || !filepath.IsAbs(identity.GitDir) {
		return errors.New("the saved removal marker path is invalid")
	}
	path := filepath.Join(identity.GitDir, identity.MarkerName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return syncRemovalMarkerDirectory(identity.GitDir, syncDirectory)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 48 {
		return nil
	}
	token, err := readRemovalMarker(path)
	if err != nil {
		return err
	}
	if token != identity.Token {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncRemovalMarkerDirectory(identity.GitDir, syncDirectory)
}

// An absent marker may follow a successful unlink whose directory sync failed.
// Retry the barrier while its parent exists. A removed Git registration has no
// remaining marker entry to synchronize.
func syncRemovalMarkerDirectory(directory string, syncDirectory func(string) error) error {
	info, err := os.Stat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	if err := syncDirectory(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func releaseRemovalIdentityBestEffort(identity *worktreeRemovalIdentity) {
	if err := releaseWorktreeRemovalIdentity(identity); err != nil {
		slog.Warn("release the worktree removal marker failed", "error", err)
	}
}

func validateWorktreeRemovalIdentity(ctx context.Context, wt db.Worktree, identity *worktreeRemovalIdentity) error {
	if identity == nil || len(identity.Token) != 48 || filepath.Base(identity.MarkerName) != identity.MarkerName || !strings.HasPrefix(identity.MarkerName, "leapmux-removal-") {
		return errors.New("the worktree removal has no valid saved identity")
	}
	directory, err := removalGitDir(ctx, wt)
	if err != nil {
		return err
	}
	if !pathutil.SamePath(directory, identity.GitDir) {
		return errors.New("the worktree administrative directory changed before removal")
	}
	token, err := readRemovalMarker(filepath.Join(directory, identity.MarkerName))
	if err != nil {
		return fmt.Errorf("validate the original worktree removal marker: %w", err)
	}
	if token != identity.Token {
		return errors.New("the worktree removal marker changed")
	}
	return nil
}
