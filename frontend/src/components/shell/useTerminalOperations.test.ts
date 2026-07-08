import type { CloseTerminalResponse } from '~/generated/leapmux/v1/terminal_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { useTerminalOperations } from '~/components/shell/useTerminalOperations'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { flush } from '~/test-support/async'

vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
}))

vi.mock('~/components/terminal/TerminalView', () => ({
  disposeTerminalInstance: vi.fn(),
}))

vi.mock('~/api/clients', () => ({
  workspaceClient: {
    addTab: vi.fn().mockResolvedValue({}),
    removeTab: vi.fn().mockResolvedValue({}),
  },
}))

// `closeResultToast` is intentionally unmocked so the close tests
// exercise its real implementation, which formats the worktree-failure
// message and forwards to the mocked showWarnToast.
vi.mock('~/api/workerRpc', () => ({
  sendInput: vi.fn(async () => ({})),
  restartTerminal: vi.fn(async () => ({})),
  listAvailableShells: vi.fn(async () => ({ shells: [], defaultShell: '' })),
  openTerminal: vi.fn(async () => ({ terminalId: 'new-tid', title: '' })),
  closeTerminal: vi.fn(async () => ({ result: { worktreeId: '', failureMessage: '' } })),
  resizeTerminal: vi.fn(async () => ({})),
  updateTerminalTitle: vi.fn(async () => ({})),
}))

const sendInputMock = workerRpc.sendInput as unknown as ReturnType<typeof vi.fn>
const restartTerminalMock = workerRpc.restartTerminal as unknown as ReturnType<typeof vi.fn>
const openTerminalMock = workerRpc.openTerminal as unknown as ReturnType<typeof vi.fn>
const closeTerminalMock = workerRpc.closeTerminal as unknown as ReturnType<typeof vi.fn>
const listAvailableShellsMock = workerRpc.listAvailableShells as unknown as ReturnType<typeof vi.fn>
const showWarnToastMock = showWarnToast as unknown as ReturnType<typeof vi.fn>

interface TabOverrides {
  id?: string
  cols?: number
  rows?: number
}

const disposers: Array<() => void> = []

beforeEach(() => {
  sendInputMock.mockClear()
  restartTerminalMock.mockClear()
  openTerminalMock.mockClear()
  closeTerminalMock.mockClear()
  listAvailableShellsMock.mockClear()
  showWarnToastMock.mockClear()
  // Reset to default success — individual tests override per scenario.
  restartTerminalMock.mockImplementation(async () => ({}))
  sendInputMock.mockImplementation(async () => ({}))
  openTerminalMock.mockImplementation(async () => ({ terminalId: 'new-tid', title: '' }))
  closeTerminalMock.mockImplementation(async () => ({ result: { worktreeId: '', failureMessage: '' } }))
  listAvailableShellsMock.mockImplementation(async () => ({ shells: [], defaultShell: '' }))
})

afterEach(() => {
  while (disposers.length > 0) {
    disposers.pop()?.()
  }
})

/**
 * Build a useTerminalOperations instance. When `status` is provided a
 * single terminal tab is registered with the requested status so the
 * input/restart tests can call handlers directly; pass `undefined` for
 * tests that prefer to register tabs themselves (bell / close tests).
 *
 * `tabOverrides` keys override the default tab fields; pass `cols:
 * undefined` (or `rows: undefined`) to exercise the "tab is missing
 * dims" path.
 */
