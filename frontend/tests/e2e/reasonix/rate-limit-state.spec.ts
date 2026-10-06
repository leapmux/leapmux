import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('receives actual quota-bearing model headers without publishing quota UI', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})
