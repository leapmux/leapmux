import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

droidTest('reports a permanent native model failure before and after reload', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.queue({
    error: { status: 401, code: 'invalid_api_key', message: 'The scripted model credential was rejected.' },
  })
  await sendMessage(page, modelScript.prompt('Reply to the scripted authentication failure.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const divider = page.locator('[data-testid="result-divider"]:visible').last()
  await expect(divider).toContainText('Turn failed')
  await page.reload()
  await expect(divider).toContainText('Turn failed')
})
