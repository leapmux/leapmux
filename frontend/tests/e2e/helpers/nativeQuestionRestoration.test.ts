import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { describe, expect, it, vi } from 'vitest'
import { restoreQuestionSelection, restoreQuestionTextDraft } from './nativeQuestionRestoration'

describe('native question restoration validation', () => {
  it.each([
    -1,
    0.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
    Number.MAX_SAFE_INTEGER + 1,
  ])('rejects page %s before reading the browser', async (pageIndex) => {
    const read = vi.fn(() => {
      throw new Error('The browser must not be read.')
    })
    const context = {
      get page() { return read() },
    } as unknown as ManagedNativeScenarioContext
    await expect(restoreQuestionSelection(context, {} as Locator, 'Green', pageIndex)).rejects.toThrow('nonnegative safe integer')
    await expect(restoreQuestionTextDraft(context, 'Answer', pageIndex)).rejects.toThrow('nonnegative safe integer')
    expect(read).not.toHaveBeenCalled()
  })
})