function setup(status: TerminalStatus | undefined = undefined, tabOverrides: TabOverrides = {}) {
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()
  const [activeWorkspace] = createSignal<Workspace | null>({ id: 'ws-1' } as Workspace)
  if (status !== undefined) {
    tabStore.addTab({
      type: TabType.TERMINAL,
      id: 'tid-1',
      title: 'Terminal',
      workerId: 'worker-1',
      workingDir: '/tmp',
      cols: 100,
      rows: 30,
      status,
      ...tabOverrides,
    })
  }

  let ops!: ReturnType<typeof useTerminalOperations>
  const dispose = createRoot((d) => {
    ops = useTerminalOperations({
      org: { orgId: () => 'org-1' },
      tabStore,
      layoutStore,
      activeWorkspace,
      isActiveWorkspaceMutatable: () => true,
      getCurrentTabContext: () => ({ workerId: 'worker-1', workingDir: '/tmp' }),
      newTerminalDialog: { open: () => {}, close: () => {}, isOpen: () => false },
      setNewTerminalLoading: () => {},
      setNewShellLoading: () => {},
    })
    return d
  })
  disposers.push(dispose)
  return { ops, tabStore }
}

interface OpenSetupOpts {
  ctx?: { workerId: string, workingDir: string }
  isMutatable?: boolean
  workspace?: Workspace | null
  setNewTerminalLoading?: (v: boolean) => void
  setNewShellLoading?: (v: boolean) => void
  dialogOpen?: () => void
}

// Open-terminal-specific setup: lets each test inject a ctx (to
// exercise the "no worker / no workingDir" guard), a dialog open spy,
// and loading-flag spies. Returns `ops` + `tabStore` so assertions
// can verify the tab seed.
function setupForOpen(opts: OpenSetupOpts = {}) {
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()
  const [activeWorkspace] = createSignal<Workspace | null>(
    opts.workspace === undefined ? ({ id: 'ws-1' } as Workspace) : opts.workspace,
  )
  let ops!: ReturnType<typeof useTerminalOperations>
  const dispose = createRoot((d) => {
    ops = useTerminalOperations({
      org: { orgId: () => 'org-1' },
      tabStore,
      layoutStore,
      activeWorkspace,
      isActiveWorkspaceMutatable: () => opts.isMutatable ?? true,
      getCurrentTabContext: () => opts.ctx ?? { workerId: 'worker-1', workingDir: '/tmp' },
      newTerminalDialog: { open: opts.dialogOpen ?? (() => {}), close: () => {}, isOpen: () => false },
      setNewTerminalLoading: opts.setNewTerminalLoading ?? (() => {}),
      setNewShellLoading: opts.setNewShellLoading ?? (() => {}),
    })
    return d
  })
  disposers.push(dispose)
  return { ops, tabStore }
}

describe('useterminaloperations.handleopenterminal', () => {
  it('happy path: opens a terminal, adds the tab, and flips loading false in the finally', async () => {
    const loadingFlips: boolean[] = []
    const { ops, tabStore } = setupForOpen({
      setNewTerminalLoading: v => loadingFlips.push(v),
    })

    await ops.handleOpenTerminal()

    expect(openTerminalMock).toHaveBeenCalledTimes(1)
    // Shell is empty (default-shell quick action). shellStartDir is
    // forwarded as empty string when the caller didn't pass one.
    expect(openTerminalMock.mock.calls[0][1]).toMatchObject({
      workspaceId: 'ws-1',
      workerId: 'worker-1',
      workingDir: '/tmp',
      shell: '',
      shellStartDir: '',
    })
    // Tab was added with the response's terminalId and seeded
    // shellStartDir falling back to workingDir.
    const newTab = tabStore.getTerminalTab('new-tid')
    expect(newTab).toBeDefined()
    expect(newTab?.workerId).toBe('worker-1')
    expect(newTab?.workingDir).toBe('/tmp')
    expect(newTab?.shellStartDir).toBe('/tmp')
    // Loading toggled true then false (finally).
    expect(loadingFlips).toEqual([true, false])
  })

  it('forwards an explicit shellStartDir to both the RPC and the tab seed', async () => {
    const { ops, tabStore } = setupForOpen()
    await ops.handleOpenTerminal('/work/dir')
    expect(openTerminalMock.mock.calls[0][1].shellStartDir).toBe('/work/dir')
    const newTab = tabStore.getTerminalTab('new-tid')
    expect(newTab?.shellStartDir).toBe('/work/dir')
  })

  it('opens the new-terminal dialog when ctx is missing (no workerId)', async () => {
    const dialogOpen = vi.fn()
    const { ops, tabStore } = setupForOpen({
      ctx: { workerId: '', workingDir: '/tmp' },
      dialogOpen,
    })
    await ops.handleOpenTerminal()
    expect(dialogOpen).toHaveBeenCalledTimes(1)
    expect(openTerminalMock).not.toHaveBeenCalled()
    expect(tabStore.state.tabs).toHaveLength(0)
  })

  it('short-circuits silently when the workspace is not mutatable', async () => {
    const dialogOpen = vi.fn()
    const setLoading = vi.fn()
    const { ops, tabStore } = setupForOpen({
      isMutatable: false,
      dialogOpen,
      setNewTerminalLoading: setLoading,
    })
    await ops.handleOpenTerminal()
    expect(openTerminalMock).not.toHaveBeenCalled()
    expect(dialogOpen).not.toHaveBeenCalled()
    expect(setLoading).not.toHaveBeenCalled()
    expect(tabStore.state.tabs).toHaveLength(0)
  })

  it('toasts on RPC failure and still clears the loading flag', async () => {
    openTerminalMock.mockRejectedValueOnce(new Error('boom'))
    const loadingFlips: boolean[] = []
    const { ops } = setupForOpen({
      setNewTerminalLoading: v => loadingFlips.push(v),
    })
    await ops.handleOpenTerminal()
    expect(showWarnToastMock).toHaveBeenCalledTimes(1)
    expect(showWarnToastMock.mock.calls[0][0]).toMatch(/open terminal/i)
    // Finally must run so the spinner doesn't get stuck.
    expect(loadingFlips).toEqual([true, false])
  })
})

