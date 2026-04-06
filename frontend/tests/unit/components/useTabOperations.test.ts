import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTabOperations } from '~/components/shell/useTabOperations'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createAgentStore } from '~/stores/agent.store'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { createTerminalStore } from '~/stores/terminal.store'

const mockInspectLastTabClose = vi.fn()
const mockScheduleWorktreeDeletion = vi.fn()
const mockShowWarnToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  inspectLastTabClose: (...args: unknown[]) => mockInspectLastTabClose(...args),
  scheduleWorktreeDeletion: (...args: unknown[]) => mockScheduleWorktreeDeletion(...args),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
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
  const terminalStore = createTerminalStore()
  const chatStore = createChatStore()
  const layoutStore = createLayoutStore()

  const tileId = 'tile-1'
  layoutStore.setLayout({ type: 'leaf', id: tileId })
  layoutStore.setFocusedTile(tileId)

  tabStore.addTab({ type: TabType.AGENT, id: 'agent-a', tileId })
  tabStore.addTab({ type: TabType.AGENT, id: 'agent-b', tileId }, { activate: false })
  tabStore.setActiveTabForTile(tileId, TabType.AGENT, 'agent-a')
  agentStore.setActiveAgent('agent-a')

  const handleCloseAgent = vi.fn()
  const handleTerminalClose = vi.fn()

  const ops = useTabOperations({
    tabStore,
    agentStore,
    terminalStore,
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
    mockScheduleWorktreeDeletion.mockReset()
    mockShowWarnToast.mockReset()
  })

  it.each([
    { tabType: TabType.AGENT, tabId: 'agent-a', label: 'agent', handlerKey: 'handleCloseAgent' as const },
    { tabType: TabType.TERMINAL, tabId: 'term-a', label: 'terminal', handlerKey: 'handleTerminalClose' as const },
  ])('marks a $label tab as closing while persisted close is in flight', async ({ tabType, tabId, handlerKey }) => {
    await createRoot(async (dispose) => {
      try {
        const result = setup()
        const { tabStore, ops } = result
        if (tabType === TabType.TERMINAL) {
          tabStore.addTab({ type: TabType.TERMINAL, id: tabId, tileId: 'tile-1', workerId: 'w-1' }, { activate: false })
        }
        const tab = tabStore.state.tabs.find(t => t.id === tabId)!
        const key = `${tabType}:${tabId}`
        let resolveInspect!: (value: { shouldPrompt: boolean }) => void
        let resolveClose!: () => void
        mockInspectLastTabClose.mockImplementationOnce(() => new Promise((resolve) => {
          resolveInspect = resolve as typeof resolveInspect
        }))
        result[handlerKey].mockImplementationOnce(() => new Promise<void>((resolve) => {
          resolveClose = resolve
        }))

        const closePromise = ops.handleTabClose(tab)
        expect(ops.closingTabKeys().has(key)).toBe(true)

        resolveInspect({ shouldPrompt: false })
        await Promise.resolve()
        expect(ops.closingTabKeys().has(key)).toBe(true)

        resolveClose()
        await closePromise
        expect(ops.closingTabKeys().has(key)).toBe(false)
      }
      finally {
        dispose()
      }
    })
  })

  it('clears the closing state if preparing the persisted close fails', async () => {
    await createRoot(async (dispose) => {
      try {
        const { tabStore, ops } = setup()
        const agentTab = tabStore.state.tabs.find(t => t.id === 'agent-a')!
        const err = new Error('boom')
        mockInspectLastTabClose.mockRejectedValueOnce(err)

        await ops.handleTabClose(agentTab)

        expect(ops.closingTabKeys().has(`${TabType.AGENT}:agent-a`)).toBe(false)
        expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to prepare tab close', err)
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
