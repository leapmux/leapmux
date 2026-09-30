import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { CURSOR_E2E_SKIP_REASON, cursorTest, expect } from './cursor-fixtures'
import { openAgentViaAPI } from './helpers/api'
import { writeMcpFormServer } from './helpers/mcpFormServer'
import { mcpToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { assistantBubbles, loginViaToken, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { createGitRepo } from './helpers/worktree'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('shows the native MCP form refusal and runs an echo tool in ACP mode', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const parent = createTestDirectory('cursor-mcp-form-')
  const workingDir = createGitRepo(parent, 'repo')
  const echoArguments = { query: 'cursor', limit: 0, tail: 'END_MCP_ARGUMENTS' }
  const script = writeMcpFormServer(parent, 'form-server.mjs', { expectedEchoArguments: echoArguments })
  const configDir = join(workingDir, '.cursor')
  mkdirSync(configDir)
  writeFileSync(join(configDir, 'mcp.json'), JSON.stringify({ mcpServers: {
    form_probe: { command: process.execPath, args: [script] },
  } }))
  const approved = execFileSync('agent', ['mcp', 'enable', 'form_probe'], {
    cwd: workingDir,
    env: { ...process.env, ...leapmuxServer.agentEnv },
    encoding: 'utf8',
  })
  expect(approved).toContain('Enabled and approved MCP server')
  const tools = execFileSync('agent', ['mcp', 'list-tools', 'form_probe'], {
    cwd: workingDir,
    env: { ...process.env, ...leapmuxServer.agentEnv },
    encoding: 'utf8',
  })
  expect(tools).toContain('ask')
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
    agentProvider: AgentProvider.CURSOR,
    ...agentOpenOptions(agentSettings(AgentProvider.CURSOR)),
  })
  await loginViaToken(page, leapmuxServer.adminToken)
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

  await modelScript.queue({ toolCalls: [mcpToolCall(AgentProvider.CURSOR, 'cursor-form', {
    server: 'form_probe',
    tool: 'ask',
    input: {},
  })] })
  await sendMessage(page, modelScript.prompt('Call the form_probe ask tool exactly once.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_DECLINED' }).first()).toBeVisible()
  await expect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_DECLINED' }).first()).toBeVisible()

  await modelScript.queue(
    { toolCalls: [mcpToolCall(AgentProvider.CURSOR, 'cursor-echo', {
      server: 'form_probe',
      tool: 'echo',
      input: echoArguments,
    })] },
    { text: 'The later turn ended.' },
  )
  await sendMessage(page, modelScript.prompt('Call the form_probe echo tool with the supplied values.'))
  await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
  await sendMessage(page, modelScript.prompt('Use the result from the form_probe echo tool.'))
  const status = await modelScript.waitForSteps(3)
  await waitForAgentIdle(page)
  const echoRequest = status.requests.find(request => request.stepIndex === 1)?.body
  const followUpRequest = status.requests.find(request => request.stepIndex === 2)?.body
  expect(echoRequest).toEqual(expect.objectContaining({ conversationId: expect.any(String) }))
  const conversationId = (echoRequest as { conversationId: string }).conversationId
  expect(conversationId).not.toBe('')
  expect(followUpRequest).toEqual(expect.objectContaining({ conversationId }))
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
})
