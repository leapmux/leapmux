import type { Locator, Page } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { compactedLabel } from '../../../src/components/chat/notificationEntries'
import { COMPACTION_NOTICE_TEXT, compactionNoticeRow, expectCompactionNoticeAfterReload } from './compaction'
import { CHAT_SCROLL_CONTAINER } from './ui'

/** The page and assertion events of one notice check, in order. */
const events = vi.hoisted(() => [] as string[])

vi.mock('./ui', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./ui')>()
  return {
    ...actual,
    waitForAgentIdle: async () => {
      events.push('idle')
    },
  }
})

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (value: unknown) => {
      if (typeof value === 'object' && value !== null && 'fakeNotice' in value) {
        return {
          toBeVisible: async () => events.push('notice'),
          toContainText: async (text: string) => events.push(`detail ${text}`),
        }
      }
      return expect(value)
    },
  }
})

beforeEach(() => {
  events.length = 0
})

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

describe('expectCompactionNoticeAfterReload', () => {
  /** A page whose notice row is a fake locator, and whose reload is an event. */
  function noticePage(): Page {
    const notice: Locator = Object.assign({} as Locator, { fakeNotice: true })
    const link: Locator = Object.assign({} as Locator, {
      locator: () => link,
      filter: () => link,
      first: () => notice,
    })
    return Object.assign({} as Page, {
      locator: () => link,
      reload: async () => {
        events.push('reload')
        return null
      },
    })
  }

  it('requires the notice, reloads, waits for the idle agent, and requires the notice again', async () => {
    await expectCompactionNoticeAfterReload(noticePage())
    expect(events).toEqual(['notice', 'reload', 'idle', 'notice'])
  })

  it('requires the detail of the notice before and after the reload', async () => {
    await expectCompactionNoticeAfterReload(noticePage(), { detail: 'manual' })
    expect(events).toEqual(['notice', 'detail manual', 'reload', 'idle', 'notice', 'detail manual'])
  })
})
