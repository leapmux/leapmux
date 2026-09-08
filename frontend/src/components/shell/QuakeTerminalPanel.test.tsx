import type { QuakeEntry } from '~/stores/quakeTerminal.store'
import type { DetachedTerminal } from '~/stores/tabView'
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { withPreferences } from '~/test-support/preferencesProvider'
import { QuakeTerminalPanel } from './QuakeTerminalPanel'

// The terminal itself is out of scope here: its own suite covers the xterm
// lifecycle, and mounting one would pull the WebGL pool into a DOM test.
vi.mock('~/components/terminal/TerminalView', () => ({
  TerminalView: (props: { activeTerminalId: string | null, visible: boolean }) => (
    <div
      data-testid="terminal-view"
      data-active-terminal={props.activeTerminalId ?? ''}
      data-visible={String(props.visible)}
    />
  ),
}))

afterEach(() => {
  cleanup()
})

function mount(over: { entry?: QuakeEntry, detached?: DetachedTerminal[] } = {}) {
  const entry = over.entry
  const detached = over.detached ?? (entry?.terminalId ? [{ id: entry.terminalId, workerId: 'w1', workspaceId: 'ws1' }] : [])
  const quakeStore = {
    entryFor: (id: string) => (entry && entry.ownerId === id ? entry : undefined),
    detachedTerminals: () => detached,
    ownerOf: () => entry?.ownerId,
    isQuakeTerminal: () => true,
    open: vi.fn(),
    close: vi.fn(),
    toggle: vi.fn(),
    handleShellExit: vi.fn(),
    retireOwners: vi.fn(),
    liveEntries: () => (entry ? [entry] : []),
  }
  const view = { getTerminalTab: (id: string) => ({ id, type: 2 }) }
  const metadata = { get: () => undefined }
  const rendered = render(withPreferences(() => (
    <QuakeTerminalPanel
      quakeStore={quakeStore as never}
      view={view as never}
      metadata={metadata as never}
      activeAgentId={() => entry?.ownerId ?? null}
      onInput={vi.fn()}
      onResize={vi.fn()}
      onContentReady={vi.fn()}
    />
  )))
  return { ...rendered, quakeStore }
}

const OPEN: QuakeEntry = { ownerId: 'a1', workerId: 'w1', workspaceId: 'ws1', terminalId: 'q1', open: true }
const CLOSED: QuakeEntry = { ...OPEN, open: false }

describe('quakeTerminalPanel', () => {
  // Lazy: a user who never presses the shortcut pays no xterm, no RPC and no
  // watch entry.
  it('renders nothing before the first open', () => {
    const { queryByTestId } = mount()
    expect(queryByTestId('quake-panel')).toBeNull()
  })

  it('shows the panel for an open entry', () => {
    const { getByTestId } = mount({ entry: OPEN })
    const panel = getByTestId('quake-panel')
    expect(panel.getAttribute('data-quake-open')).toBe('true')
    expect(panel.getAttribute('aria-hidden')).toBeNull()
  })

  // The panel stays in the DOM so the shell survives a toggle -- unmounting it
  // would dispose the xterm and throw away the buffer every time.
  it('keeps a closed panel mounted, but out of the reader and the tab order', () => {
    const { getByTestId } = mount({ entry: CLOSED })
    const panel = getByTestId('quake-panel')
    expect(panel.getAttribute('data-quake-open')).toBe('false')
    expect(panel.getAttribute('aria-hidden')).toBe('true')
    expect(panel.hasAttribute('inert')).toBe(true)
  })

  // A CSS transition needs two computed values, and the panel is CREATED by the
  // first open. Inserted already open it would jump into place, and only the
  // first open of a page load would behave differently from every later one --
  // which is the kind of defect nobody reproduces on purpose.
  it('inserts the panel closed and opens it in the same task, so the first open slides', () => {
    const flips: (string | null)[] = []
    const observer = new MutationObserver(() => {})
    observer.observe(document.body, {
      subtree: true,
      attributes: true,
      attributeOldValue: true,
      attributeFilter: ['data-quake-open'],
    })

    const { getByTestId } = mount({ entry: OPEN })

    // Records are collected synchronously; the observer's own callback is a
    // microtask and would run after the assertions.
    for (const record of observer.takeRecords())
      flips.push(record.oldValue)
    observer.disconnect()

    // Exactly one flip, and it LEAVES the closed state -- so the element was in
    // the document carrying `false` before it carried `true`.
    expect(flips).toEqual(['false'])
    expect(getByTestId('quake-panel').getAttribute('data-quake-open')).toBe('true')
  })

  // The hook `AppShell` reads to decide whether closing the panel should pull
  // the caret back to the composer.
  it('marks the panel so a focus restore can ask whether focus is inside it', () => {
    const { getByTestId } = mount({ entry: OPEN })
    expect(getByTestId('quake-panel').hasAttribute('data-quake-panel')).toBe(true)
  })

  it('passes the active owner terminal to the view, and marks it visible', () => {
    const { getByTestId } = mount({ entry: OPEN })
    const view = getByTestId('terminal-view')
    expect(view.getAttribute('data-active-terminal')).toBe('q1')
    expect(view.getAttribute('data-visible')).toBe('true')
  })

  it('holds the panel open for a background owner, so its shell keeps painting', () => {
    // No entry for the ACTIVE tab, but a companion still exists elsewhere.
    const { getByTestId } = mount({ detached: [{ id: 'q9', workerId: 'w1', workspaceId: 'ws1' }] })
    expect(getByTestId('quake-panel').getAttribute('data-quake-open')).toBe('false')
  })

  describe('geometry', () => {
    // One property drives ONE axis, and the orientation attribute picks which.
    it('states the orientation and the size for the clip to resolve', () => {
      const { getByTestId } = mount({ entry: OPEN })
      const clip = getByTestId('quake-panel').parentElement!
      expect(clip.getAttribute('data-quake-orientation')).toBe('top')
      expect(clip.style.getPropertyValue('--quake-size')).toBe('65%')
    })

    it('states the duration the slide runs for', () => {
      const { getByTestId } = mount({ entry: OPEN })
      const clip = getByTestId('quake-panel').parentElement!
      expect(clip.style.getPropertyValue('--quake-duration')).toBe('300ms')
    })

    // A percentage, because `color-mix` takes one directly.
    it('states the background opacity as a percentage', () => {
      const { getByTestId } = mount({ entry: OPEN })
      const clip = getByTestId('quake-panel').parentElement!
      expect(clip.style.getPropertyValue('--quake-opacity')).toBe('90%')
    })

    // The open state must settle on the stylesheet's `transform: none`, not on
    // an inline identity transform -- that would make the panel a containing
    // block for every fixed-position popover inside it.
    it('sets no inline transform, so the open state stays transform-free', () => {
      const { getByTestId } = mount({ entry: OPEN })
      expect(getByTestId('quake-panel').style.transform).toBe('')
    })

    it('repeats the orientation on the panel, which is what picks the slide axis', () => {
      const { getByTestId } = mount({ entry: CLOSED })
      expect(getByTestId('quake-panel').getAttribute('data-quake-orientation')).toBe('top')
    })
  })
})
