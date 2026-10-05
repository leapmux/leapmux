import type { Locator, Page } from '@playwright/test'
import { expect } from '@playwright/test'

/**
 * Locate the visible result row of one native tool call.
 * `ChatView` keeps a hidden premeasure copy of each row that has the same test IDs, so only the visible copy matches.
 * The call ID can hold any character, so the function escapes it for a quoted attribute value.
 * It puts a backslash before a quote and a backslash, and it writes a line break as a hex escape, which a quoted CSS string requires.
 */
export function nativeResultBubble(page: Page, callId: string): Locator {
  const quoted = callId
    .replace(/["\\]/g, '\\$&')
    .replace(/[\n\r\f]/g, lineBreak => `\\${lineBreak.codePointAt(0)!.toString(16)} `)
  return page.locator(`[data-testid="message-bubble"][data-tool-call-id="${quoted}"][data-tool-row-role="result"]:visible`)
}

/** Expand the exact result through its sibling toolbar, including after a fresh page load. */
export async function expandNativeResultView(result: Locator): Promise<void> {
  const view = result.locator('..')
  await view.hover()
  await view.getByRole('button', { name: 'Expand', exact: true }).click()
  await expect(view.getByRole('button', { name: 'Collapse', exact: true })).toBeVisible()
}
