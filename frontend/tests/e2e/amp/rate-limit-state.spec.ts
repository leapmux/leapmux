import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { exerciseNativeQuotaRefusal } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

ampTest('proves a real native quota response without a quota window in the info card', async ({ native }) => {
  const error = { status: 429, code: 'insufficient_quota', message: 'The isolated native quota is exhausted.' }
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaRefusal(native, { error, queueAfterFailure: 'running', receiptProof: (request) => {
    expect(request.serviceResponse).toEqual({ kind: 'amp-error-set', code: error.code, message: error.message })
  } }) })
})
