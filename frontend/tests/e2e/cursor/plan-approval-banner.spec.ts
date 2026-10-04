import { expect } from '@playwright/test'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { cursorCreatePlanToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('approves a native create-plan request', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  void authenticatedCursorWorkspace
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await modelScript.queue({ toolCalls: [cursorCreatePlanToolCall(
    'cursor-plan',
    'Review changes',
    'Review without edits.',
    '# Plan\n\n1. Inspect the files.',
  )] })
  await sendMessage(page, modelScript.prompt('Write a plan and ask for approval.'))
  await modelScript.waitForSteps()
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('Review changes')
  await expect(banner).toContainText('Inspect the files')
  await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor plan accepted' }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor plan accepted' }).first()).toBeVisible()
})