describe('useterminaloperations.handleopenterminalwithshell', () => {
  it('forwards the picked shell to the RPC and uses the shell-loading setter', async () => {
    const shellLoadingFlips: boolean[] = []
    const terminalLoadingFlips: boolean[] = []
    const { ops, tabStore } = setupForOpen({
      setNewShellLoading: v => shellLoadingFlips.push(v),
      setNewTerminalLoading: v => terminalLoadingFlips.push(v),
    })

    await ops.handleOpenTerminalWithShell('/bin/zsh')

    expect(openTerminalMock).toHaveBeenCalledTimes(1)
    expect(openTerminalMock.mock.calls[0][1].shell).toBe('/bin/zsh')
    // The shell-picker path does NOT seed shellStartDir onto the tab,
    // so a later restart re-uses the working directory the worker had
    // at launch rather than a stale per-shell override.
    const newTab = tabStore.getTerminalTab('new-tid')
    expect(newTab?.shellStartDir).toBeUndefined()
    // Only the shell-loading setter fires for the dropdown path.
    expect(shellLoadingFlips).toEqual([true, false])
    expect(terminalLoadingFlips).toEqual([])
  })
})

describe('useterminaloperations.handleterminalinput', () => {
  it('routes input to sendInput when status is READY', async () => {
    const { ops } = setup(TerminalStatus.READY)
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x61])) // 'a'
    expect(sendInputMock).toHaveBeenCalledTimes(1)
    expect(restartTerminalMock).not.toHaveBeenCalled()
    const arg = sendInputMock.mock.calls[0][1]
    expect(arg.terminalId).toBe('tid-1')
    expect(arg.workspaceId).toBe('ws-1')
  })

  it('calls restartTerminal when Enter (CR) is pressed on an EXITED terminal', async () => {
    const { ops } = setup(TerminalStatus.EXITED)
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x0D]))
    expect(restartTerminalMock).toHaveBeenCalledTimes(1)
    expect(sendInputMock).not.toHaveBeenCalled()
    const arg = restartTerminalMock.mock.calls[0][1]
    expect(arg).toMatchObject({
      orgId: 'org-1',
      workspaceId: 'ws-1',
      terminalId: 'tid-1',
      cols: 100,
      rows: 30,
    })
  })

  it('ignores non-Enter input on an EXITED terminal', async () => {
    const { ops } = setup(TerminalStatus.EXITED)
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x61])) // 'a'
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x0A])) // LF (not CR)
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x0D, 0x0A])) // multi-byte
    expect(restartTerminalMock).not.toHaveBeenCalled()
    expect(sendInputMock).not.toHaveBeenCalled()
  })

  it('drops input on STARTING/DISCONNECTED/STARTUP_FAILED', async () => {
    for (const status of [TerminalStatus.STARTING, TerminalStatus.DISCONNECTED, TerminalStatus.STARTUP_FAILED]) {
      sendInputMock.mockClear()
      restartTerminalMock.mockClear()
      const { ops } = setup(status)
      await ops.handleTerminalInput('tid-1', new Uint8Array([0x61]))
      await ops.handleTerminalInput('tid-1', new Uint8Array([0x0D]))
      expect(sendInputMock, `status=${status}`).not.toHaveBeenCalled()
      expect(restartTerminalMock, `status=${status}`).not.toHaveBeenCalled()
    }
  })

  it('shows a toast and does not throw when restartTerminal fails', async () => {
    restartTerminalMock.mockImplementation(async () => {
      throw new Error('worker offline')
    })
    const { ops } = setup(TerminalStatus.EXITED)
    // Must not propagate — the keystroke handler is called from xterm's
    // onData callback, which has no error sink.
    await expect(ops.handleTerminalInput('tid-1', new Uint8Array([0x0D]))).resolves.toBeUndefined()
    expect(restartTerminalMock).toHaveBeenCalledTimes(1)
    expect(showWarnToastMock).toHaveBeenCalledTimes(1)
    expect(showWarnToastMock.mock.calls[0][0]).toMatch(/restart/i)
  })

  it('does not call any RPC when the tab is missing', async () => {
    // Setup with a status — the tab is registered. Then call with an
    // unknown id so getTerminalTab returns undefined.
    const { ops } = setup(TerminalStatus.EXITED)
    await ops.handleTerminalInput('unknown-tid', new Uint8Array([0x0D]))
    expect(sendInputMock).not.toHaveBeenCalled()
    expect(restartTerminalMock).not.toHaveBeenCalled()
  })

  it('drops overlapping Enter presses while a restart is in flight', async () => {
    // Keep the first restart unresolved so the in-flight guard stays
    // armed. A held Enter (autorepeat) would otherwise fire one RPC
    // per keystroke and toast-spam the user with FailedPrecondition
    // rejects from the backend.
    let releaseRestart: (() => void) | undefined
    restartTerminalMock.mockImplementationOnce(() => new Promise((resolve) => {
      releaseRestart = () => resolve({})
    }))
    const { ops } = setup(TerminalStatus.EXITED)
    const firstPress = ops.handleTerminalInput('tid-1', new Uint8Array([0x0D]))
    // Second/third presses must be no-ops while the first call is pending.
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x0D]))
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x0D]))
    expect(restartTerminalMock).toHaveBeenCalledTimes(1)
    expect(showWarnToastMock).not.toHaveBeenCalled()
    releaseRestart?.()
    await firstPress
  })

  it('falls back to default cols/rows when the tab is missing dims', async () => {
    // Build a tab without explicit cols/rows; handler should fall back
    // to the documented 80x25 default rather than sending undefined.
    const { ops } = setup(TerminalStatus.EXITED, { cols: undefined, rows: undefined })
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x0D]))
    expect(restartTerminalMock).toHaveBeenCalledTimes(1)
    const arg = restartTerminalMock.mock.calls[0][1]
    expect(arg.cols).toBe(80)
    expect(arg.rows).toBe(25)
  })
})

