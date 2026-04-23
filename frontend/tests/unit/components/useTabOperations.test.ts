import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTabOperations } from '~/components/shell/useTabOperations'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createAgentStore } from '~/stores/agent.store'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'

const mockInspectLastTabClose = vi.fn()
const mockPushBranchForClose = vi.fn()
const mockShowWarnToast = vi.fn()
const mockShowInfoToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  inspectLastTabClose: (...args: unknown[]) => mockInspectLastTabClose(...args),
  pushBranchForClose: (...args: unknown[]) => mockPushBranchForClose(...args),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: (...args: unknown[]) => mockShowInfoToast(...args),
  showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
}))

vi.mock('~/components/terminal/TerminalView', () => ({
  getTerminalInstance: vi.fn(() => undefined),
}))

function makeUserMessage(id: string, seq: bigint) {
  return {
    id,
    seq,
    role: 1,
    content: new TextEncoder().encode('{"content":"test"}'),
  } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
}

function setup() {
  const tabStore = createTabStore()
  const agentStore = createAgentStore()
  const chatStore = createChatStore()
  const layoutStore = createLayoutStore()

  const tileId = 'tile-1'
  layoutStore.setLayout({ type: 'leaf', id: tileId })
  layoutStore.setFocusedTile(tileId)

  tabStore.addTab({ type: TabType.AGENT, id: 'agent-a', tileId, workerId: 'w-1' })
  tabStore.addTab({ type: TabType.AGENT, id: 'agent-b', tileId, workerId: 'w-1' }, { activate: false })
  tabStore.setActiveTabForTile(tileId, TabType.AGENT, 'agent-a')
  agentStore.setActiveAgent('agent-a')

  // handleCloseAgent / handleTerminalClose are now synchronous (void-returning)
  // fire-and-forget handlers. Use plain vi.fn() with no resolved value.
  const handleCloseAgent = vi.fn()
  const handleTerminalClose = vi.fn()

  const ops = useTabOperations({
    tabStore,
    agentStore,
    chatStore,
    layoutStore,
    agentOps: {
      handleCloseAgent,
    } as never,
    termOps: {
      handleTerminalClose,
    } as never,
    activeTab: () => tabStore.activeTab() ?? undefined,
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp', homeDir: '/home/test' }),
    focusEditor: vi.fn(),
    getScrollState: () => ({ distFromBottom: 9999, atBottom: false }),
    setFileTreePath: vi.fn(),
  })

  return { tabStore, agentStore, chatStore, ops, tileId, handleCloseAgent, handleTerminalClose }
}

