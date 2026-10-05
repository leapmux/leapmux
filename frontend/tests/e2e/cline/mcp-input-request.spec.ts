import { existsSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { applyPermissionPreset, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectUnansweredMcpInput } from '../helpers/unsupportedMcpInput'
import { clineToolError } from './toolError'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

/**
 * The request budget of the probe server, in seconds: Cline's own `timeout` field.
 *
 * Cline's stdio MCP client never answers an input request. It reads a message
 * only as the reply to one of its own pending requests, matched by a numeric
 * id, and it drops every other message, a server's own request included
 * (`StdioMcpClient.handleStdout` in Cline's
 * `sdk/packages/core/src/extensions/mcp/client.ts`). Its initialize declares no
 * elicitation capability either. The `tools/call` that waits on the input
 * request therefore ends only when this budget runs out, with Cline's own
 * timeout error. The default budget is 60 seconds. A configured budget also
 * limits the initialize, and 10 seconds is enough for a local Node server.
 */
const MCP_TIMEOUT_SECONDS = 10

clineTest('times out an MCP input request that the native client never answers, without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const workingDir = createTestDirectory('cline-native-mcp-unanswered-')
  const receiptLog = join(workingDir, 'native-mcp-receipt.json')
  const script = writeMcpFormServer(workingDir, 'native-form-server.mjs', { receiptLog })
  const config = join(environment.CLINE_DATA_DIR!, 'settings', 'cline_mcp_settings.json')
  const servers = { form_probe: { transport: { type: 'stdio', command: process.execPath, args: [script] }, timeout: MCP_TIMEOUT_SECONDS } }
  await withNativeConfigurationFile({ path: config, content: JSON.stringify({ mcpServers: servers }), runDir: getGlobalState().tmpDir }, async () => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
      agentProvider: AgentProvider.CLINE,
      ...agentOpenOptions(agentSettings(AgentProvider.CLINE)),
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await applyPermissionPreset(page, 'bypass')
    await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'ask'))).toBe(true)
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CLINE, readToolResult: clineToolError }
    const callId = 'native-cline-form-unanswered'
    await expectUnansweredMcpInput(context, {
      receiptLog,
      callId,
      nativeFailureText: `MCP request to "form_probe" (tools/call) timed out after ${MCP_TIMEOUT_SECONDS}s.`,
      invoke: async () => {
        await modelScript.queue(
          { toolCalls: [mcpToolCall(AgentProvider.CLINE, callId, { server: 'form_probe', tool: 'ask', input: {} })] },
          { text: 'The native MCP timeout reached the model.' },
        )
        await sendMessage(page, modelScript.prompt('Call the registered native probe form once.'))
        const status = await modelScript.waitForSteps(2)
        await waitForAgentIdle(page)
        const request = status.requests.find(record => record.stepIndex === 1)
        if (!request)
          throw new Error('The native MCP timeout reached no following model request.')
        return request
      },
    })
  })
})
