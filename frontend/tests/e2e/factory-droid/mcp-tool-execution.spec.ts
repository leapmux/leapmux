import { droidTest, expect } from '../droid-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { droidToolSearchToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { answerControl, messageBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { withDroidMcpWorkspace } from './mcpWorkspace'
import { readDroidToolResult } from './toolResult'

droidTest.describe('Factory Droid MCP input form', () => {
  droidTest('executes a disposable MCP tool after native permission approval', async ({ page, leapmuxServer, modelScript }) => {
    const echoArguments = { query: 'droid', limit: 0, tail: 'END_MCP_ARGUMENTS' }
    const server = writeMcpFormServer(createTestDirectory('droid-mcp-echo-'), 'droid-echo-server.mjs', { expectedEchoArguments: echoArguments })
    await withDroidMcpWorkspace({ page, modelScript, leapmuxServer }, { server, prefix: 'droid-mcp-echo' }, async (context) => {
      // The Droid builder adds `call_` to the ID, and Droid can clip it, so the result reader matches the original ID.
      const callId = 'call-echo'
      const call = mcpToolCall(context.provider, callId, { server: server.name, tool: 'echo', input: echoArguments })
      const start = await modelScript.queue(
        { toolCalls: [droidToolSearchToolCall('search-echo', `${server.name} echo`)] },
        { toolCalls: [call] },
        { text: 'The MCP echo completed.' },
      )
      await sendMessage(page, modelScript.prompt(`Load ${server.name} echo, then call it once.`))
      await modelScript.waitForSteps(start + 2)
      await expect(await waitForControlBanner(page)).toContainText(call.name)
      await answerControl(page, 'allow')
      await modelScript.waitForSteps(start + 3)
      await waitForAgentIdle(page)
      expect(readDroidToolResult(await modelScript.requestAt(start + 2), callId, call.name).text).toContain('PERMISSION_ACCEPTED')
      await expect(messageBubbles(page).filter({ hasText: 'The MCP echo completed.' }).first()).toBeVisible()
    })
  })
})
