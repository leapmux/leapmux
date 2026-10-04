import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { expectRateLimitWindow, rateLimitWindowLabel } from '../helpers/rateLimit'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'

copilotTest('publishes native quota snapshots and recovers to explicit zero use after reload', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
  void authenticatedCopilotWorkspace
  for (const utilization of [0.92, 0]) {
    const start = (await modelScript.status()).stepCount
    const rateLimits = { type: 'premium_interactions', status: utilization > 0 ? 'allowed_warning' : 'allowed', utilization, resetsAt: Math.floor(Date.now() / 1000) + 3600 }
    await modelScript.queue({ text: 'The actual Copilot quota turn completed.', rateLimits })
    await sendMessage(page, modelScript.prompt('Complete the actual native quota response.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    const request = (await modelScript.status()).requests.find(record => record.stepIndex === start)
    const header = request?.response?.headers['x-quota-snapshot-premium_interactions']
    expect(header).toBeTruthy()
    const snapshot = new URLSearchParams(header)
    expect(Number(snapshot.get('ent'))).toBe(100)
    expect(Number(snapshot.get('rem'))).toBeCloseTo((1 - utilization) * 100)
    await expectRateLimitWindow(page, rateLimits)
    const popover = await openAgentInfoCard(page)
    await expect(popover.getByText(rateLimitWindowLabel(rateLimits.type), { exact: true })).toHaveCount(1)
    await expect(popover).toContainText(utilization > 0 ? 'Warning' : 'Allowed')
    await page.reload()
    await expectRateLimitWindow(page, rateLimits)
  }
})
