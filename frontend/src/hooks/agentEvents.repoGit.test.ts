import type { AgentStatusChange } from '~/generated/leapmux/v1/agent_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { applyAgentStatusTabUpdate } from '~/hooks/agentEvents'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { createChatStore } from '~/stores/chat.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

const WS = 'ws-agent-repo-git'

function statusChange(overrides: Partial<AgentStatusChange> & { agentId: string }): AgentStatusChange {
  return {
    status: AgentStatus.ACTIVE,
    agentSessionId: 'sess-1',
    optionGroups: [],
    workerOnline: true,
    ...overrides,
  } as AgentStatusChange
}

describe('applyAgentStatusTabUpdate repo git wiring', () => {
  it('upserts git status from a status broadcast into the repo-keyed store', () => {
    createRoot((dispose) => {
      const harness = installTestBridge({ workspaceId: WS })
      const { view, metadata } = createTestTabStores(WS)
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '0', workerId: 'w1' })
      const repoGitStore = createRepoGitStore()
      const chatStore = createChatStore()

      applyAgentStatusTabUpdate(
        statusChange({
          agentId: 'a1',
          gitStatus: {
            toplevel: '/repo',
            branch: 'feature',
            originUrl: 'git@example.com:org/repo.git',
            isWorktree: true,
          } as never,
        }),
        { chatStore, view, metadata, repoGitStore },
        createLoadingSignal(),
      )

      const state = repoGitStore.get(repoKey('w1', '/repo'))
      expect(state?.branch).toBe('feature')
      expect(state?.originUrl).toBe('git@example.com:org/repo.git')
      expect(state?.isWorktree).toBe(true)
      expect(state?.gitStatusSeen).toBe(true)
      expect(metadata.get('a1')?.gitToplevel).toBe('/repo')
      dispose()
    })
  })

  it('uses streamWorkerId when the tab row is not joined yet', () => {
    createRoot((dispose) => {
      const repoGitStore = createRepoGitStore()
      const chatStore = createChatStore()
      const harness = installTestBridge({ workspaceId: WS })
      const { view, metadata } = createTestTabStores(WS)
      void harness

      applyAgentStatusTabUpdate(
        statusChange({
          agentId: 'unjoined',
          gitStatus: { toplevel: '/repo', branch: 'main' } as never,
        }),
        { chatStore, view, metadata, repoGitStore },
        createLoadingSignal(),
        'w-stream',
      )

      expect(repoGitStore.get(repoKey('w-stream', '/repo'))?.branch).toBe('main')
      dispose()
    })
  })

  it('migrates a probe-path orphan when toplevel resolves', () => {
    createRoot((dispose) => {
      const harness = installTestBridge({ workspaceId: WS })
      const { view, metadata } = createTestTabStores(WS)
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '0', workerId: 'w1' })
      metadata.patch('a1', { workingDir: '/repo/pkg' })
      const repoGitStore = createRepoGitStore()
      const chatStore = createChatStore()
      const orphanKey = repoKey('w1', '/repo/pkg')
      repoGitStore.upsert(orphanKey, {
        workerId: 'w1',
        errorHint: 'not a git repository',
        gitStatusSeen: true,
      })

      applyAgentStatusTabUpdate(
        statusChange({
          agentId: 'a1',
          gitStatus: { toplevel: '/repo', branch: 'main' } as never,
        }),
        { chatStore, view, metadata, repoGitStore },
        createLoadingSignal(),
      )

      expect(repoGitStore.get(orphanKey)).toBeUndefined()
      expect(repoGitStore.get(repoKey('w1', '/repo'))?.branch).toBe('main')
      dispose()
    })
  })

  it('keeps a stamped branch when metadata broadcasts a different branch', () => {
    createRoot((dispose) => {
      const harness = installTestBridge({ workspaceId: WS })
      const { view, metadata } = createTestTabStores(WS)
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '0', workerId: 'w1' })
      const repoGitStore = createRepoGitStore()
      const chatStore = createChatStore()
      repoGitStore.upsert(repoKey('w1', '/repo'), {
        workerId: 'w1',
        toplevel: '/repo',
        branch: 'stamped',
        branchPinnedUntilRefresh: true,
      })

      applyAgentStatusTabUpdate(
        statusChange({
          agentId: 'a1',
          gitStatus: { toplevel: '/repo', branch: 'other' } as never,
        }),
        { chatStore, view, metadata, repoGitStore },
        createLoadingSignal(),
      )

      expect(repoGitStore.get(repoKey('w1', '/repo'))?.branch).toBe('stamped')
      dispose()
    })
  })
})
