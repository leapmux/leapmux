import { diracTest } from '../dirac-fixtures'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

diracTest('reports no quota window after consuming actual native quota headers', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: () => exerciseNativeQuotaHeaders(native) })
})
