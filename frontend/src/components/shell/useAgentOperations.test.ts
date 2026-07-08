import type { CloseAgentResponse } from '~/generated/leapmux/v1/agent_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useAgentOperations } from '~/components/shell/useAgentOperations'
import { AgentInfoSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { KEY_MRU_AGENT_PROVIDERS, localStorageSet } from '~/lib/browserStorage'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createControlStore } from '~/stores/control.store'
import { createLayoutStore } from '~/stores/layout.store'
import { protoToAgentTabFields } from '~/stores/tab.helpers'
import { createTabStore } from '~/stores/tab.store'
import { flush } from '~/test-support/async'

const mockCloseAgent = vi.fn<(workerId: string, req: { agentId: string, worktreeAction?: WorktreeAction }) => Promise<CloseAgentResponse>>()
const mockOpenAgent = vi.fn()
const mockSendAgentRawMessage = vi.fn()
const mockSendAgentMessage = vi.fn()
const mockInterruptAgent = vi.fn()
const mockUpdateAgentSettings = vi.fn()
const mockListAvailableProviders = vi.fn().mockResolvedValue({ providers: [] })
const mockShowWarnToast = vi.fn()
const mockDeleteAgentMessage = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  closeAgent: (...args: unknown[]) => mockCloseAgent(...args as [string, { agentId: string, worktreeAction?: WorktreeAction }]),
  openAgent: (...args: unknown[]) => mockOpenAgent(...args),
  sendAgentMessage: (...args: unknown[]) => mockSendAgentMessage(...args),
  sendAgentRawMessage: (...args: unknown[]) => mockSendAgentRawMessage(...args),
  interruptAgent: (...args: unknown[]) => mockInterruptAgent(...args),
  sendControlResponse: vi.fn(),
  updateAgentSettings: (...args: unknown[]) => mockUpdateAgentSettings(...args),
  retryAgentMessage: vi.fn(),
  deleteAgentMessage: (...args: unknown[]) => mockDeleteAgentMessage(...args),
  listAvailableProviders: (...args: unknown[]) => mockListAvailableProviders(...args),
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
  const agentSessionStore = createAgentSessionStore()
  const controlStore = createControlStore()
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()

  const chatStore = {
    getMessages: vi.fn().mockReturnValue([]),
    clearMessageError: vi.fn(),
    setMessageError: vi.fn(),
    removeMessage: vi.fn(),
    forgetAgent: vi.fn(),
  } as any

  const ops = useAgentOperations({
    agentSessionStore,
    chatStore,
    controlStore,
    tabStore,
    layoutStore,
    settingsLoading: { start: vi.fn(), stop: vi.fn() },
    isActiveWorkspaceMutatable: () => true,
    activeWorkspace: () => ({ id: 'ws-1' } as Workspace),
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp' }),
    newAgentDialog: { open: vi.fn(), close: vi.fn(), isOpen: () => false },
    setNewAgentLoadingProvider: vi.fn(),
  })

  return { tabStore, agentSessionStore, controlStore, layoutStore, chatStore, ops }
}

