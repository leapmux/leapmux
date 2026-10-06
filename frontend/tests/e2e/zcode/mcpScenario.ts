import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { SendAgentRawMessageRequestSchema, SendAgentRawMessageResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { getTestChannel, openAgentViaAPI } from '../helpers/api'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { newNativeWorkingDir } from '../helpers/nativeAgentOpen'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
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
  const directory = newNativeWorkingDir(context, 'zcode-native-mcp-')
  const receiptLog = join(directory, 'mcp-receipt.json')
  const formServer = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  mkdirSync(join(directory, '.zcode'))
  writeFileSync(join(directory, '.zcode', 'config.json'), JSON.stringify({ mcp: { servers: { [formServer.name]: { type: 'stdio', command: formServer.command, args: formServer.args, env: {} } } } }))
  const server = context.leapmuxServer
  await openAgentViaAPI(server, context.workspaceId, directory, agentOpenOptions(context.provider))
  await openWorkspace(context.page, context.workspaceId)
  const agent = await currentNativeAgent(context)
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  await channel.callWorker(server.workerId, 'SendAgentRawMessage', SendAgentRawMessageRequestSchema, SendAgentRawMessageResponseSchema, {
    agentId: agent.id,
    content: zcodeMcpConnectFrame(`native-mcp-${randomUUID()}`, directory),
  })
  await waitForMcpToolListed(receiptLog, 'ask')
  const callId = 'zcode-native-form'
  const call = { server: formServer.name, tool: 'ask', callId, input: {} }
  const catalog = await sendNativeAnswer(context, 'Reply once after the native MCP server connects.', 'The actual native MCP catalog reached the model.')
  expect(nativeModelToolNames(catalog)).toContain(mcpToolCall(context.provider, callId, call).name)
  await expectUnsupportedMcpInput(context, { receiptLog, callId, invoke: () => invokeNativeMcpTool(context, call) })
}
