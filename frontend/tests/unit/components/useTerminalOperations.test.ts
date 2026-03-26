import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { useTerminalOperations } from '~/components/shell/useTerminalOperations'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { createTerminalStore } from '~/stores/terminal.store'

vi.mock('~/api/workerRpc', () => ({
  createTerminal: vi.fn(),
  createTerminalWithShell: vi.fn(),
  sendTerminalInput: vi.fn(),
  resizeTerminal: vi.fn(),
  closeTerminal: vi.fn(),
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
  showWarnToast: vi.fn(),
}))

function setup() {
  const tabStore = createTabStore()
  const terminalStore = createTerminalStore()
  const layoutStore = createLayoutStore()

  const ops = useTerminalOperations({
    org: { orgId: () => 'org-1' },
    tabStore,
    terminalStore,
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

describe('useTerminalOperations', () => {
  it('does not notify the active terminal tab on bell', () => {
    createRoot((dispose) => {
      const { tabStore, ops } = setup()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', tileId: 'tile-1' })

      ops.handleTerminalBell('term-1')

      expect(tabStore.state.tabs[0].hasNotification).not.toBe(true)
      dispose()
    })
  })
})
