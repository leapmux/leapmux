import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { chatScrollContainer, waitForAgentIdle } from './ui'

/** The label of the notice row the transcript draws after a compaction. */
export const COMPACTION_NOTICE_TEXT = 'Context compacted'
const COMPACTION_NOTICE_PATTERN = new RegExp(`(?:^|,)\\s*${COMPACTION_NOTICE_TEXT}(?:\\s|\\(|$)`)

/** Find a completed divider or Worker status. Assistant text cannot prove native compaction. */
export function compactionNoticeRow(page: Page): Locator {
  const transcript = chatScrollContainer(page)
  const dividers = transcript
    .locator('[data-testid="notification-divider"]:visible')
    .filter({ hasText: COMPACTION_NOTICE_PATTERN })
  const statuses = transcript
    .locator('[data-testid="message-bubble"][data-role="leapmux"]:visible')
    .getByText(COMPACTION_NOTICE_PATTERN)
  return dividers.or(statuses).first()
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
export async function expectCompactionNoticeAfterReload(page: Page, options: { detail?: string } = {}): Promise<void> {
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForAgentIdle(page)
    }
    await expectCompactionNotice(page)
    if (options.detail !== undefined)
      await expect(compactionNoticeRow(page)).toContainText(options.detail)
  }
}
