import { clineTest } from '../cline-fixtures'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

clineTest('proves a real native quota response without a quota window in the info card', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})
