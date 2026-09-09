import type { Tab } from './tab.types'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createQuakeTerminalStore, quakeKeyForTab, quakeKeyId } from './quakeTerminal.store'
import { createTabMetadataStore } from './tabMetadata.store'

vi.mock('~/api/workerRpc', () => ({
  listTerminals: vi.fn(async () => ({ terminals: [], verdicts: [] })),
  openTerminal: vi.fn(async () => ({ terminalId: 'quake-1', title: 'Terminal Alpha' })),
  // Mocked so a call can be REFUSED by assertion. The store must never make
  // one: the worker closes a quake terminal itself.
  closeTerminal: vi.fn(async () => ({ result: {} })),
}))

const disposeInstance = vi.fn()
vi.mock('~/components/terminal/TerminalView', () => ({
  disposeTerminalInstance: (...args: unknown[]) => disposeInstance(...args),
}))

const warnToast = vi.fn()
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: (...args: unknown[]) => warnToast(...args),
}))

const listTerminals = workerRpc.listTerminals as unknown as ReturnType<typeof vi.fn>
const openTerminal = workerRpc.openTerminal as unknown as ReturnType<typeof vi.fn>
const closeTerminal = workerRpc.closeTerminal as unknown as ReturnType<typeof vi.fn>

const AGENT: Tab = {
  type: TabType.AGENT,
  id: 'a1',
  workspaceId: 'ws1',
  workerId: 'w1',
  workingDir: '/repo',
}

/** A SECOND agent tab in the same directory. The whole point of the model. */
const SIBLING: Tab = { ...AGENT, id: 'a2' }

/** A terminal TAB in the same directory, so the panel is not an agent feature. */
const TERMINAL_TAB: Tab = {
  type: TabType.TERMINAL,
  id: 't1',
  workspaceId: 'ws1',
  workerId: 'w1',
  workingDir: '/repo',
}

/** A file viewer, the tab kind furthest from a shell, in another directory. */
const FILE_TAB: Tab = {
  type: TabType.FILE,
  id: 'f1',
  workspaceId: 'ws1',
  workerId: 'w1',
  workingDir: '/other',
}

const KEY = quakeKeyId({ workerId: 'w1', workingDir: '/repo' })
const OTHER_KEY = quakeKeyId({ workerId: 'w1', workingDir: '/other' })

function setup(closeDelayMs = 0, mutatable = true) {
  const metadata = createTabMetadataStore()
  const focusComposer = vi.fn()
  // A box rather than a constant, so a test can archive the workspace while a
  // panel is open -- which is the case the open/close asymmetry exists for.
  const workspace = { mutatable }
  // The live tab set, as the shell's `findTabInWorkingDir` sees it. A box so a
  // test can close the last tab of a directory.
  const tabs = { live: [AGENT, SIBLING, TERMINAL_TAB, FILE_TAB] as Tab[] }
  let dispose!: () => void
  const store = createRoot((d) => {
    dispose = d
    return createQuakeTerminalStore({
      metadata,
      tabForKey: key => tabs.live.find(t => t.workerId === key.workerId && t.workingDir === key.workingDir),
      focusComposer,
      closeDelayMs: () => closeDelayMs,
      isWorkspaceMutatable: () => workspace.mutatable,
    })
  })
  return { store, metadata, focusComposer, workspace, tabs, dispose }
}

