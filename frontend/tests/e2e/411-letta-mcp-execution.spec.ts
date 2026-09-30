import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { openAgentViaAPI } from './helpers/api'
import { writeMcpImageServer } from './helpers/mcpImageServer'
import { createTestDirectory } from './helpers/runDirectory'
import { writeToolImage } from './helpers/toolImages'
import { loginViaToken, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

lettaTest('offers no local MCP tool to the model on its App Server path', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const workingDir = createTestDirectory('letta-mcp-')
  const imageName = writeToolImage(workingDir, 'letta-mcp')
  const server = writeMcpImageServer(workingDir, imageName)
  writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({
    mcpServers: { image_probe: { command: server.command, args: server.args } },
  }))
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
    agentProvider: AgentProvider.LETTA,
    ...agentOpenOptions(agentSettings(AgentProvider.LETTA)),
  })
  await loginViaToken(page, leapmuxServer.adminToken)
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await modelScript.rule(LETTA_TITLE_RULE)
  await modelScript.queue({ text: 'The native turn ended.' })
  await sendMessage(page, modelScript.prompt('Reply once.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)

  const request = status.requests.find(record => record.stepIndex === 0)
  const tools = (request?.body as { tools?: Array<{ function?: { name?: string }, name?: string }> } | undefined)?.tools ?? []
  expect(tools.length).toBeGreaterThan(0)
  expect(tools.map(tool => tool.function?.name ?? tool.name).filter(Boolean).some(name => name?.startsWith('mcp__'))).toBe(false)
  expect(existsSync(server.ready)).toBe(false)
})
