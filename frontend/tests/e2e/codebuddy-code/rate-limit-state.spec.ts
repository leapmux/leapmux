import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

codebuddyTest.describe('CodeBuddy Code rate-limit state', () => {
  codebuddyTest('shows no rate-limit window from model response headers', async ({ native }) => {
    await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
  })
})
