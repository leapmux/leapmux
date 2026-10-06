import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { cursorWebFetchPermissionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

cursorTest('forwards a native web-fetch permission decision', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  void authenticatedCursorWorkspace
  await modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall('cursor-fetch', 'https://example.invalid/cursor-probe')] })
  await sendMessage(page, modelScript.prompt('Ask permission to fetch the scripted URL.'))
  await modelScript.waitForSteps()
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('https://example.invalid/cursor-probe')
  await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch approved' }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch approved' }).first()).toBeVisible()
})

/**
 * The Deny button selects the ACP option reject-once.
 * The Cursor CLI then answers the web-fetch query with its rejected branch and the reason "User rejected".
 * The mock Run service writes this text only for an answer that carries the ID of the pending query.
 * An answer with any other ID ends the stream, and no text arrives.
 */
const CURSOR_WEB_FETCH_REFUSAL = 'Cursor web fetch rejected: User rejected'

cursorTest('forwards a native web-fetch Deny decision', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  void authenticatedCursorWorkspace
  // The CLI skips the question for a domain on its allowlist, and "Allow always" adds one.
  // A separate reserved domain keeps this question independent of any approval of example.invalid.
  const url = 'https://cursor-denied-probe.invalid/native-web-fetch'
  await modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall('cursor-denied-fetch', url)] })
  await sendMessage(page, modelScript.prompt('Ask permission to fetch the scripted private URL.'))
  await modelScript.waitForSteps()
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText(url)
  await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

  await waitForAgentIdle(page)
  await expect(banner).toHaveCount(0)
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_REFUSAL }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch approved' })).toHaveCount(0)
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_REFUSAL }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch approved' })).toHaveCount(0)
})
