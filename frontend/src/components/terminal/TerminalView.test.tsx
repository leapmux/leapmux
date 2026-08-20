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
import { applyTheme, DEFAULT_THEME_VALUE } from '~/lib/themeStore'
import { webglPool } from '~/lib/webglTerminalPool'
import { compositionPreview } from '~/test-support/compositionPreview'
import { stubMatchMedia } from '~/test-support/matchMediaStub'

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
  stubMatchMedia()
})

/**
 * The DOM shape xterm builds inside `open()`, which the IME layer hooks:
 * `.xterm > .xterm-screen > .xterm-helpers > .xterm-helper-textarea`. The mock
 * terminal below has to provide it, because `attachTerminalIme` binds on
 * `terminal.element` and positions its preview against `terminal.textarea`.
 */
function makeMockTerminalDom(): { element: HTMLElement, textarea: HTMLTextAreaElement } {
  const element = document.createElement('div')
  element.className = 'xterm'
  const screen = document.createElement('div')
  screen.className = 'xterm-screen'
  const helpers = document.createElement('div')
  helpers.className = 'xterm-helpers'
  const textarea = document.createElement('textarea')
  textarea.className = 'xterm-helper-textarea'
  helpers.appendChild(textarea)
  screen.appendChild(helpers)
  element.appendChild(screen)
  return { element, textarea }
}

function makeMockTerminalInstance(): TerminalInstance {
  let bellHandler: (() => void) | undefined
  const { element, textarea } = makeMockTerminalDom()

  const terminal = {
    // Undefined until open(), exactly as real xterm behaves — TerminalView
    // branches on it to tell a first mount from a re-parent.
    element: undefined as HTMLElement | undefined,
    textarea,
    attachCustomKeyEventHandler: vi.fn(),
    // Real xterm's onData returns an IDisposable; the IME layer disposes it.
    onData: vi.fn(() => ({ dispose: () => {} })),
    onTitleChange: vi.fn(),
    onBell: vi.fn((cb: () => void) => {
      bellHandler = cb
    }),
    open: vi.fn((parent: HTMLElement) => {
      terminal.element = element
      parent.appendChild(element)
    }),
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

  /**
   * A `TerminalTab` is a join result (see tabView), rebuilt whenever any field it
   * derives from `tabMetadata` changes -- an OSC title the shell emits per prompt,
   * a status transition, a cols/rows write, the MRU stamp every click leaves. When
   * the row list was keyed on that OBJECT, each of those disposed the row and
   * mounted a fresh one, tearing down and re-creating the xterm instance and its
   * pooled WebGL context. It LOOKED fine only because teardown serializes the
   * scrollback and the replacement repaints it.
   */
  it('keeps the terminal container mounted when its tab object is rebuilt', async () => {
    const instance = makeMockTerminalInstance()
    mockCreateTerminalInstance.mockReturnValue(instance)
    const tab = (title: string): TerminalTab => ({
      id: 'term-1',
      type: TabType.TERMINAL,
      workspaceId: 'ws-1',
      title,
      screen: new Uint8Array(),
    })
    const [terminals, setTerminals] = createSignal<TerminalTab[]>([tab('bash')])

    const { container } = render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={terminals()}
          activeTerminalId="term-1"
          visible
          tileFocused={false}
          onInput={vi.fn()}
          onResize={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))

    const paneFor = (id: string) => container.querySelector(`[data-terminal-id="${id}"]`)
    await waitFor(() => expect(paneFor('term-1')).not.toBeNull())
    const before = paneFor('term-1')
    const createCalls = mockCreateTerminalInstance.mock.calls.length

    // A fresh object with the SAME id -- exactly what the join hands back after any
    // metadata write on this tab.
    setTerminals([tab('zsh — ~/repo')])
    await Promise.resolve()

    expect(paneFor('term-1'), 'the container is the SAME DOM node').toBe(before)
    expect(mockCreateTerminalInstance.mock.calls.length, 'and no xterm was re-created').toBe(createCalls)
  })

  // Bell during snapshot replay is owned by the worker-side test suite
  // (service/terminal_test.go); TerminalView no longer wires xterm onBell.

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
    const matchMedia = stubMatchMedia()
    const colorScheme = () => matchMedia.handlersFor('(prefers-color-scheme: dark)')[0]

    try {
      // `themeStore` owns the one prefers-color-scheme subscription and
      // re-subscribes on a UI MODE change -- the seam that lets a host install
      // `matchMedia` after the module was imported. This module is imported
      // statically (through PreferencesContext), so it was built before
      // `stubMatchMedia()` ran and holds no listener until nudged.
      applyTheme({ ...DEFAULT_THEME_VALUE, mode: 'light' })
      applyTheme(DEFAULT_THEME_VALUE)

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
            onContentReady={vi.fn()}
          />
        </PreferencesProvider>
      ))

      await waitFor(() => expect(colorScheme()).toBeDefined())

      // Flip the OS to dark: the effect's change guard must let a genuine
      // change through and re-apply the dark theme to the live instance.
      colorScheme()!({ matches: true })
      await waitFor(() => {
        expect(instance.terminal.options.theme).toBe(darkTerminalTheme)
      })

      // Flip back to light: a second genuine change must also propagate,
      // proving the guard updates its last-applied theme rather than latching
      // on the first one it saw.
      colorScheme()!({ matches: false })
      await waitFor(() => {
        expect(instance.terminal.options.theme).toBe(lightTerminalTheme)
      })
    }
    finally {
      matchMedia.restore()
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
    const matchMedia = stubMatchMedia()
    const colorSchemeHandlers = () => matchMedia.handlersFor('(prefers-color-scheme: dark)')

    try {
      // Same reason as the test above: nudge `themeStore` so its one
      // subscription lands on the stub this test just installed.
      applyTheme({ ...DEFAULT_THEME_VALUE, mode: 'light' })
      applyTheme(DEFAULT_THEME_VALUE)

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
            onContentReady={vi.fn()}
          />
          <TerminalView
            terminals={[{ id: 'themed-B', ...baseTab }]}
            activeTerminalId="themed-B"
            visible
            tileFocused
            onInput={vi.fn()}
            onResize={vi.fn()}
            onContentReady={vi.fn()}
          />
        </PreferencesProvider>
      ))

      // Both views mount, both register a handler, both instances exist.
      await waitFor(() => {
        // ONE app-lifetime subscription, not one per tile. Two mounted views
        // used to install two listeners for a question with one answer.
        expect(colorSchemeHandlers().length).toBe(1)
        expect(getTerminalInstance('themed-A')).toBe(instanceA)
        expect(getTerminalInstance('themed-B')).toBe(instanceB)
      })

      // The mount-time application (light) already exercised the guard across
      // both views; zero the counters so we measure only the flip below.
      const baselineA = writesA()
      const baselineB = writesB()

      // Flip the OS to dark and drive every view's handler.
      for (const handler of colorSchemeHandlers())
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
      matchMedia.restore()
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
/**
 * Reset every module mock and pool this file's suites share, so the reset
 * list stays in sync when a new mock or pool joins the file.
 */
