import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { nativeTextStep } from '../helpers/nativeScenario'
import { rateLimitWindowLabel } from '../helpers/rateLimit'
import { assistantBubbles, openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

fastAgentTest('reports no quota window after consuming actual native quota headers', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  const rateLimits = { type: 'five_hour', status: 'allowed_warning', utilization: 0.92, resetsAt: Math.floor(Date.now() / 1000) + 3600 }
  const start = (await modelScript.status()).stepCount
  const answer = 'The native quota header probe completed.'
  await modelScript.queue({ ...nativeTextStep(context, answer), rateLimits })
  await sendMessage(page, modelScript.prompt('Complete after the native quota headers arrive.'))
  const status = await modelScript.waitForSteps(start + 1)
  expect(status.requests.find(request => request.stepIndex === start)).toBeDefined()
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
  await expect(await openAgentInfoCard(page)).not.toContainText(rateLimitWindowLabel(rateLimits.type))
  await page.keyboard.press('Escape')
  await page.reload()
  await expect(await openAgentInfoCard(page)).not.toContainText(rateLimitWindowLabel(rateLimits.type))
})
