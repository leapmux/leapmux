import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { lettaTest } from '../letta-fixtures'

lettaTest('reports no quota window after consuming actual native quota headers', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})

// Track this LeapMux limitation: https://github.com/leapmux/leapmux/issues/513.
