import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

fastAgentTest('reports no quota window after consuming actual native quota headers', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})
