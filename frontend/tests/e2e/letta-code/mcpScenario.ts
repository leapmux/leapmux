import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { PrivateMcpLettaWorkspace } from './fixtures'
import { realpathSync } from 'node:fs'
import { expect } from '@playwright/test'
import { requireBinary } from '../helpers/binaryOnPath'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { lettaMcpCatalogArguments, lettaMcpCatalogToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles } from '../helpers/ui'
import { parseLettaMcpCatalog } from './mcpCatalog'
import { readLettaMcpCliReceipt, writeLettaMcpCliCapture } from './mcpCliReceipt'

/** Require the registered native catalog and the model's own MCP reminder. */
export async function exerciseLettaMcpCatalog(context: ManagedNativeScenarioContext, workspace: PrivateMcpLettaWorkspace, agentId: string, server: string) {
  const callId = `letta-native-${server}-catalog`
  const answer = `The ${server} native catalog reached the agent.`
  const executable = realpathSync(requireBinary('letta', 'The private Letta MCP catalog requires its actual resolved CLI executable', workspace.server.agentEnv))
  const capture = writeLettaMcpCliCapture(workspace.runDirectory)
  const toolCall = lettaMcpCatalogToolCall(callId, {
    agentId,
    server,
    executable,
    nodeExecutable: workspace.nodeExecutable,
    captureScriptPath: capture.scriptPath,
    receiptId: capture.receiptId,
  })
  const { toolRequest, resultRequest } = await runNativeToolTurn(context, {
    toolCalls: [toolCall],
    prompt: 'Read the actual registered MCP catalog before its tool call.',
    answer,
  })
  const instructions = nativeModelInstructionText(toolRequest)
  expect(instructions).toContain(server)
  expect(instructions).toContain('letta mcp call')
  const result = nativeToolResult(resultRequest, callId)
  const receipt = readLettaMcpCliReceipt(capture.receiptPath, result, {
    receiptId: capture.receiptId,
    callId,
    executable,
    args: lettaMcpCatalogArguments(agentId, server),
  })
  const catalog = parseLettaMcpCatalog(receipt)
  await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
  return catalog
}