describe('useTabOperations', () => {
  afterEach(() => {
    mockInspectLastTabClose.mockReset()
    mockPushBranchForClose.mockReset()
    mockShowWarnToast.mockReset()
    mockShowInfoToast.mockReset()
  })

  it('marks a tab as closing during decide phase, then clears once inspect resolves', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const key = `${TabType.AGENT}:agent-a`

        let resolveInspect!: (value: { shouldPrompt: boolean }) => void
        mockInspectLastTabClose.mockImplementationOnce(() => new Promise((resolve) => {
          resolveInspect = resolve as typeof resolveInspect
        }))

        const closePromise = ops.handleTabClose(tab)
        expect(ops.closingTabKeys().has(key)).toBe(true)
        expect(handleCloseAgent).not.toHaveBeenCalled()

        resolveInspect({ shouldPrompt: false })
        await closePromise

        // Decide phase done: spinner cleared, commit phase already ran.
        expect(ops.closingTabKeys().has(key)).toBe(false)
        expect(handleCloseAgent).toHaveBeenCalledTimes(1)
      }
      finally {
        dispose()
      }
    })
  })

  it('no-prompt path: tab is removed from the store synchronously with KEEP action', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: false })

        await ops.handleTabClose(tab)

        expect(handleCloseAgent).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      }
      finally {
        dispose()
      }
    })
  })

  it('dialog cancel path: tab stays, handler not invoked, spinner cleared', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent, handleTerminalClose } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const key = `${TabType.AGENT}:agent-a`
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        // Simulate the dialog resolving cancel as soon as it's opened.
        const closePromise = ops.handleTabClose(tab)
        // Wait for the handler to reach setLastTabConfirm and open the
        // dialog; the signal holds the resolve fn.
        await Promise.resolve()
        await Promise.resolve()
        const dlg = ops.lastTabConfirm()
        expect(dlg).not.toBeNull()
        dlg!.resolve('cancel')
        await closePromise

        expect(tabStore.state.tabs.some(t => t.id === 'agent-a')).toBe(true)
        expect(handleCloseAgent).not.toHaveBeenCalled()
        expect(handleTerminalClose).not.toHaveBeenCalled()
        expect(ops.closingTabKeys().has(key)).toBe(false)
      }
      finally {
        dispose()
      }
    })
  })

  it('dialog close-anyway path: commit runs with KEEP', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        const closePromise = ops.handleTabClose(tab)
        await Promise.resolve()
        await Promise.resolve()
        ops.lastTabConfirm()!.resolve('close-anyway')
        await closePromise

        expect(handleCloseAgent).toHaveBeenCalledWith('agent-a', WorktreeAction.KEEP)
      }
      finally {
        dispose()
      }
    })
  })

  it('dialog schedule-delete path: commit runs with REMOVE', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        mockInspectLastTabClose.mockResolvedValueOnce({ shouldPrompt: true })

        const closePromise = ops.handleTabClose(tab)
        await Promise.resolve()
        await Promise.resolve()
        ops.lastTabConfirm()!.resolve('schedule-delete')
        await closePromise

        expect(handleCloseAgent).toHaveBeenCalledWith('agent-a', WorktreeAction.REMOVE)
        expect(mockShowInfoToast).toHaveBeenCalledWith('Worktree will be removed')
      }
      finally {
        dispose()
      }
    })
  })

  it('inspect error: toast shown, handler not invoked, spinner cleared', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const agentTab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const err = new Error('boom')
        mockInspectLastTabClose.mockRejectedValueOnce(err)

        await ops.handleTabClose(agentTab)

        expect(ops.closingTabKeys().has(`${TabType.AGENT}:agent-a`)).toBe(false)
        expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to prepare tab close', err)
        expect(handleCloseAgent).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })

  it('rapid double-close dedupes during decide phase', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops, handleCloseAgent } = setup()
        const tab = tabStore.state.tabs.find(t => t.id === 'agent-a')!

        let resolveInspect!: (value: { shouldPrompt: boolean }) => void
        mockInspectLastTabClose.mockImplementationOnce(() => new Promise((resolve) => {
          resolveInspect = resolve as typeof resolveInspect
        }))

        const p1 = ops.handleTabClose(tab)
        const p2 = ops.handleTabClose(tab)

        // Only one inspect should have been dispatched.
        expect(mockInspectLastTabClose).toHaveBeenCalledTimes(1)

        resolveInspect({ shouldPrompt: false })
        await Promise.all([p1, p2])

        expect(handleCloseAgent).toHaveBeenCalledTimes(1)
      }
      finally {
        dispose()
      }
    })
  })

  it('trims the previous agent when switching to another tab in the same tile', () => {
    createRoot((dispose) => {
      const { tabStore, chatStore, ops, tileId } = setup()
      const initial = Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES + 10 }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('agent-a', initial)

      const nextTab = tabStore.state.tabs.find(t => t.id === 'agent-b')!
      ops.handleTabSelect(nextTab)
      tabStore.setActiveTabForTile(tileId, nextTab.type, nextTab.id)

      const trimmed = chatStore.getMessages('agent-a')
      expect(trimmed).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES)
      expect(trimmed[0].seq).toBe(11n)
      expect(trimmed.at(-1)?.seq).toBe(60n)
      expect(chatStore.hasOlderMessages('agent-a')).toBe(true)
      dispose()
    })
  })

  it('does not trim when switching focus to a tab in a different tile', () => {
    createRoot((dispose) => {
      const { tabStore, chatStore, ops } = setup()
      const initial = Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES + 10 }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('agent-a', initial)

      tabStore.addTab({ type: TabType.AGENT, id: 'agent-c', tileId: 'tile-2' }, { activate: false })
      const nextTab = tabStore.state.tabs.find(t => t.id === 'agent-c')!
      ops.handleTabSelect(nextTab)
      tabStore.setActiveTabForTile('tile-2', nextTab.type, nextTab.id)

      const messages = chatStore.getMessages('agent-a')
      expect(messages).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES + 10)
      expect(messages[0].seq).toBe(1n)
      expect(messages.at(-1)?.seq).toBe(60n)
      dispose()
    })
  })
})
