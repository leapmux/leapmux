import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { museTest } from '../muse-fixtures'

// Muse reports account quota only through its own native usage events, which the local
// mock model cannot carry. Quota-bearing generic model responses stay absent from the UI.
museTest('receives actual quota-bearing model headers without publishing quota UI', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})
