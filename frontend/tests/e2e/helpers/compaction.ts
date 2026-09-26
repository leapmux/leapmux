import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { CHAT_SCROLL_CONTAINER } from './ui'

/**
 * The compaction notice row.
 *
 * A `/compact` turn compacts the context and the transcript draws one notice
 * row for it. The row carries the label plus a detail in parentheses
 * ("Context compacted (manual, 12.0k → 3.0k)"), so the assertions match the
 * label as a substring and take the first row.
 */

/** The label of the notice row the transcript draws after a compaction. */
export const COMPACTION_NOTICE_TEXT = 'Context compacted'

/**
 * The selector of the chat rows a notice can appear in.
 *
 * Scoped to `:visible`: ChatView keeps a hidden premeasure copy of every
 * unmeasured row, and a bare container locator matches it as well.
 */
export function compactionChatSelector(): string {
  return `${CHAT_SCROLL_CONTAINER}:visible`
}

/** The first notice row in the visible transcript. */
export function compactionNoticeRow(page: Page): Locator {
  return page.locator(compactionChatSelector()).filter({ hasText: COMPACTION_NOTICE_TEXT }).first()
}

/** Assert the transcript draws the compaction notice. */
export async function expectCompactionNotice(page: Page): Promise<void> {
  await expect(compactionNoticeRow(page)).toBeVisible()
}
