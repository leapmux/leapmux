import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { join } from 'node:path'
import { droidTest, expect } from '../droid-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { nativeMcpRefusal, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { droidToolSearchToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { answerControl, expectNoControlBanner, messageBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { withDroidMcpWorkspace } from './mcpWorkspace'
import { readDroidToolResult } from './toolResult'

droidTest.describe('Factory Droid MCP input form', () => {
  droidTest('shows the native refusal instead of an input form', async ({ page, leapmuxServer, modelScript }) => {
    const directory = createTestDirectory('droid-mcp-form-')
    const receiptLog = join(directory, 'droid-form-receipt.json')
    const server = writeMcpFormServer(directory, 'droid-form-server.mjs', { receiptLog })
    await withDroidMcpWorkspace({ page, modelScript, leapmuxServer }, { server, prefix: 'droid-mcp-form' }, async (context) => {
      // The Droid builder adds `call_` to the ID, and Droid can clip it, so the result reader matches the original ID.
      const callId = 'call-form'
      const call = mcpToolCall(context.provider, callId, { server: server.name, tool: 'ask', input: {} })
      let resultRequest: MockModelRequestRecord | undefined
      await expectNoNativeControl(context, {
        testId: 'elicitation-form',
        relatedProof: async () => {
          const start = await modelScript.queue(
            { toolCalls: [droidToolSearchToolCall('search-form', `${server.name} ask`)] },
            { toolCalls: [call] },
            { text: 'The form request failed.' },
          )
          await sendMessage(page, modelScript.prompt(`Load ${server.name} ask, then call it once.`))
          await modelScript.waitForSteps(start + 2)
          await expect(await waitForControlBanner(page)).toContainText(call.name)
          await answerControl(page, 'allow')
          await modelScript.waitForSteps(start + 3)
          await waitForAgentIdle(page)
          resultRequest = await modelScript.requestAt(start + 2)
        },
      })
      if (!resultRequest)
        throw new Error('The Droid MCP form turn ended without its result request.')
      const refusal = nativeMcpRefusal(readMcpServerReceipt(receiptLog))
      expect(refusal.toolResult.id).toBe(refusal.request.toolRequestId)
      expect(readDroidToolResult(resultRequest, callId, call.name).text).toContain(refusal.toolResult.text)
      await expectNoControlBanner(page)
      await expect(messageBubbles(page).filter({ hasText: refusal.toolResult.text }).first()).toBeVisible()
    })
  })
})