describe('useterminaloperations.availableshells', () => {
  it('loads shells from listAvailableShells on mount when workspace + worker are present', async () => {
    listAvailableShellsMock.mockResolvedValueOnce({
      shells: ['/bin/zsh', '/bin/bash'],
      defaultShell: '/bin/zsh',
    })
    const { ops } = setup()
    await flush()
    await flush()
    expect(listAvailableShellsMock).toHaveBeenCalledTimes(1)
    expect(listAvailableShellsMock).toHaveBeenCalledWith(
      'worker-1',
      expect.objectContaining({ workspaceId: 'ws-1', workerId: 'worker-1' }),
    )
    expect(ops.availableShells()).toEqual(['/bin/zsh', '/bin/bash'])
    expect(ops.defaultShell()).toBe('/bin/zsh')
  })

  it('clears shells on RPC failure', async () => {
    listAvailableShellsMock.mockRejectedValueOnce(new Error('worker offline'))
    const { ops } = setup()
    await flush()
    await flush()
    expect(ops.availableShells()).toEqual([])
    expect(ops.defaultShell()).toBe('')
  })
})

describe('useterminaloperations.handleterminalbell', () => {
  it('does not notify the active terminal tab on bell', () => {
    const { tabStore, ops } = setup()
    tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', tileId: 'tile-1' })

    ops.handleTerminalBell('term-1')

    expect(tabStore.state.tabs[0].hasNotification).not.toBe(true)
  })
})

