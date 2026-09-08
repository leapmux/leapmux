import type { AgentTab } from './tab.types'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createQuakeTerminalStore } from './quakeTerminal.store'
import { createTabMetadataStore } from './tabMetadata.store'

vi.mock('~/api/workerRpc', () => ({
  listTerminals: vi.fn(async () => ({ terminals: [], verdicts: [] })),
  openTerminal: vi.fn(async () => ({ terminalId: 'quake-1', title: 'Terminal Alpha' })),
  // Mocked so a call can be REFUSED by assertion. The store must never make
  // one: the worker closes a companion itself.
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

const OWNER: AgentTab = {
  type: TabType.AGENT,
  id: 'a1',
  workspaceId: 'ws1',
  workerId: 'w1',
  workingDir: '/repo',
}

function setup(closeDelayMs = 0, mutatable = true) {
  const metadata = createTabMetadataStore()
  const focusComposer = vi.fn()
  // A box rather than a constant, so a test can archive the workspace while a
  // panel is open -- which is the case the open/close asymmetry exists for.
  const workspace = { mutatable }
  let dispose!: () => void
  const store = createRoot((d) => {
    dispose = d
    return createQuakeTerminalStore({
      metadata,
      getAgentTab: id => (id === OWNER.id ? OWNER : undefined),
      focusComposer,
      closeDelayMs: () => closeDelayMs,
      isWorkspaceMutatable: () => workspace.mutatable,
    })
  })
  return { store, metadata, focusComposer, workspace, dispose }
}

beforeEach(() => {
  vi.clearAllMocks()
  listTerminals.mockResolvedValue({ terminals: [], verdicts: [] })
  openTerminal.mockResolvedValue({ terminalId: 'quake-1', title: 'Terminal Alpha' })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('createQuakeTerminalStore', () => {
  it('asks the worker for an existing companion before opening one', async () => {
    const { store, dispose } = setup()
    await store.open(OWNER)

    expect(listTerminals).toHaveBeenCalledWith('w1', { tabIds: [], ownerAgentIds: ['a1'] })
    expect(openTerminal).toHaveBeenCalledOnce()
    expect(openTerminal.mock.calls[0][1]).toMatchObject({ ownerAgentId: 'a1', shell: '', workingDir: '/repo' })
    expect(store.entryFor('a1')?.terminalId).toBe('quake-1')
    dispose()
  })

  // The second device. Its list HITS, so it attaches to the shell the first
  // device started instead of asking for one the worker's unique index would
  // refuse anyway.
  it('adopts the companion the worker already has, and opens nothing', async () => {
    listTerminals.mockResolvedValue({
      terminals: [{ terminalId: 'shared-1', ownerAgentId: 'a1', cols: 80, rows: 25, screen: new Uint8Array() }],
      verdicts: [],
    })
    const { store, metadata, dispose } = setup()
    await store.open(OWNER)

    expect(openTerminal).not.toHaveBeenCalled()
    expect(store.entryFor('a1')?.terminalId).toBe('shared-1')
    expect(metadata.get('shared-1')).toBeDefined()
    dispose()
  })

  it('seeds a freshly opened companion as starting, so the panel shows its startup', async () => {
    const { store, metadata, dispose } = setup()
    await store.open(OWNER)

    expect(metadata.get('quake-1')?.title).toBe('Terminal Alpha')
    expect(metadata.get('quake-1')?.hydrated).toBe(true)
    dispose()
  })

  // What "toggling does not terminate it" means: the second open is free.
  it('issues no RPC when a panel that already exists is opened again', async () => {
    const { store, dispose } = setup()
    await store.open(OWNER)
    store.close('a1')
    vi.clearAllMocks()

    await store.open(OWNER)

    expect(listTerminals).not.toHaveBeenCalled()
    expect(openTerminal).not.toHaveBeenCalled()
    expect(store.entryFor('a1')?.open).toBe(true)
    dispose()
  })

  it('toggles between showing and hidden', async () => {
    const { store, dispose } = setup()
    await store.open(OWNER)
    expect(store.entryFor('a1')?.open).toBe(true)

    store.toggle(OWNER)
    expect(store.entryFor('a1')?.open).toBe(false)

    store.toggle(OWNER)
    expect(store.entryFor('a1')?.open).toBe(true)
    dispose()
  })

  // The shell's restore asks whether focus is still INSIDE the panel, and
  // closing it marks it `inert`, which blurs whatever it holds. Asked
  // afterwards, the answer would always be "focus is elsewhere" and the caret
  // would never come back.
  it('asks for the focus restore while the panel is still open', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(OWNER)

    let openAtRestore: boolean | undefined
    focusComposer.mockImplementation(() => {
      openAtRestore = store.entryFor('a1')?.open
    })
    store.close('a1')

    expect(openAtRestore).toBe(true)
    expect(store.entryFor('a1')?.open).toBe(false)
    dispose()
  })

  // The restore now runs BEFORE the flip, so the "already closed" guard is what
  // stops a second close pulling the caret out of wherever the user moved it.
  it('asks for no focus restore when the panel is already closed', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(OWNER)
    store.close('a1')
    focusComposer.mockClear()

    store.close('a1')
    store.close('never-opened')

    expect(focusComposer).not.toHaveBeenCalled()
    dispose()
  })

  it('asks for the focus restore before retracting on a shell exit too', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(OWNER)

    let openAtRestore: boolean | undefined
    focusComposer.mockImplementation(() => {
      openAtRestore = store.entryFor('a1')?.open
    })
    store.handleShellExit('quake-1')

    expect(openAtRestore).toBe(true)
    dispose()
  })

  it('publishes its companions to the tab view, and only once resolved', async () => {
    const { store, dispose } = setup()
    expect(store.detachedTerminals()).toEqual([])

    await store.open(OWNER)

    expect(store.detachedTerminals()).toEqual([{ id: 'quake-1', workerId: 'w1', workspaceId: 'ws1' }])
    expect(store.isQuakeTerminal('quake-1')).toBe(true)
    expect(store.ownerOf('quake-1')).toBe('a1')
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
      await store.open(OWNER)

      expect(listTerminals).not.toHaveBeenCalled()
      expect(openTerminal).not.toHaveBeenCalled()
      expect(store.entryFor('a1')).toBeUndefined()
      dispose()
    })

    // Hiding a panel starts nothing. Refusing it stranded a user whose
    // workspace was archived while the panel was up: it covers the whole centre
    // area and carries no close control of its own.
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
      await store.open(OWNER)
      expect(store.entryFor('a1')?.open).toBe(true)
      vi.clearAllMocks()

      workspace.mutatable = false
      store.toggle(OWNER)
      expect(store.entryFor('a1')?.open, 'the toggle still hides it').toBe(false)

      store.toggle(OWNER)
      expect(store.entryFor('a1')?.open, 'and still shows the shell it already has').toBe(true)
      expect(openTerminal, 'neither direction starts anything').not.toHaveBeenCalled()
      dispose()
    })
  })

  it('leaves no half-entry when the worker refuses', async () => {
    openTerminal.mockRejectedValue(new Error('worker offline'))
    const { store, dispose } = setup()

    await store.open(OWNER)

    expect(store.entryFor('a1')).toBeUndefined()
    expect(store.detachedTerminals()).toEqual([])
    expect(warnToast).toHaveBeenCalled()
    dispose()
  })

  describe('the shell exiting', () => {
    it('retracts the panel, then releases it once the slide is over', async () => {
      vi.useFakeTimers()
      const { store, metadata, dispose } = setup(300)
      await store.open(OWNER)

      store.handleShellExit('quake-1')
      expect(store.entryFor('a1')?.open, 'the user watches it leave').toBe(false)
      expect(store.entryFor('a1')).toBeDefined()

      vi.advanceTimersByTime(300)
      expect(store.entryFor('a1')).toBeUndefined()
      expect(disposeInstance).toHaveBeenCalledWith('quake-1', { captureScreen: false })
      expect(metadata.get('quake-1')).toBeUndefined()
      dispose()
    })

    it('releases at once when there is no animation to wait out', async () => {
      const { store, dispose } = setup(0)
      await store.open(OWNER)

      store.handleShellExit('quake-1')

      expect(store.entryFor('a1')).toBeUndefined()
      dispose()
    })

    // Not the "press Enter to restart" contract a terminal TAB has: the next
    // open misses the worker's companion lookup and spawns a fresh shell.
    it('lets the next open start a new shell', async () => {
      const { store, dispose } = setup(0)
      await store.open(OWNER)
      store.handleShellExit('quake-1')
      vi.clearAllMocks()
      openTerminal.mockResolvedValue({ terminalId: 'quake-2', title: 'Terminal Bravo' })

      await store.open(OWNER)

      expect(openTerminal).toHaveBeenCalledOnce()
      expect(store.entryFor('a1')?.terminalId).toBe('quake-2')
      dispose()
    })

    // A 300 ms retract is a real window, and the toggle is one keypress. The
    // panel must survive it AND come back with a working shell -- releasing the
    // entry on the timer would unmount a panel the user just asked for.
    it('keeps the panel and starts a fresh shell when it is reopened mid-retract', async () => {
      vi.useFakeTimers()
      const { store, dispose } = setup(300)
      await store.open(OWNER)

      store.handleShellExit('quake-1')
      store.toggle(OWNER)
      expect(store.entryFor('a1')?.open).toBe(true)

      openTerminal.mockResolvedValue({ terminalId: 'quake-2', title: 'Terminal Bravo' })
      vi.advanceTimersByTime(300)
      await vi.runAllTimersAsync()

      expect(store.entryFor('a1'), 'the panel the user reopened must survive').toBeDefined()
      expect(store.entryFor('a1')?.open).toBe(true)
      expect(store.entryFor('a1')?.terminalId).toBe('quake-2')
      expect(disposeInstance).toHaveBeenCalledWith('quake-1', { captureScreen: false })
      dispose()
    })

    it('ignores a terminal it does not own', async () => {
      const { store, dispose } = setup(0)
      await store.open(OWNER)

      store.handleShellExit('some-other-terminal')

      expect(store.entryFor('a1')).toBeDefined()
      dispose()
    })
  })

  describe('the owner tab closing', () => {
    it('releases the companion the owner held', async () => {
      const { store, metadata, dispose } = setup()
      await store.open(OWNER)

      store.retireOwners(new Set(['a1']))

      expect(store.entryFor('a1')).toBeUndefined()
      expect(disposeInstance).toHaveBeenCalledWith('quake-1', { captureScreen: false })
      expect(metadata.get('quake-1')).toBeUndefined()
      dispose()
    })

    // The worker's own close already ended the shell, on every close path and
    // from whichever device ran it. A second RPC from here would race it.
    it('issues no close RPC, because the worker already closed the shell', async () => {
      const { store, dispose } = setup()
      await store.open(OWNER)
      vi.clearAllMocks()

      store.retireOwners(new Set(['a1']))

      expect(closeTerminal).not.toHaveBeenCalled()
      dispose()
    })

    it('ignores an owner with no panel', () => {
      const { store, dispose } = setup()
      expect(() => store.retireOwners(new Set(['nobody']))).not.toThrow()
      dispose()
    })
  })

  // A device with the panel open receives both endings when its owner tab
  // closes: the worker's TerminalClosed, and the CRDT tombstone that drives the
  // metadata sweep. Whichever lands first does the work.
  it('survives both teardown paths running for one panel', async () => {
    const { store, dispose } = setup(0)
    await store.open(OWNER)

    store.handleShellExit('quake-1')
    expect(() => store.retireOwners(new Set(['a1']))).not.toThrow()
    expect(store.entryFor('a1')).toBeUndefined()
    dispose()
  })

  // The shell of a panel the user already hid must not pull the caret back: the
  // close that hid it already restored focus, and the caret moved on since.
  it('asks for no focus restore when the shell of an already-closed panel exits', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(OWNER)
    store.close('a1')
    focusComposer.mockClear()

    store.handleShellExit('quake-1')

    expect(focusComposer).not.toHaveBeenCalled()
    dispose()
  })

  // The owner's tab can close while the two resolve RPCs are in flight. A write
  // to a store path whose parent key `retireOwners` deleted does not no-op:
  // Solid's `updatePath` dereferences the absent parent and THROWS, and the
  // rejection reached the caller's catch as "Failed to open the quake
  // terminal" -- a failure toast for a tab the user merely closed.
  it('survives its owner retiring while the open RPC is in flight', async () => {
    const { store, dispose } = setup()
    let resolveOpen!: (v: { terminalId: string, title: string }) => void
    openTerminal.mockReturnValue(new Promise((r) => {
      resolveOpen = r
    }))

    const opening = store.open(OWNER)
    store.retireOwners(new Set([OWNER.id]))
    resolveOpen({ terminalId: 'quake-1', title: 'Terminal Alpha' })
    await opening

    expect(warnToast, 'closing a tab is not an open failure').not.toHaveBeenCalled()
    expect(store.entryFor(OWNER.id)).toBeUndefined()
    // The shell the worker may have spawned in that window is the WORKER's to
    // reclaim -- see `retireOwners`. A CloseTerminal from here would race
    // closeAgentTabCommon and the orphan reconciler.
    expect(closeTerminal).not.toHaveBeenCalled()
    dispose()
  })

  // A fresh shell after an exit is a COLD open, so it takes the same refusal
  // `open` applies. Without it the client asks the worker for a shell in an
  // archived workspace and then shows a failure toast for its own request.
  it('does not respawn into a workspace archived during the retract', async () => {
    vi.useFakeTimers()
    const { store, workspace, dispose } = setup(300)
    await store.open(OWNER)
    openTerminal.mockClear()

    store.handleShellExit('quake-1')
    // Reopened inside the animation window, and archived in the same window.
    void store.open(OWNER)
    workspace.mutatable = false
    vi.advanceTimersByTime(300)
    await Promise.resolve()

    expect(openTerminal, 'an archived workspace takes no new shell').not.toHaveBeenCalled()
    expect(store.entryFor(OWNER.id)).toBeUndefined()
    dispose()
  })

  // One panel element holds every companion's terminal, so "is focus inside the
  // panel?" is true whenever ANY of them has the caret. The store therefore
  // reports WHICH shell it retracts, and the shell tells the caller apart from
  // a background owner whose panel closed underneath it.
  it('reports the terminal it retracts, so a background close cannot steal focus', async () => {
    const { store, focusComposer, dispose } = setup()
    await store.open(OWNER)

    store.close(OWNER.id)

    expect(focusComposer).toHaveBeenCalledWith(OWNER.id, 'quake-1')
    dispose()
  })

  it('reports no terminal for a panel whose RPC never resolved', async () => {
    const { store, focusComposer, dispose } = setup()
    let resolveOpen!: (v: { terminalId: string, title: string }) => void
    openTerminal.mockReturnValue(new Promise((r) => {
      resolveOpen = r
    }))
    const opening = store.open(OWNER)

    store.close(OWNER.id)
    expect(focusComposer).toHaveBeenCalledWith(OWNER.id, undefined)

    resolveOpen({ terminalId: 'quake-1', title: 'Terminal Alpha' })
    await opening
    dispose()
  })
})
