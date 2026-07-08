import type { TerminalInstance } from '~/lib/terminal'
import type { Tab, TerminalTab } from '~/stores/tab.types'
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { createStore } from 'solid-js/store'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { darkTerminalTheme, lightTerminalTheme } from '~/lib/terminal'
import { webglPool } from '~/lib/webglTerminalPool'

const mockCreateTerminalInstance = vi.fn()

vi.mock('~/lib/terminal', async () => {
  const actual = await vi.importActual<typeof import('~/lib/terminal')>('~/lib/terminal')
  return {
    ...actual,
    createTerminalInstance: (...args: unknown[]) => mockCreateTerminalInstance(...args),
  }
})

const { TerminalView, getTerminalInstance, disposeTerminalInstance, captureTerminalScreens }
  = await import('~/components/terminal/TerminalView')

// captureTerminalScreens serializes a *live* xterm, so those tests need the
// real factory rather than the component-suite mock installed above.
const { createTerminalInstance } = await vi.importActual<typeof import('~/lib/terminal')>('~/lib/terminal')

beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
  window.matchMedia = vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }) as any
})

function makeMockTerminalInstance(): TerminalInstance {
  let bellHandler: (() => void) | undefined

  const terminal = {
    onData: vi.fn(),
    onTitleChange: vi.fn(),
    onBell: vi.fn((cb: () => void) => {
      bellHandler = cb
    }),
    open: vi.fn(),
    reset: vi.fn(),
    write: vi.fn((data: string | Uint8Array, cb?: () => void) => {
      const text = typeof data === 'string' ? data : new TextDecoder().decode(data)
      if (text.includes('\x07')) {
        bellHandler?.()
      }
      cb?.()
    }),
    focus: vi.fn(),
    scrollPages: vi.fn(),
    loadAddon: vi.fn(),
    clearTextureAtlas: vi.fn(),
    options: {},
    buffer: {
      active: {
        length: 0,
        getLine: () => null,
      },
    },
    cols: 80,
    rows: 24,
    dispose: vi.fn(),
  } as any

  return {
    terminal,
    fitAddon: { fit: vi.fn() } as any,
    serializeAddon: { serialize: vi.fn() } as any,
    suppressInput: false,
    // WebGL-ineligible so the shared pool never tries to attach a real context
    // to this mock during the on-screen effect; the acquire/release wiring is
    // still exercised and spyable.
    webglAllowed: false,
    fontsReady: Promise.resolve(),
    webglAddon: undefined,
    dispose: vi.fn(),
  }
}

// Await xterm's async parser via the write callback so serialize sees
// the data we just wrote.
function writeAndWait(
  inst: ReturnType<typeof createTerminalInstance>,
  data: string,
): Promise<void> {
  return new Promise<void>(resolve => inst.terminal.write(data, () => resolve()))
}

