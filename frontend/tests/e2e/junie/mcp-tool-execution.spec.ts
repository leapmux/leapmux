import { expect } from '@playwright/test'
import { invokeNativeMcpTool, nativeMcpAnswer } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { createTestDirectory } from '../helpers/runDirectory'
import { messageBubbles, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { junieTest } from '../junie-fixtures'
import { junieCapabilityAnswer } from './housekeeping'
import { writeJunieMcpConfig } from './mcpConfig'
import { JUNIE_AGENT, nativeContext } from './scenarios'

junieTest.describe('native mcp tool execution', () => {
  junieTest('runs a project MCP tool through the native agent', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const echoArguments = { query: 'probe', limit: 0, tail: 'END_MCP_ARGUMENTS' }
    const server = writeMcpFormServer(createTestDirectory('junie-mcp-echo-'), 'echo-server.mjs', { expectedEchoArguments: echoArguments })
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'on' }, prepare: (workingDir) => {
      writeJunieMcpConfig(workingDir, server.name, server.command, [...server.args])
    } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    // Junie asks its capability filter which listed tool the request needs. The echo tool is the second one.
    await modelScript.rule(junieCapabilityAnswer('junie-echo-capability', '2'))
    const callId = 'junie-mcp-echo'
    // Brave mode runs the tool without a request, so the call must raise no banner.
    await expectNoNativeControl(context, {
      testId: 'control-banner',
      relatedProof: async () => {
        const request = await invokeNativeMcpTool(context, { server: server.name, tool: 'echo', callId, input: echoArguments })
        expect(nativeToolResult(request, callId)).toContain('PERMISSION_ACCEPTED')
      },
    })
    await expect(messageBubbles(page).filter({ hasText: nativeMcpAnswer(callId) }).first()).toBeVisible()
  })
})
