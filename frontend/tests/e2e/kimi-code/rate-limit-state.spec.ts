import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { kimiTest } from '../kimi-fixtures'

kimiTest('proves a real native quota response without a quota window in the info card', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})

// Track this LeapMux limitation: https://github.com/leapmux/leapmux/issues/513.
