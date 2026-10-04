import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoRateLimitState } from '../helpers/unsupportedRateLimit'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')
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
    const resume = page.locator('[data-testid="queue-pause-button"]:visible')
    if (await resume.count() > 0 && (await resume.textContent())?.includes('Resume'))
      await resume.click()
    await sendNativeAnswer(context, 'Reply once after the actual quota refusal.', 'The actual native quota recovery completed.')
  } })
})
