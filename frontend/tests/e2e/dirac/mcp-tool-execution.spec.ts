import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { diracTest, expect } from '../dirac-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { mcpDiracTest } from './fixtures'
import { readDiracMcpSessionObservation } from './mcpConfiguration'
import { nativeContext } from './scenarios'

diracTest.describe('native mcp tool execution', () => {
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
    await waitForAgentIdle(page)

    const request = status.requests.find(record => record.stepIndex === 0)
    if (!request)
      throw new Error('The actual project discovery turn reached no model request.')
    const tools = nativeModelToolNames(request)
    expect(tools.length).toBeGreaterThan(0)
    expect(tools).not.toContain('mcp_image_probe_show')
    expect(existsSync(server.ready)).toBe(false)
  })
})

mcpDiracTest('offers no MCP tool after its actual native ACP request accepts the configured server list', async ({ configuredMcpDiracWorkspace, page, modelScript }) => {
  const workspace = configuredMcpDiracWorkspace
  const context = await nativeContext({ page, modelScript, leapmuxServer: workspace.server, workspaceId: workspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const observation = readDiracMcpSessionObservation(workspace.sessionReceipt)
  expect(observation.request.params).toEqual(expect.objectContaining({ cwd: workspace.workingDir, mcpServers: workspace.configuredServers }))
  expect(observation.sessionId).toBe(agent.agentSessionId)
  const request = await sendNativeAnswer(context, 'Complete the actual configured native server discovery turn.', 'The actual configured native server discovery turn completed.')
  const tools = nativeModelToolNames(request)
  expect(tools).toContain('respond')
  expect(tools.some(name => /mcp|form_probe/i.test(name))).toBe(false)
  expect(existsSync(workspace.formReceipt)).toBe(false)
})
