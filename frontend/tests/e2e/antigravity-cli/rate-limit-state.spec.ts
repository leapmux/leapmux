import { expect } from '@playwright/test'
import { RATE_LIMIT_UPDATE_FIELD, RATE_LIMIT_UPDATE_MODE, SESSION_INFO_KEY } from '../../../src/generated/contracts/session-info'
import { NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { antigravityTest } from '../antigravity-fixtures'
import { watchAgentEvents } from '../helpers/agentEventWatch'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { exerciseNativeQuotaHeaders } from '../helpers/nativeQuota'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { openAgentInfoCard } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

antigravityTest('retains the native quota result without an unsupported rate-limit surface', async ({ native }) => {
  await expectNoRateLimitState(native, {
    relatedProof: () => exerciseNativeQuotaHeaders(native),
  })
})

antigravityTest('shows no subscription usage for a Gemini API-key session', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const watch = await watchAgentEvents(native.leapmuxServer, agent.id, {
    label: 'the native quota snapshot',
    select: (event) => {
      if (event.replay || event.event.case !== 'agentMessage' || event.event.value.seq !== -1n)
        return undefined
      const body = nativeMessageBody(event.event.value)
      if (!isObject(body) || body.type !== NOTIFICATION_TYPE.AgentSessionInfo)
        return undefined
      return pickObject(pickObject(body, 'info'), SESSION_INFO_KEY.RateLimits) ?? undefined
    },
  })
  try {
    await sendNativeAnswer(native, 'Complete a turn on the API key.', 'The API-key turn is complete.')
    await expect.poll(watch.items).toContainEqual({
      [RATE_LIMIT_UPDATE_FIELD.Mode]: RATE_LIMIT_UPDATE_MODE.Replace,
      [RATE_LIMIT_UPDATE_FIELD.Values]: {},
    })
    const card = await openAgentInfoCard(native.page)
    await expect(card.getByText(/^Gemini Models · /)).toHaveCount(0)
    await expect(card.getByText(/\b(?:Allowed|Exceeded)\b|% used/)).toHaveCount(0)
  }
  finally {
    watch.cancel()
  }
})
