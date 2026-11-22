import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { openAgentInfoCard } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

antigravityTest('retains the native quota result without an unsupported rate-limit surface', async ({ native }) => {
  await expectNoRateLimitState(native, {
    relatedProof: () => exerciseNativeQuotaHeaders(native),
  })
})

antigravityTest('shows no subscription usage for a Gemini API-key session', async ({ native }) => {
  await sendNativeAnswer(native, 'Complete a turn on the API key.', 'The API-key turn is complete.')
  const card = await openAgentInfoCard(native.page)
  await expect(card.getByText(/^Gemini Models · /)).toHaveCount(0)
  await expect(card.getByText(/\b(?:Allowed|Exceeded)\b|% used/)).toHaveCount(0)
})
