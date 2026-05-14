import type { Tab, TerminalTab } from '~/stores/tab.types'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createTerminalInstance } from '~/lib/terminal'
import { captureTerminalScreens } from './TerminalView'

beforeEach(() => {
  localStorage.clear()
  // jsdom doesn't implement matchMedia, which createTerminalInstance reads.
  window.matchMedia = vi.fn().mockReturnValue({ matches: false }) as any
})

// Await xterm's async parser via the write callback so serialize sees
// the data we just wrote.
function writeAndWait(
  inst: ReturnType<typeof createTerminalInstance>,
  data: string,
): Promise<void> {
  return new Promise<void>(resolve => inst.terminal.write(data, () => resolve()))
}

describe('captureTerminalScreens', () => {
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
