import type { Locator } from '@playwright/test'
import { expect } from '@playwright/test'

/**
 * Expand the exact result through its sibling toolbar, including after a fresh page load.
 * `toolCallRow` in `./ui` locates the result row of a tool call.
 */
export async function expandNativeResultView(result: Locator): Promise<void> {
  const view = result.locator('..')
  await view.hover()
  await view.getByRole('button', { name: 'Expand', exact: true }).click()
  await expect(view.getByRole('button', { name: 'Collapse', exact: true })).toBeVisible()
}
