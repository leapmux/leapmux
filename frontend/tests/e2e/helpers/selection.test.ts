import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it } from 'vitest'
import { selectedText } from './selection'

/** A page whose `evaluate` runs the page function in this document, as Playwright does in the browser. */
const page = { evaluate: async <R>(body: () => R) => body() } as unknown as Page

afterEach(() => {
  window.getSelection()?.removeAllRanges()
  document.body.replaceChildren()
})

/** Select the characters `[start, end)` of a new text node, and return that node. */
function select(text: string, start: number, end: number): Text {
  const paragraph = document.createElement('p')
  const node = document.createTextNode(text)
  paragraph.append(node)
  document.body.append(paragraph)
  const range = document.createRange()
  range.setStart(node, start)
  range.setEnd(node, end)
  const selection = window.getSelection()!
  selection.removeAllRanges()
  selection.addRange(range)
  return node
}

describe('selectedText', () => {
  it('reads the text of the selected range', async () => {
    select('the quick brown fox', 10, 15)
    await expect(selectedText(page)).resolves.toBe('brown')
  })

  it('reads a collapsed selection as the empty string', async () => {
    select('the quick brown fox', 4, 4)
    await expect(selectedText(page)).resolves.toBe('')
  })

  it('reads no selection as the empty string', async () => {
    window.getSelection()?.removeAllRanges()
    await expect(selectedText(page)).resolves.toBe('')
  })
})
