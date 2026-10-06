import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLAUDE_AGENT, claudeTest } from '../claude-fixtures'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

claudeTest.describe('Claude Code MCP input form', () => {
  claudeTest('sends zero and false form values back to the native MCP tool', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const directory = createTestDirectory('claude-mcp-form-')
    const script = writeMcpFormServer(directory, 'form-server.mjs')
    writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, CLAUDE_AGENT, { workingDir: directory, optionValues: { permissionMode: 'bypassPermissions' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.CLAUDE_CODE, 'claude-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'The form completed.' },
    )
    await sendMessage(page, modelScript.prompt('Call form_probe ask exactly once.'))
    await modelScript.waitForSteps(1)

    const form = page.getByTestId('elicitation-form').filter({ visible: true })
    await expect(form).toBeVisible()
    await form.getByLabel('Count *').fill('0')
    await form.getByRole('button', { name: 'Enabled *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'No', exact: true }).click()
    await form.getByRole('button', { name: 'Color *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Blue', exact: true }).click()
    await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('FORM_ROUND_TRIP_OK')
    await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
    await expect(form).toHaveCount(0)
  })
})