describe('terminalView', () => {
  beforeEach(() => {
    mockCreateTerminalInstance.mockReset()
    // Reset shared pool state between tests (module-level singleton).
    webglPool.disposeAll()
  })

  it('acquires a pooled WebGL context only for the visible terminal', async () => {
    const instanceA = makeMockTerminalInstance()
    const instanceB = makeMockTerminalInstance()
    mockCreateTerminalInstance
      .mockReturnValueOnce(instanceA)
      .mockReturnValueOnce(instanceB)
    const acquireSpy = vi.spyOn(webglPool, 'acquire')
    const releaseSpy = vi.spyOn(webglPool, 'release')

    const baseTab = { type: TabType.TERMINAL as const, screen: new Uint8Array() }
    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[
            { id: 'vis-A', ...baseTab },
            { id: 'hid-B', ...baseTab },
          ]}
          activeTerminalId="vis-A"
          visible
          tileFocused
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    // The active + visible terminal claims a slot with focus priority.
    await waitFor(() => {
      expect(acquireSpy).toHaveBeenCalledWith('vis-A', instanceA, { focused: true })
    })
    // The hidden sibling never acquires; it releases instead.
    const acquiredIds = acquireSpy.mock.calls.map(call => call[0])
    expect(acquiredIds).not.toContain('hid-B')
    expect(releaseSpy).toHaveBeenCalledWith('hid-B')

    acquireSpy.mockRestore()
    releaseSpy.mockRestore()
  })

  it('does not forward bell notifications during snapshot replay', async () => {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)
    const onBell = vi.fn()

    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[{
            id: 'term-1',
            type: TabType.TERMINAL,
            screen: new TextEncoder().encode('\x07restored'),
          }]}
          activeTerminalId="term-1"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={onBell}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    await waitFor(() => {
      expect(instance.terminal.write).toHaveBeenCalled()
      expect(instance.suppressInput).toBe(false)
    })

    expect(onBell).not.toHaveBeenCalled()
  })

  // The overlay covers an xterm that hasn't painted content yet. The
  // label comes from the backend's TerminalStatusChange.startup_message
  // (e.g. "Starting zsh…") so users see the resolved shell name, and
  // falls back to "Starting terminal…" when the message is missing
  // (pre-statusChange, legacy callers, etc.).
  it('renders startupMessage in the terminal startup overlay when provided', async () => {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)

    const { findByTestId, findByText } = render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[{
            id: 'term-1',
            type: TabType.TERMINAL,
            status: TerminalStatus.STARTING,
            startupMessage: 'Starting zsh…',
            screen: new Uint8Array(),
          }]}
          activeTerminalId="term-1"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    await findByTestId('terminal-startup-overlay')
    await findByText('Starting zsh…')
  })

  it('falls back to the default label when startupMessage is missing', async () => {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)

    const { findByTestId, findByText } = render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[{
            id: 'term-1',
            type: TabType.TERMINAL,
            status: TerminalStatus.STARTING,
            screen: new Uint8Array(),
          }]}
          activeTerminalId="term-1"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    await findByTestId('terminal-startup-overlay')
    await findByText('Starting terminal…')
  })

  it('does not show the startup overlay for an exited terminal without restored screen bytes', async () => {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)

    const { queryByTestId, queryByText } = render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[{
            id: 'term-exited-empty',
            type: TabType.TERMINAL,
            status: TerminalStatus.EXITED,
            screen: new Uint8Array(),
          }]}
          activeTerminalId="term-exited-empty"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    await waitFor(() => {
      expect(instance.terminal.open).toHaveBeenCalled()
    })

    expect(queryByTestId('terminal-startup-overlay')).toBeNull()
    expect(queryByText('Starting terminal…')).toBeNull()
  })

  // Closing a single tab must dispose exactly that terminal's xterm
  // instance (releasing its WebGL context, scrollback, and listener
  // refs) and leave other tabs' instances intact. The disposal is
  // driven by TerminalView's tabs-diff effect, not by the full-unmount
  // onCleanup — a workspace switch is a separate path.
  it('disposes a terminal instance when explicitly closed', async () => {
    const instanceA = makeMockTerminalInstance()
    const instanceB = makeMockTerminalInstance()
    // createTerminalInstance is called once per new terminal; return in
    // the order TerminalContainer mounts them.
    mockCreateTerminalInstance
      .mockReturnValueOnce(instanceA)
      .mockReturnValueOnce(instanceB)

    const baseTab = { type: TabType.TERMINAL as const, screen: new Uint8Array() }
    const [terminals, setTerminals] = createSignal<TerminalTab[]>([
      { id: 'dispose-test-A', ...baseTab },
      { id: 'dispose-test-B', ...baseTab },
    ])

    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={terminals()}
          activeTerminalId="dispose-test-A"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    await waitFor(() => {
      expect(getTerminalInstance('dispose-test-A')).toBe(instanceA)
      expect(getTerminalInstance('dispose-test-B')).toBe(instanceB)
    })

    // Mirror the production close path: explicit dispose, then drop the
    // tab from the prop list. With per-view ownership tracking, removing
    // alone does not auto-dispose (the id may have moved to another tile).
    const releaseSpy = vi.spyOn(webglPool, 'release')
    disposeTerminalInstance('dispose-test-A')
    setTerminals([{ id: 'dispose-test-B', ...baseTab }])

    expect(getTerminalInstance('dispose-test-A')).toBeUndefined()
    expect(instanceA.dispose).toHaveBeenCalledTimes(1)
    // Disposal must also relinquish the pool's WebGL slot for that id.
    expect(releaseSpy).toHaveBeenCalledWith('dispose-test-A')
    // B stays live — only the closed tab's instance should be disposed.
    expect(getTerminalInstance('dispose-test-B')).toBe(instanceB)
    expect(instanceB.dispose).not.toHaveBeenCalled()
    releaseSpy.mockRestore()
  })

  it('moves the pooled WebGL context when the active terminal changes', async () => {
    const instanceA = makeMockTerminalInstance()
    const instanceB = makeMockTerminalInstance()
    mockCreateTerminalInstance
      .mockReturnValueOnce(instanceA)
      .mockReturnValueOnce(instanceB)

    const baseTab = { type: TabType.TERMINAL as const, screen: new Uint8Array() }
    const [activeId, setActiveId] = createSignal('switch-A')
    const acquireSpy = vi.spyOn(webglPool, 'acquire')
    const releaseSpy = vi.spyOn(webglPool, 'release')

    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[
            { id: 'switch-A', ...baseTab },
            { id: 'switch-B', ...baseTab },
          ]}
          activeTerminalId={activeId()}
          visible
          tileFocused
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    // Initially A is the visible tab and claims the slot.
    await waitFor(() => {
      expect(acquireSpy).toHaveBeenCalledWith('switch-A', instanceA, { focused: true })
    })

    // Switch the active tab to B: A must relinquish its slot, B must claim one.
    setActiveId('switch-B')
    await waitFor(() => {
      expect(acquireSpy).toHaveBeenCalledWith('switch-B', instanceB, { focused: true })
    })
    expect(releaseSpy).toHaveBeenCalledWith('switch-A')

    acquireSpy.mockRestore()
    releaseSpy.mockRestore()
  })

  it('re-applies a genuine terminal-theme change to every live instance', async () => {
    localStorage.clear()
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)

    // Drive an OS prefers-color-scheme flip through the change handler the view
    // registers. With the default match-ui terminal theme + system UI theme the
    // resolved theme follows the OS, so this exercises the (guarded) theme
    // effect end to end. matchMedia starts in light.
    const originalMatchMedia = window.matchMedia
    let colorSchemeHandler: ((e: { matches: boolean }) => void) | undefined
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      addEventListener: (_type: string, cb: (e: { matches: boolean }) => void) => {
        colorSchemeHandler = cb
      },
      removeEventListener: vi.fn(),
    }) as any

    try {
      const baseTab = { type: TabType.TERMINAL as const, screen: new Uint8Array() }
      render(() => (
        <PreferencesProvider>
          <TerminalView
            terminals={[{ id: 'theme-A', ...baseTab }]}
            activeTerminalId="theme-A"
            visible
            tileFocused
            onInput={vi.fn()}
            onResize={vi.fn()}
            onTitleChange={vi.fn()}
            onBell={vi.fn()}
            onContentReady={vi.fn()}
          />
        </PreferencesProvider>
      ))

      await waitFor(() => expect(colorSchemeHandler).toBeDefined())

      // Flip the OS to dark: the effect's change guard must let a genuine
      // change through and re-apply the dark theme to the live instance.
      colorSchemeHandler!({ matches: true })
      await waitFor(() => {
        expect(instance.terminal.options.theme).toBe(darkTerminalTheme)
      })

      // Flip back to light: a second genuine change must also propagate,
      // proving the guard updates its last-applied theme rather than latching
      // on the first one it saw.
      colorSchemeHandler!({ matches: false })
      await waitFor(() => {
        expect(instance.terminal.options.theme).toBe(lightTerminalTheme)
      })
    }
    finally {
      window.matchMedia = originalMatchMedia
    }
  })

  it('writes each instance theme once on a change, not once per mounted view', async () => {
    localStorage.clear()

    // Two tiles (two TerminalView instances) share the module-level `instances`
    // map, so BOTH views' theme effects iterate BOTH instances on a theme flip.
    // The per-instance guard must collapse that to one write per instance
    // instead of tiles x instances writes (each xterm theme write rebuilds the
    // color table). Count writes via a defined accessor on each mock.
    function withThemeCounter(inst: TerminalInstance): () => number {
      let stored: unknown
      let writes = 0
      Object.defineProperty(inst.terminal, 'options', {
        configurable: true,
        value: Object.defineProperties({} as Record<string, unknown>, {
          theme: {
            configurable: true,
            get: () => stored,
            set: (v: unknown) => {
              stored = v
              writes++
            },
          },
        }),
      })
      return () => writes
    }

    const instanceA = makeMockTerminalInstance()
    const instanceB = makeMockTerminalInstance()
    const writesA = withThemeCounter(instanceA)
    const writesB = withThemeCounter(instanceB)
    mockCreateTerminalInstance
      .mockReturnValueOnce(instanceA)
      .mockReturnValueOnce(instanceB)

    // Collect every view's prefers-color-scheme handler so we can flip both.
    const originalMatchMedia = window.matchMedia
    const colorSchemeHandlers: Array<(e: { matches: boolean }) => void> = []
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      addEventListener: (_type: string, cb: (e: { matches: boolean }) => void) => {
        colorSchemeHandlers.push(cb)
      },
      removeEventListener: vi.fn(),
    }) as any

    try {
      const baseTab = { type: TabType.TERMINAL as const, screen: new Uint8Array() }
      render(() => (
        <PreferencesProvider>
          <TerminalView
            terminals={[{ id: 'themed-A', ...baseTab }]}
            activeTerminalId="themed-A"
            visible
            tileFocused
            onInput={vi.fn()}
            onResize={vi.fn()}
            onTitleChange={vi.fn()}
            onBell={vi.fn()}
            onContentReady={vi.fn()}
          />
          <TerminalView
            terminals={[{ id: 'themed-B', ...baseTab }]}
            activeTerminalId="themed-B"
            visible
            tileFocused
            onInput={vi.fn()}
            onResize={vi.fn()}
            onTitleChange={vi.fn()}
            onBell={vi.fn()}
            onContentReady={vi.fn()}
          />
        </PreferencesProvider>
      ))

      // Both views mount, both register a handler, both instances exist.
      await waitFor(() => {
        expect(colorSchemeHandlers.length).toBe(2)
        expect(getTerminalInstance('themed-A')).toBe(instanceA)
        expect(getTerminalInstance('themed-B')).toBe(instanceB)
      })

      // The mount-time application (light) already exercised the guard across
      // both views; zero the counters so we measure only the flip below.
      const baselineA = writesA()
      const baselineB = writesB()

      // Flip the OS to dark and drive every view's handler.
      for (const handler of colorSchemeHandlers)
        handler({ matches: true })

      await waitFor(() => {
        expect(instanceA.terminal.options.theme).toBe(darkTerminalTheme)
        expect(instanceB.terminal.options.theme).toBe(darkTerminalTheme)
      })

      // Exactly one write per instance for the flip — the second view finds the
      // theme already applied and skips. Without the guard each instance would
      // be written twice (once per view).
      expect(writesA() - baselineA).toBe(1)
      expect(writesB() - baselineB).toBe(1)
    }
    finally {
      window.matchMedia = originalMatchMedia
    }
  })

  it('scrolls the active terminal by one page', async () => {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)
    let pageScroll!: (direction: -1 | 1) => void

    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[{
            id: 'term-1',
            type: TabType.TERMINAL,
            screen: new Uint8Array(),
          }]}
          activeTerminalId="term-1"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
          pageScrollRef={(fn) => { pageScroll = fn }}
        />
      </PreferencesProvider>
    ))

    await waitFor(() => {
      expect(instance.terminal.open).toHaveBeenCalled()
    })

    pageScroll(-1)
    expect(instance.terminal.scrollPages).toHaveBeenCalledWith(-1)
  })

  // Regression: the saved screen snapshot can arrive *after* the
  // TerminalContainer has mounted, e.g. when ListTerminals is queued
  // behind a worker reconnect on a full-restart restore. The component
  // must apply the snapshot reactively when `screen` becomes non-empty,
  // not just inside onMount, or the restored xterm stays blank.
  //
  // Uses a Solid store (mirroring tabStore.updateTab in production) so
  // the terminal object reference stays stable across the screen update
  // — `<For>` would otherwise unmount + remount on a replaced array
  // entry and re-trigger onMount, masking the regression.
  it('applies the screen snapshot when it becomes available after mount', async () => {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)

    const initialPayload = new TextEncoder().encode('restored screen')
    const [terminals, setTerminals] = createStore<TerminalTab[]>([{
      id: 'term-late-screen',
      type: TabType.TERMINAL,
      // screen is undefined initially — ListTerminals hasn't returned yet.
      screen: undefined,
    }])

    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={terminals}
          activeTerminalId="term-late-screen"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    // Mount happens with an undefined screen — nothing is written yet.
    await waitFor(() => {
      expect(instance.terminal.open).toHaveBeenCalled()
    })
    expect(instance.terminal.write).not.toHaveBeenCalled()

    // ListTerminals returns later. tabStore.updateTab mutates the existing
    // tab's screen field in place — the For loop does NOT re-mount.
    setTerminals(0, 'screen', initialPayload)

    await waitFor(() => {
      expect(instance.terminal.write).toHaveBeenCalled()
    })
    // The first write should carry the restored payload bytes.
    const writtenArg = (instance.terminal.write as any).mock.calls[0][0]
    expect(writtenArg).toBe(initialPayload)
  })

  // Counterpart: re-applying the snapshot every time props change would
  // double-paint the restored state on top of any subsequent live data.
  // The instance-level latch must keep the post-mount effect a one-shot.
  it('does not re-apply the snapshot when an unrelated prop changes', async () => {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)

    const screen = new TextEncoder().encode('once')
    const [terminals, setTerminals] = createStore<TerminalTab[]>([{
      id: 'term-no-double-write',
      type: TabType.TERMINAL,
      screen,
      title: 'Initial',
    }])

    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={terminals}
          activeTerminalId="term-no-double-write"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    await waitFor(() => {
      expect(instance.terminal.write).toHaveBeenCalledTimes(1)
    })

    // Bump an unrelated field — screen reference is unchanged.
    setTerminals(0, 'title', 'Updated')

    // Flush any pending reactive re-runs (microtask + animation frame).
    await Promise.resolve()
    await new Promise(r => requestAnimationFrame(() => r(undefined)))
    expect(instance.terminal.write).toHaveBeenCalledTimes(1)
  })
})