describe('useterminaloperations.handleterminalclose', () => {
  it('removes the terminal tab synchronously and fires closeTerminal with KEEP by default', () => {
    const { tabStore, ops } = setup()
    tabStore.addTab({ type: TabType.TERMINAL, id: 'term-close', tileId: 'tile-1', workerId: 'w-1' })

    // Never resolves so the test stays in the synchronous-effects window.
    closeTerminalMock.mockReturnValueOnce(new Promise(() => {}))

    ops.handleTerminalClose('term-close')

    expect(tabStore.state.tabs.find(t => t.id === 'term-close')).toBeUndefined()
    expect(closeTerminalMock).toHaveBeenCalledWith('w-1', expect.objectContaining({
      terminalId: 'term-close',
      worktreeAction: WorktreeAction.KEEP,
    }))
  })

  it('passes through the worktreeAction argument', async () => {
    const { tabStore, ops } = setup()
    tabStore.addTab({ type: TabType.TERMINAL, id: 'term-remove', tileId: 'tile-1', workerId: 'w-1' })

    closeTerminalMock.mockResolvedValueOnce({
      result: {
        worktreeId: '',
        failureMessage: '',
      },
    } as CloseTerminalResponse)

    ops.handleTerminalClose('term-remove', WorktreeAction.REMOVE)
    await flush()

    expect(closeTerminalMock).toHaveBeenCalledWith('w-1', expect.objectContaining({
      terminalId: 'term-remove',
      worktreeAction: WorktreeAction.REMOVE,
    }))
  })

  it('toasts a failure_message on partial failure', async () => {
    const { tabStore, ops } = setup()
    tabStore.addTab({ type: TabType.TERMINAL, id: 'term-fail', tileId: 'tile-1', workerId: 'w-1' })

    closeTerminalMock.mockResolvedValueOnce({
      result: {
        worktreeId: 'wt-1',
        worktreePath: '/some/wt',
        failureMessage: 'Failed to remove worktree',
        failureDetail: 'git worktree remove exit 128',
      },
    } as CloseTerminalResponse)

    ops.handleTerminalClose('term-fail', WorktreeAction.REMOVE)
    await flush()

    expect(showWarnToastMock).toHaveBeenCalledWith('Failed to remove worktree: git worktree remove exit 128')
  })

  it('toasts a generic failure on RPC reject', async () => {
    const { tabStore, ops } = setup()
    tabStore.addTab({ type: TabType.TERMINAL, id: 'term-reject', tileId: 'tile-1', workerId: 'w-1' })

    const err = new Error('offline')
    closeTerminalMock.mockRejectedValueOnce(err)

    ops.handleTerminalClose('term-reject')
    await flush()

    expect(showWarnToastMock).toHaveBeenCalledWith('Failed to close terminal', err)
  })
})
