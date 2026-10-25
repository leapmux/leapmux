import type { Page } from '@playwright/test'
import type { KimiPlanWindows } from '../helpers/kimiCodeSurface'
import type { MockModelRateLimits } from '../helpers/mockModelScript'
import type { MockQuotaHTTPResponse, MockQuotaReceipt } from '../helpers/scenarioQuota'
import { expect } from '@playwright/test'
import { KIMI_USAGE_CONTROL_PREFIX, kimiPlanUsage } from '../helpers/kimiCodeSurface'
import { KIMI_MOCK_MODELS, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModel } from '../helpers/nativeSettings'
import { expectRateLimitWindow, rateLimitWindowLabel } from '../helpers/rateLimit'
import { getGlobalState } from '../helpers/server'
import { closeComposerMenus, loginViaToken, openAgentInfoCard, openWorkspace, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent, withTestWorkspace } from '../helpers/workspace'
import { kimiTest } from '../kimi-fixtures'
import { KIMI_AGENT, nativeContext } from './scenarios'

/** The three windows that the plan limits, as the card states them. */
const PLAN_WINDOW_TYPES = ['five_hour', 'seven_day', 'one_month'] as const

/**
 * The quota windows of the mocked plan, each with its used share and its reset. The plan also reports the Kimi Code
 * share of the monthly window, which limits nothing by itself.
 */
function planWindows(fiveHour: number, weekly: number, monthly: number): KimiPlanWindows {
  const now = Date.now()
  return {
    limit_5h: { used_ratio: fiveHour, reset_time: new Date(now + 3 * 3_600_000).toISOString() },
    limit_7d: { used_ratio: weekly, reset_time: new Date(now + 5 * 86_400_000).toISOString() },
    limit_month_total: { used_ratio: monthly, reset_time: new Date(now + 20 * 86_400_000).toISOString() },
    limit_month_code: { used_ratio: monthly / 2 },
  }
}

function controlURL(scenarioId: string): URL {
  return new URL(`${KIMI_USAGE_CONTROL_PREFIX}${encodeURIComponent(scenarioId)}`, getGlobalState().mockModelUrl)
}

async function configureUsage(scenarioId: string, profile: MockQuotaHTTPResponse): Promise<void> {
  const response = await fetch(controlURL(scenarioId), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(profile) })
  expect(response.status, await response.text()).toBe(200)
}

async function usageReceipts(scenarioId: string): Promise<MockQuotaReceipt[]> {
  const response = await fetch(controlURL(scenarioId))
  expect(response.status).toBe(200)
  return (await response.json() as { receipts: MockQuotaReceipt[] }).receipts
}

/** Require each window of the plan on the card, and the time to its reset in its row. */
async function expectPlanWindows(page: Page, windows: readonly MockModelRateLimits[]): Promise<void> {
  for (const window of windows)
    await expectRateLimitWindow(page, window)
  const popover = await openAgentInfoCard(page)
  for (const type of PLAN_WINDOW_TYPES)
    await expect(popover.getByText(rateLimitWindowLabel(type), { exact: true }).locator('..'), `the ${type} row states its reset`).toContainText('resets in')
  // The Kimi Code share of the monthly window is no window of its own.
  await expect(popover.getByText(/^(?:Rate Limit(?:\s|$)|.+ Rate Limit$)/)).toHaveCount(PLAN_WINDOW_TYPES.length)
  await closeComposerMenus(page)
}

/** Require that the card shows no window. */
async function expectNoPlanWindows(page: Page): Promise<void> {
  const popover = await openAgentInfoCard(page)
  await expect(popover.getByText(/^(?:Rate Limit(?:\s|$)|.+ Rate Limit$)/)).toHaveCount(0)
  await closeComposerMenus(page)
}

const STARTING_WINDOWS: MockModelRateLimits[] = [
  { type: 'five_hour', status: 'allowed', utilization: 0.42 },
  { type: 'seven_day', status: 'allowed_warning', utilization: 0.85 },
  { type: 'one_month', status: 'allowed', utilization: 0.1 },
]

// An exceeded window states its status alone.
const EXHAUSTED_WINDOWS: MockModelRateLimits[] = [
  { type: 'five_hour', status: 'exceeded' },
  { type: 'seven_day', status: 'allowed_warning', utilization: 0.9 },
  { type: 'one_month', status: 'allowed', utilization: 0.15 },
]

// Kimi Code reads the plan usage from its own service, which the mock stands in for (helpers/kimiCodeSurface.ts), with
// the token of the account that the suite signs Kimi Code in to (helpers/kimiEnvironment.ts). The plan limits only
// the account's own model, so only an agent on that model shows the windows.
kimiTest('shows the plan usage windows of the Kimi Code account with their reset times, reads them after each turn, and shows none for a model of another provider', async ({ page, modelScript, leapmuxServer }) => {
  const fixtures = { page, modelScript, leapmuxServer }
  await configureUsage(modelScript.id, kimiPlanUsage(planWindows(0.42, 0.85, 0.1)))
  await withTestWorkspace(leapmuxServer, 'kimi-usage', async ({ workspaceId }) => {
    await openProviderAgent(leapmuxServer, workspaceId, KIMI_AGENT, { model: KIMI_MOCK_MODELS.account })
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    // The session opens on the account's model, so the windows appear before the first turn.
    await expectPlanWindows(page, STARTING_WINDOWS)

    // The turn uses up the five-hour window, and the read after the turn states it.
    const context = await nativeContext({ ...fixtures, workspaceId })
    await configureUsage(modelScript.id, kimiPlanUsage(planWindows(1, 0.9, 0.15)))
    const request = await sendNativeAnswer(context, 'Complete the turn on the plan model.', 'The turn on the plan model completed.')
    expect(request.body, 'the turn ran on the model of the account').toHaveProperty('model', MOCK_MODELS.zai)
    await expectPlanWindows(page, EXHAUSTED_WINDOWS)
    const popover = await openAgentInfoCard(page)
    await expect(popover.getByText(rateLimitWindowLabel('five_hour'), { exact: true }).locator('..'), 'the five-hour row is exceeded').toContainText('Exceeded')
    await closeComposerMenus(page)
    await page.reload()
    await waitForAgentIdle(page)
    await expectPlanWindows(page, EXHAUSTED_WINDOWS)

    // The plan does not limit a model of another provider.
    await chooseNativeModel(context, KIMI_MOCK_MODELS.thinking)
    await expectNoPlanWindows(page)
    await page.reload()
    await waitForAgentIdle(page)
    await expectNoPlanWindows(page)

    // The account's model shows the plan again.
    await chooseNativeModel(context, KIMI_MOCK_MODELS.account)
    await expectPlanWindows(page, EXHAUSTED_WINDOWS)

    const receipts = await usageReceipts(modelScript.id)
    expect(receipts.every(receipt => receipt.status === 200 && receipt.authorized), 'every read used the token of the mocked account').toBe(true)
    expect(receipts.length, 'the start, the turn end and the switch back each read the usage').toBeGreaterThanOrEqual(3)
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/513.
