import type { LettaQuotaHTTPResponse, LettaQuotaReceipt } from '../helpers/lettaSurface'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { LETTA_CHATGPT_ACCOUNT_ID, LETTA_CHATGPT_MODEL_ID } from '../helpers/lettaEnvironment'
import { LETTA_QUOTA_CONTROL_PREFIX } from '../helpers/lettaSurface'
import { LETTA_REASONING_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectRateLimitWindow } from '../helpers/rateLimit'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { getGlobalState } from '../helpers/server'
import { chooseSettingsOption, closeComposerMenus, loginViaToken, openAgentInfoCard, openWorkspace, visibleOnly, waitForSettingsIdle } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { openProviderAgent, withTestWorkspace } from '../helpers/workspace'
import { lettaTest } from '../letta-fixtures'
import { LETTA_AGENT, nativeContext } from './scenarios'

type Fixtures = Omit<NativeContextFixtures, 'workspaceId'>

function quotaResponse(used: number, resetsAt = Math.floor(Date.now() / 1000) + 3600, weekly?: { used: number, resetsAt: number }): LettaQuotaHTTPResponse {
  return { status: 200, body: { plan_type: 'plus', rate_limit: {
    limit_reached: false,
    primary_window: { used_percent: used, limit_window_seconds: 18_000, reset_at: resetsAt },
    ...(weekly === undefined ? {} : { secondary_window: { used_percent: weekly.used, limit_window_seconds: 604_800, reset_at: weekly.resetsAt } }),
  } } }
}

function controlURL(scenarioId: string): URL {
  return new URL(`${LETTA_QUOTA_CONTROL_PREFIX}${encodeURIComponent(scenarioId)}`, getGlobalState().mockModelUrl)
}

async function configureQuota(scenarioId: string, profile: LettaQuotaHTTPResponse): Promise<void> {
  const response = await fetch(controlURL(scenarioId), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(profile) })
  expect(response.status, await response.text()).toBe(200)
}

async function quotaReceipts(scenarioId: string): Promise<LettaQuotaReceipt[]> {
  const response = await fetch(controlURL(scenarioId))
  expect(response.status).toBe(200)
  const state = await response.json() as { receipts: LettaQuotaReceipt[] }
  return state.receipts
}

async function withQuotaWorkspace(fixtures: Fixtures, profile: LettaQuotaHTTPResponse, run: (context: ManagedNativeScenarioContext) => Promise<void>): Promise<void> {
  await configureQuota(fixtures.modelScript.id, profile)
  await withTestWorkspace(fixtures.leapmuxServer, 'letta-quota', async ({ workspaceId }) => {
    await openProviderAgent(fixtures.leapmuxServer, workspaceId, LETTA_AGENT, { model: LETTA_CHATGPT_MODEL_ID })
    await loginViaToken(fixtures.page, fixtures.leapmuxServer.adminToken)
    await openWorkspace(fixtures.page, workspaceId)
    await run(await nativeContext({ ...fixtures, workspaceId }))
  })
}

lettaTest('shows native subscription windows and retains them after reload', async ({ page, modelScript, leapmuxServer }) => {
  const resetsAt = Math.floor(Date.now() / 1000) + 3600
  await withQuotaWorkspace({ page, modelScript, leapmuxServer }, quotaResponse(92, resetsAt, { used: 25, resetsAt: resetsAt + 86_400 }), async (context) => {
    const request = await sendNativeAnswer(context, 'Complete the native subscription quota check.', 'The native subscription model answered.')
    expect(request.body).toHaveProperty('model', 'gpt-5.5')
    expect(request.mockCredential?.accepted).toBe(true)
    await expectRateLimitWindow(page, { type: 'five_hour', utilization: 0.92, status: 'allowed_warning', resetsAt })
    await expectRateLimitWindow(page, { type: 'seven_day', utilization: 0.25, status: 'allowed', resetsAt: resetsAt + 86_400 })
    await page.reload()
    await expectRateLimitWindow(page, { type: 'five_hour', utilization: 0.92, status: 'allowed_warning', resetsAt })
    const receipts = await quotaReceipts(modelScript.id)
    expect(receipts.some(receipt => receipt.status === 200 && receipt.authorized && receipt.accountId === LETTA_CHATGPT_ACCOUNT_ID)).toBe(true)
  })
})

lettaTest('retains quota during endpoint throttling and recovers with zero usage', async ({ page, modelScript, leapmuxServer }) => {
  await withQuotaWorkspace({ page, modelScript, leapmuxServer }, quotaResponse(90), async (context) => {
    await sendNativeAnswer(context, 'Complete the first quota turn.', 'The first quota turn completed.')
    await expectRateLimitWindow(page, { type: 'five_hour', utilization: 0.9, status: 'allowed_warning' })
    const baseline = (await quotaReceipts(modelScript.id)).at(-1)?.sequence ?? 0
    await configureQuota(modelScript.id, { status: 429, body: { error: 'Scripted quota endpoint refusal.' }, retryAfterSeconds: 1 })
    await closeComposerMenus(page)
    await sendNativeAnswer(context, 'Complete a turn while the quota endpoint is unavailable.', 'The model still works while quota is unavailable.')
    await retryUntilPass(async () => {
      const refused = (await quotaReceipts(modelScript.id)).filter(receipt => receipt.sequence > baseline && receipt.status === 429)
      expect(refused.length).toBeGreaterThanOrEqual(2)
    })
    await expectRateLimitWindow(page, { type: 'five_hour', utilization: 0.9, status: 'allowed_warning' })
    await configureQuota(modelScript.id, quotaResponse(0))
    await expectRateLimitWindow(page, { type: 'five_hour', utilization: 0, status: 'allowed' })
    expect((await modelScript.status()).requests.filter(request => request.stepIndex !== undefined)).toHaveLength(2)
  })
})

lettaTest('clears subscription quota when the model switches to an API key', async ({ page, modelScript, leapmuxServer }) => {
  await withQuotaWorkspace({ page, modelScript, leapmuxServer }, quotaResponse(90), async (context) => {
    await sendNativeAnswer(context, 'Complete the subscription turn.', 'The subscription turn completed.')
    await expectRateLimitWindow(page, { type: 'five_hour', utilization: 0.9, status: 'allowed_warning' })
    await closeComposerMenus(page)
    await chooseSettingsOption(page, `model-${LETTA_REASONING_MODEL_ID}`)
    await waitForSettingsIdle(page)
    const request = await sendNativeAnswer(context, 'Complete the API-key turn.', 'The API-key turn completed.')
    expect(request.body).toHaveProperty('model', 'gpt-5.4')
    const popover = await openAgentInfoCard(page)
    await expect(popover).not.toContainText('5-Hour Rate Limit')
  })
})

lettaTest('keeps an epoch-zero reset in the urgent quota tooltip', async ({ page, modelScript, leapmuxServer }) => {
  await withQuotaWorkspace({ page, modelScript, leapmuxServer }, quotaResponse(90, 0, { used: 90, resetsAt: Math.floor(Date.now() / 1000) + 3600 }), async (context) => {
    await sendNativeAnswer(context, 'Complete the zero-reset quota turn.', 'The zero-reset quota turn completed.')
    const trigger = visibleOnly(page.getByTestId('agent-info-trigger')).first()
    const warning = trigger.getByText('Warning', { exact: true })
    await expect(warning).toBeVisible()
    await warning.hover()
    await expect(visibleOnly(page.getByRole('tooltip'))).toContainText(/1969|1970/)
  })
})

lettaTest('does not invent subscription quota from API-key model headers', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})
