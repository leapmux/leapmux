import type { Page } from '@playwright/test'
import type { MockQuotaHTTPResponse, MockQuotaReceipt } from '../helpers/scenarioQuota'
import { expect } from '@playwright/test'
import { KIRO_USAGE_CONTROL_PREFIX } from '../helpers/kiroSurface'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeQuotaRefusal } from '../helpers/nativeQuota'
import { getGlobalState } from '../helpers/server'
import { closeComposerMenus, loginViaToken, openAgentInfoCard, openWorkspace, waitForAgentIdle } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { openProviderAgent, withTestWorkspace } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_AGENT, nativeContext } from './scenarios'

const RESET_DATE = '2026-11-25'
const QUOTA_LABEL = 'Native Probe Plan — Credits'

function usageResponse(used: number | null): MockQuotaHTTPResponse {
  return { status: 200, body: {
    subscriptionInfo: { subscriptionTitle: 'Native Probe Plan', overageCapability: 'OVERAGE_CAPABLE' },
    nextDateReset: Date.UTC(2026, 10, 25) / 1000,
    usageBreakdownList: used === null ? [] : [{ resourceType: 'CREDIT', displayName: 'Credit', displayNamePlural: 'Credits', currentUsageWithPrecision: used, usageLimitWithPrecision: 100 }],
    overageConfiguration: { overageStatus: 'DISABLED' },
  } }
}

function controlURL(scenario: string): URL {
  return new URL(`${KIRO_USAGE_CONTROL_PREFIX}${encodeURIComponent(scenario)}`, getGlobalState().mockModelUrl)
}

async function configureUsage(scenario: string, response: MockQuotaHTTPResponse): Promise<void> {
  const result = await fetch(controlURL(scenario), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(response) })
  expect(result.status, await result.text()).toBe(200)
}

async function usageReceipts(scenario: string): Promise<(MockQuotaReceipt & { origin: string })[]> {
  const result = await fetch(controlURL(scenario))
  expect(result.status).toBe(200)
  return (await result.json() as { receipts: (MockQuotaReceipt & { origin: string })[] }).receipts
}

async function expectAccountQuota(page: Page, status: string, utilization?: number): Promise<void> {
  const popover = await openAgentInfoCard(page)
  const row = popover.getByText(QUOTA_LABEL, { exact: true }).locator('..')
  await expect(row).toContainText(status)
  if (utilization !== undefined)
    await expect(row).toContainText(`${utilization}% used`)
  else
    await expect(row).not.toContainText('% used')
  await expect(row).toContainText(`resets on ${RESET_DATE}`)
  await expect(row).not.toContainText('resets in')
  await closeComposerMenus(page)
}

kiroTest('reads native plan usage at startup and after turns, preserves failed reads and keeps the calendar reset date', async ({ page, modelScript, leapmuxServer }) => {
  await configureUsage(modelScript.id, usageResponse(25.5))
  await withTestWorkspace(leapmuxServer, 'kiro-account-usage', async ({ workspaceId }) => {
    await openProviderAgent(leapmuxServer, workspaceId, KIRO_AGENT)
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId })
    await expectAccountQuota(page, 'Allowed', 26)

    for (const [used, status, utilization] of [[85, 'Warning', 85], [105, 'Exceeded', undefined]] as const) {
      await configureUsage(modelScript.id, usageResponse(used))
      await sendNativeAnswer(context, `Complete the turn with ${used} credits used.`, `The ${used}-credit turn ended.`)
      await expectAccountQuota(page, status, utilization)
    }
    await page.reload()
    await waitForAgentIdle(page)
    await expectAccountQuota(page, 'Exceeded')

    await configureUsage(modelScript.id, { status: 403, body: { __type: 'AccessDeniedException', message: 'The isolated account quota is temporarily unavailable.' } })
    await sendNativeAnswer(context, 'Finish a turn while the account service refuses the usage read.', 'The turn completed despite the account-service refusal.')
    await expect.poll(async () => (await usageReceipts(modelScript.id)).some(receipt => receipt.status === 403)).toBe(true)
    await expectAccountQuota(page, 'Exceeded')

    await configureUsage(modelScript.id, usageResponse(0))
    await sendNativeAnswer(context, 'Read the reset account usage.', 'The reset quota turn ended.')
    await expectAccountQuota(page, 'Allowed', 0)

    await configureUsage(modelScript.id, usageResponse(null))
    await sendNativeAnswer(context, 'Read an account with no recurring usage limits.', 'The account has no recurring usage limits.')
    const popover = await openAgentInfoCard(page)
    await expect(popover.getByText(QUOTA_LABEL, { exact: true })).toHaveCount(0)
    await closeComposerMenus(page)

    const receipts = await usageReceipts(modelScript.id)
    expect(receipts.length).toBeGreaterThanOrEqual(6)
    expect(receipts.every(receipt => receipt.authorized && receipt.origin === 'AI_EDITOR')).toBe(true)
  })
})

kiroTest('keeps a model-service busy notice separate from account quota windows', async ({ native }) => {
  const error = { status: 429, code: 'ThrottlingException', message: 'The isolated native quota is exhausted.' }
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaRefusal(native, { error, queueAfterFailure: 'running', receiptProof: (request) => {
    expect(request.response).toMatchObject({ status: 429, serviceError: { code: error.code, message: error.message } })
  } }) })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/513.