function resetTerminalViewMocks(): void {
  mockCreateTerminalInstance.mockReset()
  mockBufferHasVisibleContent.mockReset().mockReturnValue(undefined)
  mockSerializeXtermBuffer.mockReset().mockReturnValue(undefined)
  webglPool.disposeAll()
  // Re-install the full stub in case an earlier suite left a partial one
  // behind; beforeAll has already run, so it does not re-arm on its own.
  stubMatchMedia()
}

describe('disposeTerminalInstance scrollback capture', () => {
  beforeEach(resetTerminalViewMocks)

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
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(getTerminalInstance(id)).toBe(instance))
    return instance
  }

  it('hands the live buffer and its parsed offset to the sink before tearing the instance down', async () => {
    const instance = await mount('term-dispose')
    instance.lastParsedOffset = 4321
    mockBufferHasVisibleContent.mockReturnValue(true)
    mockSerializeXtermBuffer.mockReturnValue(new TextEncoder().encode('scrollback-worth-keeping'))

    const captured: Array<{ id: string, text: string, parsedOffset?: number }> = []
    setTerminalScreenSink((id, screen, parsedOffset) => {
      captured.push({ id, text: new TextDecoder().decode(screen), parsedOffset })
    })

    disposeTerminalInstance('term-dispose')

    expect(captured).toHaveLength(1)
    expect(captured[0].id).toBe('term-dispose')
    expect(captured[0].text).toBe('scrollback-worth-keeping')
    // The offset the serialized screen actually covers rides along, so the
    // store can rewind lastOffset onto it.
    expect(captured[0].parsedOffset).toBe(4321)
    // And the instance really is gone — capturing must not keep it alive.
    expect(getTerminalInstance('term-dispose')).toBeUndefined()
  })

  it('skips the capture while a write is still unparsed', async () => {
    // A dispose mid-parse serializes fewer bytes than the live cursor
    // claims. Storing that screen next to the inflated cursor would make the
    // remount trim the never-painted span of the next catch-up delta as
    // "already rendered" — so the capture waits for a parsed write instead
    // and the previous stored screen stays.
    const instance = await mount('term-midparse')
    instance.lastParsedOffset = undefined
    mockBufferHasVisibleContent.mockReturnValue(true)
    mockSerializeXtermBuffer.mockReturnValue(new TextEncoder().encode('half-parsed'))

    const captured: string[] = []
    setTerminalScreenSink(id => captured.push(id))

    disposeTerminalInstance('term-midparse')

    expect(captured).toEqual([])
    expect(getTerminalInstance('term-midparse')).toBeUndefined()
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
    instance.lastParsedOffset = 10
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

describe('terminalView IME wiring', () => {
  beforeEach(resetTerminalViewMocks)

  async function mount(id: string, onInput = vi.fn(), instance = makeMockTerminalInstance()) {
    mockCreateTerminalInstance.mockReturnValue(instance)
    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={[{ id, type: TabType.TERMINAL, workspaceId: 'ws-1', screen: new Uint8Array() } as TerminalTab]}
          activeTerminalId={id}
          visible
          tileFocused={false}
          onInput={onInput}
          onResize={vi.fn()}
          onContentReady={vi.fn()}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(getTerminalInstance(id)).toBe(instance))
    return { instance, onInput }
  }

  /** The handler TerminalView passed to xterm's attachCustomKeyEventHandler. */
  function keyHandler(instance: TerminalInstance): (e: KeyboardEvent) => boolean {
    const attach = instance.terminal.attachCustomKeyEventHandler as unknown as ReturnType<typeof vi.fn>
    expect(attach).toHaveBeenCalledTimes(1)
    return attach.mock.calls[0][0]
  }

  it('attaches the key handler and the IME layer on every platform', async () => {
    // jsdom is not macOS. Before the IME work this handler was attached only on
    // macOS, so CJK input had no interception path at all on Linux or Windows.
    const { instance } = await mount('term-ime-attach')
    expect(instance.terminal.attachCustomKeyEventHandler).toHaveBeenCalledTimes(1)
    expect(instance.ime).toBeDefined()

    disposeTerminalInstance('term-ime-attach')
  })

  it('logs the input the gate forwarded, and only that', async () => {
    // The e2e IME specs assert against this log; it must record every path
    // the gate lets through (keystrokes, IME commits, programmatic writes)
    // and nothing the gate suppressed, or the doubling/loss assertions would
    // measure the wrong thing.
    const onInput = vi.fn()
    const { instance } = await mount('term-ime-log', onInput)

    const commit = (text: string) => instance.terminal.textarea!.dispatchEvent(
      new CompositionEvent('compositionend', { data: text, bubbles: true, composed: true }),
    )

    commit('\uC548')
    instance.sendInput!(new TextEncoder().encode('!!'))
    expect((window as any).__getActiveTerminalInputLog()).toBe('\uC548!!')

    // A write mid-replay is swallowed by the gate and must stay out of the
    // log — the log is the send-side truth, not the attempt-side.
    instance.suppressInput = true
    commit('\uB155')
    expect((window as any).__getActiveTerminalInputLog()).toBe('\uC548!!')

    disposeTerminalInstance('term-ime-log')
  })

  it('releases the IME layer before the terminal on dispose', async () => {
    const { instance } = await mount('term-ime-order')
    const order: string[] = []
    const imeDispose = instance.ime!.dispose.bind(instance.ime)
    instance.ime!.dispose = () => {
      order.push('ime')
      imeDispose()
    }
    const instanceDispose = instance.dispose.bind(instance)
    instance.dispose = () => {
      order.push('terminal')
      instanceDispose()
    }

    disposeTerminalInstance('term-ime-order')

    // The layer's listeners and its preview node hang off terminal.element,
    // which xterm removes as it tears down, so the layer must go first.
    expect(order).toEqual(['ime', 'terminal'])
    expect(instance.ime).toBeUndefined()
  })

  it('keeps the shell alive when the IME layer cannot attach', async () => {
    // An xterm upgrade that changes the helper DOM shape makes
    // attachTerminalIme throw by design. The throw must cost the composition
    // path only — escaping onMount would reach the route-level boundary and
    // blank every tile.
    const broken = makeMockTerminalInstance()
    broken.terminal.textarea!.remove()
    const { instance } = await mount('term-ime-broken', vi.fn(), broken)

    expect(instance.ime).toBeUndefined()
    // The plain key path is still wired.
    expect(instance.terminal.attachCustomKeyEventHandler).toHaveBeenCalledTimes(1)

    disposeTerminalInstance('term-ime-broken')
  })

  it('takes composed keystrokes away from xterm', async () => {
    const { instance } = await mount('term-ime-keys')
    const handle = keyHandler(instance)

    // false means "xterm must not process this".
    expect(handle(new KeyboardEvent('keydown', { key: 'Process', keyCode: 229 } as KeyboardEventInit))).toBe(false)
    expect(handle(new KeyboardEvent('keydown', { key: 'd', isComposing: true } as KeyboardEventInit))).toBe(false)
    expect(handle(new KeyboardEvent('keydown', { key: 'd', keyCode: 68 }))).toBe(true)

    disposeTerminalInstance('term-ime-keys')
  })

  it('leaves the arrow keys to xterm off macOS', async () => {
    // The CMD/ALT+Arrow rule stays macOS-only even though the handler is now
    // attached everywhere; the shortcut system only sends those sequences there.
    const { instance } = await mount('term-ime-arrows')
    const handle = keyHandler(instance)

    expect(handle(new KeyboardEvent('keydown', { key: 'ArrowLeft', metaKey: true }))).toBe(true)
    expect(handle(new KeyboardEvent('keydown', { key: 'ArrowRight', altKey: true }))).toBe(true)

    disposeTerminalInstance('term-ime-arrows')
  })

  it('sends composed text through the same suppressInput gate as onData', async () => {
    const onInput = vi.fn()
    const { instance } = await mount('term-ime-gate', onInput)

    const commit = () => instance.terminal.textarea!.dispatchEvent(
      new CompositionEvent('compositionend', { data: '안', bubbles: true, composed: true }),
    )

    commit()
    expect(onInput).toHaveBeenCalledTimes(1)
    expect(new TextDecoder().decode(onInput.mock.calls[0][1])).toBe('안')

    // Snapshot replay must swallow composed input exactly as it swallows
    // ordinary keystrokes, or a reconnect replays the user's typing at the PTY.
    onInput.mockClear()
    instance.suppressInput = true
    commit()
    expect(onInput).not.toHaveBeenCalled()

    disposeTerminalInstance('term-ime-gate')
  })

  it('suppresses programmatic sendInput on snapshot replay like a keystroke', async () => {
    const onInput = vi.fn()
    const { instance } = await mount('term-ime-sendgate', onInput)

    // Programmatic writes (keyboard shortcuts, the e2e input hook) go through
    // the same gate as keystrokes: a write that leaks through mid-replay
    // interleaves with the snapshot being replayed.
    instance.sendInput!(new TextEncoder().encode('\x01'))
    expect(onInput).toHaveBeenCalledTimes(1)
    expect(onInput.mock.calls[0][0]).toBe('term-ime-sendgate')

    instance.suppressInput = true
    instance.sendInput!(new TextEncoder().encode('\x05'))
    expect(onInput).toHaveBeenCalledTimes(1)

    disposeTerminalInstance('term-ime-sendgate')
  })

  it('releases the IME layer when the instance is disposed', async () => {
    const { instance } = await mount('term-ime-dispose')
    const preview = () => compositionPreview(instance.terminal.element)
    expect(preview()).not.toBeNull()

    // The real dispose() tears the IME layer down; the mock instance's dispose
    // is a spy, so drive the layer directly to assert the teardown contract.
    instance.ime!.dispose()
    expect(preview()).toBeNull()

    disposeTerminalInstance('term-ime-dispose')
  })
})
