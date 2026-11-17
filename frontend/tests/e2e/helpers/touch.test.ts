import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { recordClicks, touchFling } from './touch'

/** A page whose `evaluate` runs the function in this test's own window, as the browser runs it in the page. */
function windowPage(): Pick<Page, 'evaluate'> {
  return {
    evaluate: (async (read: (arg: unknown) => unknown, arg: unknown) => read(arg)) as Page['evaluate'],
  }
}

/** Select the whole text of a new paragraph. */
function selectParagraph(text: string): void {
  const paragraph = document.createElement('p')
  paragraph.textContent = text
  document.body.append(paragraph)
  const range = document.createRange()
  range.selectNodeContents(paragraph)
  const selection = window.getSelection()!
  selection.removeAllRanges()
  selection.addRange(range)
}

afterEach(() => {
  window.getSelection()?.removeAllRanges()
  document.body.replaceChildren()
})

describe('recordClicks', () => {
  it('records each later click with the selection text at that moment, oldest first', async () => {
    window.dispatchEvent(new MouseEvent('click'))
    const clicks = await recordClicks(windowPage())
    expect(await clicks.selections()).toEqual([])
    window.dispatchEvent(new MouseEvent('click'))
    selectParagraph('jumps')
    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(await clicks.selections()).toEqual(['', 'jumps'])
  })

  it('records a click whose own handler stops it, because the record listens in the capture phase', async () => {
    const button = document.createElement('button')
    button.addEventListener('click', event => event.stopPropagation())
    document.body.append(button)
    const clicks = await recordClicks(windowPage())
    button.click()
    expect(await clicks.selections()).toEqual([''])
  })

  it('keeps two records apart', async () => {
    const first = await recordClicks(windowPage())
    window.dispatchEvent(new MouseEvent('click'))
    const second = await recordClicks(windowPage())
    window.dispatchEvent(new MouseEvent('click'))
    expect(await first.selections()).toEqual(['', ''])
    expect(await second.selections()).toEqual([''])
  })

  it('fails when the record is gone, as after a navigation', async () => {
    const page = windowPage()
    const clicks = await recordClicks(page)
    const names = Object.getOwnPropertyNames(window).filter(name => name.startsWith('__e2eRecordedClicks_'))
    for (const name of names)
      Reflect.deleteProperty(window, name)
    await expect(clicks.selections()).rejects.toThrow('a navigation replaced the page')
  })
})

describe('touchFling', () => {
  /** A page whose CDP session records each command and its detach, and fails a command with `failure`. */
  function cdpPage(failure?: Error) {
    const log: unknown[] = []
    const session = {
      send: vi.fn(async (method: string, params: unknown) => {
        log.push({ method, params })
        if (failure !== undefined)
          throw failure
      }),
      detach: vi.fn(async () => {
        log.push('detach')
      }),
    }
    const newCDPSession = vi.fn(async () => session)
    const page = {
      context: () => ({ newCDPSession }),
    } as unknown as Page
    return { page, log, newCDPSession }
  }

  it('sends one touch scroll gesture that keeps its momentum, then detaches the session', async () => {
    const { page, log } = cdpPage()
    await touchFling(page, {
      from: { x: 200, y: 300 },
      distanceY: 500,
      speed: 2500,
    })
    expect(log).toEqual([
      {
        method: 'Input.synthesizeScrollGesture',
        params: {
          x: 200,
          y: 300,
          xDistance: 0,
          yDistance: 500,
          speed: 2500,
          gestureSourceType: 'touch',
          preventFling: false,
        },
      },
      'detach',
    ])
  })

  it('detaches the session when the gesture fails', async () => {
    const { page, log } = cdpPage(new Error('no touch'))
    await expect(touchFling(page, {
      from: { x: 0, y: 0 },
      distanceY: -100,
      speed: 800,
    })).rejects.toThrow('no touch')
    expect(log.at(-1)).toBe('detach')
  })

  it.each([
    { distanceY: 0, speed: 800 },
    { distanceY: Number.NaN, speed: 800 },
    { distanceY: 100, speed: 0 },
    { distanceY: 100, speed: -5 },
    { distanceY: 100, speed: Number.POSITIVE_INFINITY },
  ])('refuses a distance of $distanceY at a speed of $speed before it opens a session', async ({ distanceY, speed }) => {
    const { page, newCDPSession } = cdpPage()
    await expect(touchFling(page, {
      from: { x: 0, y: 0 },
      distanceY,
      speed,
    })).rejects.toThrow('A touch fling needs')
    expect(newCDPSession).not.toHaveBeenCalled()
  })
})
