import type { TerminalInstance } from '~/lib/terminal'
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { createStore } from 'solid-js/store'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'

const mockCreateTerminalInstance = vi.fn()

vi.mock('~/lib/terminal', async () => {
  const actual = await vi.importActual<typeof import('~/lib/terminal')>('~/lib/terminal')
  return {
    ...actual,
    createTerminalInstance: (...args: unknown[]) => mockCreateTerminalInstance(...args),
  }
})

const { TerminalView, getTerminalInstance } = await import('~/components/terminal/TerminalView')

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
    suppressInput: false,
    dispose: vi.fn(),
  }
}

describe('terminalView', () => {
  beforeEach(() => {
    mockCreateTerminalInstance.mockReset()
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
            workspaceId: 'ws-1',
            screen: new TextEncoder().encode('\x07restored'),
          }]}
          activeTerminalId="term-1"
          visible
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={onBell}
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
            workspaceId: 'ws-1',
            status: TerminalStatus.STARTING,
            startupMessage: 'Starting zsh…',
            screen: new Uint8Array(),
          }]}
          activeTerminalId="term-1"
          visible
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
            workspaceId: 'ws-1',
            status: TerminalStatus.STARTING,
            screen: new Uint8Array(),
          }]}
          activeTerminalId="term-1"
          visible
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

  // Closing a single tab must dispose exactly that terminal's xterm
  // instance (releasing its WebGL context, scrollback, and listener
  // refs) and leave other tabs' instances intact. The disposal is
  // driven by TerminalView's tabs-diff effect, not by the full-unmount
  // onCleanup — a workspace switch is a separate path.
  it('disposes a terminal instance when its tab is closed', async () => {
    const instanceA = makeMockTerminalInstance()
    const instanceB = makeMockTerminalInstance()
    // createTerminalInstance is called once per new terminal; return in
    // the order TerminalContainer mounts them.
    mockCreateTerminalInstance
      .mockReturnValueOnce(instanceA)
      .mockReturnValueOnce(instanceB)

    const baseTab = { workspaceId: 'ws-1', screen: new Uint8Array() }
    const [terminals, setTerminals] = createSignal([
      { id: 'dispose-test-A', ...baseTab },
      { id: 'dispose-test-B', ...baseTab },
    ])

    render(() => (
      <PreferencesProvider>
        <TerminalView
          terminals={terminals()}
          activeTerminalId="dispose-test-A"
          visible
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

    // Close tab A by removing it from the list.
    setTerminals([{ id: 'dispose-test-B', ...baseTab }])

    await waitFor(() => {
      expect(getTerminalInstance('dispose-test-A')).toBeUndefined()
    })
    expect(instanceA.dispose).toHaveBeenCalledTimes(1)
    // B stays live — only the closed tab's instance should be disposed.
    expect(getTerminalInstance('dispose-test-B')).toBe(instanceB)
    expect(instanceB.dispose).not.toHaveBeenCalled()
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
            workspaceId: 'ws-1',
            screen: new Uint8Array(),
          }]}
          activeTerminalId="term-1"
          visible
          onInput={vi.fn()}
          onResize={vi.fn()}
          onTitleChange={vi.fn()}
          onBell={vi.fn()}
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
    const [terminals, setTerminals] = createStore<Array<{
      id: string
      workspaceId: string
      screen?: Uint8Array
    }>>([{
      id: 'term-late-screen',
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
    const [terminals, setTerminals] = createStore<Array<{
      id: string
      workspaceId: string
      screen: Uint8Array
      title?: string
    }>>([{
      id: 'term-no-double-write',
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
