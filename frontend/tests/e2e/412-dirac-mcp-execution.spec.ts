import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from './dirac-fixtures'
import { openAgentViaAPI } from './helpers/api'
import { writeMcpImageServer } from './helpers/mcpImageServer'
import { diracRespondToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

diracTest('offers no MCP tool from a project server in its ACP session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const workingDir = createTestDirectory('dirac-mcp-')
  const server = writeMcpImageServer(workingDir, 'unused.png')
  writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({
    mcpServers: { image_probe: { command: server.command, args: server.args } },
  }))
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
    agentProvider: AgentProvider.DIRAC,
    ...agentOpenOptions(agentSettings(AgentProvider.DIRAC)),
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-mcp-answer', 'complete', 'The native turn ended.')] })
  await sendMessage(page, modelScript.prompt('Reply once.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 120_000)

  const request = status.requests.find(record => record.stepIndex === 0)
  const tools = (request?.body as { tools?: Array<{ function?: { name?: string }, name?: string }> } | undefined)?.tools ?? []
  expect(tools.length).toBeGreaterThan(0)
  expect(tools.map(tool => tool.function?.name ?? tool.name).filter(Boolean)).not.toContain('mcp_image_probe_show')
  expect(existsSync(server.ready)).toBe(false)
})
