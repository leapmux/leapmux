import type { CloseAgentResponse } from '~/generated/leapmux/v1/agent_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { useAgentOperations } from '~/components/shell/useAgentOperations'
import { AgentInfoSchema } from '~/generated/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createAgentStore } from '~/stores/agent.store'
import { createControlStore } from '~/stores/control.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'

const mockCloseAgent = vi.fn<(workerId: string, req: { agentId: string, worktreeAction?: WorktreeAction }) => Promise<CloseAgentResponse>>()

vi.mock('~/api/workerRpc', () => ({
  closeAgent: (...args: unknown[]) => mockCloseAgent(...args as [string, { agentId: string, worktreeAction?: WorktreeAction }]),
  openAgent: vi.fn(),
  sendAgentMessage: vi.fn(),
  sendControlResponse: vi.fn(),
  updateAgentSettings: vi.fn(),
  retryAgentMessage: vi.fn(),
  deleteAgentMessage: vi.fn(),
}))

vi.mock('~/api/clients', () => ({
  workspaceClient: {
    addTab: vi.fn().mockResolvedValue({}),
    removeTab: vi.fn().mockResolvedValue({}),
  },
}))

vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
}))

function setup() {
  const agentStore = createAgentStore()
  const controlStore = createControlStore()
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()

  const ops = useAgentOperations({
    agentStore,
    chatStore: {} as any,
    controlStore,
    tabStore,
    layoutStore,
    settingsLoading: { start: vi.fn(), stop: vi.fn() },
    isActiveWorkspaceMutatable: () => true,
    activeWorkspace: () => ({ id: 'ws-1' } as Workspace),
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp' }),
    setShowNewAgentDialog: vi.fn(),
    setNewAgentLoading: vi.fn(),
    setShowResumeDialog: vi.fn(),
  })

  return { agentStore, controlStore, tabStore, layoutStore, ops }
}

describe('useAgentOperations', () => {
  describe('handleCloseAgent', () => {
    it('should call closeAgent RPC when workerId is available', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-1', workerId: 'w-1' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-1', title: 'Agent 1', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({ worktreeCleanupPending: false, worktreeId: '' } as CloseAgentResponse)

          await ops.handleCloseAgent('a-1')

          expect(mockCloseAgent).toHaveBeenCalledWith('w-1', { agentId: 'a-1', worktreeAction: WorktreeAction.UNSPECIFIED })
          expect(agentStore.state.agents.find(a => a.id === 'a-1')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    it('should skip RPC and still remove tab when workerId is missing', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, tabStore, ops } = setup()
          // Agent with no workerId (e.g. worker gone after restart)
          const agent = create(AgentInfoSchema, { id: 'a-2', workerId: '' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-2', title: 'Agent 2', tileId: 'tile-1', workerId: '', workingDir: '' })

          mockCloseAgent.mockClear()

          await ops.handleCloseAgent('a-2')

          expect(mockCloseAgent).not.toHaveBeenCalled()
          expect(agentStore.state.agents.find(a => a.id === 'a-2')).toBeUndefined()
          expect(tabStore.state.tabs.find(t => t.id === 'a-2')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    it('should pass KEEP worktree action when worktreeChoice is keep', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-1', workerId: 'w-1' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-1', title: 'Agent 1', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({ worktreeCleanupPending: false, worktreeId: '' } as CloseAgentResponse)

          await ops.handleCloseAgent('a-1', WorktreeAction.KEEP)

          expect(mockCloseAgent).toHaveBeenCalledWith('w-1', { agentId: 'a-1', worktreeAction: WorktreeAction.KEEP })
        }
        finally {
          dispose()
        }
      })
    })

    it('should pass REMOVE worktree action when worktreeChoice is remove', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-1', workerId: 'w-1' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-1', title: 'Agent 1', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({ worktreeCleanupPending: false, worktreeId: '' } as CloseAgentResponse)

          await ops.handleCloseAgent('a-1', WorktreeAction.REMOVE)

          expect(mockCloseAgent).toHaveBeenCalledWith('w-1', { agentId: 'a-1', worktreeAction: WorktreeAction.REMOVE })
        }
        finally {
          dispose()
        }
      })
    })

    it('should skip RPC when agent is not found in store', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()

          mockCloseAgent.mockClear()

          await ops.handleCloseAgent('nonexistent')

          expect(mockCloseAgent).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })
  })
})
