import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { chatScrollContainer, waitForAgentIdle } from './ui'

/**
 * The compaction notice row.
 *
 * A `/compact` turn compacts the context and the transcript draws one notice
 * row for it. The row carries the label plus a detail in parentheses
 * ("Context compacted (manual, 12.0k → 3.0k)"). The selector takes the first
 * visible notification divider whose text starts with the label.
 */

/** The label of the notice row the transcript draws after a compaction. */
export const COMPACTION_NOTICE_TEXT = 'Context compacted'
const COMPACTION_NOTICE_PATTERN = new RegExp(`^\\s*${COMPACTION_NOTICE_TEXT}(?:\\s|\\(|$)`)

/** The first notice row in the visible transcript. */
export function compactionNoticeRow(page: Page): Locator {
  return chatScrollContainer(page)
    .locator('[data-testid="notification-divider"]:visible')
    .filter({ hasText: COMPACTION_NOTICE_PATTERN })
    .first()
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
