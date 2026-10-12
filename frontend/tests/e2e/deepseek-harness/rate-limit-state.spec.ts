import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

deepseekHarnessTest('consumes a native request with quota headers without publishing an unsupported quota window', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: async () => {
    expect((await exerciseNativeQuotaHeaders(native)).protocol).toBe('anthropic-messages')
  } })
})
