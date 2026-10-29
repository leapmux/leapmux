import type { Locator, Page } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { compactedLabel } from '../../../src/components/chat/notificationEntries'
import { COMPACTION_NOTICE_TEXT, compactionNoticeRow, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from './compaction'
import { CHAT_SCROLL_CONTAINER } from './ui'

/** The page and assertion events of one notice check, in order. */
const events = vi.hoisted(() => [] as string[])
const counts = vi.hoisted(() => ({ value: 1, afterReload: 1 }))

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
          toHaveCount: async (count: number) => {
            events.push(`count ${count}`)
            expect(counts.value).toBe(count)
          },
          toContainText: async (text: string) => events.push(`detail ${text}`),
        }
      }
      return expect(value)
    },
  }
})

beforeEach(() => {
  events.length = 0
  counts.value = 1
  counts.afterReload = 1
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
      getByText: (text: string | RegExp) => {
        steps.push(text)
        return link
      },
      or: () => {
        steps.push('or')
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
    expect(steps).toContain('[data-testid="message-bubble"][data-role="leapmux"]:visible')
    expect(steps).toContain('or')
    expect(steps.at(-1)).toBe('first')
  })

  it('accepts the label alone or with a detail, and refuses a row that only starts with the same words', () => {
    const { page, steps } = chainPage()
    compactionNoticeRow(page)
    const pattern = steps[2]
    if (!(pattern instanceof RegExp))
      throw new TypeError('The notice row filters on no pattern.')
    expect(pattern.test(compactedLabel(undefined))).toBe(true)
    expect(pattern.test(compactedLabel({ trigger: 'manual', pre: 12040, post: 3000 }))).toBe(true)
    expect(pattern.test(`${COMPACTION_NOTICE_TEXT}ly`)).toBe(false)
    expect(pattern.test(`Not ${COMPACTION_NOTICE_TEXT}`)).toBe(false)
    expect(pattern.test('Compacting the context')).toBe(false)
    expect(pattern.test('Mode changed, Context compacted from 1000 to 200 tokens')).toBe(true)
  })
})

describe('expectCompactionNoticeAfterReload', () => {
  /** A page whose notice row is a fake locator, and whose reload is an event. */
  function noticePage(): Page {
    const link: Locator = Object.assign({} as Locator, {
      fakeNotice: true,
      locator: () => link,
      filter: () => link,
      getByText: () => link,
      or: () => link,
      first: () => link,
    })
    return Object.assign({} as Page, {
      locator: () => link,
      reload: async () => {
        events.push('reload')
        counts.value = counts.afterReload
        return null
      },
    })
  }

  it('requires the notice, reloads, waits for the idle agent, and requires the notice again', async () => {
    await expectCompactionNoticeAfterReload(noticePage())
    expect(events).toEqual(['count 1', 'reload', 'idle', 'count 1'])
  })

  it('requires the detail of the notice before and after the reload', async () => {
    await expectCompactionNoticeAfterReload(noticePage(), { detail: 'manual' })
    expect(events).toEqual([
      'count 1',
      'detail manual',
      'reload',
      'idle',
      'count 1',
      'detail manual',
    ])
  })

  it.each(['live', 'stored'])('refuses an extra notice in the %s transcript', async (phase) => {
    if (phase === 'live')
      counts.value = 2
    else
      counts.afterReload = 2
    await expect(expectCompactionNoticeAfterReload(noticePage())).rejects.toThrow('expected 2 to be 1')
  })

  it('accepts an explicit count', async () => {
    counts.value = 2
    counts.afterReload = 2
    await expectCompactionNoticeAfterReload(noticePage(), { count: 2 })
    expect(events).toEqual(['count 2', 'reload', 'idle', 'count 2'])
  })

  it.each([
    -1,
    0.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
    Number.MAX_SAFE_INTEGER + 1,
  ])('refuses an invalid count: %s', async (count) => {
    await expect(expectCompactionNoticeAfterReload(noticePage(), { count })).rejects.toThrow('nonnegative whole number')
    expect(events).toEqual([])
  })

  it('accepts only dividers when the caller requires a divider', async () => {
    const page = noticePage()
    const link = page.locator('probe')
    const statuses = vi.spyOn(link, 'getByText')
    await expectCompactionNoticeAfterReload(page, { divider: true })
    expect(statuses).not.toHaveBeenCalled()
  })

  it('checks the visible live compaction before and after a reload', async () => {
    counts.value = 0
    counts.afterReload = 0
    const page = noticePage()
    const locate = vi.spyOn(page, 'locator')
    await expectNoLiveCompaction(page)
    expect(locate.mock.calls).toEqual([
      ['[data-testid="compaction-indicator"]:visible'],
      ['[data-testid="compaction-indicator"]:visible'],
    ])
    expect(events).toEqual(['count 0', 'reload', 'idle', 'count 0'])
  })
})
