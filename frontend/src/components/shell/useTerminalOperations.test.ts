import type { CloseTerminalResponse } from '~/generated/leapmux/v1/terminal_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { useTerminalOperations } from '~/components/shell/useTerminalOperations'
import { disposeTerminalInstance } from '~/components/terminal/TerminalView'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { handleTerminalBell } from '~/hooks/terminalEvents'
import { emitAddTab } from '~/stores/tabOps'
import { flush } from '~/test-support/async'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

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
let nextPosition = 0

/**
 * Place a terminal tab: its placement through the op path, everything else
 * into metadata. The split is the point — the hook reads a joined `Tab` and
 * writes worker-sourced fields back to `tabMetadata`, never to the projection.
 */
function addTerminal(
  stores: ReturnType<typeof createTestTabStores>,
  tileId: string,
  fields: { id: string } & TabOverrides & Record<string, unknown>,
) {
  const { id, ...meta } = fields
  nextPosition += 1
  emitAddTab({
    type: TabType.TERMINAL,
    id,
    tileId,
    position: `p${nextPosition}`,
    workerId: (meta.workerId as string | undefined) ?? '',
  })
  stores.metadata.patch(id, meta)
}

function setup(status: TerminalStatus | undefined = undefined, tabOverrides: TabOverrides = {}) {
  const harness = installTestBridge({ workspaceId: 'ws-1' })
  let stores!: ReturnType<typeof createTestTabStores>
  const [activeWorkspace] = createSignal<Workspace | null>({ id: 'ws-1' } as Workspace)

  let ops!: ReturnType<typeof useTerminalOperations>
  const dispose = createRoot((d) => {
    stores = createTestTabStores('ws-1')
    if (status !== undefined) {
      addTerminal(stores, harness.rootTileId, {
        id: 'tid-1',
        title: 'Terminal',
        workerId: 'worker-1',
        workingDir: '/tmp',
        cols: 100,
        rows: 30,
        terminalStatus: status,
        ...tabOverrides,
      })
    }
    ops = useTerminalOperations({
      view: stores.view,
      metadata: stores.metadata,
      selection: stores.selection,
      layoutStore: stores.layoutStore,
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
  return {
    ops,
    ...stores,
    /** Place a terminal on the seeded root tile — the only tile that exists. */
    add: (id: string, fields: TabOverrides & Record<string, unknown> = {}) =>
      addTerminal(stores, harness.rootTileId, { id, ...fields }),
  }
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
// and loading-flag spies. Returns `ops` + the store bundle so assertions
// can verify the tab seed.
function setupForOpen(opts: OpenSetupOpts = {}) {
  const harness = installTestBridge({ workspaceId: 'ws-1' })
  let stores!: ReturnType<typeof createTestTabStores>
  const [activeWorkspace] = createSignal<Workspace | null>(
    opts.workspace === undefined ? ({ id: 'ws-1' } as Workspace) : opts.workspace,
  )
  let ops!: ReturnType<typeof useTerminalOperations>
  const dispose = createRoot((d) => {
    stores = createTestTabStores('ws-1')
    ops = useTerminalOperations({
      view: stores.view,
      metadata: stores.metadata,
      selection: stores.selection,
      layoutStore: stores.layoutStore,
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
  return { ops, harness, ...stores }
}

/**
 * An active terminal tab that has already resolved its git identity, sitting in
 * the same directory `getCurrentTabContext` reports (`/tmp`). The optimistic
 * seed reads BOTH the active tab's git fields and its effective git dir, so a
 * test without a real active tab would exercise nothing: `resolveOptimisticGitInfo`
 * returns `{}` for a null tab regardless of the guard.
 */
function seedActiveRepoTab(s: ReturnType<typeof setupForOpen>) {
  addTerminal(s, s.harness.rootTileId, {
    id: 'active-tab',
    workerId: 'worker-1',
    workingDir: '/tmp',
    gitBranch: 'main',
    gitOriginUrl: 'git@example.com:o/r.git',
    gitToplevel: '/repo',
  })
  s.selection.setActiveById(TabType.TERMINAL, 'active-tab')
}

describe('useterminaloperations.handleopenterminal', () => {
  it('happy path: opens a terminal, adds the tab, and flips loading false in the finally', async () => {
    const loadingFlips: boolean[] = []
    const { ops, view } = setupForOpen({
      setNewTerminalLoading: v => loadingFlips.push(v),
    })

    await ops.handleOpenTerminal()

    expect(openTerminalMock).toHaveBeenCalledTimes(1)
    // Shell is empty (default-shell quick action). shellStartDir is
    // forwarded as empty string when the caller didn't pass one.
    expect(openTerminalMock.mock.calls[0][1]).toMatchObject({
      workerId: 'worker-1',
      workingDir: '/tmp',
      shell: '',
      shellStartDir: '',
    })
    // Tab was added with the response's terminalId and seeded
    // shellStartDir falling back to workingDir.
    const newTab = view.getTerminalTab('new-tid')
    expect(newTab).toBeDefined()
    expect(newTab?.workerId).toBe('worker-1')
    expect(newTab?.workingDir).toBe('/tmp')
    expect(newTab?.shellStartDir).toBe('/tmp')
    // Loading toggled true then false (finally).
    expect(loadingFlips).toEqual([true, false])
  })

  it('forwards an explicit shellStartDir to both the RPC and the tab seed', async () => {
    const { ops, view } = setupForOpen()
    await ops.handleOpenTerminal('/work/dir')
    expect(openTerminalMock.mock.calls[0][1].shellStartDir).toBe('/work/dir')
    const newTab = view.getTerminalTab('new-tid')
    expect(newTab?.shellStartDir).toBe('/work/dir')
  })

  /**
   * The optimistic git seed exists so a new terminal renders under the right
   * branch before the worker's phase-1 broadcast lands. It is only correct when
   * the new tab's working tree is the SAME one as the active tab's — and
   * `effectiveGitDir` is `shellStartDir || workingDir`, so the guard needs
   * `shellStartDir` to reach it. Passing only `workingDir` (which comes from
   * the active tab) made both sides resolve to the same value, so the guard
   * could never reject and "open a terminal here" on a sibling worktree
   * inherited the wrong repo's branch, origin and diff badges.
   */
  it('does NOT seed git info when the new terminal is in a different directory', async () => {
    const s = setupForOpen()
    seedActiveRepoTab(s)

    // `handleOpenTerminal(dir)` passes `dir` as shellStartDir, which is what
    // `effectiveGitDir` prefers -- so this new tab's tree is /repo-worktree
    // while the active tab's is /tmp.
    await s.ops.handleOpenTerminal('/repo-worktree')

    const newTab = s.view.getTerminalTab('new-tid')
    expect(newTab?.gitBranch, 'a sibling worktree must not inherit the branch').toBeUndefined()
    expect(newTab?.gitOriginUrl).toBeUndefined()
    expect(newTab?.gitToplevel).toBeUndefined()
  })

  // The other half of the same guard: when the directories DO match, seeding is
  // the whole point -- without it the tab renders ungrouped until the worker's
  // phase-1 broadcast lands. A test for the rejection alone would still pass if
  // the seed were removed entirely.
  it('dOES seed git info when the new terminal shares the active tab directory', async () => {
    const s = setupForOpen()
    seedActiveRepoTab(s)

    await s.ops.handleOpenTerminal()

    const newTab = s.view.getTerminalTab('new-tid')
    expect(newTab?.gitBranch).toBe('main')
    expect(newTab?.gitToplevel).toBe('/repo')
  })

  it('opens the new-terminal dialog when ctx is missing (no workerId)', async () => {
    const dialogOpen = vi.fn()
    const { ops, view } = setupForOpen({
      ctx: { workerId: '', workingDir: '/tmp' },
      dialogOpen,
    })
    await ops.handleOpenTerminal()
    expect(dialogOpen).toHaveBeenCalledTimes(1)
    expect(openTerminalMock).not.toHaveBeenCalled()
    expect(view.all()).toHaveLength(0)
  })

  it('short-circuits silently when the workspace is not mutatable', async () => {
    const dialogOpen = vi.fn()
    const setLoading = vi.fn()
    const { ops, view } = setupForOpen({
      isMutatable: false,
      dialogOpen,
      setNewTerminalLoading: setLoading,
    })
    await ops.handleOpenTerminal()
    expect(openTerminalMock).not.toHaveBeenCalled()
    expect(dialogOpen).not.toHaveBeenCalled()
    expect(setLoading).not.toHaveBeenCalled()
    expect(view.all()).toHaveLength(0)
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
    const { ops, view } = setupForOpen({
      setNewShellLoading: v => shellLoadingFlips.push(v),
      setNewTerminalLoading: v => terminalLoadingFlips.push(v),
    })

    await ops.handleOpenTerminalWithShell('/bin/zsh')

    expect(openTerminalMock).toHaveBeenCalledTimes(1)
    expect(openTerminalMock.mock.calls[0][1].shell).toBe('/bin/zsh')
    // The shell-picker path does NOT seed shellStartDir onto the tab,
    // so a later restart re-uses the working directory the worker had
    // at launch rather than a stale per-shell override.
    const newTab = view.getTerminalTab('new-tid')
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
  })

  it('keeps one SendInput in flight per terminal so the PTY sees arrival order', async () => {
    // The worker dispatches every inner RPC on its own goroutine, so two calls
    // in flight together can reach the PTY transposed. Two syllables swapped is
    // exactly the corruption CJK typing shows.
    const { ops } = setup(TerminalStatus.READY)
    let release: (() => void) | undefined
    sendInputMock.mockImplementationOnce(async () => {
      await new Promise<void>((resolve) => {
        release = resolve
      })
      return {}
    })

    const first = ops.handleTerminalInput('tid-1', new TextEncoder().encode('\uC548'))
    await Promise.resolve()
    expect(sendInputMock).toHaveBeenCalledTimes(1)

    // Typed while the first call is still out. Neither may start a second call.
    void ops.handleTerminalInput('tid-1', new TextEncoder().encode('\uB155'))
    void ops.handleTerminalInput('tid-1', new TextEncoder().encode('!'))
    await Promise.resolve()
    expect(sendInputMock).toHaveBeenCalledTimes(1)

    release!()
    await first

    // The queued bytes left together, in the order they were typed.
    expect(sendInputMock).toHaveBeenCalledTimes(2)
    expect(new TextDecoder().decode(sendInputMock.mock.calls[0][1].data)).toBe('\uC548')
    expect(new TextDecoder().decode(sendInputMock.mock.calls[1][1].data)).toBe('\uB155!')
  })

  it('starts a fresh queue once a burst has drained', async () => {
    // The queue is dropped when it empties, so a later keystroke must not land
    // in a stale one -- nor be batched onto bytes that already went out.
    const { ops } = setup(TerminalStatus.READY)

    await ops.handleTerminalInput('tid-1', new TextEncoder().encode('a'))
    await ops.handleTerminalInput('tid-1', new TextEncoder().encode('b'))

    expect(sendInputMock).toHaveBeenCalledTimes(2)
    expect(new TextDecoder().decode(sendInputMock.mock.calls[0][1].data)).toBe('a')
    expect(new TextDecoder().decode(sendInputMock.mock.calls[1][1].data)).toBe('b')
  })

  it('keeps draining after a failed send', async () => {
    const { ops } = setup(TerminalStatus.READY)
    let release: (() => void) | undefined
    sendInputMock.mockImplementationOnce(async () => {
      await new Promise<void>((resolve) => {
        release = resolve
      })
      throw new Error('worker offline')
    })

    const first = ops.handleTerminalInput('tid-1', new TextEncoder().encode('a'))
    await Promise.resolve()
    void ops.handleTerminalInput('tid-1', new TextEncoder().encode('b'))
    release!()
    // One failed write must not discard the rest of what the user typed, and
    // must not propagate: xterm's onData callback has no error sink.
    await expect(first).resolves.toBeUndefined()
    expect(sendInputMock).toHaveBeenCalledTimes(2)
    expect(new TextDecoder().decode(sendInputMock.mock.calls[1][1].data)).toBe('b')
  })

  it('stops draining when the terminal exits mid-burst', async () => {
    const { ops, metadata } = setup(TerminalStatus.READY)
    let release: (() => void) | undefined
    sendInputMock.mockImplementationOnce(async () => {
      await new Promise<void>((resolve) => {
        release = resolve
      })
      return {}
    })

    const first = ops.handleTerminalInput('tid-1', new TextEncoder().encode('a'))
    await Promise.resolve()
    void ops.handleTerminalInput('tid-1', new TextEncoder().encode('b'))
    // The shell exits while the first write is still out.
    metadata.patch('tid-1', { terminalStatus: TerminalStatus.EXITED })
    release!()
    await first

    expect(sendInputMock).toHaveBeenCalledTimes(1)
  })

  it('calls restartTerminal when Enter (CR) is pressed on an EXITED terminal', async () => {
    const { ops } = setup(TerminalStatus.EXITED)
    await ops.handleTerminalInput('tid-1', new Uint8Array([0x0D]))
    expect(restartTerminalMock).toHaveBeenCalledTimes(1)
    expect(sendInputMock).not.toHaveBeenCalled()
    const arg = restartTerminalMock.mock.calls[0][1]
    expect(arg).toMatchObject({
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
  it('loads shells from listAvailableShells on mount when a worker is present', async () => {
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
      expect.objectContaining({ workerId: 'worker-1' }),
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

describe('terminal bell via watch events', () => {
  it('does not notify the active terminal tab on bell', () => {
    const { view, selection, metadata, add } = setup()
    add('term-1')
    selection.setActiveById(TabType.TERMINAL, 'term-1')
    handleTerminalBell('term-1', { metadata, selection, getActiveWorkspaceId: () => 'ws-1' })
    expect(view.getTerminalTab('term-1')?.hasNotification).not.toBe(true)
  })

  it('notifies a terminal tab that is not the active one', () => {
    const { view, selection, metadata, add } = setup()
    add('term-1')
    add('term-2')
    selection.setActiveById(TabType.TERMINAL, 'term-2')
    handleTerminalBell('term-1', { metadata, selection, getActiveWorkspaceId: () => 'ws-1' })
    expect(view.getTerminalTab('term-1')?.hasNotification).toBe(true)
    expect(view.getTerminalTab('term-2')?.hasNotification).not.toBe(true)
  })
})

describe('useterminaloperations.handleterminalclose', () => {
  it('removes the terminal tab synchronously and fires closeTerminal with KEEP by default', () => {
    const { ops, view, add } = setup()
    add('term-close', { workerId: 'w-1' })

    // Never resolves so the test stays in the synchronous-effects window.
    closeTerminalMock.mockReturnValueOnce(new Promise(() => {}))

    ops.handleTerminalClose('term-close')

    expect(view.getTerminalTab('term-close')).toBeUndefined()
    expect(closeTerminalMock).toHaveBeenCalledWith('w-1', expect.objectContaining({
      terminalId: 'term-close',
      worktreeAction: WorktreeAction.KEEP,
    }))
  })

  /**
   * Disposal must run BEFORE the tombstone, and must not capture the buffer.
   *
   * `emitRemoveTab` applies to `speculativeState` synchronously and bumps
   * `pendingVersion`, so Solid flushes the metadata retention sweep before the
   * next statement. Disposing afterwards fired the screen sink for a tab the
   * sweep had just reclaimed, re-creating its metadata row — carrying a full
   * serialized scrollback — with nothing left to evict it until some other tab
   * happened to be created or closed. Capturing at all is pointless here: the
   * tab is being destroyed, so the bytes have no future reader.
   */
  it('disposes the xterm before tombstoning the tab, without capturing the buffer', () => {
    const { ops, add } = setup()
    add('term-order', { workerId: 'w-1' })
    vi.mocked(disposeTerminalInstance).mockClear()
    closeTerminalMock.mockReturnValueOnce(new Promise(() => {}))

    ops.handleTerminalClose('term-order')

    expect(disposeTerminalInstance).toHaveBeenCalledWith('term-order', { captureScreen: false })
  })

  it('passes through the worktreeAction argument', async () => {
    const { ops, add } = setup()
    add('term-remove', { workerId: 'w-1' })

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
    const { ops, add } = setup()
    add('term-fail', { workerId: 'w-1' })

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
    const { ops, add } = setup()
    add('term-reject', { workerId: 'w-1' })

    const err = new Error('offline')
    closeTerminalMock.mockRejectedValueOnce(err)

    ops.handleTerminalClose('term-reject')
    await flush()

    expect(showWarnToastMock).toHaveBeenCalledWith('Failed to close terminal', err)
  })
})