describe('useAgentOperations', () => {
  beforeEach(() => {
    mockOpenAgent.mockReset()
    mockListAvailableProviders.mockReset()
    mockListAvailableProviders.mockResolvedValue({ providers: [] })
    localStorage.clear()
  })

  describe('handleOpenAgent', () => {
    it('uses the active agent tab provider for quick create', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX] })
          mockOpenAgent.mockResolvedValue({
            agent: create(AgentInfoSchema, {
              id: 'new-agent',
              workerId: 'w-1',
              workingDir: '/tmp',
              agentProvider: AgentProvider.CODEX,
            }),
          })
          localStorageSet(KEY_MRU_AGENT_PROVIDERS, [AgentProvider.CLAUDE_CODE])

          const { tabStore, ops } = setup()
          tabStore.addTab({
            type: TabType.AGENT,
            id: 'active-agent',
            tileId: 'tile-1',
            workerId: 'w-1',
            workingDir: '/tmp',
            agentProvider: AgentProvider.CODEX,
          })

          await flush()
          await ops.handleOpenAgent()

          expect(mockOpenAgent).toHaveBeenCalledWith('w-1', expect.objectContaining({
            agentProvider: AgentProvider.CODEX,
            workingDir: '/tmp',
          }))
        }
        finally {
          dispose()
        }
      })
    })

    it('falls back to the MRU provider when the active tab is not an agent tab', async () => {
      await createRoot(async (dispose) => {
        try {
          mockListAvailableProviders.mockResolvedValue({ providers: [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX] })
          mockOpenAgent.mockResolvedValue({
            agent: create(AgentInfoSchema, {
              id: 'new-agent',
              workerId: 'w-1',
              workingDir: '/tmp',
              agentProvider: AgentProvider.CODEX,
            }),
          })
          localStorageSet(KEY_MRU_AGENT_PROVIDERS, [AgentProvider.CODEX, AgentProvider.CLAUDE_CODE])

          const { tabStore, ops } = setup()
          tabStore.addTab({
            type: TabType.TERMINAL,
            id: 'terminal-1',
            tileId: 'tile-1',
            workerId: 'w-1',
            workingDir: '/tmp',
          })

          await flush()
          await ops.handleOpenAgent()

          expect(mockOpenAgent).toHaveBeenCalledWith('w-1', expect.objectContaining({
            agentProvider: AgentProvider.CODEX,
          }))
        }
        finally {
          dispose()
        }
      })
    })

    it('opens the dialog when the working directory is unknown', async () => {
      await createRoot(async (dispose) => {
        try {
          const newAgentDialog = { open: vi.fn(), close: vi.fn(), isOpen: () => false }
          const agentSessionStore = createAgentSessionStore()
          const controlStore = createControlStore()
          const tabStore = createTabStore()
          const layoutStore = createLayoutStore()
          const chatStore = {
            getMessages: vi.fn().mockReturnValue([]),
            clearMessageError: vi.fn(),
            setMessageError: vi.fn(),
            removeMessage: vi.fn(),
            forgetAgent: vi.fn(),
          } as any

          const ops = useAgentOperations({
            agentSessionStore,
            chatStore,
            controlStore,
            tabStore,
            layoutStore,
            settingsLoading: { start: vi.fn(), stop: vi.fn() },
            isActiveWorkspaceMutatable: () => true,
            activeWorkspace: () => ({ id: 'ws-1' } as Workspace),
            getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '' }),
            newAgentDialog,
            setNewAgentLoadingProvider: vi.fn(),
          })

          await ops.handleOpenAgent()

          expect(newAgentDialog.open).toHaveBeenCalled()
          expect(mockOpenAgent).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleInterrupt', () => {
    it('calls the worker-side InterruptAgent RPC', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'codex-1',
            workerId: 'w-1',
            agentProvider: AgentProvider.CODEX,
            agentSessionId: 'thread-1',
          })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          mockInterruptAgent.mockResolvedValue({})

          await ops.handleInterrupt('codex-1')

          expect(mockInterruptAgent).toHaveBeenCalledWith('w-1', {
            agentId: 'codex-1',
          })
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleAgentSettingChange', () => {
    it('rolls back to the prior current value and labels the toast from the option group', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          // The current value of an axis lives on its option group's
          // `currentValue`; the tab derives `optionValues.opencode_mode` from it.
          const agent = create(AgentInfoSchema, {
            id: 'a-1',
            workerId: 'w-1',
            optionGroups: [{
              id: 'opencode_mode',
              label: 'Execution Mode',
              currentValue: 'safe',
              defaultValue: 'safe',
              options: [
                { id: 'safe', name: 'Safe' },
                { id: 'fast', name: 'Fast' },
              ],
            }],
          })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          mockUpdateAgentSettings.mockRejectedValueOnce(new Error('boom'))

          await ops.handleAgentSettingChange('a-1', { sets: { opencode_mode: 'fast' } })

          // One RPC carrying the uniform `{ options: { [groupKey]: value } }` payload.
          expect(mockUpdateAgentSettings).toHaveBeenCalledWith('w-1', {
            agentId: 'a-1',
            settings: { options: { opencode_mode: 'fast' } },
          })
          // The failed change rolls back to the prior current value ('safe').
          expect(tabStore.getAgentTab('a-1')?.optionValues?.opencode_mode).toBe('safe')
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
          const { tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'a-concurrent',
            workerId: 'w-1',
            optionGroups: [
              {
                id: 'sandbox_policy',
                label: 'Sandbox Policy',
                currentValue: 'workspace-write',
                defaultValue: 'workspace-write',
                options: [
                  { id: 'workspace-write', name: 'Workspace Write' },
                  { id: 'danger-full-access', name: 'Full Access' },
                ],
              },
              {
                id: 'network_access',
                label: 'Network Access',
                currentValue: 'restricted',
                defaultValue: 'restricted',
                options: [
                  { id: 'restricted', name: 'Restricted' },
                  { id: 'enabled', name: 'Enabled' },
                ],
              },
            ],
          })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })

          // First call will fail; second succeeds.
          let rejectFirst!: (err: Error) => void
          mockUpdateAgentSettings.mockImplementationOnce(() => new Promise((_resolve, reject) => {
            rejectFirst = reject
          }))
          mockUpdateAgentSettings.mockResolvedValueOnce({})

          // Launch both changes concurrently.
          const p1 = ops.handleAgentSettingChange('a-concurrent', { sets: { sandbox_policy: 'danger-full-access' } })
          const p2 = ops.handleAgentSettingChange('a-concurrent', { sets: { network_access: 'enabled' } })

          // Both optimistic updates should be applied.
          const mid = tabStore.getAgentTab('a-concurrent')
          expect(mid?.optionValues?.sandbox_policy).toBe('danger-full-access')
          expect(mid?.optionValues?.network_access).toBe('enabled')

          // Fail the first RPC — its rollback should only revert sandbox_policy,
          // leaving network_access intact.
          rejectFirst(new Error('sandbox fail'))
          await p1
          await p2

          const final = tabStore.getAgentTab('a-concurrent')
          expect(final?.optionValues?.sandbox_policy).toBe('workspace-write')
          expect(final?.optionValues?.network_access).toBe('enabled')
        }
        finally {
          dispose()
        }
      })
    })

    it('rolls back to unset when the group had no prior current value', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          // No `currentValue` on the group, so the tab carries no prior value for
          // the axis; a failed change reverts by DELETING the key (not writing ''),
          // so agentTabOptionGroups falls through to the catalog's confirmed value
          // instead of blanking the group with a spurious empty override.
          const agent = create(AgentInfoSchema, {
            id: 'a-2',
            workerId: 'w-1',
            optionGroups: [{
              id: 'opencode_mode',
              label: 'Execution Mode',
              options: [
                { id: 'safe', name: 'Safe' },
                { id: 'fast', name: 'Fast' },
              ],
            }],
          })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          mockUpdateAgentSettings.mockRejectedValueOnce(new Error('boom'))

          await ops.handleAgentSettingChange('a-2', { sets: { opencode_mode: 'fast' } })

          const values = tabStore.getAgentTab('a-2')?.optionValues
          expect(values && 'opencode_mode' in values).toBe(false)
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleRetryMessage', () => {
    it('clears the delivery error before resending the message', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, chatStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-1', workerId: 'w-1' })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          chatStore.getMessages.mockReturnValue([{
            id: 'local-1',
            source: MessageSource.USER,
            content: new TextEncoder().encode(JSON.stringify({ content: 'retry me' })),
            contentCompression: ContentCompression.NONE,
          }])
          mockSendAgentMessage.mockResolvedValueOnce({})

          await ops.handleRetryMessage('a-1', 'local-1')

          expect(chatStore.clearMessageError).toHaveBeenCalledWith('local-1')
          expect(mockSendAgentMessage).toHaveBeenCalledWith('w-1', { agentId: 'a-1', content: 'retry me' })
          expect(chatStore.removeMessage).toHaveBeenCalledWith('a-1', 'local-1')
        }
        finally {
          dispose()
        }
      })
    })

    it('restores the delivery error if resend fails', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, chatStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-2', workerId: 'w-1' })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          chatStore.getMessages.mockReturnValue([{
            id: 'local-2',
            source: MessageSource.USER,
            content: new TextEncoder().encode(JSON.stringify({ content: 'retry me' })),
            contentCompression: ContentCompression.NONE,
          }])
          mockSendAgentMessage.mockRejectedValueOnce(new Error('offline'))

          await ops.handleRetryMessage('a-2', 'local-2')

          expect(chatStore.clearMessageError).toHaveBeenCalledWith('local-2')
          expect(chatStore.setMessageError).toHaveBeenCalledWith('local-2', 'Failed to deliver')
          expect(mockShowWarnToast).toHaveBeenCalledWith('Retry failed', expect.any(Error))
        }
        finally {
          dispose()
        }
      })
    })

    it('does not re-stamp a delivery error when the resend succeeds but the cleanup delete fails', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, chatStore, ops } = setup()
          mockSendAgentMessage.mockReset()
          mockDeleteAgentMessage.mockReset()
          mockShowWarnToast.mockReset()
          const agent = create(AgentInfoSchema, { id: 'a-3', workerId: 'w-1' })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          // A SERVER-persisted failed message (non-local id) so the deleteAgentMessage
          // cleanup path runs.
          chatStore.getMessages.mockReturnValue([{
            id: 'srv-3',
            source: MessageSource.USER,
            content: new TextEncoder().encode(JSON.stringify({ content: 'retry me' })),
            contentCompression: ContentCompression.NONE,
          }])
          mockSendAgentMessage.mockResolvedValueOnce({}) // resend SUCCEEDS
          mockDeleteAgentMessage.mockRejectedValueOnce(new Error('not a failed user message')) // cleanup fails

          await ops.handleRetryMessage('a-3', 'srv-3')

          // The resend landed, so the old bubble must NOT be re-marked as failed.
          expect(chatStore.setMessageError).not.toHaveBeenCalled()
          // The cleanup failure is surfaced softly, NOT as a "Retry failed".
          expect(mockShowWarnToast).toHaveBeenCalledWith('Could not remove the old failed message', expect.any(Error))
          expect(mockShowWarnToast).not.toHaveBeenCalledWith('Retry failed', expect.any(Error))
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleAgentClose', () => {
    it('removes agent/tab synchronously BEFORE the close RPC resolves', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, chatStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-1', workerId: 'w-1' })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          tabStore.addTab({ type: TabType.AGENT, id: 'a-1', title: 'Agent Olivia', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          // Never-resolving RPC to prove the UI mutation is synchronous.
          mockCloseAgent.mockReturnValueOnce(new Promise(() => {}))

          ops.handleAgentClose('a-1')

          // Store mutations happened synchronously.
          expect(tabStore.getAgentTab('a-1')).toBeUndefined()
          expect(tabStore.state.tabs.find(t => t.id === 'a-1')).toBeUndefined()
          // Chat-store per-agent state is reclaimed synchronously too (no leak).
          expect(chatStore.forgetAgent).toHaveBeenCalledWith('a-1')
          // RPC was dispatched with KEEP as the default worktree action.
          expect(mockCloseAgent).toHaveBeenCalledWith('w-1', { agentId: 'a-1', worktreeAction: WorktreeAction.KEEP })
        }
        finally {
          dispose()
        }
      })
    })

    it('passes through the worktreeAction argument', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-remove', workerId: 'w-1' })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          tabStore.addTab({ type: TabType.AGENT, id: 'a-remove', title: 'Agent Remove', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({
            result: {
              worktreePath: '',
              worktreeId: '',
              failureMessage: '',
              failureDetail: '',
            },
          } as CloseAgentResponse)

          ops.handleAgentClose('a-remove', WorktreeAction.REMOVE)
          await flush()

          expect(mockCloseAgent).toHaveBeenCalledWith('w-1', { agentId: 'a-remove', worktreeAction: WorktreeAction.REMOVE })
        }
        finally {
          dispose()
        }
      })
    })

    it('surfaces failure_message + failure_detail via toast when the RPC reports a partial failure', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-fail', workerId: 'w-1' })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          tabStore.addTab({ type: TabType.AGENT, id: 'a-fail', title: 'Agent Fail', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({
            result: {
              worktreeId: 'wt-1',
              worktreePath: '/some/wt',
              failureMessage: 'Failed to remove worktree',
              failureDetail: 'git worktree remove /some/wt: exit 128',
            },
          } as CloseAgentResponse)

          ops.handleAgentClose('a-fail', WorktreeAction.REMOVE)
          await flush()

          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to remove worktree: git worktree remove /some/wt: exit 128')
          // Tab was removed synchronously — failure doesn't roll back UI.
          expect(tabStore.state.tabs.find(t => t.id === 'a-fail')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    it('surfaces a generic toast when the RPC rejects', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-reject', workerId: 'w-1' })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          tabStore.addTab({ type: TabType.AGENT, id: 'a-reject', title: 'Agent Reject', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          const err = new Error('network down')
          mockCloseAgent.mockRejectedValueOnce(err)

          ops.handleAgentClose('a-reject')
          await flush()

          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to close agent', err)
          expect(tabStore.state.tabs.find(t => t.id === 'a-reject')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    it('skips RPC and still removes tab when workerId is missing', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-2', workerId: '' })
          tabStore.addTab({ type: TabType.AGENT, id: agent.id, ...protoToAgentTabFields(agent.workerId, agent) })
          tabStore.addTab({ type: TabType.AGENT, id: 'a-2', title: 'Agent Liam', tileId: 'tile-1', workerId: '', workingDir: '' })

          mockCloseAgent.mockClear()

          ops.handleAgentClose('a-2')

          expect(mockCloseAgent).not.toHaveBeenCalled()
          expect(tabStore.getAgentTab('a-2')).toBeUndefined()
          expect(tabStore.state.tabs.find(t => t.id === 'a-2')).toBeUndefined()
        }
        finally {
          dispose()
        }
      })
    })

    it('skips RPC when agent is not found in store', async () => {
      await createRoot(async (dispose) => {
        try {
          const { ops } = setup()

          mockCloseAgent.mockClear()

          ops.handleAgentClose('nonexistent')

          expect(mockCloseAgent).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })
  })
})
