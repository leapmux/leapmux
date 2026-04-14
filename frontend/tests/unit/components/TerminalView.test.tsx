import type { TerminalInstance } from '~/lib/terminal'
import { render, waitFor } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'

const mockCreateTerminalInstance = vi.fn()

vi.mock('~/lib/terminal', async () => {
  const actual = await vi.importActual<typeof import('~/lib/terminal')>('~/lib/terminal')
  return {
    ...actual,
    createTerminalInstance: (...args: unknown[]) => mockCreateTerminalInstance(...args),
  }
})

const { TerminalView } = await import('~/components/terminal/TerminalView')

beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
  window.matchMedia = vi.fn().mockReturnValue({ matches: false }) as any
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
    screenRestored: false,
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
})
