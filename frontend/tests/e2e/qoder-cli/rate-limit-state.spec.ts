import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('rate-limit state for Qoder CLI', () => {
  qoderTest('does not show a rate-limit window from BYOK model headers', async ({ native }) => {
    await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
  })
})

// Track this LeapMux limitation: https://github.com/leapmux/leapmux/issues/513.
