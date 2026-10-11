import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it } from 'vitest'
import { recordClicks } from './touch'

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
