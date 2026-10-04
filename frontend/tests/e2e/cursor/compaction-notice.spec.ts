import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'

cursorTest('forwards compact as native prompt text without a completed notice', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await expectNoCompactionNotice(context, {
    relatedProof: async () => {
      const first = await sendNativeAnswer(context, 'Keep CURSOR_OLD_COMPACTION_CONTEXT in the native service.', 'CURSOR_OLD_COMPACTION_ANSWER')
      expect(first.serverContext?.conversationId).toBeTruthy()
      const before = await currentNativeAgent(context)
      const start = (await modelScript.status()).stepCount
      await modelScript.queue({ text: 'The actual Cursor service received the slash text.' })
      await sendMessage(page, '/compact')
      const status = await modelScript.waitForSteps(start + 1)
      const compact = status.requests.find(request => request.stepIndex === start)
      if (!compact)
        throw new Error('The native Cursor compact command reached no Run request.')
      expect(nativeModelContextText(compact)).toContain('/compact')
      expect(compact?.serverContext?.conversationId).toBe(first.serverContext?.conversationId)
      await waitForAgentIdle(page)
      const next = await sendNativeAnswer(context, 'Continue after the unsupported compact command.', 'The original Cursor context remains.')
      expect(nativeModelContextText(next)).toContain('CURSOR_OLD_COMPACTION_CONTEXT')
      expect(nativeModelContextText(next)).toContain('CURSOR_OLD_COMPACTION_ANSWER')
      expect((await currentNativeAgent(context)).agentSessionId).toBe(before.agentSessionId)
    },
  })
})
