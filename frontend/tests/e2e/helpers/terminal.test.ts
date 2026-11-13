import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { activeXterm, clickTerminalText, getTerminalRows, terminalXterm, waitForTerminalReady, waitForTerminalText } from './terminal'
import { startTestDeadline, WAIT_REPORT_MARGIN_MS } from './testDeadline'

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(window, '__getActiveTerminalRows')
  Reflect.deleteProperty(window, '__getActiveTerminalText')
  Reflect.deleteProperty(window, '__activeTerminalPointAt')
})

/** A page whose `evaluate` runs the page function in this document, and whose `locator` returns its selector. */
const page = {
  evaluate: async <R>(body: () => R) => body(),
  locator: (selector: string) => selector,
} as unknown as Page

/** A page whose reads never end, so that no attempt of a wait ends before the wait's limit. */
const stalledPage = {
  evaluate: () => new Promise<never>(() => {}),
  locator: (selector: string) => selector,
} as unknown as Page

/**
 * Run `body` in a test that has run almost to its deadline, which leaves each wait the minimum of one millisecond.
 * An attempt of the wait cannot end in that time when its read never ends, and may not end when the machine is busy.
 */
async function nearTheTestDeadline(body: () => Promise<void>): Promise<void> {
  const end = startTestDeadline(Date.now() - 60_000, () => 60_000 + WAIT_REPORT_MARGIN_MS)
  try {
    await body()
  }
  finally {
    end()
  }
}

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
    await nearTheTestDeadline(async () => {
      await expect(waitForTerminalText(page, 'NEVER')).rejects.toThrow('the active terminal shows the text "NEVER"')
    })
  })

  it('fails with its own message when no read of the terminal ends before the test deadline', async () => {
    await nearTheTestDeadline(async () => {
      await expect(waitForTerminalText(stalledPage, 'NEVER')).rejects.toThrow('the active terminal shows the text "NEVER"')
    })
  })

  it('refuses an empty text before it reads the terminal', async () => {
    const getText = vi.fn(() => '')
    Reflect.set(window, '__getActiveTerminalText', getText)
    await expect(waitForTerminalText(page, '')).rejects.toThrow('needs text')
    expect(getText).not.toHaveBeenCalled()
  })
})

describe('waitForTerminalReady', () => {
  it('types the marker again until the shell echoes it whole, and never types the marker itself', async () => {
    const typed: string[] = []
    let output = '$ '
    const terminalPage = {
      evaluate: async <R>(body: () => R) => body(),
      keyboard: {
        type: async (command: string) => {
          typed.push(command)
        },
        press: async (key: string) => {
          const command = typed.at(-1)
          if (key !== 'Enter' || command === undefined)
            return
          // The first line loses its leading characters, as a line typed before the shell reads input does.
          const line = typed.length === 1
            ? command.slice(2)
            : command
          const echoed = line.startsWith('echo ')
            ? line.slice('echo '.length).replace('""', '')
            : `sh: ${line.split(' ')[0]}: not found`
          output += `${line}\n${echoed}\n$ `
        },
      },
    } as unknown as Page
    Reflect.set(window, '__getActiveTerminalText', () => output)

    await waitForTerminalReady(terminalPage)

    expect(typed).toHaveLength(2)
    const marker = typed[1]!.slice('echo '.length).replace('""', '')
    expect(marker).toMatch(/^RDY[0-9A-Z]+$/)
    // The shell joins the quoted halves, so only its output holds the whole marker.
    expect(typed[1]).not.toContain(marker)
  })

  it('fails with its own message when no attempt ends before the test deadline', async () => {
    await nearTheTestDeadline(async () => {
      await expect(waitForTerminalReady(stalledPage)).rejects.toThrow('the shell of the active terminal echoes a typed marker')
    })
  })
})

describe('clickTerminalText', () => {
  it('hovers another cell of the column, then clicks the cell that shows the text', async () => {
    const asked: string[] = []
    Reflect.set(window, '__activeTerminalPointAt', (text: string) => {
      asked.push(text)
      return { x: 10, y: 20, awayY: 40 }
    })
    const moves: string[] = []
    const mousePage = {
      evaluate: async <R, A>(body: (arg: A) => R, arg: A) => body(arg),
      mouse: {
        move: async (x: number, y: number) => {
          moves.push(`move ${x},${y}`)
        },
        click: async (x: number, y: number) => {
          moves.push(`click ${x},${y}`)
        },
      },
    } as unknown as Page

    await clickTerminalText(mousePage, 'https://example.test')

    expect(asked).toEqual(['https://example.test'])
    expect(moves).toEqual(['move 10,40', 'click 10,20'])
  })

  it('fails with its own message before the test deadline when no cell shows the text', async () => {
    Reflect.set(window, '__activeTerminalPointAt', () => null)
    const evaluatingPage = {
      evaluate: async <R, A>(body: (arg: A) => R, arg: A) => body(arg),
    } as unknown as Page
    await nearTheTestDeadline(async () => {
      await expect(clickTerminalText(evaluatingPage, 'https://example.test')).rejects.toThrow('no terminal cell shows https://example.test')
    })
  })

  it('fails with its own message when no read of the terminal ends before the test deadline', async () => {
    await nearTheTestDeadline(async () => {
      await expect(clickTerminalText(stalledPage, 'https://example.test')).rejects.toThrow('no terminal cell shows https://example.test')
    })
  })
})
