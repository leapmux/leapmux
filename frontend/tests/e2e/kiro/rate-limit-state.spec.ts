import { expect } from '@playwright/test'
import { exerciseNativeQuotaRefusal } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { kiroTest } from '../kiro-fixtures'

kiroTest('proves a real native quota response without a quota window in the info card', async ({ native }) => {
  const error = { status: 429, code: 'ThrottlingException', message: 'The isolated native quota is exhausted.' }
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaRefusal(native, { error, queueAfterFailure: 'running', receiptProof: (request) => {
    expect(request.response).toMatchObject({ status: 429, serviceError: { code: error.code, message: error.message } })
  } }) })
})
