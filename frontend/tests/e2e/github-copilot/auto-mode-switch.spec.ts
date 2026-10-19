import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { controlActions, expectNoControlBanner, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

copilotTest('ends the turn after the reader declines the native Auto switch', async ({ native }) => {
  const { page, modelScript } = native
  // copilot_probe2.py scenario R uses this native error code to request an Auto switch.
  const start = await modelScript.queue(...Array.from({ length: 6 }, () => ({
    error: { status: 429, code: 'user_model_rate_limited', message: 'probe rate limit' },
  })))
  await sendMessage(page, modelScript.prompt('Run the scripted model rate limit.'))
  await modelScript.waitForSteps(start + 6)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Copilot asks to switch to Auto')
  await controlActions(page).getByRole('button', { name: 'Decline', exact: true }).click()
  await waitForAgentIdle(page)
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  await expectNoControlBanner(page)
  await page.reload()
  await waitForAgentIdle(page)
  await expectNoControlBanner(page)
})
