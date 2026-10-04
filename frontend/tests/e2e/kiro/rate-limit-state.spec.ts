import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativeQuotaRefusal } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('proves a real native quota response without a quota window in the info card', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  const error = { status: 429, code: 'ThrottlingException', message: 'The isolated native quota is exhausted.' }
  await expectNoRateLimitState(context, { relatedProof: () => exerciseNativeQuotaRefusal(context, { error, receiptProof: (request) => {
    expect(request.response).toMatchObject({ status: 429, serviceError: { code: error.code, message: error.message } })
  } }) })
})
