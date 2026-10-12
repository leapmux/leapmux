import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI compaction and rate limits', () => {
  qoderTest('does not show a rate-limit window from BYOK model headers', async ({ native }) => {
    await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
  })
})
