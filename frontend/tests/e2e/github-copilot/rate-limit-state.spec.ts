import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { exerciseRateLimitWindow, expectRateLimitWindow, NEAR_LIMIT_UTILIZATION, nearLimitRateLimits, rateLimitWindowLabel } from '../helpers/rateLimit'
import { openAgentInfoCard } from '../helpers/ui'

copilotTest('publishes native quota snapshots and recovers to explicit zero use after reload', async ({ native }) => {
  for (const utilization of [NEAR_LIMIT_UTILIZATION, 0]) {
    const rateLimits = { ...nearLimitRateLimits('premium_interactions'), status: utilization > 0 ? 'allowed_warning' : 'allowed', utilization }
    const request = await exerciseRateLimitWindow(native, rateLimits)
    const header = request.response?.headers['x-quota-snapshot-premium_interactions']
    expect(header).toBeTruthy()
    const snapshot = new URLSearchParams(header)
    expect(Number(snapshot.get('ent'))).toBe(100)
    expect(Number(snapshot.get('rem'))).toBeCloseTo((1 - utilization) * 100)
    const popover = await openAgentInfoCard(native.page)
    await expect(popover.getByText(rateLimitWindowLabel(rateLimits.type), { exact: true })).toHaveCount(1)
    await expect(popover).toContainText(utilization > 0 ? 'Warning' : 'Allowed')
    await native.page.reload()
    await expectRateLimitWindow(native.page, rateLimits)
  }
})
