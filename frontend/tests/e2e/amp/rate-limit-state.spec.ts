import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { exerciseNativeQuotaRefusal } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

ampTest('proves a real native quota response without a quota window in the info card', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  const error = { status: 429, code: 'insufficient_quota', message: 'The isolated native quota is exhausted.' }
  await expectNoRateLimitState(context, { relatedProof: () => exerciseNativeQuotaRefusal(context, { error, receiptProof: (request) => {
    expect(request.serviceResponse).toEqual({ kind: 'amp-error-set', code: error.code, message: error.message })
  } }) })
})
