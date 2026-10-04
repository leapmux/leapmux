import { expect } from '@playwright/test'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { cursorWebFetchPermissionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

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
