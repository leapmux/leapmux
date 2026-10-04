import type { McpServerReceipt } from '../helpers/mcpServerReceipt'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { hubSpawnEnv } from '../helpers/server'
import { assistantBubbles, loginViaToken, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { createGitRepo } from '../helpers/worktree'

/** Validate Cursor's automatic decline through its actual SDK capabilities and correlated native reply. */
export function cursorAutomaticMcpDecline(receipt: McpServerReceipt) {
  const capabilities = receipt.initializeCapabilities
  if (!capabilities || !isObject(capabilities.elicitation) || !isObject(capabilities.elicitation.form))
    throw new Error('The Cursor MCP decline requires its actual native form capability.')
  if (!receipt.toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'ask')))
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
  const workingDir = createGitRepo(parent, 'repo')
  const receiptLog = join(parent, 'native-mcp-receipt.json')
  const echoArguments = { query: 'cursor', limit: 0, tail: 'END_MCP_ARGUMENTS' }
  const script = writeMcpFormServer(parent, 'form-server.mjs', { expectedEchoArguments: echoArguments, receiptLog })
  const configDir = join(workingDir, '.cursor')
  mkdirSync(configDir)
  writeFileSync(join(configDir, 'mcp.json'), JSON.stringify({ mcpServers: {
    form_probe: { command: process.execPath, args: [script] },
  } }))
  const approved = execFileSync('agent', ['mcp', 'enable', 'form_probe'], {
    cwd: workingDir,
    env: hubSpawnEnv(leapmuxServer.agentEnv),
    encoding: 'utf8',
  })
  expect(approved).toContain('Enabled and approved MCP server')
  const tools = execFileSync('agent', ['mcp', 'list-tools', 'form_probe'], {
    cwd: workingDir,
    env: hubSpawnEnv(leapmuxServer.agentEnv),
    encoding: 'utf8',
  })
  expect(tools).toContain('ask')
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, workingDir, {
    agentProvider: AgentProvider.CURSOR,
    ...agentOpenOptions(agentSettings(AgentProvider.CURSOR)),
  })
  await loginViaToken(page, leapmuxServer.adminToken)
  await openWorkspace(page, context.workspaceId)

  await expectNoNativeControl(context, {
    testId: 'elicitation-form',
    additionalTestIds: ['control-question-group'],
    relatedControl: async () => {
      await modelScript.queue({ toolCalls: [mcpToolCall(AgentProvider.CURSOR, 'cursor-form', { server: 'form_probe', tool: 'ask', input: {} })] })
      await sendMessage(page, modelScript.prompt('Call the form_probe ask tool exactly once.'))
      await modelScript.waitForSteps()
      await waitForAgentIdle(page)
      const decline = cursorAutomaticMcpDecline(readMcpServerReceipt(receiptLog))
      await expect(assistantBubbles(page).filter({ hasText: decline.toolResult.text }).first()).toBeVisible()
      await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
    },
  })
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_DECLINED' }).first()).toBeVisible()

  await modelScript.queue(
    { toolCalls: [mcpToolCall(AgentProvider.CURSOR, 'cursor-echo', { server: 'form_probe', tool: 'echo', input: echoArguments })] },
    { text: 'The later turn ended.' },
  )
  await sendMessage(page, modelScript.prompt('Call the form_probe echo tool with the supplied values.'))
  await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const echoResult = readMcpServerReceipt(receiptLog).toolResults.findLast(value => value.tool === 'echo')
  if (!echoResult)
    throw new Error('The actual Cursor MCP echo has no native server result.')
  expect(echoResult.isError).toBe(false)
  expect(echoResult.text).toBe('PERMISSION_ACCEPTED')
  await expect(assistantBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
  await sendMessage(page, modelScript.prompt('Use the result from the form_probe echo tool.'))
  const status = await modelScript.waitForSteps(3)
  await waitForAgentIdle(page)
  const echoRequest = status.requests.find(request => request.stepIndex === 1)
  const followUpRequest = status.requests.find(request => request.stepIndex === 2)
  if (!echoRequest || !followUpRequest || !isObject(echoRequest.body) || typeof echoRequest.body.conversationId !== 'string')
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
