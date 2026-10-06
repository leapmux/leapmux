import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { messageContents, resumeQueueAfterFailure, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

cursorTest('reports actual native quota refusal without a quota meter', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await expectNoRateLimitState(context, { relatedProof: async () => {
    const start = (await modelScript.status()).stepCount
    await modelScript.queue({ error: { status: 429, message: 'The native Cursor quota request was refused.' } })
    await sendMessage(page, modelScript.prompt('Complete the actual native quota probe.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: 'Upgrade your plan to continue' }).first()).toBeVisible()
    const receipt = (await modelScript.status()).requests.find(record => record.stepIndex === start)?.response
    expect(receipt?.serviceError).toBeDefined()
    // Cursor reports the refusal as the end of its turn, so the queue keeps running.
    await resumeQueueAfterFailure(page, 'running')
    await sendNativeAnswer(context, 'Reply once after the actual quota refusal.', 'The actual native quota recovery completed.')
  } })
})
