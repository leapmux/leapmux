import type { NativeContextFixtures } from '../helpers/nativeScenario'
import type { MockQuotaHTTPResponse, MockQuotaReceipt } from '../helpers/scenarioQuota'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { QODER_QUOTA_CONTROL_PREFIX } from '../helpers/qoderSurface'
import { expectRateLimitWindow } from '../helpers/rateLimit'
import { getGlobalState } from '../helpers/server'
import { chooseSettingsOption, closeComposerMenus, loginViaToken, openAgentInfoCard, openWorkspace, waitForSettingsIdle } from '../helpers/ui'
import { openProviderAgent, withTestWorkspace } from '../helpers/workspace'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext, QODER_AGENT } from './scenarios'

type Fixtures = Omit<NativeContextFixtures, 'workspaceId'>

/**
 * The account quota that Qoder's quota service answers, in the service's own snake_case shape. Qoder normalizes each
 * fraction into a percent. The quota states an expiry, which LeapMux does not show as a reset time.
 */
function quotaResponse(planUsed: number, addOnUsed: number, exceeded = false): MockQuotaHTTPResponse {
  return {
    status: 200,
    body: {
      user_id: 'leapmux-e2e',
      user_type: 'personal',
      total_usage_percentage: planUsed,
      is_quota_exceeded: exceeded,
      expires_at: Date.now() + 7 * 86_400_000,
      user_quota: {
        total: 1000,
        used: planUsed * 10,
        remaining: Math.max(0, 1000 - planUsed * 10),
        percentage: planUsed / 100,
        unit: 'credits',
      },
      add_on_quota: {
        total: 200,
        used: addOnUsed * 2,
        remaining: Math.max(0, 200 - addOnUsed * 2),
        percentage: addOnUsed / 100,
        unit: 'credits',
      },
    },
  }
}

function controlURL(scenarioId: string): URL {
  return new URL(`${QODER_QUOTA_CONTROL_PREFIX}${encodeURIComponent(scenarioId)}`, getGlobalState().mockModelUrl)
}

async function configureQuota(scenarioId: string, profile: MockQuotaHTTPResponse): Promise<void> {
  const response = await fetch(controlURL(scenarioId), {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(profile),
  })
  expect(response.status, await response.text()).toBe(200)
}

async function quotaReceipts(scenarioId: string): Promise<MockQuotaReceipt[]> {
  const response = await fetch(controlURL(scenarioId))
  expect(response.status).toBe(200)
  return (await response.json() as { receipts: MockQuotaReceipt[] }).receipts
}

/** Require that the agent info card shows no reset time: Qoder states none that LeapMux shows. */
async function expectNoResetTime(fixtures: Fixtures): Promise<void> {
  const popover = await openAgentInfoCard(fixtures.page)
  await expect(popover).not.toContainText('resets in')
  await closeComposerMenus(fixtures.page)
}

qoderTest('shows the native account quota windows, keeps them after reload, and reads them again after a restart', async ({ page, modelScript, leapmuxServer }) => {
  const fixtures = { page, modelScript, leapmuxServer }
  await configureQuota(modelScript.id, quotaResponse(42, 85))
  await withTestWorkspace(leapmuxServer, 'qoder-quota', async ({ workspaceId }) => {
    await openProviderAgent(leapmuxServer, workspaceId, QODER_AGENT)
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    const context = await nativeContext({ ...fixtures, workspaceId })
    await sendNativeAnswer(context, 'Complete the native quota check.', 'The native quota turn completed.')
    await expectRateLimitWindow(page, { type: 'user_quota', status: 'allowed', utilization: 0.42 })
    await expectRateLimitWindow(page, { type: 'add_on_quota', status: 'allowed_warning', utilization: 0.85 })
    await expectNoResetTime(fixtures)
    await page.reload()
    await expectRateLimitWindow(page, { type: 'user_quota', status: 'allowed', utilization: 0.42 })
    await expectRateLimitWindow(page, { type: 'add_on_quota', status: 'allowed_warning', utilization: 0.85 })

    // Qoder caches the quota for 15 seconds, so the next value comes from a new process: an effort change restarts it.
    await configureQuota(modelScript.id, quotaResponse(100, 100, true))
    await closeComposerMenus(page)
    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await sendNativeAnswer(context, 'Complete the turn after the quota ran out.', 'The turn after the quota ran out completed.')
    // An exceeded window states its status alone.
    await expectRateLimitWindow(page, { type: 'user_quota', status: 'exceeded' })
    await expectRateLimitWindow(page, { type: 'account', status: 'exceeded' })
    const popover = await openAgentInfoCard(page)
    for (const type of ['user_quota', 'add_on_quota', 'account'])
      await expect(popover.getByText(`Rate Limit (${type})`, { exact: true }).locator('..'), `the ${type} row is exceeded`).toContainText('Exceeded')
    await expect(popover).not.toContainText('% used')
    await expect(popover).not.toContainText('resets in')

    const receipts = await quotaReceipts(modelScript.id)
    expect(receipts.filter(receipt => receipt.status === 200 && receipt.authorized).length, 'both processes read the quota with the mocked account').toBeGreaterThanOrEqual(2)
  })
})
