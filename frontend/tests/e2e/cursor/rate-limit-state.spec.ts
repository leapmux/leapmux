import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { messageContents, resumeQueueAfterFailure, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

cursorTest('reports actual native quota refusal without a quota meter', async ({ native }) => {
  const { page, modelScript } = native
  await expectNoRateLimitState(native, { relatedProof: async () => {
    const start = await modelScript.queue({ error: { status: 429, message: 'The native Cursor quota request was refused.' } })
    await sendMessage(page, modelScript.prompt('Complete the actual native quota probe.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: 'Upgrade your plan to continue' }).first()).toBeVisible()
    expect((await modelScript.requestAt(start)).response?.serviceError).toBeDefined()
    // Cursor reports the refusal as the end of its turn, so the queue keeps running.
    await resumeQueueAfterFailure(page, 'running')
    await sendNativeAnswer(native, 'Reply once after the actual quota refusal.', 'The actual native quota recovery completed.')
  } })
})