describe('captureTerminalScreens', () => {
  beforeEach(() => {
    localStorage.clear()
    // jsdom doesn't implement matchMedia, which createTerminalInstance reads.
    window.matchMedia = vi.fn().mockReturnValue({ matches: false }) as any
  })

  it('replaces tab.screen with the live xterm serialization for TERMINAL tabs whose instance is mounted', async () => {
    const inst = createTerminalInstance({ cols: 80, rows: 24 })
    await writeAndWait(inst, 'live-content-here\r\n')

    const stale = new TextEncoder().encode('stale-initial-snapshot')
    const tabs: Tab[] = [
      { type: TabType.TERMINAL, id: 'term-1', screen: stale } as Tab,
    ]

    const captured = captureTerminalScreens(tabs, id =>
      id === 'term-1' ? inst : undefined)

    expect(captured).toHaveLength(1)
    expect((captured[0] as TerminalTab).screen).toBeDefined()
    const decoded = new TextDecoder().decode((captured[0] as TerminalTab).screen!)
    expect(decoded).toContain('live-content-here')
    // The post-initial-snapshot bytes the user has been watching must
    // replace the stale screen captured at first hydration.
    expect(decoded).not.toContain('stale-initial-snapshot')

    inst.dispose()
  })

  it('leaves TERMINAL tabs without a live instance unchanged (identity-preserved)', () => {
    const stale = new TextEncoder().encode('stale-but-only-thing-we-have')
    const tabs: Tab[] = [
      { type: TabType.TERMINAL, id: 'term-missing', screen: stale } as Tab,
    ]
    const captured = captureTerminalScreens(tabs, () => undefined)
    expect(captured[0]).toBe(tabs[0])
    expect((captured[0] as TerminalTab).screen).toBe(stale)
  })

  it('leaves non-TERMINAL tabs unchanged and never invokes lookup for them', () => {
    const tabs: Tab[] = [
      { type: TabType.AGENT, id: 'agent-1' } as Tab,
      { type: TabType.FILE, id: 'file-1' } as Tab,
    ]
    const lookup = vi.fn(() => {
      throw new Error('lookup should not be called for non-TERMINAL tabs')
    })
    const captured = captureTerminalScreens(tabs, lookup)
    expect(captured[0]).toBe(tabs[0])
    expect(captured[1]).toBe(tabs[1])
    expect(lookup).not.toHaveBeenCalled()
  })

  it('preserves the original tab.screen when the live buffer has no visible content yet', () => {
    // Simulates a workspace switch that fires before the freshly-mounted
    // xterm has parsed its initial snapshot write — the instance exists
    // and the lookup hits, but the buffer is blank. Overwriting with the
    // (effectively empty) serialization would lose the bytes
    // `ListTerminals` returned, so the helper must keep the original.
    const blank = createTerminalInstance({ cols: 80, rows: 24 })
    const initialScreen = new TextEncoder().encode('initial-from-listterminals')
    const tabs: Tab[] = [
      { type: TabType.TERMINAL, id: 'term-pending', screen: initialScreen } as Tab,
    ]

    const captured = captureTerminalScreens(tabs, id =>
      id === 'term-pending' ? blank : undefined)

    expect(captured[0]).toBe(tabs[0])
    expect((captured[0] as TerminalTab).screen).toBe(initialScreen)

    blank.dispose()
  })

  it('round-trips through applyTerminalData on a fresh instance', async () => {
    const source = createTerminalInstance({ cols: 80, rows: 10 })
    await writeAndWait(source, 'first line\r\n')
    await writeAndWait(source, 'second line\r\n')

    const tabs: Tab[] = [{ type: TabType.TERMINAL, id: 'term-x' } as Tab]
    const captured = captureTerminalScreens(tabs, () => source)
    const screen = (captured[0] as TerminalTab).screen!
    expect(screen.length).toBeGreaterThan(0)

    // Replay into a fresh instance the same way the production restore
    // path does (terminal.reset then write).
    const restored = createTerminalInstance({ cols: 80, rows: 10 })
    restored.terminal.reset()
    await new Promise<void>(resolve =>
      restored.terminal.write(screen, () => resolve()))

    const buf = restored.terminal.buffer.active
    let dump = ''
    for (let i = 0; i < buf.length; i++)
      dump += `${buf.getLine(i)?.translateToString(true) ?? ''}\n`

    expect(dump).toContain('first line')
    expect(dump).toContain('second line')

    source.dispose()
    restored.dispose()
  })
})
