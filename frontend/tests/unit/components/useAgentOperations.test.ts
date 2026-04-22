import type { CloseAgentResponse } from '~/generated/leapmux/v1/agent_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useAgentOperations } from '~/components/shell/useAgentOperations'
import { AgentInfoSchema, AgentProvider, ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createAgentStore } from '~/stores/agent.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createControlStore } from '~/stores/control.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'

const mockCloseAgent = vi.fn<(workerId: string, req: { agentId: string, worktreeAction?: WorktreeAction }) => Promise<CloseAgentResponse>>()
const mockOpenAgent = vi.fn()
const mockSendAgentRawMessage = vi.fn()
const mockSendAgentMessage = vi.fn()
const mockUpdateAgentSettings = vi.fn()
const mockListAvailableProviders = vi.fn().mockResolvedValue({ providers: [] })
const mockShowWarnToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  closeAgent: (...args: unknown[]) => mockCloseAgent(...args as [string, { agentId: string, worktreeAction?: WorktreeAction }]),
  openAgent: (...args: unknown[]) => mockOpenAgent(...args),
  sendAgentMessage: (...args: unknown[]) => mockSendAgentMessage(...args),
  sendAgentRawMessage: (...args: unknown[]) => mockSendAgentRawMessage(...args),
  sendControlResponse: vi.fn(),
  updateAgentSettings: (...args: unknown[]) => mockUpdateAgentSettings(...args),
  retryAgentMessage: vi.fn(),
  deleteAgentMessage: vi.fn(),
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
  const agentStore = createAgentStore()
  const agentSessionStore = createAgentSessionStore()
  const controlStore = createControlStore()
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()

  const chatStore = {
    getMessages: vi.fn().mockReturnValue([]),
    clearMessageError: vi.fn(),
    setMessageError: vi.fn(),
    removeMessage: vi.fn(),
  } as any

  const ops = useAgentOperations({
    agentStore,
    agentSessionStore,
    chatStore,
    controlStore,
    tabStore,
    layoutStore,
    settingsLoading: { start: vi.fn(), stop: vi.fn() },
    isActiveWorkspaceMutatable: () => true,
    activeWorkspace: () => ({ id: 'ws-1' } as Workspace),
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp' }),
    setShowNewAgentDialog: vi.fn(),
    setNewAgentLoadingProvider: vi.fn(),
  })

  return { agentStore, agentSessionStore, controlStore, tabStore, layoutStore, chatStore, ops }
}

