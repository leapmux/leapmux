import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { openWorkspace, tabById } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { mcpDiracTest } from './fixtures'
import { readDiracMcpSessionObservation } from './mcpConfiguration'
import { DIRAC_AGENT, nativeContext } from './scenarios'

diracTest.describe('native mcp tool execution', () => {
  diracTest('offers no MCP tool from a project server in its ACP session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const workingDir = newProviderWorkingDir(DIRAC_AGENT, 'dirac-mcp-')
    const server = writeMcpImageServer(workingDir, 'unused.png')
    writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify(mcpServersConfig(server)))
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT, { workingDir })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await tabById(page, agentId).click()
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const request = await sendNativeAnswer(context, 'Reply once.', 'The native turn ended.')
    const tools = nativeModelToolNames(request)
    expect(tools).toContain('respond')
    expect(tools.some(name => /mcp/i.test(name) || name.includes(server.name))).toBe(false)
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
  expect(tools.some(name => /mcp/i.test(name) || workspace.configuredServers.some(server => name.includes(server.name)))).toBe(false)
  expect(existsSync(workspace.formReceipt)).toBe(false)
})
