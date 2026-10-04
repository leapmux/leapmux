import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { diracTest } from '../dirac-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, tabById } from '../helpers/ui'
import { mcpDiracTest } from './fixtures'
import { readDiracMcpSessionObservation } from './mcpConfiguration'
import { nativeContext } from './scenarios'

diracTest('offers no project MCP input route in the actual native tool catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const workingDir = createTestDirectory('dirac-project-form-')
  const receiptLog = join(workingDir, 'project-form-receipt.json')
  const script = writeMcpFormServer(workingDir, 'project-form.mjs', { receiptLog })
  writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, workingDir, {
    agentProvider: AgentProvider.DIRAC,
    ...agentOpenOptions(agentSettings(AgentProvider.DIRAC)),
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await tabById(page, agentId).click()
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const request = await sendNativeAnswer(context, 'Return one actual native answer from the configured project.', 'The native project capability probe completed.')
  expect(nativeModelToolNames(request).some(name => name.includes('form_probe'))).toBe(false)
  expect(existsSync(receiptLog)).toBe(false)
  await expect(page.locator('[data-testid="elicitation-form"]:visible')).toHaveCount(0)
  await page.reload()
  await expect(page.locator('[data-testid="elicitation-form"]:visible')).toHaveCount(0)
})

mcpDiracTest('starts no MCP form client from its actual configured native ACP server list', async ({ configuredMcpDiracWorkspace, page, modelScript }) => {
  const workspace = configuredMcpDiracWorkspace
  const context = await nativeContext({ page, modelScript, leapmuxServer: workspace.server, workspaceId: workspace.workspaceId })
  const observation = readDiracMcpSessionObservation(workspace.sessionReceipt)
  expect(observation.request.params).toEqual(expect.objectContaining({ cwd: workspace.workingDir, mcpServers: workspace.configuredServers }))
  expect(observation.sessionId).toBe((await currentNativeAgent(context)).agentSessionId)
  await expectNoNativeControl(context, {
    testId: 'elicitation-form',
    additionalTestIds: ['control-banner'],
    relatedControl: async () => {
      const request = await sendNativeAnswer(context, 'Complete a real native turn with the configured form server.', 'The configured form discovery turn completed.')
      const tools = nativeModelToolNames(request)
      expect(tools).toContain('respond')
      expect(tools.some(name => /mcp|form_probe/i.test(name))).toBe(false)
      expect(existsSync(workspace.formReceipt)).toBe(false)
    },
  })
})
