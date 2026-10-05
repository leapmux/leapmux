import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { SendAgentRawMessageRequestSchema, SendAgentRawMessageResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { getTestChannel, openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'

/** Build the installed app-server's native project MCP connection request. */
export function zcodeMcpConnectFrame(requestId: string, workingDir: string): string {
  if (!requestId || !workingDir)
    throw new Error('The native ZCode MCP request requires a request ID and working directory.')
  return JSON.stringify({ id: requestId, method: 'mcp/list', params: { workspace: { workspacePath: workingDir, workspaceKey: workingDir }, mode: 'connect' } })
}

/** Prove the installed client's actual MCP form refusal through its native project configuration. */
export async function exerciseZCodeMcpInputLimit(context: ManagedNativeScenarioContext): Promise<void> {
  const directory = createTestDirectory('zcode-native-mcp-')
  const receiptLog = join(directory, 'mcp-receipt.json')
  const script = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  mkdirSync(join(directory, '.zcode'))
  writeFileSync(join(directory, '.zcode', 'config.json'), JSON.stringify({ mcp: { servers: { form_probe: { type: 'stdio', command: process.execPath, args: [script], env: {} } } } }))
  const server = context.leapmuxServer
  await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, context.workspaceId, directory, {
    agentProvider: context.provider,
    ...agentOpenOptions(agentSettings(context.provider)),
  })
  await openWorkspace(context.page, context.workspaceId)
  const agent = await currentNativeAgent(context)
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  await channel.callWorker(server.workerId, 'SendAgentRawMessage', SendAgentRawMessageRequestSchema, SendAgentRawMessageResponseSchema, {
    agentId: agent.id,
    content: zcodeMcpConnectFrame(`native-mcp-${crypto.randomUUID()}`, directory),
  })
  await expect.poll(() => existsSync(receiptLog) ? readMcpServerReceipt(receiptLog).toolCatalogs.flatMap(catalog => catalog.tools.map(tool => tool.name)) : []).toContain('ask')
  const catalog = await sendNativeAnswer(context, 'Reply once after the native MCP server connects.', 'The actual native MCP catalog reached the model.')
  expect(nativeModelToolNames(catalog)).toContain('mcp__form_probe__ask')
  const callId = 'zcode-native-form'
  await expectUnsupportedMcpInput(context, {
    receiptLog,
    callId,
    invoke: async () => (await runNativeToolTurn(context, {
      toolCalls: [mcpToolCall(context.provider, callId, { server: 'form_probe', tool: 'ask', input: {} })],
      prompt: 'Call the configured native form_probe ask tool once.',
      answer: 'The actual ZCode MCP refusal reached the next model request.',
    })).resultRequest,
  })
}
