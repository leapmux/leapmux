import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

commandCodeTest('consumes actual native quota headers without reporting a quota window', async ({ native }) => {
  await expectNoRateLimitState(native, { relatedProof: async () => {
    expect((await exerciseNativeQuotaHeaders(native)).protocol).toBe('openai-chat-completions')
  } })
})
