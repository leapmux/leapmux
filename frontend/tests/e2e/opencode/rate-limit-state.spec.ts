import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('receives actual quota-bearing model headers without publishing quota UI', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})
