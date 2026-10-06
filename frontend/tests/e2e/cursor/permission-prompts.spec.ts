import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { cursorWebFetchPermissionToolCall } from '../helpers/providerToolCalls'
import { answerControl, assistantBubbles, expectNoControlBanner, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

// The Cursor CLI answers its own Run stream with the result of the web fetch, and the mock Cursor service ends the
// turn with that result. So each turn queues the tool call alone, and the turn needs no second model answer.

cursorTest('forwards a native web-fetch permission decision', async ({ native }) => {
  const { page, modelScript } = native
  const start = await modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall('cursor-fetch', 'https://example.invalid/cursor-probe')] })
  await sendMessage(page, modelScript.prompt('Ask permission to fetch the scripted URL.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('https://example.invalid/cursor-probe')
  await answerControl(page, 'allow')

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

cursorTest('forwards a native web-fetch Deny decision', async ({ native }) => {
  const { page, modelScript } = native
  // The CLI skips the question for a domain on its allowlist, and "Allow always" adds one.
  // A separate reserved domain keeps this question independent of any approval of example.invalid.
  const url = 'https://cursor-denied-probe.invalid/native-web-fetch'
  const start = await modelScript.queue({ toolCalls: [cursorWebFetchPermissionToolCall('cursor-denied-fetch', url)] })
  await sendMessage(page, modelScript.prompt('Ask permission to fetch the scripted private URL.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText(url)
  await answerControl(page, 'deny')

  await waitForAgentIdle(page)
  await expectNoControlBanner(page)
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_REFUSAL }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch approved' })).toHaveCount(0)
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: CURSOR_WEB_FETCH_REFUSAL }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor web fetch approved' })).toHaveCount(0)
})
