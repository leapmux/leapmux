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
const mockUpdateAgentSettings = vi.fn()
const mockShowWarnToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  closeAgent: (...args: unknown[]) => mockCloseAgent(...args as [string, { agentId: string, worktreeAction?: WorktreeAction }]),
  openAgent: vi.fn(),
  sendAgentMessage: vi.fn(),
  sendControlResponse: vi.fn(),
  updateAgentSettings: (...args: unknown[]) => mockUpdateAgentSettings(...args),
  retryAgentMessage: vi.fn(),
  deleteAgentMessage: vi.fn(),
  listAvailableProviders: vi.fn().mockResolvedValue({ providers: [] }),
}))

vi.mock('~/api/clients', () => ({
  workspaceClient: {
    addTab: vi.fn().mockResolvedValue({}),
    removeTab: vi.fn().mockResolvedValue({}),
  },
}))

vi.mock('~/components/common/Toast', () => ({
  showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
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
  describe('handleOptionGroupChange', () => {
    it('uses option-group metadata for default rollback and error labeling', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, ops } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'a-1',
            workerId: 'w-1',
            extraSettings: { opencode_mode: 'safe' },
            availableOptionGroups: [{
              key: 'opencode_mode',
              label: 'Execution Mode',
              options: [
                { id: 'safe', name: 'Safe', isDefault: true },
                { id: 'fast', name: 'Fast' },
              ],
            }],
          })
          agentStore.addAgent(agent)
          mockUpdateAgentSettings.mockRejectedValueOnce(new Error('boom'))

          await ops.handleOptionGroupChange('a-1', 'opencode_mode', 'fast')

          expect(mockUpdateAgentSettings).toHaveBeenCalledWith('w-1', {
            agentId: 'a-1',
            settings: { extraSettings: { opencode_mode: 'fast' } },
          })
          expect(agentStore.state.agents.find(a => a.id === 'a-1')?.extraSettings?.opencode_mode).toBe('safe')
          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to change Execution Mode', expect.any(Error))
        }
        finally {
          dispose()
        }
      })
    })

    it('rollback re-reads current state to avoid clobbering concurrent changes', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, ops } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'a-concurrent',
            workerId: 'w-1',
            extraSettings: { sandbox_policy: 'workspace-write', network_access: 'restricted' },
            availableOptionGroups: [
              {
                key: 'sandbox_policy',
                label: 'Sandbox Policy',
                options: [
                  { id: 'workspace-write', name: 'Workspace Write', isDefault: true },
                  { id: 'danger-full-access', name: 'Full Access' },
                ],
              },
              {
                key: 'network_access',
                label: 'Network Access',
                options: [
                  { id: 'restricted', name: 'Restricted', isDefault: true },
                  { id: 'enabled', name: 'Enabled' },
                ],
              },
            ],
          })
          agentStore.addAgent(agent)

          // First call will fail; second succeeds.
          let rejectFirst!: (err: Error) => void
          mockUpdateAgentSettings.mockImplementationOnce(() => new Promise((_resolve, reject) => {
            rejectFirst = reject
          }))
          mockUpdateAgentSettings.mockResolvedValueOnce({})

          // Launch both changes concurrently.
          const p1 = ops.handleOptionGroupChange('a-concurrent', 'sandbox_policy', 'danger-full-access')
          const p2 = ops.handleOptionGroupChange('a-concurrent', 'network_access', 'enabled')

          // Both optimistic updates should be applied.
          const mid = agentStore.state.agents.find(a => a.id === 'a-concurrent')
          expect(mid?.extraSettings?.sandbox_policy).toBe('danger-full-access')
          expect(mid?.extraSettings?.network_access).toBe('enabled')

          // Fail the first RPC — its rollback should only revert sandbox_policy,
          // leaving network_access intact.
          rejectFirst(new Error('sandbox fail'))
          await p1
          await p2

          const final = agentStore.state.agents.find(a => a.id === 'a-concurrent')
          expect(final?.extraSettings?.sandbox_policy).toBe('workspace-write')
          expect(final?.extraSettings?.network_access).toBe('enabled')
        }
        finally {
          dispose()
        }
      })
    })

    it('falls back to the first option when no explicit default is marked', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, ops } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'a-2',
            workerId: 'w-1',
            availableOptionGroups: [{
              key: 'opencode_mode',
              label: 'Execution Mode',
              options: [
                { id: 'safe', name: 'Safe' },
                { id: 'fast', name: 'Fast' },
              ],
            }],
          })
          agentStore.addAgent(agent)
          mockUpdateAgentSettings.mockRejectedValueOnce(new Error('boom'))

          await ops.handleOptionGroupChange('a-2', 'opencode_mode', 'fast')

          expect(agentStore.state.agents.find(a => a.id === 'a-2')?.extraSettings?.opencode_mode).toBe('safe')
        }
        finally {
          dispose()
        }
      })
    })
  })

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
