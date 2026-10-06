import { CODEBUDDY_AGENT, CODEBUDDY_BYPASS, codebuddyTest, createCodebuddyWorkingDir, expect } from '../codebuddy-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codebuddyWaitForMcpServersToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { withCodebuddyUserMcpServer } from './mcpConfiguration'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code MCP input form', () => {
  codebuddyTest('executes a disposable MCP tool through the native server', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    const workingDir = createCodebuddyWorkingDir()
    const echoArguments = { query: 'codebuddy', limit: 0, tail: 'END_MCP_ARGUMENTS' }
    const server = writeMcpFormServer(workingDir, 'form-server.mjs', { expectedEchoArguments: echoArguments })
    await withCodebuddyUserMcpServer(leapmuxServer.agentEnv, server, async () => {
      await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, CODEBUDDY_AGENT, { ...CODEBUDDY_BYPASS, workingDir })
      await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
      const call = mcpToolCall(context.provider, 'codebuddy-mcp-echo', { server: server.name, tool: 'echo', input: echoArguments })
      const start = await modelScript.queue(
        { toolCalls: [codebuddyWaitForMcpServersToolCall('wait-for-echo', [server.name])] },
        { toolCalls: [call] },
        nativeTextStep(context, 'The MCP echo completed.'),
      )
      await sendMessage(page, modelScript.prompt(`Wait for ${server.name}, then call its echo tool.`))
      await modelScript.waitForSteps(start + 3)
      await waitForAgentIdle(page)
      expect(nativeToolResult(await modelScript.requestAt(start + 2), call.id)).toContain('PERMISSION_ACCEPTED')
      await expect(messageBubbles(page).filter({ hasText: 'The MCP echo completed.' }).first()).toBeVisible()
    })
  })
})
