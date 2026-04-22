import type { CloseTerminalResponse } from '~/generated/leapmux/v1/terminal_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTerminalOperations } from '~/components/shell/useTerminalOperations'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'

const mockCloseTerminal = vi.fn<(workerId: string, req: { terminalId: string, worktreeAction?: WorktreeAction }) => Promise<CloseTerminalResponse>>()
const mockShowWarnToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  createTerminal: vi.fn(),
  createTerminalWithShell: vi.fn(),
  sendTerminalInput: vi.fn(),
  resizeTerminal: vi.fn(),
  closeTerminal: (...args: unknown[]) => mockCloseTerminal(...args as [string, { terminalId: string, worktreeAction?: WorktreeAction }]),
  updateTerminalTitle: vi.fn(),
  listAvailableShells: vi.fn().mockResolvedValue({ shells: [], defaultShell: '' }),
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
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()

  const ops = useTerminalOperations({
    org: { orgId: () => 'org-1' },
    tabStore,
    layoutStore,
    activeWorkspace: () => ({ id: 'ws-1' } as Workspace),
    isActiveWorkspaceMutatable: () => true,
    getCurrentTabContext: () => ({ workerId: 'w-1', workingDir: '/tmp' }),
    setShowNewTerminalDialog: vi.fn(),
    setNewTerminalLoading: vi.fn(),
    setNewShellLoading: vi.fn(),
  })

  return { tabStore, ops }
}

async function flushMicrotasks() {
  await Promise.resolve()
  await Promise.resolve()
}

describe('useTerminalOperations', () => {
  afterEach(() => {
    mockCloseTerminal.mockReset()
    mockShowWarnToast.mockReset()
  })

  it('does not notify the active terminal tab on bell', () => {
    createRoot((dispose) => {
      const { tabStore, ops } = setup()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', tileId: 'tile-1' })

      ops.handleTerminalBell('term-1')

      expect(tabStore.state.tabs[0].hasNotification).not.toBe(true)
      dispose()
    })
  })

  describe('handleTerminalClose', () => {
    it('removes the terminal tab synchronously and fires closeTerminal with KEEP by default', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          tabStore.addTab({ type: TabType.TERMINAL, id: 'term-close', tileId: 'tile-1', workerId: 'w-1' })

          mockCloseTerminal.mockReturnValueOnce(new Promise(() => {}))

          ops.handleTerminalClose('term-close')

          expect(tabStore.state.tabs.find(t => t.id === 'term-close')).toBeUndefined()
          expect(mockCloseTerminal).toHaveBeenCalledWith('w-1', expect.objectContaining({
            terminalId: 'term-close',
            worktreeAction: WorktreeAction.KEEP,
          }))
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
          tabStore.addTab({ type: TabType.TERMINAL, id: 'term-remove', tileId: 'tile-1', workerId: 'w-1' })

          mockCloseTerminal.mockResolvedValueOnce({
            result: {
              worktreeId: '',
              failureMessage: '',
            },
          } as CloseTerminalResponse)

          ops.handleTerminalClose('term-remove', WorktreeAction.REMOVE)
          await flushMicrotasks()

          expect(mockCloseTerminal).toHaveBeenCalledWith('w-1', expect.objectContaining({
            terminalId: 'term-remove',
            worktreeAction: WorktreeAction.REMOVE,
          }))
        }
        finally {
          dispose()
        }
      })
    })

    it('toasts a failure_message on partial failure', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          tabStore.addTab({ type: TabType.TERMINAL, id: 'term-fail', tileId: 'tile-1', workerId: 'w-1' })

          mockCloseTerminal.mockResolvedValueOnce({
            result: {
              worktreeId: 'wt-1',
              worktreePath: '/some/wt',
              failureMessage: 'Failed to remove worktree',
              failureDetail: 'git worktree remove exit 128',
            },
          } as CloseTerminalResponse)

          ops.handleTerminalClose('term-fail', WorktreeAction.REMOVE)
          await flushMicrotasks()

          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to remove worktree: git worktree remove exit 128')
        }
        finally {
          dispose()
        }
      })
    })

    it('toasts a generic failure on RPC reject', async () => {
      await createRoot(async (dispose) => {
        try {
          const { tabStore, ops } = setup()
          tabStore.addTab({ type: TabType.TERMINAL, id: 'term-reject', tileId: 'tile-1', workerId: 'w-1' })

          const err = new Error('offline')
          mockCloseTerminal.mockRejectedValueOnce(err)

          ops.handleTerminalClose('term-reject')
          await flushMicrotasks()

          expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to close terminal', err)
        }
        finally {
          dispose()
        }
      })
    })
  })
})
