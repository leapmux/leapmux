import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { activeXterm, getTerminalRows, terminalXterm, waitForTerminalText } from './terminal'
import { startTestDeadline, WAIT_REPORT_MARGIN_MS } from './testDeadline'

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(window, '__getActiveTerminalRows')
  Reflect.deleteProperty(window, '__getActiveTerminalText')
})

/** A page whose `evaluate` runs the page function in this document, and whose `locator` returns its selector. */
const page = {
  evaluate: async <R>(body: () => R) => body(),
  locator: (selector: string) => selector,
} as unknown as Page

describe('activeXterm', () => {
  it('selects the xterm of each terminal that is the visible tab of its tile', () => {
    expect(activeXterm(page)).toBe('[data-terminal-id][data-active="true"] .xterm')
  })
})

describe('terminalXterm', () => {
  it('selects the xterm of one terminal by its ID', () => {
    expect(terminalXterm(page, 'term-1')).toBe('[data-terminal-id="term-1"] .xterm')
  })

  it('escapes a quote in the ID', () => {
    expect(terminalXterm(page, 'a"b')).toBe('[data-terminal-id="a\\"b"] .xterm')
  })
})

describe('getTerminalRows', () => {
  it('reads the row count from the hook of the active terminal', async () => {
    Reflect.set(window, '__getActiveTerminalRows', () => 42)
    await expect(getTerminalRows(page)).resolves.toBe(42)
  })

  it('reads 0 before xterm registers the hook', async () => {
    await expect(getTerminalRows(page)).resolves.toBe(0)
  })
})

describe('waitForTerminalText', () => {
  it('waits until the active terminal shows the text', async () => {
    const reads = ['$ ', '$ echo DONE', '$ echo DONE\nDONE']
    const getText = vi.fn(() => reads.shift() ?? '$ echo DONE\nDONE\n$ ')
    Reflect.set(window, '__getActiveTerminalText', getText)
    await waitForTerminalText(page, '\nDONE')
    expect(getText.mock.calls.length).toBeGreaterThanOrEqual(3)
  })

  it('fails with its own message before the test deadline when the text never shows', async () => {
    Reflect.set(window, '__getActiveTerminalText', () => '$ ')
    // A test that has run almost to its deadline leaves the wait the minimum of one millisecond.
    const end = startTestDeadline(Date.now() - 60_000, () => 60_000 + WAIT_REPORT_MARGIN_MS)
    try {
      await expect(waitForTerminalText(page, 'NEVER')).rejects.toThrow('the active terminal shows the text')
    }
    finally {
      end()
    }
  })

  it('refuses an empty text before it reads the terminal', async () => {
    const getText = vi.fn(() => '')
    Reflect.set(window, '__getActiveTerminalText', getText)
    await expect(waitForTerminalText(page, '')).rejects.toThrow('needs text')
    expect(getText).not.toHaveBeenCalled()
  })
})
