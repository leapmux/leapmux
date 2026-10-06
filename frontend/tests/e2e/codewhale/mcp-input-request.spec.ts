import { existsSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { codewhaleTest } from '../codewhale-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { applyPermissionPreset, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectUnansweredMcpInput } from '../helpers/unsupportedMcpInput'

/**
 * The time limit of a call to the probe server, in seconds: Codewhale's own
 * `execute_timeout` field.
 *
 * Codewhale's MCP client never answers an input request. Its `initialize`
 * declares no elicitation capability. While it waits for the reply to its own
 * request, it skips every message whose id differs, and a server's own request
 * is such a message (`McpConnection::recv` in Codewhale's `tui/src/mcp.rs`).
 * The `tools/call` that waits on the input request therefore ends only when
 * this limit expires, with Codewhale's own timeout error. The default limit is
 * 60 seconds. The limit applies to tool calls only. `connect_timeout` limits
 * the initialize.
 */
const MCP_EXECUTE_TIMEOUT_SECONDS = 10

codewhaleTest('times out an MCP input request that the native client never answers, without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const workingDir = createTestDirectory('codewhale-native-mcp-unanswered-')
  const receiptLog = join(workingDir, 'native-mcp-receipt.json')
  const script = writeMcpFormServer(workingDir, 'native-form-server.mjs', { receiptLog })
  const config = join(environment.CODEWHALE_HOME!, 'mcp.json')
  const servers = { form_probe: { command: process.execPath, args: [script], execute_timeout: MCP_EXECUTE_TIMEOUT_SECONDS } }
  await withNativeConfigurationFile({ path: config, content: JSON.stringify({ servers }), runDir: getGlobalState().tmpDir }, async () => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
      agentProvider: AgentProvider.CODEWHALE,
      ...agentOpenOptions(agentSettings(AgentProvider.CODEWHALE)),
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await applyPermissionPreset(page, 'bypass')
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
    const callId = 'native-codewhale-form-unanswered'
    await expectUnansweredMcpInput(context, {
      receiptLog,
      callId,
      nativeFailureText: `MCP tool failed: MCP method 'tools/call' on server 'form_probe' timed out after ${MCP_EXECUTE_TIMEOUT_SECONDS}s`,
      invoke: async () => {
        await modelScript.queue(
          { toolCalls: [mcpToolCall(AgentProvider.CODEWHALE, callId, { server: 'form_probe', tool: 'ask', input: {} })] },
          { text: 'The native MCP timeout reached the model.' },
        )
        await sendMessage(page, modelScript.prompt('Call the registered native probe form once.'))
        // The direct MCP call initializes Codewhale's lazy server pool.
        await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'ask'))).toBe(true)
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
