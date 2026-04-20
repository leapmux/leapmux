import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createRoot } from 'solid-js'
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

    it('replaces state.files when content differs', async () => {
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
          // Different linesAdded — should trigger replacement.
          files: [makeEntry({
            path: 'a.txt',
            unstagedStatus: GitFileStatusCode.MODIFIED,
            linesAdded: 2,
          })],
        })
        await store.refresh('worker1', '/repo')
        expect(store.state.files).not.toBe(firstRef)
        expect(store.state.files[0].linesAdded).toBe(2)

        dispose()
      })
    })
  })
})
