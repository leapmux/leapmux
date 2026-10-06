import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { applyPermissionPreset, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'

ampTest('returns the actual native MCP unsupported-method reply without a browser form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const workingDir = createTestDirectory('amp-native-mcp-refusal-')
  const receiptLog = join(workingDir, 'native-mcp-receipt.json')
  const script = writeMcpFormServer(workingDir, 'native-form-server.mjs', { receiptLog })
  const config = join(environment.XDG_CONFIG_HOME!, 'amp', 'settings.json')
  await withNativeConfigurationFile({ path: config, content: JSON.stringify({ ...(existsSync(config) ? JSON.parse(readFileSync(config, 'utf8')) : {}), 'amp.mcpServers': { form_probe: { command: process.execPath, args: [script] } } }), runDir: getGlobalState().tmpDir }, async () => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
      agentProvider: AgentProvider.AMP,
      ...agentOpenOptions(agentSettings(AgentProvider.AMP)),
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await applyPermissionPreset(page, 'bypass')
    const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.AMP }
    context.readToolResult = ampToolResultReader(context)
    // The Worker starts the Amp process for the first message, and Amp starts its MCP servers with that process.
    // So no server can list its tools before a first turn.
    await sendNativeAnswer(context, 'Start the native session that loads the registered form server.', 'The native session started.')
    await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'ask'))).toBe(true)
    const callId = 'native-amp-form-refusal'
    await expectUnsupportedMcpInput(context, { receiptLog, callId, invoke: async () => {
      const start = (await modelScript.status()).stepCount
      await modelScript.queue(
        { toolCalls: [mcpToolCall(AgentProvider.AMP, callId, { server: 'form_probe', tool: 'ask', input: {} })] },
        { text: 'The native MCP refusal reached the model.' },
      )
      await sendMessage(page, modelScript.prompt('Call the registered native probe form once.'))
      const status = await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      const request = status.requests.find(record => record.stepIndex === start + 1)
      if (!request)
        throw new Error('The native MCP refusal reached no following model request.')
      return request
    } })
  })
})
