import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { SendAgentRawMessageRequestSchema, SendAgentRawMessageResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { getTestChannel, openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { readMcpServerReceipt, waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { newNativeWorkingDir } from '../helpers/nativeAgentOpen'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { answerControl, openWorkspace, waitForControlBanner } from '../helpers/ui'

/** Build the installed app-server's native project MCP connection request. */
export function zcodeMcpConnectFrame(requestId: string, workingDir: string): string {
  if (!requestId || !workingDir)
    throw new Error('The native ZCode MCP request requires a request ID and working directory.')
  return JSON.stringify({
    id: requestId,
    method: 'mcp/list',
    params: {
      workspace: { workspacePath: workingDir, workspaceKey: workingDir },
      mode: 'connect',
    },
  })
}

/**
 * Round-trip a project MCP server's form through ZCode. ZCode forwards the form as a user-input request, and the browser
 * answers it with the typed MCP form, whose values the worker turns back into the text answers that ZCode reads. The
 * tool asks for approval first, because the default mode asks before a tool with side effects runs.
 */
export async function exerciseZCodeMcpForm(context: ManagedNativeScenarioContext): Promise<void> {
  const directory = newNativeWorkingDir(context, 'zcode-native-mcp-')
  const receiptLog = join(directory, 'mcp-receipt.json')
  const formServer = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  mkdirSync(join(directory, '.zcode'))
  writeFileSync(join(directory, '.zcode', 'config.json'), JSON.stringify({
    mcp: {
      servers: {
        [formServer.name]: {
          type: 'stdio',
          command: formServer.command,
          args: formServer.args,
          env: {},
        },
      },
    },
  }))
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
  const toolName = mcpToolCall(context.provider, callId, { server: formServer.name, tool: 'ask', input: {} }).name
  const catalog = await sendNativeAnswer(context, 'Reply once after the native MCP server connects.', 'The actual native MCP catalog reached the model.')
  expect(nativeModelToolNames(catalog)).toContain(toolName)
  await exerciseMcpProbeFormRoundTrip(context, {
    callId,
    reloadBeforeSubmit: true,
    approveTool: async () => {
      await expect(await waitForControlBanner(context.page)).toContainText(toolName)
      await answerControl(context.page, 'allow')
    },
  })
  const replies = readMcpServerReceipt(receiptLog).elicitationReplies
  expect(replies.map(reply => reply.kind === 'result'
    ? reply.result
    : reply), 'the server received the accepted typed answers once').toEqual([
    {
      action: 'accept',
      content: { count: 0, enabled: false, color: 'b' },
    },
  ])
}
