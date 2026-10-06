import { join } from 'node:path'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { nativeMcpRefusal, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'
import { openProviderAgent } from '../helpers/workspace'
import { JUNIE_AGENT, expect as junieExpect, junieTest } from '../junie-fixtures'
import { junieCapabilityAnswer } from './housekeeping'
import { writeJunieMcpConfig } from './mcpConfig'
import { nativeContext } from './scenarios'

junieTest.describe('Junie MCP input form', () => {
  junieTest('declines a local MCP form request without opening a browser form', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    const directory = createTestDirectory('junie-mcp-form-')
    const receiptLog = join(directory, 'form-receipt.json')
    const server = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'on' }, prepare: (workingDir) => {
      writeJunieMcpConfig(workingDir, server.name, server.command, [...server.args])
    } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    // Junie asks its capability filter which listed tool the request needs. The ask tool is the first one.
    await modelScript.rule(junieCapabilityAnswer('junie-mcp-capability', '1'))
    const callId = 'junie-mcp-form'
    await expectUnsupportedMcpInput(context, { receiptLog, callId, additionalTestIds: ['control-banner'], invoke: () => invokeNativeMcpTool(context, { server: server.name, tool: 'ask', callId, input: {} }) })
    const receipt = readMcpServerReceipt(receiptLog)
    await testInfo.attach('junie-mcp-form-native-reply', { body: JSON.stringify(receipt), contentType: 'application/json' })
    const refusal = nativeMcpRefusal(receipt)
    junieExpect(refusal.reply.error).toMatchObject({ code: -32601, message: 'Server does not support elicitation/create' })
    junieExpect(refusal.toolResult.id).toBe(refusal.request.toolRequestId)
  })
})
