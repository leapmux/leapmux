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
