import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { syncGitStatusToTabs } from '~/components/shell/syncGitStatusToTabs'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createAgentStore } from '~/stores/agent.store'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { createTabStore, tabKey } from '~/stores/tab.store'

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
      const agentStore = createAgentStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo/sub' })
      tabStore.addTab({ type: TabType.TERMINAL, id: 't2', workingDir: '/elsewhere' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore, agentStore })

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

  it('reads agent workingDir from agentStore (not from the agent tab itself)', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const agentStore = createAgentStore()
      const gitFileStatusStore = createGitFileStatusStore()

      agentStore.addAgent({ id: 'a1', workingDir: '/repo/agent-cwd' } as Parameters<typeof agentStore.addAgent>[0])
      tabStore.addTab({ type: TabType.AGENT, id: 'a1' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore, agentStore })

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
      const agentStore = createAgentStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore, agentStore })

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
})
