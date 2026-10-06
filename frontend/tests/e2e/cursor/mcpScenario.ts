import type { McpServerReceipt } from '../helpers/mcpServerReceipt'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { mcpReceiptListsTool, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { deliberateWorkingDir } from '../helpers/providerWorkingDir'
import { createTestDirectory } from '../helpers/runDirectory'
import { hubSpawnEnv } from '../helpers/server'
import { assistantBubbles, loginViaToken, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { createGitRepo } from '../helpers/worktree'

/** Validate Cursor's automatic decline through its actual SDK capabilities and correlated native reply. */
export function cursorAutomaticMcpDecline(receipt: McpServerReceipt) {
  const capabilities = receipt.initializeCapabilities
  if (!capabilities || !isObject(capabilities.elicitation) || !isObject(capabilities.elicitation.form))
    throw new Error('The Cursor MCP decline requires its actual native form capability.')
  if (!mcpReceiptListsTool(receipt, 'ask'))
    throw new Error('The Cursor MCP decline requires its actual native ask catalog.')
  const request = receipt.elicitationRequests.at(-1)
  if (!request || request.params.mode !== 'form' || !isObject(request.params.requestedSchema))
    throw new Error('The Cursor MCP decline requires the actual form request.')
  const reply = receipt.elicitationReplies.findLast(value => value.id === request.id)
  if (!reply || reply.kind !== 'result' || reply.result.action !== 'decline' || Object.hasOwn(reply.result, 'content'))
    throw new Error('The Cursor MCP form has no matching automatic decline reply.')
  const toolResult = receipt.toolResults.findLast(value => value.id === request.toolRequestId && value.tool === 'ask')
  if (!toolResult || toolResult.isError || toolResult.text !== 'FORM_ROUND_TRIP_DECLINED')
    throw new Error('The Cursor MCP decline has no exact matched native tool result.')
  return { request, reply, toolResult }
}

/** Execute real Cursor MCP calls and retain their actual result in the next server conversation. */
export async function exerciseCursorMcpSession(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript, leapmuxServer } = context
  const parent = createTestDirectory('cursor-mcp-form-')
  const workingDir = deliberateWorkingDir(
    createGitRepo(parent, 'repo'),
    'Cursor reads `.cursor/mcp.json` and keeps the approval of `agent mcp enable` for the project of its directory. A repository of its own makes the directory that project, and the server and its receipt stay outside it in the parent.',
  )
  const receiptLog = join(parent, 'native-mcp-receipt.json')
  const echoArguments = { query: 'cursor', limit: 0, tail: 'END_MCP_ARGUMENTS' }
  const server = writeMcpFormServer(parent, 'form-server.mjs', { expectedEchoArguments: echoArguments, receiptLog })
  const configDir = join(workingDir, '.cursor')
  mkdirSync(configDir)
  writeFileSync(join(configDir, 'mcp.json'), JSON.stringify(mcpServersConfig(server)))
  const approved = execFileSync('agent', ['mcp', 'enable', server.name], {
    cwd: workingDir,
    env: hubSpawnEnv(leapmuxServer.agentEnv),
    encoding: 'utf8',
  })
  expect(approved).toContain('Enabled and approved MCP server')
  const tools = execFileSync('agent', ['mcp', 'list-tools', server.name], {
    cwd: workingDir,
    env: hubSpawnEnv(leapmuxServer.agentEnv),
    encoding: 'utf8',
  })
  expect(tools).toContain('ask')
  await openProviderAgent(leapmuxServer, context.workspaceId, context.providerAgent, { workingDir })
  await loginViaToken(page, leapmuxServer.adminToken)
  await openWorkspace(page, context.workspaceId)

  await expectNoNativeControl(context, {
    testId: 'elicitation-form',
    additionalTestIds: ['control-question-group'],
    relatedProof: async () => {
      // Cursor answers its own Run with the tool result, so the turn uses one model step.
      const form = await modelScript.queue({ toolCalls: [mcpToolCall(context.provider, 'cursor-form', { server: server.name, tool: 'ask', input: {} })] })
      await sendMessage(page, modelScript.prompt(`Call the ${server.name} ask tool exactly once.`))
      await modelScript.waitForSteps(form + 1)
      await waitForAgentIdle(page)
      const decline = cursorAutomaticMcpDecline(readMcpServerReceipt(receiptLog))
      await expect(assistantBubbles(page).filter({ hasText: decline.toolResult.text }).first()).toBeVisible()
      await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
    },
  })
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_DECLINED' }).first()).toBeVisible()

  const echo = await modelScript.queue({ toolCalls: [mcpToolCall(context.provider, 'cursor-echo', { server: server.name, tool: 'echo', input: echoArguments })] })
  await sendMessage(page, modelScript.prompt(`Call the ${server.name} echo tool with the supplied values.`))
  await modelScript.waitForSteps(echo + 1)
  await waitForAgentIdle(page)
  const echoResult = readMcpServerReceipt(receiptLog).toolResults.findLast(value => value.tool === 'echo')
  if (!echoResult)
    throw new Error('The actual Cursor MCP echo has no native server result.')
  expect(echoResult.isError).toBe(false)
  expect(echoResult.text).toBe('PERMISSION_ACCEPTED')
  await expect(assistantBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
  const followUp = await modelScript.queue({ text: 'The later turn ended.' })
  await sendMessage(page, modelScript.prompt(`Use the result from the ${server.name} echo tool.`))
  await modelScript.waitForSteps(followUp + 1)
  await waitForAgentIdle(page)
  const echoRequest = await modelScript.requestAt(echo)
  const followUpRequest = await modelScript.requestAt(followUp)
  if (!isObject(echoRequest.body) || typeof echoRequest.body.conversationId !== 'string')
    throw new Error('The real Cursor MCP calls have no native conversation requests.')
  const conversationId = echoRequest.body.conversationId
  expect(conversationId).not.toBe('')
  expect(followUpRequest.body).toEqual(expect.objectContaining({ conversationId }))
  expect(followUpRequest.serverContext?.conversationId).toBe(conversationId)
  expect(nativeModelContextText(followUpRequest)).toContain(echoResult.text)
  expect(nativeModelContextText(followUpRequest)).toContain('FORM_ROUND_TRIP_DECLINED')
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
}