beforeEach(() => {
  vi.clearAllMocks()
  listTerminals.mockResolvedValue({ terminals: [], verdicts: [] })
  openTerminal.mockResolvedValue({ terminalId: 'quake-1', title: 'Terminal Alpha' })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('quakeKeyForTab', () => {
  // The one place that decides whether a tab has a panel, and it asks about the
  // DIRECTORY rather than the tab type. Pinned because the shortcut, the panel
  // and the watch plan all route through it: narrowing it back to agent tabs
  // would silently take the panel away from three tab kinds.
  it('names a key for every tab type that carries a worker and a directory', () => {
    for (const tab of [AGENT, TERMINAL_TAB, FILE_TAB]) {
      expect(quakeKeyForTab(tab), `${TabType[tab.type]} tabs have a quake panel`).toEqual({
        workerId: tab.workerId,
        workingDir: tab.workingDir,
      })
    }
  })

  it('names no key for a tab with no worker or no directory', () => {
    expect(quakeKeyForTab({ ...AGENT, workerId: undefined })).toBeUndefined()
    expect(quakeKeyForTab({ ...AGENT, workingDir: undefined })).toBeUndefined()
    expect(quakeKeyForTab(undefined)).toBeUndefined()
  })

  // Two workers can both have /repo, and they are two machines.
  it('separates one directory on two workers', () => {
    expect(quakeKeyId({ workerId: 'w1', workingDir: '/repo' }))
      .not
      .toBe(quakeKeyId({ workerId: 'w2', workingDir: '/repo' }))
  })
})

describe('createQuakeTerminalStore', () => {
  it('asks the worker for an existing quake terminal before opening one', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)

    expect(listTerminals).toHaveBeenCalledWith('w1', { tabIds: [], quakeWorkingDirs: ['/repo'] })
    expect(openTerminal).toHaveBeenCalledOnce()
    expect(openTerminal.mock.calls[0][1]).toMatchObject({ quake: true, shell: '', workingDir: '/repo' })
    expect(store.entryFor(KEY)?.terminalId).toBe('quake-1')
    dispose()
  })

  // The headline property. Two agent tabs on one checkout are ONE panel and one
  // shell: the second open finds the entry the first made and issues no RPC at
  // all, so switching between them cannot swap the terminal underneath.
  it('gives two tabs in one directory the same panel', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)
    vi.clearAllMocks()

    await store.open(SIBLING)

    expect(listTerminals, 'the second tab reuses the entry, not the RPC').not.toHaveBeenCalled()
    expect(openTerminal).not.toHaveBeenCalled()
    expect(store.entryFor(KEY)?.terminalId).toBe('quake-1')
    expect(store.detachedTerminals()).toHaveLength(1)
    dispose()
  })

  // The panel is not an agent feature. A terminal tab in the same directory
  // reaches the SAME shell the agent tab beside it shows.
  it('gives a terminal tab in that directory the same panel too', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)
    vi.clearAllMocks()

    await store.open(TERMINAL_TAB)

    expect(openTerminal).not.toHaveBeenCalled()
    expect(store.entryFor(KEY)?.terminalId).toBe('quake-1')
    dispose()
  })

  // And a file viewer, which owns no process of its own, still opens a panel --
  // in ITS directory, which is a different shell.
  it('opens a separate panel for a tab in another directory', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)
    openTerminal.mockResolvedValue({ terminalId: 'quake-2', title: 'Terminal Bravo' })

    await store.open(FILE_TAB)

    expect(store.entryFor(KEY)?.terminalId).toBe('quake-1')
    expect(store.entryFor(OTHER_KEY)?.terminalId).toBe('quake-2')
    expect(store.detachedTerminals()).toHaveLength(2)
    dispose()
  })

  // A shell shared across workspaces follows the tab that last reached it, so a
  // later cold reopen is refused in the workspace the user is actually in.
  it('re-stamps the workspace when another tab reaches the same shell', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)
    expect(store.entryFor(KEY)?.workspaceId).toBe('ws1')

    await store.open({ ...SIBLING, workspaceId: 'ws2' })

    expect(store.entryFor(KEY)?.workspaceId).toBe('ws2')
    dispose()
  })

  // The second device. Its list HITS, so it attaches to the shell the first
  // device started instead of asking for one the worker's unique index would
  // refuse anyway.
  it('adopts the quake terminal the worker already has, and opens nothing', async () => {
    listTerminals.mockResolvedValue({
      terminals: [{ terminalId: 'shared-1', quake: true, cols: 80, rows: 25, screen: new Uint8Array() }],
      verdicts: [],
    })
    const { store, metadata, dispose } = setup()
    await store.open(AGENT)

    expect(openTerminal).not.toHaveBeenCalled()
    expect(store.entryFor(KEY)?.terminalId).toBe('shared-1')
    expect(metadata.get('shared-1')).toBeDefined()
    dispose()
  })

  it('seeds a freshly opened quake terminal as starting, so the panel shows its startup', async () => {
    const { store, metadata, dispose } = setup()
    await store.open(AGENT)

    expect(metadata.get('quake-1')?.title).toBe('Terminal Alpha')
    expect(metadata.get('quake-1')?.hydrated).toBe(true)
    dispose()
  })

  // What "toggling does not terminate it" means: the second open is free.
  it('issues no RPC when a panel that already exists is opened again', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)
    store.close(KEY)
    vi.clearAllMocks()

    await store.open(AGENT)

    expect(listTerminals).not.toHaveBeenCalled()
    expect(openTerminal).not.toHaveBeenCalled()
    expect(store.entryFor(KEY)?.open).toBe(true)
    dispose()
  })

  it('toggles between showing and hidden', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)
    expect(store.entryFor(KEY)?.open).toBe(true)

    store.toggle(AGENT)
    expect(store.entryFor(KEY)?.open).toBe(false)

    store.toggle(AGENT)
    expect(store.entryFor(KEY)?.open).toBe(true)
    dispose()
  })

  // One toggle from either tab acts on the one panel they share, which is what
  // makes the chord mean the same thing wherever the user presses it.
  it('lets a sibling tab toggle the panel the other one opened', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)

    store.toggle(SIBLING)

    expect(store.entryFor(KEY)?.open).toBe(false)
    dispose()
  })

  // The shell's restore asks whether focus is still INSIDE the panel, and
  // closing it marks it `inert`, which blurs whatever it holds. Asked
  // afterwards, the answer would always be "focus is elsewhere" and the caret
  // would never come back.
  it('asks for the focus restore while the panel is still open', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(AGENT)

    let openAtRestore: boolean | undefined
    focusComposer.mockImplementation(() => {
      openAtRestore = store.entryFor(KEY)?.open
    })
    store.close(KEY)

    expect(openAtRestore).toBe(true)
    expect(store.entryFor(KEY)?.open).toBe(false)
    dispose()
  })

  // The restore now runs BEFORE the flip, so the "already closed" guard is what
  // stops a second close pulling the caret out of wherever the user moved it.
  it('asks for no focus restore when the panel is already closed', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(AGENT)
    store.close(KEY)
    focusComposer.mockClear()

    store.close(KEY)
    store.close('never-opened')

    expect(focusComposer).not.toHaveBeenCalled()
    dispose()
  })

  it('asks for the focus restore before retracting on a shell exit too', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(AGENT)

    let openAtRestore: boolean | undefined
    focusComposer.mockImplementation(() => {
      openAtRestore = store.entryFor(KEY)?.open
    })
    store.handleShellExit('quake-1')

    expect(openAtRestore).toBe(true)
    dispose()
  })

  it('publishes its quake terminals to the tab view, and only once resolved', async () => {
    const { store, dispose } = setup()
    expect(store.detachedTerminals()).toEqual([])

    await store.open(AGENT)

    expect(store.detachedTerminals()).toEqual([{ id: 'quake-1', workerId: 'w1', workspaceId: 'ws1' }])
    expect(store.isQuakeTerminal('quake-1')).toBe(true)
    expect(store.keyOf('quake-1')).toBe(KEY)
    dispose()
  })

  // A quake terminal has no row any surface renders, so a background shell's
  // notification badge goes on a TAB in its directory instead.
  it('names a tab in the directory as the badge target', async () => {
    const { store, dispose } = setup()
    await store.open(AGENT)

    expect(store.badgeTabFor('quake-1')).toBe('a1')
    expect(store.badgeTabFor('not-a-quake-terminal')).toBeUndefined()
    dispose()
  })

  /**
   * An archived workspace opens no terminal, and the panel must not be the one
   * surface that does.
   *
   * The guard lives here rather than in each caller, because the keyboard
   * commands and the Control CLI both arrive through these three methods and
   * would otherwise be able to answer differently.
   */
  describe('an archived workspace', () => {
    it('opens no shell', async () => {
      const { store, dispose } = setup(0, false)
      await store.open(AGENT)

      expect(listTerminals).not.toHaveBeenCalled()
      expect(openTerminal).not.toHaveBeenCalled()
      expect(store.entryFor(KEY)).toBeUndefined()
      dispose()
    })

    /**
     * A panel the user opened BEFORE the workspace was archived keeps toggling
     * both ways, and issues no RPC either way.
     *
     * Archiving does not end a running shell -- an archived terminal tab keeps
     * its own -- so showing one that already exists starts nothing. Refusing
     * the toggle here would strand the user instead: the panel covers the whole
     * centre area and carries no close control of its own.
     */
    it('still toggles a panel the user opened before it was archived', async () => {
      const { store, workspace, dispose } = setup()
      await store.open(AGENT)
      expect(store.entryFor(KEY)?.open).toBe(true)
      vi.clearAllMocks()

      workspace.mutatable = false
      store.toggle(AGENT)
      expect(store.entryFor(KEY)?.open, 'the toggle still hides it').toBe(false)

      store.toggle(AGENT)
      expect(store.entryFor(KEY)?.open, 'and still shows the shell it already has').toBe(true)
      expect(openTerminal, 'neither direction starts anything').not.toHaveBeenCalled()
      dispose()
    })
  })

  it('leaves no half-entry when the worker refuses', async () => {
    openTerminal.mockRejectedValue(new Error('worker offline'))
    const { store, dispose } = setup()

    await store.open(AGENT)

    expect(store.entryFor(KEY)).toBeUndefined()
    expect(store.detachedTerminals()).toEqual([])
    expect(warnToast).toHaveBeenCalled()
    dispose()
  })

  describe('the shell exiting', () => {
    it('retracts the panel, then releases it once the slide is over', async () => {
      vi.useFakeTimers()
      const { store, metadata, dispose } = setup(300)
      await store.open(AGENT)

      store.handleShellExit('quake-1')
      expect(store.entryFor(KEY)?.open, 'the user watches it leave').toBe(false)
      expect(store.entryFor(KEY)).toBeDefined()

      vi.advanceTimersByTime(300)
      expect(store.entryFor(KEY)).toBeUndefined()
      expect(disposeInstance).toHaveBeenCalledWith('quake-1', { captureScreen: false })
      expect(metadata.get('quake-1')).toBeUndefined()
      dispose()
    })

    it('releases at once when there is no animation to wait out', async () => {
      const { store, dispose } = setup(0)
      await store.open(AGENT)

      store.handleShellExit('quake-1')

      expect(store.entryFor(KEY)).toBeUndefined()
      dispose()
    })

    // Not the "press Enter to restart" contract a terminal TAB has: the next
    // open misses the worker's directory lookup and spawns a fresh shell.
    it('lets the next open start a new shell', async () => {
      const { store, dispose } = setup(0)
      await store.open(AGENT)
      store.handleShellExit('quake-1')
      vi.clearAllMocks()
      openTerminal.mockResolvedValue({ terminalId: 'quake-2', title: 'Terminal Bravo' })

      await store.open(AGENT)

      expect(openTerminal).toHaveBeenCalledOnce()
      expect(store.entryFor(KEY)?.terminalId).toBe('quake-2')
      dispose()
    })

    // A 300 ms retract is a real window, and the toggle is one keypress. The
    // panel must survive it AND come back with a working shell -- releasing the
    // entry on the timer would unmount a panel the user just asked for.
    it('keeps the panel and starts a fresh shell when it is reopened mid-retract', async () => {
      vi.useFakeTimers()
      const { store, dispose } = setup(300)
      await store.open(AGENT)

      store.handleShellExit('quake-1')
      store.toggle(AGENT)
      expect(store.entryFor(KEY)?.open).toBe(true)

      openTerminal.mockResolvedValue({ terminalId: 'quake-2', title: 'Terminal Bravo' })
      vi.advanceTimersByTime(300)
      await vi.runAllTimersAsync()

      expect(store.entryFor(KEY), 'the panel the user reopened must survive').toBeDefined()
      expect(store.entryFor(KEY)?.open).toBe(true)
      expect(store.entryFor(KEY)?.terminalId).toBe('quake-2')
      expect(disposeInstance).toHaveBeenCalledWith('quake-1', { captureScreen: false })
      dispose()
    })

    it('ignores a terminal it does not hold', async () => {
      const { store, dispose } = setup(0)
      await store.open(AGENT)

      store.handleShellExit('some-other-terminal')

      expect(store.entryFor(KEY)).toBeDefined()
      dispose()
    })
  })

  describe('the last tab in a directory closing', () => {
    it('releases the shell that directory held', async () => {
      const { store, metadata, tabs, dispose } = setup()
      await store.open(AGENT)

      tabs.live = [FILE_TAB]
      store.retireStaleKeys()

      expect(store.entryFor(KEY)).toBeUndefined()
      expect(disposeInstance).toHaveBeenCalledWith('quake-1', { captureScreen: false })
      expect(metadata.get('quake-1')).toBeUndefined()
      dispose()
    })

    // The counterpart, and the reason the sweep asks about the DIRECTORY rather
    // than about the retired tab ids: one tab of a shared shell going away is
    // not the end of the shell.
    it('keeps the shell while any tab still works there', async () => {
      const { store, tabs, dispose } = setup()
      await store.open(AGENT)

      tabs.live = [SIBLING]
      store.retireStaleKeys()

      expect(store.entryFor(KEY)?.terminalId).toBe('quake-1')
      expect(disposeInstance).not.toHaveBeenCalled()
      dispose()
    })

    // The worker's own close already ended the shell, on every close path and
    // from whichever device ran it. A second RPC from here would race it.
    it('issues no close RPC, because the worker already closed the shell', async () => {
      const { store, tabs, dispose } = setup()
      await store.open(AGENT)
      vi.clearAllMocks()

      tabs.live = []
      store.retireStaleKeys()

      expect(closeTerminal).not.toHaveBeenCalled()
      dispose()
    })

    it('does nothing when no panel is open', () => {
      const { store, tabs, dispose } = setup()
      tabs.live = []
      expect(() => store.retireStaleKeys()).not.toThrow()
      dispose()
    })
  })

  // A device with the panel open receives both endings when the last tab in its
  // directory closes: the worker's TerminalClosed, and the CRDT tombstone that
  // drives the metadata sweep. Whichever lands first does the work.
  it('survives both teardown paths running for one panel', async () => {
    const { store, tabs, dispose } = setup(0)
    await store.open(AGENT)

    store.handleShellExit('quake-1')
    tabs.live = []
    expect(() => store.retireStaleKeys()).not.toThrow()
    expect(store.entryFor(KEY)).toBeUndefined()
    dispose()
  })

  // The shell of a panel the user already hid must not pull the caret back: the
  // close that hid it already restored focus, and the caret moved on since.
  it('asks for no focus restore when the shell of an already-closed panel exits', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(AGENT)
    store.close(KEY)
    focusComposer.mockClear()

    store.handleShellExit('quake-1')

    expect(focusComposer).not.toHaveBeenCalled()
    dispose()
  })

  // The last tab of a directory can close while the two resolve RPCs are in
  // flight. A write to a store path whose parent key `retireStaleKeys` deleted
  // does not no-op: Solid's `updatePath` dereferences the absent parent and
  // THROWS, and the rejection reached the caller's catch as "Failed to open the
  // quake terminal" -- a failure toast for a tab the user merely closed.
  it('survives its directory emptying while the open RPC is in flight', async () => {
    const { store, tabs, dispose } = setup()
    let resolveOpen!: (v: { terminalId: string, title: string }) => void
    openTerminal.mockReturnValue(new Promise((r) => {
      resolveOpen = r
    }))

    const opening = store.open(AGENT)
    tabs.live = []
    store.retireStaleKeys()
    resolveOpen({ terminalId: 'quake-1', title: 'Terminal Alpha' })
    await opening

    expect(warnToast, 'closing a tab is not an open failure').not.toHaveBeenCalled()
    expect(store.entryFor(KEY)).toBeUndefined()
    // The shell the worker may have spawned in that window is the WORKER's to
    // reclaim -- see `retireStaleKeys`. A CloseTerminal from here would race
    // closeQuakeTerminalIfUnused and the orphan reconciler.
    expect(closeTerminal).not.toHaveBeenCalled()
    dispose()
  })

  // A fresh shell after an exit is a COLD open, so it takes the same refusal
  // `open` applies. Without it the client asks the worker for a shell in an
  // archived workspace and then shows a failure toast for its own request.
  it('does not respawn into a workspace archived during the retract', async () => {
    vi.useFakeTimers()
    const { store, workspace, dispose } = setup(300)
    await store.open(AGENT)
    openTerminal.mockClear()

    store.handleShellExit('quake-1')
    // Reopened inside the animation window, and archived in the same window.
    void store.open(AGENT)
    workspace.mutatable = false
    vi.advanceTimersByTime(300)
    await Promise.resolve()

    expect(openTerminal, 'an archived workspace takes no new shell').not.toHaveBeenCalled()
    expect(store.entryFor(KEY)).toBeUndefined()
    dispose()
  })

  // One panel element holds every directory's terminal, so "is focus inside the
  // panel?" is true whenever ANY of them has the caret. The store therefore
  // reports WHICH shell it retracts, and the shell tells the caller apart from
  // a background directory whose panel closed underneath it.
  it('reports the terminal it retracts, so a background close cannot steal focus', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(AGENT)

    store.close(KEY)

    expect(focusComposer).toHaveBeenCalledWith(KEY, 'quake-1')
    dispose()
  })

  it('reports no terminal for a panel whose RPC never resolved', async () => {
    const { store, focusComposer, dispose } = setup()
    let resolveOpen!: (v: { terminalId: string, title: string }) => void
    openTerminal.mockReturnValue(new Promise((r) => {
      resolveOpen = r
    }))
    const opening = store.open(AGENT)

    store.close(KEY)
    expect(focusComposer).toHaveBeenCalledWith(KEY, undefined)

    resolveOpen({ terminalId: 'quake-1', title: 'Terminal Alpha' })
    await opening
    dispose()
  })

  // A tab with no worker or no working directory names no panel, and every
  // entry point must decline rather than key an entry on an empty string.
  it('opens nothing for a tab that names no key', async () => {
    const { store, dispose } = setup()

    await store.open({ ...AGENT, workingDir: undefined })
    store.toggle({ ...AGENT, workerId: undefined })

    expect(openTerminal).not.toHaveBeenCalled()
    expect(store.detachedTerminals()).toEqual([])
    dispose()
  })
})
