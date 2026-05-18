import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { syncGitStatusToTabs } from '~/components/shell/syncGitStatusToTabs'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { tabKey } from '~/stores/tab.helpers'
import { createTabStore } from '~/stores/tab.store'

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

describe('syncGitStatusToTabs', () => {
  it('stamps git fields on terminal tabs whose workingDir sits under repoRoot', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo/sub' })
      tabStore.addTab({ type: TabType.TERMINAL, id: 't2', workingDir: '/elsewhere' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'git@example.com:org/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 5, linesDeleted: 2 }),
          makeEntry({ path: 'b.ts', unstagedStatus: GitFileStatusCode.UNTRACKED }),
        ],
      })

      await gitFileStatusStore.refresh('worker1', '/repo')

      const t1 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      expect(t1?.gitDiffAdded).toBe(5)
      expect(t1?.gitDiffDeleted).toBe(2)
      expect(t1?.gitDiffUntracked).toBe(1)
      expect(t1?.gitOriginUrl).toBe('git@example.com:org/repo.git')
      expect(t1?.gitBranch).toBe('main')

      // Tab outside the repo isn't touched.
      const t2 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't2' }))
      expect(t2?.gitDiffAdded).toBeUndefined()
      expect(t2?.gitOriginUrl).toBeUndefined()

      dispose()
    })
  })

  // Per-agent metadata lives on the Tab record now, so the agent's
  // workingDir comes off the tab directly — no separate agentStore.
  it('reads agent workingDir from the tab record itself', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.AGENT, id: 'a1', workingDir: '/repo/agent-cwd' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: '',
        currentBranch: '',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 1, linesDeleted: 0 }),
        ],
      })

      await gitFileStatusStore.refresh('worker1', '/repo')

      const a1 = tabStore.getTabByKey(tabKey({ type: TabType.AGENT, id: 'a1' }))
      expect(a1?.gitDiffAdded).toBe(1)

      dispose()
    })
  })

  it('skips no-op writes when fields already match the resolved git stats', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValue({
        repoRoot: '/repo',
        originUrl: '',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 3, linesDeleted: 0 }),
        ],
      })

      await gitFileStatusStore.refresh('worker1', '/repo')
      const after1 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      const refBefore = after1
      expect(after1?.gitDiffAdded).toBe(3)

      // Second refresh with identical files: predicate filters out matching
      // tabs so no `updateMatchingTabs` write fires, leaving the same proxy
      // reference in place.
      await gitFileStatusStore.refresh('worker1', '/repo')
      const after2 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      expect(after2?.gitDiffAdded).toBe(3)
      expect(after2).toBe(refBefore)

      dispose()
    })
  })

  it('does not stamp the focused repo onto a nested tab that belongs to a different repo', async () => {
    // Sidebar grouping bug: tab A is rooted at /parent (its own git repo),
    // tab B's working dir is /parent/sub but it belongs to a *different*
    // git repo whose toplevel is /parent/sub. When tab A is the focused
    // repo the directory tree publishes A's repoRoot. The sync effect
    // must not overwrite tab B's identity just because B's path is
    // lexically inside A's tree — otherwise the workspace tab tree
    // groups them under one repo.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({
        type: TabType.TERMINAL,
        id: 'a',
        workingDir: '/parent',
        gitOriginUrl: 'https://example.com/a.git',
        gitToplevel: '/parent',
        gitBranch: 'main',
      })
      tabStore.addTab({
        type: TabType.TERMINAL,
        id: 'b',
        workingDir: '/parent/sub',
        gitOriginUrl: 'https://example.com/b.git',
        gitToplevel: '/parent/sub',
        gitBranch: 'feature',
      })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/parent',
        originUrl: 'https://example.com/a.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/parent')

      const tabB = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 'b' }))
      expect(tabB?.gitOriginUrl).toBe('https://example.com/b.git')
      expect(tabB?.gitToplevel).toBe('/parent/sub')
      expect(tabB?.gitBranch).toBe('feature')

      dispose()
    })
  })

  it('stamps git fields on a FILE tab whose filePath sits under repoRoot', async () => {
    // FILE tabs hydrated after a refresh come back with only `filePath`
    // (the CRDT projection doesn't carry workingDir, and the path
    // hydrator only fills filePath). syncGitStatusToTabs must use
    // filePath as a stand-in for the containment check — otherwise the
    // workspace tree groups the tab under the wrong repo (or in the
    // ungrouped bucket).
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.FILE, id: 'f1', filePath: '/repo/src/foo.ts' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'foo.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 7, linesDeleted: 1 }),
        ],
      })

      await gitFileStatusStore.refresh('worker1', '/repo')

      const f1 = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'f1' }))
      expect(f1?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(f1?.gitBranch).toBe('main')
      expect(f1?.gitToplevel).toBe('/repo')
      expect(f1?.gitDiffAdded).toBe(7)
      expect(f1?.gitDiffDeleted).toBe(1)

      dispose()
    })
  })

  it('does not stamp a FILE tab whose filePath lives outside repoRoot', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      // FILE tab outside the focused repo's tree — must stay unstamped.
      tabStore.addTab({ type: TabType.FILE, id: 'outside', filePath: '/other-repo/src/x.ts' })
      // Sanity reference: an inside tab so we can assert the effect did fire.
      tabStore.addTab({ type: TabType.FILE, id: 'inside', filePath: '/repo/y.ts' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      const outside = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'outside' }))
      expect(outside?.gitOriginUrl).toBeUndefined()
      expect(outside?.gitToplevel).toBeUndefined()

      const inside = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'inside' }))
      expect(inside?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(inside?.gitToplevel).toBe('/repo')

      dispose()
    })
  })

  it('prefers workingDir over filePath on a FILE tab that has both', async () => {
    // Live-session FILE tabs (those opened via useTabOperations.openFile)
    // carry both `workingDir` and `filePath`. The containment check uses
    // workingDir first; we verify by giving the FILE tab a workingDir
    // OUTSIDE the focused repo and a filePath INSIDE — the tab must not
    // be stamped, proving workingDir won.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({
        type: TabType.FILE,
        id: 'f1',
        filePath: '/repo/inside.ts',
        workingDir: '/elsewhere',
      })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      const f1 = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'f1' }))
      expect(f1?.gitOriginUrl).toBeUndefined()
      expect(f1?.gitToplevel).toBeUndefined()

      dispose()
    })
  })

  it('skips a FILE tab that has neither workingDir nor a filePath yet', async () => {
    // Pre-hydration: the path hydrator hasn't filled filePath in yet,
    // and the CRDT projection didn't carry workingDir. There's nothing
    // to compare against repoRoot, so the effect must leave the tab
    // alone (no false positives).
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.FILE, id: 'f1' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      const f1 = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'f1' }))
      expect(f1?.gitOriginUrl).toBeUndefined()
      expect(f1?.gitToplevel).toBeUndefined()
      expect(f1?.gitBranch).toBeUndefined()

      dispose()
    })
  })

  it('stamps a tab that is added AFTER the git store already has data', async () => {
    // Regression: when the user opens a new file in the same focused
    // repo, the git store state doesn't change (same files, branch,
    // repoRoot). Before the fix, the effect tracked only store state,
    // so the new FILE tab arrived after the last store-state change
    // and never got stamped — the workspace tab tree placed it under
    // "Ungrouped" until a page refresh re-ran the restore→refresh
    // sequence in the right order.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'foo.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 4, linesDeleted: 2 }),
        ],
      })
      // Populate the store first. No tabs exist yet, so the effect
      // runs and finds nothing to stamp.
      await gitFileStatusStore.refresh('worker1', '/repo')

      // Now the user opens a file — new FILE tab arrives after the
      // store already settled. The effect must still notice and stamp
      // it on the next reactive flush.
      tabStore.addTab({ type: TabType.FILE, id: 'newly-opened', filePath: '/repo/foo.ts', workingDir: '/repo' })
      await Promise.resolve()

      const tab = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'newly-opened' }))
      expect(tab?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(tab?.gitBranch).toBe('main')
      expect(tab?.gitToplevel).toBe('/repo')
      expect(tab?.gitDiffAdded).toBe(4)
      expect(tab?.gitDiffDeleted).toBe(2)

      dispose()
    })
  })

  it('seeds gitToplevel on a tab that has not yet learned its toplevel', async () => {
    // First-sync fallback: a freshly-created tab has no gitToplevel yet,
    // so the path-prefix check is the best we can do. After the first
    // sync, the tab carries its authoritative gitToplevel for subsequent
    // runs to compare against.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo/nested' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      const t1 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      expect(t1?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(t1?.gitToplevel).toBe('/repo')
      expect(t1?.gitBranch).toBe('main')

      dispose()
    })
  })
})
