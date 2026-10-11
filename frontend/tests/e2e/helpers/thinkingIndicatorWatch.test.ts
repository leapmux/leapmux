import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it } from 'vitest'
import { installThinkingIndicatorWatch, thinkingIndicatorShownDuring } from './thinkingIndicatorWatch'

/** Add an indicator with the inline style that ThinkingIndicator sets. */
function indicator(display: 'grid' | 'none', rows: '0fr' | '1fr'): HTMLElement {
  const element = document.createElement('div')
  element.dataset.testid = 'thinking-indicator'
  element.style.display = display
  element.style.gridTemplateRows = rows
  document.body.append(element)
  return element
}

/** Let the MutationObserver callbacks of the last change run. */
async function settle(): Promise<void> {
  await new Promise(resolve => setTimeout(resolve, 0))
}

/** A page whose `evaluate` runs the function in this DOM, as the browser does. */
const page = { evaluate: async <T>(run: () => T) => run() } as unknown as Pick<Page, 'evaluate'>

afterEach(() => {
  window.__thinkingIndicatorWatch?.stop()
  delete window.__thinkingIndicatorWatch
  document.body.replaceChildren()
})

describe('installThinkingIndicatorWatch', () => {
  it('counts an expanded indicator that is already shown at install', () => {
    indicator('grid', '1fr')
    installThinkingIndicatorWatch()
    expect(window.__thinkingIndicatorWatch?.shown).toBe(true)
  })

  it.each([
    { display: 'none', rows: '0fr' },
    { display: 'none', rows: '1fr' },
    { display: 'grid', rows: '0fr' },
  ] as const)('does not count an indicator with display $display and rows $rows', async ({ display, rows }) => {
    installThinkingIndicatorWatch()
    indicator(display, rows)
    await settle()
    expect(window.__thinkingIndicatorWatch?.shown).toBe(false)
  })

  it('records a later change of the style to the expanded state, and keeps it after the collapse', async () => {
    const element = indicator('grid', '0fr')
    installThinkingIndicatorWatch()
    expect(window.__thinkingIndicatorWatch?.shown).toBe(false)
    element.style.gridTemplateRows = '1fr'
    await settle()
    element.style.gridTemplateRows = '0fr'
    element.style.display = 'none'
    await settle()
    expect(window.__thinkingIndicatorWatch?.shown).toBe(true)
  })

  it('records a shown indicator in any ChatView, not only the first one', async () => {
    indicator('none', '0fr')
    const second = indicator('grid', '0fr')
    installThinkingIndicatorWatch()
    second.style.gridTemplateRows = '1fr'
    await settle()
    expect(window.__thinkingIndicatorWatch?.shown).toBe(true)
  })
})

describe('thinkingIndicatorShownDuring', () => {
  it('returns true for an indicator that shows during the operation', async () => {
    const element = indicator('none', '0fr')
    const shown = await thinkingIndicatorShownDuring(page, async () => {
      element.style.display = 'grid'
      element.style.gridTemplateRows = '1fr'
      await settle()
      element.style.display = 'none'
      await settle()
    })
    expect(shown).toBe(true)
    expect(window.__thinkingIndicatorWatch).toBeUndefined()
  })

  it('returns false when the indicator stays hidden, and ignores a change after the operation', async () => {
    const element = indicator('none', '0fr')
    const shown = await thinkingIndicatorShownDuring(page, settle)
    element.style.display = 'grid'
    element.style.gridTemplateRows = '1fr'
    await settle()
    expect(shown).toBe(false)
  })

  it('stops the watch after a failed operation, and keeps the error of the operation', async () => {
    await expect(thinkingIndicatorShownDuring(page, async () => {
      throw new Error('the turn failed')
    })).rejects.toThrow('the turn failed')
    expect(window.__thinkingIndicatorWatch).toBeUndefined()
  })

  it('fails when the watch left the page during the operation, as a reload does', async () => {
    await expect(thinkingIndicatorShownDuring(page, async () => {
      window.__thinkingIndicatorWatch?.stop()
      delete window.__thinkingIndicatorWatch
    })).rejects.toThrow('not in the page')
  })
})
