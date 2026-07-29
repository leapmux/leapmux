import type { TerminalInstance } from '~/lib/terminal'
import type { TerminalTab } from '~/stores/tab.types'
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
// Overridable only where a test needs to drive the disposal-capture guard; the
// default delegates to the real implementation so every other suite is
// unaffected.
const mockBufferHasVisibleContent = vi.fn<(t: unknown) => boolean | undefined>(() => undefined)
const mockSerializeXtermBuffer = vi.fn<(i: unknown) => Uint8Array | undefined>(() => undefined)

vi.mock('~/lib/terminal', async () => {
  const actual = await vi.importActual<typeof import('~/lib/terminal')>('~/lib/terminal')
  return {
    ...actual,
    createTerminalInstance: (...args: unknown[]) => mockCreateTerminalInstance(...args),
    bufferHasVisibleContent: (t: unknown) => mockBufferHasVisibleContent(t) ?? actual.bufferHasVisibleContent(t as never),
    serializeXtermBuffer: (i: unknown) => mockSerializeXtermBuffer(i) ?? actual.serializeXtermBuffer(i as never),
  }
})

const { TerminalView, getTerminalInstance, disposeTerminalInstance, setTerminalScreenSink }
  = await import('~/components/terminal/TerminalView')

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

    const baseTab = { type: TabType.TERMINAL as const, workspaceId: 'ws-1', screen: new Uint8Array() }
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
            workspaceId: 'ws-1',
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
            workspaceId: 'ws-1',
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
            workspaceId: 'ws-1',
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
            workspaceId: 'ws-1',
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

    const baseTab = { type: TabType.TERMINAL as const, workspaceId: 'ws-1', screen: new Uint8Array() }
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

    const baseTab = { type: TabType.TERMINAL as const, workspaceId: 'ws-1', screen: new Uint8Array() }
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
      const baseTab = { type: TabType.TERMINAL as const, workspaceId: 'ws-1', screen: new Uint8Array() }
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
      const baseTab = { type: TabType.TERMINAL as const, workspaceId: 'ws-1', screen: new Uint8Array() }
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
            workspaceId: 'ws-1',
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
      workspaceId: 'ws-1',
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
      workspaceId: 'ws-1',
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

/**
 * Scrollback is rescued at the teardown chokepoint, not at the workspace
 * switch. A switch is only one of the ways a terminal's view goes away — a
 * cross-workspace move of the tab, or closing a floating window that holds it,
 * unmounts and disposes with no switch involved. Every one of those paths runs
 * through `disposeTerminalInstance`, and nothing re-fetches the bytes
 * afterwards (`hydrated` is already true), so a miss here is permanent.
 */
describe('disposeTerminalInstance scrollback capture', () => {
  beforeEach(() => {
    mockCreateTerminalInstance.mockReset()
    mockBufferHasVisibleContent.mockReset().mockReturnValue(undefined)
    mockSerializeXtermBuffer.mockReset().mockReturnValue(undefined)
    webglPool.disposeAll()
    // An earlier suite may leave matchMedia as a bare `{ matches }` stub with
    // no addEventListener, and beforeAll has already run — restore the full
    // shape the rendered component needs.
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }) as any
  })

  afterEach(() => {
    setTerminalScreenSink(null)
  })

  /** Mount one TerminalView so its instance lands in the module registry. */
  async function mount(id: string) {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)
    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[{ id, type: TabType.TERMINAL, workspaceId: 'ws-1', screen: new Uint8Array() } as TerminalTab]}
          activeTerminalId={id}
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
    await waitFor(() => expect(getTerminalInstance(id)).toBe(instance))
    return instance
  }

  it('hands the live buffer to the sink before tearing the instance down', async () => {
    await mount('term-dispose')
    mockBufferHasVisibleContent.mockReturnValue(true)
    mockSerializeXtermBuffer.mockReturnValue(new TextEncoder().encode('scrollback-worth-keeping'))

    const captured: Array<{ id: string, text: string }> = []
    setTerminalScreenSink((id, screen) => {
      captured.push({ id, text: new TextDecoder().decode(screen) })
    })

    disposeTerminalInstance('term-dispose')

    expect(captured).toHaveLength(1)
    expect(captured[0].id).toBe('term-dispose')
    expect(captured[0].text).toBe('scrollback-worth-keeping')
    // And the instance really is gone — capturing must not keep it alive.
    expect(getTerminalInstance('term-dispose')).toBeUndefined()
  })

  it('writes nothing for a buffer that has not painted yet', async () => {
    // A freshly-mounted xterm still parsing its initial snapshot serializes
    // blank; writing that back would erase what ListTerminals returned.
    await mount('term-blank')
    mockBufferHasVisibleContent.mockReturnValue(false)

    const captured: string[] = []
    setTerminalScreenSink(id => captured.push(id))

    disposeTerminalInstance('term-blank')

    expect(captured).toEqual([])
    expect(getTerminalInstance('term-blank')).toBeUndefined()
  })

  it('still tears down when the sink throws', async () => {
    const instance = await mount('term-throws')
    mockBufferHasVisibleContent.mockReturnValue(true)
    mockSerializeXtermBuffer.mockReturnValue(new Uint8Array([1]))

    setTerminalScreenSink(() => {
      throw new Error('quota exceeded')
    })

    expect(() => disposeTerminalInstance('term-throws')).not.toThrow()
    expect(getTerminalInstance('term-throws'), 'the instance must not leak').toBeUndefined()
    expect(instance.dispose).toHaveBeenCalledTimes(1)
  })
})
