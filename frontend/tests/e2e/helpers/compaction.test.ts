import type { Locator, Page } from '@playwright/test'
import { describe, expect, it } from 'vitest'
import { compactedLabel } from '../../../src/components/chat/notificationEntries'
import { COMPACTION_NOTICE_TEXT, compactionNoticeRow } from './compaction'
import { CHAT_SCROLL_CONTAINER } from './ui'

describe('COMPACTION_NOTICE_TEXT', () => {
  // The label is app-owned. If the app rewords the notice and this constant
  // does not follow, the e2e looks for a row that never renders.
  it('is the label the app draws for a compaction', () => {
    expect(compactedLabel(undefined)).toBe(COMPACTION_NOTICE_TEXT)
  })

  it('stays the prefix of a notice that carries a token detail', () => {
    const detailed = compactedLabel({ trigger: 'manual', pre: 12040, post: 3000 })
    expect(detailed.startsWith(COMPACTION_NOTICE_TEXT)).toBe(true)
  })
})

describe('compactionNoticeRow', () => {
  /** Record each step of the locator chain that the helper builds. */
  function chainPage(): { page: Page, steps: unknown[] } {
    const steps: unknown[] = []
    const link: Locator = Object.assign({} as Locator, {
      locator: (selector: string) => {
        steps.push(selector)
        return link
      },
      filter: (options: { hasText?: unknown }) => {
        steps.push(options.hasText)
        return link
      },
      first: () => {
        steps.push('first')
        return link
      },
    })
    const page = Object.assign({} as Page, {
      locator: (selector: string) => {
        steps.push(selector)
        return link
      },
    })
    return { page, steps }
  }

  it('reads the visible notice rows inside the visible chat, which excludes the hidden premeasure copy', () => {
    const { page, steps } = chainPage()
    compactionNoticeRow(page)
    expect(steps.slice(0, 2)).toEqual([`${CHAT_SCROLL_CONTAINER}:visible`, '[data-testid="notification-divider"]:visible'])
    expect(steps.at(-1)).toBe('first')
  })

  it('accepts the label alone or with a detail, and refuses a row that only starts with the same words', () => {
    const { page, steps } = chainPage()
    compactionNoticeRow(page)
    const pattern = steps[2]
    if (!(pattern instanceof RegExp))
      throw new Error('The notice row filters on no pattern.')
    expect(pattern.test(compactedLabel(undefined))).toBe(true)
    expect(pattern.test(compactedLabel({ trigger: 'manual', pre: 12040, post: 3000 }))).toBe(true)
    expect(pattern.test(`${COMPACTION_NOTICE_TEXT}ly`)).toBe(false)
    expect(pattern.test(`Not ${COMPACTION_NOTICE_TEXT}`)).toBe(false)
  })
})