async function flushMicrotasks() {
  await Promise.resolve()
  await Promise.resolve()
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
          localStorage.setItem('leapmux:mru-agent-providers', JSON.stringify([AgentProvider.CLAUDE_CODE]))

          const { tabStore, ops } = setup()
          tabStore.addTab({
            type: TabType.AGENT,
            id: 'active-agent',
            tileId: 'tile-1',
            workerId: 'w-1',
            workingDir: '/tmp',
            agentProvider: AgentProvider.CODEX,
          })

          await flushMicrotasks()
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
          localStorage.setItem('leapmux:mru-agent-providers', JSON.stringify([AgentProvider.CODEX, AgentProvider.CLAUDE_CODE]))

          const { tabStore, ops } = setup()
          tabStore.addTab({
            type: TabType.TERMINAL,
            id: 'terminal-1',
            tileId: 'tile-1',
            workerId: 'w-1',
            workingDir: '/tmp',
          })

          await flushMicrotasks()
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
          const setShowNewAgentDialog = vi.fn()
          const agentStore = createAgentStore()
          const agentSessionStore = createAgentSessionStore()
          const controlStore = createControlStore()
          const tabStore = createTabStore()
          const layoutStore = createLayoutStore()
          const chatStore = {
            getMessages: vi.fn().mockReturnValue([]),
            clearMessageError: vi.fn(),
            setMessageError: vi.fn(),
            removeMessage: vi.fn(),
          } as any

          const ops = useAgentOperations({
            agentStore,
            agentSessionStore,
            chatStore,
            controlStore,
            tabStore,
            layoutStore,
            settingsLoading: { start: vi.fn(), stop: vi.fn() },
            isActiveWorkspaceMutatable: () => true,
            activeWorkspace: () => ({ id: 'ws-1' } as Workspace),
            getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '' }),
            setShowNewAgentDialog,
            setNewAgentLoadingProvider: vi.fn(),
          })

          await ops.handleOpenAgent()

          expect(setShowNewAgentDialog).toHaveBeenCalledWith(true)
          expect(mockOpenAgent).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })
  })

  describe('handleInterrupt', () => {
    it('sends provider interrupt payload via raw agent input', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, agentSessionStore, ops } = setup()
          const agent = create(AgentInfoSchema, {
            id: 'codex-1',
            workerId: 'w-1',
            agentProvider: AgentProvider.CODEX,
            agentSessionId: 'thread-1',
          })
          agentStore.addAgent(agent)
          agentSessionStore.updateInfo('codex-1', { codexTurnId: 'turn-1' })

          await ops.handleInterrupt('codex-1')

          expect(mockSendAgentRawMessage).toHaveBeenCalledWith('w-1', {
            agentId: 'codex-1',
            content: '{"jsonrpc":"2.0","id":1001,"method":"turn/interrupt","params":{"threadId":"thread-1","turnId":"turn-1"}}',
          })
        }
        finally {
          dispose()
        }
      })
    })
  })

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

  describe('handleRetryMessage', () => {
    it('clears the delivery error before resending the message', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, chatStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-1', workerId: 'w-1' })
          agentStore.addAgent(agent)
          chatStore.getMessages.mockReturnValue([{
            id: 'local-1',
            role: MessageRole.USER,
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
          const { agentStore, chatStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-2', workerId: 'w-1' })
          agentStore.addAgent(agent)
          chatStore.getMessages.mockReturnValue([{
            id: 'local-2',
            role: MessageRole.USER,
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
  })

  describe('handleCloseAgent', () => {
    it('removes agent/tab synchronously BEFORE the close RPC resolves', async () => {
      await createRoot(async (dispose) => {
        try {
          const { agentStore, tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-1', workerId: 'w-1' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-1', title: 'Agent 1', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          // Never-resolving RPC to prove the UI mutation is synchronous.
          mockCloseAgent.mockReturnValueOnce(new Promise(() => {}))

          ops.handleCloseAgent('a-1')

          // Store mutations happened synchronously.
          expect(agentStore.state.agents.find(a => a.id === 'a-1')).toBeUndefined()
          expect(tabStore.state.tabs.find(t => t.id === 'a-1')).toBeUndefined()
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
          const { agentStore, tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-remove', workerId: 'w-1' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-remove', title: 'Agent Remove', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({
            result: {
              worktreePath: '',
              worktreeId: '',
              failureMessage: '',
              failureDetail: '',
            },
          } as CloseAgentResponse)

          ops.handleCloseAgent('a-remove', WorktreeAction.REMOVE)
          await flushMicrotasks()

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
          const { agentStore, tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-fail', workerId: 'w-1' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-fail', title: 'Agent Fail', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          mockCloseAgent.mockResolvedValueOnce({
            result: {
              worktreeId: 'wt-1',
              worktreePath: '/some/wt',
              failureMessage: 'Failed to remove worktree',
              failureDetail: 'git worktree remove /some/wt: exit 128',
            },
          } as CloseAgentResponse)

          ops.handleCloseAgent('a-fail', WorktreeAction.REMOVE)
          await flushMicrotasks()

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
          const { agentStore, tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-reject', workerId: 'w-1' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-reject', title: 'Agent Reject', tileId: 'tile-1', workerId: 'w-1', workingDir: '/tmp' })

          const err = new Error('network down')
          mockCloseAgent.mockRejectedValueOnce(err)

          ops.handleCloseAgent('a-reject')
          await flushMicrotasks()

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
          const { agentStore, tabStore, ops } = setup()
          const agent = create(AgentInfoSchema, { id: 'a-2', workerId: '' })
          agentStore.addAgent(agent)
          tabStore.addTab({ type: TabType.AGENT, id: 'a-2', title: 'Agent 2', tileId: 'tile-1', workerId: '', workingDir: '' })

          mockCloseAgent.mockClear()

          ops.handleCloseAgent('a-2')

          expect(mockCloseAgent).not.toHaveBeenCalled()
          expect(agentStore.state.agents.find(a => a.id === 'a-2')).toBeUndefined()
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

          ops.handleCloseAgent('nonexistent')

          expect(mockCloseAgent).not.toHaveBeenCalled()
        }
        finally {
          dispose()
        }
      })
    })
  })
})
