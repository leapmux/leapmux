import { expect } from '@playwright/test'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { assistantBubbles, bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('keeps native thinking separate from the answer and restores it after reload', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
  void authenticatedAmpWorkspace
  const reasoning = 'NATIVEAMPTHINKING the two terms need one sum.'
  const answer = 'NATIVEAMPANSWER the sum is complete.'
  await modelScript.queue({ reasoning, text: answer })
  await sendMessage(page, modelScript.prompt('Reply once with the native reasoning channel.'))
  const status = await modelScript.waitForSteps()
  expect(status.requests).toHaveLength(1)
  await waitForAgentIdle(page)
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  }
})
