import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { chatScrollContainer, waitForAgentIdle } from './ui'

/** The label of the notice row the transcript draws after a compaction. */
export const COMPACTION_NOTICE_TEXT = 'Context compacted'
const COMPACTION_NOTICE_PATTERN = new RegExp(`(?:^|,)\\s*${COMPACTION_NOTICE_TEXT}(?:\\s|\\(|$)`)

/** Find a completed divider or Worker status. Assistant text cannot prove native compaction. */
export function compactionNoticeRows(page: Page): Locator {
  const transcript = chatScrollContainer(page)
  const dividers = transcript
    .locator('[data-testid="notification-divider"]:visible')
    .filter({ hasText: COMPACTION_NOTICE_PATTERN })
  const statuses = transcript
    .locator('[data-testid="message-bubble"][data-role="leapmux"]:visible')
    .getByText(COMPACTION_NOTICE_PATTERN)
  return dividers.or(statuses)
}

/** Find the first completed compaction notice. */
export function compactionNoticeRow(page: Page): Locator {
  return compactionNoticeRows(page).first()
}

/** Find the live compaction indicator, without its hidden measurement copy. */
export function compactionIndicator(page: Page): Locator {
  return page.locator('[data-testid="compaction-indicator"]:visible')
}

/** Require no live compaction before and after a reload. */
export async function expectNoLiveCompaction(page: Page): Promise<void> {
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForAgentIdle(page)
    }
    await expect(compactionIndicator(page)).toHaveCount(0)
  }
}

/** Assert the transcript draws the compaction notice. */
export async function expectCompactionNotice(page: Page): Promise<void> {
  await expect(compactionNoticeRow(page)).toBeVisible()
}

/**
 * Require the compaction notice, reload, and require it again from the stored transcript.
 * With `detail`, the notice row must also show that text both times, such as the trigger that the event states.
 * After the reload, the check waits for the idle agent, so a replay of the stored transcript has ended.
 */
export async function expectCompactionNoticeAfterReload(page: Page, options: { detail?: string, count?: number, divider?: boolean } = {}): Promise<void> {
  const count = options.count ?? 1
  if (!Number.isSafeInteger(count) || count < 0)
    throw new Error('The compaction notice count must be a nonnegative whole number.')
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForAgentIdle(page)
    }
    const rows = options.divider
      ? chatScrollContainer(page).locator('[data-testid="notification-divider"]:visible').filter({ hasText: COMPACTION_NOTICE_PATTERN })
      : compactionNoticeRows(page)
    await expect(rows).toHaveCount(count)
    if (options.detail !== undefined)
      await expect(rows.first()).toContainText(options.detail)
  }
}
