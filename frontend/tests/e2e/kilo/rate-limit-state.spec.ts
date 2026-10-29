import type { Page } from '@playwright/test'
import type { KiloQuotaWindow } from '../helpers/kiloCloudSurface'
import type { MockQuotaHTTPResponse, MockQuotaReceipt } from '../helpers/scenarioQuota'
import { expect } from '@playwright/test'
import { KILO_USAGE_CONTROL_PREFIX, kiloCodingPlanUsage } from '../helpers/kiloCloudSurface'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectRateLimitWindow, rateLimitWindowLabel } from '../helpers/rateLimit'
import { getGlobalState } from '../helpers/server'
import { closeComposerMenus, loginViaToken, openAgentInfoCard, openWorkspace, tabById } from '../helpers/ui'
import { openProviderAgent, withTestWorkspace } from '../helpers/workspace'
import { kiloTest } from '../kilo-fixtures'
import { KILO_AGENT, nativeContext } from './scenarios'

/** The two quota windows of the mocked coding plan: five hours and one week, each with the share that remains. */
function planWindows(fiveHourRemaining: number, weeklyRemaining: number): KiloQuotaWindow[] {
  const now = Date.now()
  return [
    {
      id: 'five_hour',
      remainingPercent: fiveHourRemaining,
      resetsAt: new Date(now + 3 * 3_600_000).toISOString(),
      period: { unit: 'hour', value: 5 },
    },
    {
      id: 'weekly',
      remainingPercent: weeklyRemaining,
      resetsAt: new Date(now + 5 * 86_400_000).toISOString(),
      period: { unit: 'week', value: 1 },
    },
  ]
}

function controlURL(scenarioId: string): URL {
  return new URL(`${KILO_USAGE_CONTROL_PREFIX}${encodeURIComponent(scenarioId)}`, getGlobalState().mockModelUrl)
}

async function configureUsage(scenarioId: string, profile: MockQuotaHTTPResponse): Promise<void> {
  const response = await fetch(controlURL(scenarioId), {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(profile),
  })
  expect(response.status, await response.text()).toBe(200)
}

async function usageReceipts(scenarioId: string): Promise<MockQuotaReceipt[]> {
  const response = await fetch(controlURL(scenarioId))
  expect(response.status).toBe(200)
  return (await response.json() as { receipts: MockQuotaReceipt[] }).receipts
}

/** Require that the row of each window states the time to its reset, as the plan reports it. */
async function expectResetTimes(page: Page): Promise<void> {
  const popover = await openAgentInfoCard(page)
  for (const type of ['five_hour', 'seven_day'])
    await expect(popover.getByText(rateLimitWindowLabel(type), { exact: true }).locator('..'), `the ${type} row states its reset`).toContainText('resets in')
  await closeComposerMenus(page)
}

// Kilo reads the plan usage from its cloud, which the mock stands in for (helpers/kiloCloudSurface.ts). The mocked
// account has its coding plan only while this scenario owns its usage, so the usage is set before the agent starts.
kiloTest('shows the native plan usage windows with their reset times, keeps them after reload, and reads them again in a new process', async ({ page, modelScript, leapmuxServer }) => {
  const fixtures = { page, modelScript, leapmuxServer }
  await configureUsage(modelScript.id, kiloCodingPlanUsage(planWindows(58, 15)))
  await withTestWorkspace(leapmuxServer, 'kilo-usage', async ({ workspaceId }) => {
    await openProviderAgent(leapmuxServer, workspaceId, KILO_AGENT)
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    const context = await nativeContext({ ...fixtures, workspaceId })
    await sendNativeAnswer(context, 'Complete the native usage check.', 'The native usage turn completed.')
    await expectRateLimitWindow(page, { type: 'five_hour', status: 'allowed', utilization: 0.42 })
    await expectRateLimitWindow(page, { type: 'seven_day', status: 'allowed_warning', utilization: 0.85 })
    await expectResetTimes(page)
    await page.reload()
    await expectRateLimitWindow(page, { type: 'five_hour', status: 'allowed', utilization: 0.42 })
    await expectRateLimitWindow(page, { type: 'seven_day', status: 'allowed_warning', utilization: 0.85 })
    await expectResetTimes(page)

    // Kilo keeps a plan's usage for a minute, so the next value comes from a new process: a second agent.
    await configureUsage(modelScript.id, kiloCodingPlanUsage(planWindows(0, 15)))
    const second = await openProviderAgent(leapmuxServer, workspaceId, KILO_AGENT)
    await tabById(page, second.agentId).click()
    await sendNativeAnswer(context, 'Complete the turn after the five-hour quota ran out.', 'The turn after the quota ran out completed.')
    // An exceeded window states its status alone.
    await expectRateLimitWindow(page, { type: 'five_hour', status: 'exceeded' })
    const popover = await openAgentInfoCard(page)
    await expect(popover.getByText(rateLimitWindowLabel('five_hour'), { exact: true }).locator('..'), 'the five-hour row is exceeded').toContainText('Exceeded')
    await expect(popover.getByText(rateLimitWindowLabel('seven_day'), { exact: true }).locator('..'), 'the weekly row keeps its use').toContainText('85% used')
    await closeComposerMenus(page)

    const receipts = await usageReceipts(modelScript.id)
    expect(receipts.filter(receipt => receipt.status === 200 && receipt.authorized).length, 'both processes read the usage with the mocked account').toBeGreaterThanOrEqual(2)
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/513.
