import { existsSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { copilotTest } from '../copilot-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { fillMcpProbeForm, waitForMcpProbeFormDraft } from '../helpers/mcpProbeForm'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

copilotTest('submits zero and false through a native MCP form after reload', async ({ authenticatedEmptyWorkspace, leapmuxServer, modelScript, page }) => {
  const directory = createTestDirectory('copilot-mcp-form-')
  const script = writeMcpFormServer(directory, 'form-server.mjs')
  const home = leapmuxServer.agentEnv.COPILOT_HOME
  if (!home)
    throw new Error('the isolated Copilot home is unavailable')
  const config = join(home, 'mcp-config.json')
  if (existsSync(config))
    throw new Error('the isolated Copilot MCP configuration already exists')
  writeFileSync(config, JSON.stringify({
    mcpServers: { form_probe: { type: 'local', command: process.execPath, args: [script], tools: ['*'] } },
  }))
  try {
    const settings = agentOpenOptions(agentSettings(AgentProvider.GITHUB_COPILOT))
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.GITHUB_COPILOT,
      ...settings,
      optionValues: { ...settings.optionValues, permissionMode: COPILOT_PERMISSION_MODE.AllowAll },
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue({ text: 'The Copilot MCP server is ready.' })
    await sendMessage(page, modelScript.prompt('Confirm the MCP server is available.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const body = JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)
    const names = [...body.matchAll(/"name":"([^"]*form_probe[^"]*)"/g)].map(match => match[1])
    expect(names).toContain('form_probe-ask')

    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.GITHUB_COPILOT, 'copilot-form', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'The Copilot form completed.' },
    )
    await sendMessage(page, modelScript.prompt('Call form_probe ask exactly once.'))
    await modelScript.waitForSteps(2)
    const form = await fillMcpProbeForm(page)
    await waitForMcpProbeFormDraft(page, leapmuxServer.adminUserId)
    await page.reload()
    await expect(form.getByLabel('Count *')).toHaveValue('0')
    await expect(form.getByRole('button', { name: 'Enabled *', exact: true })).toHaveText('No')
    await expect(form.getByRole('button', { name: 'Color *', exact: true })).toHaveText('Blue')
    await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()

    const continued = await modelScript.waitForSteps(3)
    await waitForAgentIdle(page)
    expect(JSON.stringify(continued.requests.find(request => request.stepIndex === 2)?.body)).toContain('FORM_ROUND_TRIP_OK')
    await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
    await expect(form).toHaveCount(0)
  }
  finally {
    unlinkSync(config)
  }
})
