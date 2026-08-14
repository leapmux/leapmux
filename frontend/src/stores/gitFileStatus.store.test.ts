import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createMemo, createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'

// Mock workerRpc to control refresh() responses.
const mockGetGitFileStatus = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  getGitFileStatus: (...args: unknown[]) => mockGetGitFileStatus(...args),
}))

function makeEntry(overrides: Partial<GitFileStatusEntry> & { path: string }): GitFileStatusEntry {
  return {
    $typeName: 'leapmux.v1.GitFileStatusEntry',
    stagedStatus: GitFileStatusCode.UNSPECIFIED,
    unstagedStatus: GitFileStatusCode.UNSPECIFIED,
    linesAdded: 0,
    linesDeleted: 0,
    stagedLinesAdded: 0,
    stagedLinesDeleted: 0,
    oldPath: '',
    isDir: false,
    ...overrides,
  }
}

describe('gitFileStatusStore', () => {
  describe('getNodeDiffStats (directories)', () => {
    it('counts untracked files separately in diff stats', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'untracked.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        const stats = store.getNodeDiffStats('/repo', true)
        expect(stats.added).toBe(0)
        expect(stats.deleted).toBe(0)
        expect(stats.untracked).toBe(1)

        dispose()
      })
    })

    it('sums tracked lines and counts untracked files separately', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'modified.txt',
              unstagedStatus: GitFileStatusCode.MODIFIED,
              linesAdded: 5,
              linesDeleted: 2,
            }),
            makeEntry({
              path: 'staged.txt',
              stagedStatus: GitFileStatusCode.ADDED,
              stagedLinesAdded: 20,
            }),
            makeEntry({
              path: 'untracked.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        const stats = store.getNodeDiffStats('/repo', true)
        expect(stats.added).toBe(5 + 20)
        expect(stats.deleted).toBe(2)
        expect(stats.untracked).toBe(1)

        dispose()
      })
    })

    it('scopes stats to subdirectory', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'src/untracked.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
            makeEntry({
              path: 'other.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        const srcStats = store.getNodeDiffStats('/repo/src', true)
        expect(srcStats.untracked).toBe(1)

        const rootStats = store.getNodeDiffStats('/repo', true)
        expect(rootStats.untracked).toBe(2)

        dispose()
      })
    })

    it('matches untracked directory entry for merged single-child dir', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'build/',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        // Merged node "build/bin" should pick up stats from ancestor "build/"
        const stats = store.getNodeDiffStats('/repo/build/bin', true)
        expect(stats.untracked).toBe(1)

        // Deeply merged node should also match
        const deepStats = store.getNodeDiffStats('/repo/build/bin/sub', true)
        expect(deepStats.untracked).toBe(1)

        dispose()
      })
    })

    it('does not false-match unrelated directory entries', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'other/',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        const stats = store.getNodeDiffStats('/repo/build/bin', true)
        expect(stats.untracked).toBe(0)

        dispose()
      })
    })

    it('does not ancestor-match file entries without trailing slash', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'build',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        // "build" (no trailing slash) is a file, not a directory —
        // should not match "build/bin" via ancestor check.
        const stats = store.getNodeDiffStats('/repo/build/bin', true)
        expect(stats.untracked).toBe(0)

        dispose()
      })
    })
  })

  describe('hasChanges with merged directories', () => {
    it('returns true for merged child of untracked directory', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'build/',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        expect(store.hasChanges('/repo/build/bin')).toBe(true)
        expect(store.hasChanges('/repo/build/bin/sub')).toBe(true)
        expect(store.hasChanges('/repo/other')).toBe(false)

        dispose()
      })
    })
  })

  describe('originUrl and currentBranch', () => {
    it('stores originUrl and currentBranch after successful refresh', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          originUrl: 'https://github.com/test/repo.git',
          currentBranch: 'main',
          files: [],
        })

        await store.refresh('worker1', '/repo')

        expect(store.state.originUrl).toBe('https://github.com/test/repo.git')
        expect(store.state.currentBranch).toBe('main')

        dispose()
      })
    })

    it('clears originUrl and currentBranch on refresh error', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        // First, populate with valid data.
        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          originUrl: 'https://github.com/test/repo.git',
          currentBranch: 'main',
          files: [],
        })
        await store.refresh('worker1', '/repo')

        // Now simulate an error.
        mockGetGitFileStatus.mockRejectedValueOnce(new Error('network error'))
        await store.refresh('worker1', '/repo')

        expect(store.state.originUrl).toBe('')
        expect(store.state.currentBranch).toBe('')

        dispose()
      })
    })

    it('does not rewrite the empty state on consecutive refresh errors', async () => {
      // First failure clears state from valid → empty. Second failure
      // hitting already-empty state must NOT trigger reactive writes —
      // the guard short-circuits when each field already matches its
      // zero value, so dependent memos (e.g. the file tree's prefix
      // index) don't re-fire on every flaky-probe poll.
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        // Populate, then fail once to reach the all-empty error state.
        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          originUrl: 'https://github.com/test/repo.git',
          currentBranch: 'main',
          isWorktree: false,
          files: [],
        })
        await store.refresh('worker1', '/repo')
        mockGetGitFileStatus.mockRejectedValueOnce(new Error('first failure'))
        await store.refresh('worker1', '/repo')

        // Snapshot identities — store proxies expose stable references
        // for unchanged fields. The `files` array reference is the most
        // sensitive one (its identity drives the prefixIndex memo).
        const filesBefore = store.state.files
        const repoRootBefore = store.state.repoRoot

        mockGetGitFileStatus.mockRejectedValueOnce(new Error('second failure'))
        await store.refresh('worker1', '/repo')

        // Same references → no setState fired for any field.
        expect(store.state.files).toBe(filesBefore)
        expect(store.state.repoRoot).toBe(repoRootBefore)
        expect(store.state.isGitRepo).toBe(false)
        expect(store.state.originUrl).toBe('')
        expect(store.state.currentBranch).toBe('')
        expect(store.state.isWorktree).toBe(false)

        dispose()
      })
    })
  })

  /**
   * "Not a git repo" is a SUCCESSFUL empty response carrying only `errorHint`
   * -- this message has no `is_git_repo` field. Treating any resolved RPC as
   * proof of a repo left a plain directory advertising `isGitRepo: true` with
   * an empty toplevel, which renders the git filter tab bar over a non-repo.
   */
  describe('non-repo replies', () => {
    it('does not claim a repo when the worker reports none', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()
        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '',
          toplevel: '',
          originUrl: '',
          currentBranch: '',
          isWorktree: false,
          errorHint: 'not a git repository',
          files: [],
        })

        await store.refresh('worker1', '/tmp/plain-dir')

        expect(store.state.isGitRepo, 'an empty toplevel is not a repo').toBe(false)
        expect(store.state.errorHint, 'the diagnostic must reach the UI').toBe('not a git repository')
        dispose()
      })
    })

    it('clears a previous repo when the focus moves to a non-repo directory', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()
        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          toplevel: '/repo',
          originUrl: 'git@example.com:o/r.git',
          currentBranch: 'main',
          isWorktree: false,
          errorHint: '',
          files: [],
        })
        await store.refresh('worker1', '/repo')
        expect(store.state.isGitRepo).toBe(true)

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '',
          toplevel: '',
          originUrl: '',
          currentBranch: '',
          isWorktree: false,
          errorHint: 'not a git repository',
          files: [],
        })
        await store.refresh('worker1', '/tmp/plain-dir')

        expect(store.state.isGitRepo, 'the flag must fall back, not latch').toBe(false)
        dispose()
      })
    })
  })

  describe('getChangedFiles', () => {
    it('includes untracked files in changed and unstaged filters', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({
              path: 'untracked.txt',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
              linesAdded: 5,
            }),
          ],
        })

        await store.refresh('worker1', '/repo')

        expect(store.getChangedFiles('changed')).toHaveLength(1)
        expect(store.getChangedFiles('unstaged')).toHaveLength(1)
        expect(store.getChangedFiles('staged')).toHaveLength(0)

        dispose()
      })
    })
  })

  describe('windows-flavored repoRoot', () => {
    it('resolves absolute path lookups against a C:\\ repoRoot', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: 'C:\\repo',
          // git always reports paths with '/' regardless of host OS.
          files: [
            makeEntry({
              path: 'src/foo.ts',
              unstagedStatus: GitFileStatusCode.MODIFIED,
              linesAdded: 3,
              linesDeleted: 1,
            }),
            makeEntry({
              path: 'build/',
              unstagedStatus: GitFileStatusCode.UNTRACKED,
            }),
          ],
        })

        await store.refresh('worker1', 'C:\\repo')

        // getFileStatus: flavor-native abs path → relativized and compared
        // against the git-style path.
        const entry = store.getFileStatus('C:\\repo\\src\\foo.ts')
        expect(entry?.path).toBe('src/foo.ts')

        // Subdir stats scoped to Windows path.
        const srcStats = store.getNodeDiffStats('C:\\repo\\src', true)
        expect(srcStats.added).toBe(3)
        expect(srcStats.deleted).toBe(1)

        // Untracked dir "build/" should match merged descendant C:\repo\build\bin.
        const buildStats = store.getNodeDiffStats('C:\\repo\\build\\bin', true)
        expect(buildStats.untracked).toBe(1)

        expect(store.hasChanges('C:\\repo\\build\\bin')).toBe(true)
        expect(store.hasChanges('C:\\repo\\other')).toBe(false)

        dispose()
      })
    })

    it('case-insensitively matches C:\\ prefixes', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: 'C:\\Repo',
          files: [makeEntry({
            path: 'src/foo.ts',
            unstagedStatus: GitFileStatusCode.MODIFIED,
          })],
        })

        await store.refresh('worker1', 'C:\\Repo')

        // Different casing on the drive letter / dir should still resolve.
        expect(store.getFileStatus('c:\\repo\\src\\foo.ts')?.path).toBe('src/foo.ts')

        dispose()
      })
    })
  })

  describe('hasChanges at repoRoot', () => {
    it('returns true when any file has changed', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [makeEntry({
            path: 'src/foo.ts',
            unstagedStatus: GitFileStatusCode.MODIFIED,
          })],
        })

        await store.refresh('worker1', '/repo')

        expect(store.hasChanges('/repo')).toBe(true)

        dispose()
      })
    })

    it('returns false when the repo is clean', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [],
        })

        await store.refresh('worker1', '/repo')

        expect(store.hasChanges('/repo')).toBe(false)

        dispose()
      })
    })
  })

  describe('toplevel field (worktree-aware tab stamping identity)', () => {
    it('stores resp.toplevel separately from repoRoot for a worktree query', async () => {
      // For a worktree query the worker returns the MAIN repo root via
      // `repo_root` and the WORKTREE dir via `toplevel`. The frontend
      // store has to keep them separate so syncGitStatusToTabs uses
      // toplevel for tab matching — otherwise a focused worktree's
      // branch leaks onto every main-tree tab whose gitToplevel equals
      // the canonical repo root.
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          toplevel: '/repo-wts/feature',
          originUrl: '',
          currentBranch: 'feature',
          isWorktree: true,
          files: [],
        })

        await store.refresh('worker1', '/repo-wts/feature')

        expect(store.state.repoRoot).toBe('/repo')
        expect(store.state.toplevel).toBe('/repo-wts/feature')
        expect(store.state.isWorktree).toBe(true)

        dispose()
      })
    })

    it('does NOT alias an empty resp.toplevel onto resp.repoRoot', async () => {
      // `toplevel` is authoritative: the worker sets it on every success
      // path, so an empty value means "no working tree", not "old build".
      // Aliasing it to `repoRoot` would hand a WORKTREE query the MAIN
      // tree's root — the exact cross-stamp `toplevel` was introduced to
      // prevent — so an empty toplevel must stay empty and let
      // `applyGitStatusToTabs` decline to stamp.
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          // `''`, not omitted: proto materialises an unset string as empty,
          // so this is the shape the wire actually delivers.
          toplevel: '',
          originUrl: '',
          currentBranch: 'main',
          isWorktree: false,
          files: [],
        })
        await store.refresh('worker1', '/repo')
        expect(store.state.toplevel).toBe('')
        expect(store.state.repoRoot, 'repoRoot is still recorded for path resolution').toBe('/repo')

        dispose()
      })
    })

    it('clears toplevel on refresh error alongside the other reset fields', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          toplevel: '/repo-wts/feature',
          originUrl: '',
          currentBranch: 'feature',
          isWorktree: true,
          files: [],
        })
        await store.refresh('worker1', '/repo-wts/feature')
        expect(store.state.toplevel).toBe('/repo-wts/feature')

        mockGetGitFileStatus.mockRejectedValueOnce(new Error('network blip'))
        await store.refresh('worker1', '/repo-wts/feature')
        expect(store.state.toplevel).toBe('')

        dispose()
      })
    })
  })

  describe('refresh equality guard', () => {
    it('preserves state.files reference when content is unchanged', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        const firstFiles = [
          makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 1 }),
          makeEntry({ path: 'b.txt', unstagedStatus: GitFileStatusCode.UNTRACKED }),
        ]
        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: firstFiles,
        })
        await store.refresh('worker1', '/repo')
        const firstRef = store.state.files

        // Different array with identical contents — guard should prevent
        // reassignment so downstream memos don't invalidate.
        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 1 }),
            makeEntry({ path: 'b.txt', unstagedStatus: GitFileStatusCode.UNTRACKED }),
          ],
        })
        await store.refresh('worker1', '/repo')
        expect(store.state.files).toBe(firstRef)

        dispose()
      })
    })

    it('applies a changed field to the entry in place', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED })],
        })
        await store.refresh('worker1', '/repo')
        const firstRef = store.state.files

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [makeEntry({
            path: 'a.txt',
            unstagedStatus: GitFileStatusCode.MODIFIED,
            linesAdded: 2,
          })],
        })
        await store.refresh('worker1', '/repo')
        // The new value lands, and the array keeps its identity: a keyed
        // reconcile writes through to the entry instead of swapping the array.
        expect(store.state.files[0].linesAdded).toBe(2)
        expect(store.state.files).toBe(firstRef)

        dispose()
      })
    })

    /**
     * The sidebar SORTS the flat changed-file list by size and modification
     * time, so a size-only change has to reach the row -- but it must not drag
     * the rest of the store with it.
     *
     * An agent that rewrites one file in place leaves its git status and its
     * line counts alone, so this is the common turn-end shape, not a rare one.
     * The old field-by-field guard answered "something differs" by replacing
     * the whole array, which rebuilt `filesByPath` and `prefixIndex` (which
     * walks every file x every ancestor) and handed `<For>` fresh objects for
     * every row. These two assertions are the ones that would have caught it:
     * the value moves, and a consumer that reads only paths does not re-run.
     */
    it('moves a size-only change without invalidating a path-only consumer', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED, size: 10n, modTime: '2026-01-01T00:00:00.000000000Z' }),
            makeEntry({ path: 'b.txt', unstagedStatus: GitFileStatusCode.MODIFIED, size: 20n, modTime: '2026-01-01T00:00:00.000000000Z' }),
          ],
        })
        await store.refresh('worker1', '/repo')

        // Stands in for filesByPath / prefixIndex: it reads only `path`.
        let pathMemoRuns = 0
        const paths = createMemo(() => {
          pathMemoRuns++
          return store.state.files.map(f => f.path).join(',')
        })
        // Stands in for the flat list's comparator: it reads `size`.
        let sizeMemoRuns = 0
        const sizes = createMemo(() => {
          sizeMemoRuns++
          return store.state.files.map(f => Number(f.size ?? -1n)).join(',')
        })
        expect(paths()).toBe('a.txt,b.txt')
        expect(sizes()).toBe('10,20')
        expect(pathMemoRuns).toBe(1)
        expect(sizeMemoRuns).toBe(1)

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED, size: 4096n, modTime: '2026-01-01T00:00:00.000000000Z' }),
            makeEntry({ path: 'b.txt', unstagedStatus: GitFileStatusCode.MODIFIED, size: 20n, modTime: '2026-01-01T00:00:00.000000000Z' }),
          ],
        })
        await store.refresh('worker1', '/repo')

        expect(sizes(), 'a size-only change must reach the row').toBe('4096,20')
        expect(sizeMemoRuns, 'the size consumer re-runs').toBe(2)
        expect(paths()).toBe('a.txt,b.txt')
        expect(pathMemoRuns, 'a path-only consumer must not re-run').toBe(1)

        // The same holds for the modification time.
        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED, size: 4096n, modTime: '2026-02-01T00:00:00.000000000Z' }),
            makeEntry({ path: 'b.txt', unstagedStatus: GitFileStatusCode.MODIFIED, size: 20n, modTime: '2026-01-01T00:00:00.000000000Z' }),
          ],
        })
        await store.refresh('worker1', '/repo')

        expect(store.state.files[0].modTime).toBe('2026-02-01T00:00:00.000000000Z')
        expect(paths()).toBe('a.txt,b.txt')
        expect(pathMemoRuns, 'an mtime-only change must not re-run it either').toBe(1)

        dispose()
      })
    })

    it('adds and removes entries without disturbing the survivors', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED }),
            makeEntry({ path: 'b.txt', unstagedStatus: GitFileStatusCode.MODIFIED }),
          ],
        })
        await store.refresh('worker1', '/repo')
        const survivor = store.state.files[0]

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/repo',
          files: [
            makeEntry({ path: 'a.txt', unstagedStatus: GitFileStatusCode.MODIFIED }),
            makeEntry({ path: 'c.txt', unstagedStatus: GitFileStatusCode.UNTRACKED }),
          ],
        })
        await store.refresh('worker1', '/repo')

        expect(store.state.files.map(f => f.path)).toEqual(['a.txt', 'c.txt'])
        // `<For>` maps by reference, so an untouched row must keep its object
        // or every sibling row is disposed and rebuilt around it.
        expect(store.state.files[0]).toBe(survivor)

        dispose()
      })
    })
  })

  describe('statusRoot', () => {
    /**
     * `git status --porcelain=v2` emits paths relative to the WORKING-TREE
     * root. In a linked worktree the worker reports `repoRoot` as the parent
     * repo, and LeapMux puts a worktree at `<repo>-worktrees/<branch>`, a
     * SIBLING of the repo -- so relativizing against `repoRoot` matched nothing
     * and every worktree tab lost its status icons, its diff badges and its
     * git filter tabs.
     */
    it('resolves a worktree path against toplevel, not the parent repo', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/src/leapmux',
          toplevel: '/src/leapmux-worktrees/feature',
          isWorktree: true,
          files: [makeEntry({ path: 'src/app.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 3 })],
        })
        await store.refresh('worker1', '/src/leapmux-worktrees/feature')

        expect(store.statusRoot()).toBe('/src/leapmux-worktrees/feature')
        expect(store.getFileStatus('/src/leapmux-worktrees/feature/src/app.ts')?.linesAdded).toBe(3)
        expect(store.getNodeDiffStats('/src/leapmux-worktrees/feature/src', true).added).toBe(3)
        expect(store.hasChanges('/src/leapmux-worktrees/feature/src')).toBe(true)
        // The parent repo is not the base; a same-named path under it is a
        // different file and must not pick up the worktree's status.
        expect(store.getFileStatus('/src/leapmux/src/app.ts')).toBeUndefined()

        dispose()
      })
    })

    it('falls back to repoRoot for a main-tree query', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '/src/leapmux',
          toplevel: '/src/leapmux',
          files: [makeEntry({ path: 'src/app.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 1 })],
        })
        await store.refresh('worker1', '/src/leapmux')

        expect(store.statusRoot()).toBe('/src/leapmux')
        expect(store.getFileStatus('/src/leapmux/src/app.ts')?.linesAdded).toBe(1)

        dispose()
      })
    })
  })

  describe('clear', () => {
    it('clears errorHint along with every other field', async () => {
      await createRoot(async (dispose) => {
        const store = createGitFileStatusStore()

        mockGetGitFileStatus.mockResolvedValueOnce({
          repoRoot: '',
          errorHint: 'not a git repository',
          files: [],
        })
        await store.refresh('worker1', '/plain/dir')
        expect(store.state.errorHint).toBe('not a git repository')

        store.clear()
        expect(store.state.errorHint).toBe('')

        dispose()
      })
    })
  })
})
