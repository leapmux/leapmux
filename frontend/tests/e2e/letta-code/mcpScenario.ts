import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { PrivateMcpLettaWorkspace } from './fixtures'
import { realpathSync } from 'node:fs'
import { expect } from '@playwright/test'
import { findBinary } from '../helpers/binaryOnPath'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { lettaMcpCatalogArguments, lettaMcpCatalogToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { parseLettaMcpCatalog } from './mcpCatalog'
import { readLettaMcpCliReceipt, writeLettaMcpCliCapture } from './mcpCliReceipt'

/** Require the registered native catalog and the model's own MCP reminder. */
export async function exerciseLettaMcpCatalog(context: ManagedNativeScenarioContext, workspace: PrivateMcpLettaWorkspace, agentId: string, server: string) {
  const start = (await context.modelScript.status()).stepCount
  const callId = `letta-native-${server}-catalog`
  const answer = `The ${server} native catalog reached the agent.`
  const resolved = findBinary('letta', workspace.server.agentEnv)
  if (!resolved)
    throw new Error('The private Letta MCP catalog requires its actual resolved CLI executable.')
  const executable = realpathSync(resolved)
  const capture = writeLettaMcpCliCapture(workspace.runDirectory)
  const toolCall = lettaMcpCatalogToolCall(callId, {
    agentId,
    server,
    executable,
    nodeExecutable: workspace.nodeExecutable,
    captureScriptPath: capture.scriptPath,
    receiptId: capture.receiptId,
  })
  await context.modelScript.queue({ toolCalls: [toolCall] }, { text: answer })
  await sendMessage(context.page, context.modelScript.prompt('Read the actual registered MCP catalog before its tool call.'))
  const status = await context.modelScript.waitForSteps(start + 2)
  const first = status.requests.find(record => record.stepIndex === start)
  if (!first)
    throw new Error('The native MCP catalog command reached no model request.')
  const instructions = nativeModelInstructionText(first)
  expect(instructions).toContain(server)
  expect(instructions).toContain('letta mcp call')
  const result = nativeToolResult(status.requests.find(record => record.stepIndex === start + 1), callId)
  const receipt = readLettaMcpCliReceipt(capture.receiptPath, result, {
    receiptId: capture.receiptId,
    callId,
    executable,
    args: lettaMcpCatalogArguments(agentId, server),
  })
  const catalog = parseLettaMcpCatalog(receipt)
  await waitForAgentIdle(context.page)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  return catalog
}
