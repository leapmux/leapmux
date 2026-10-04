import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { fillMcpProbeForm } from '../helpers/mcpProbeForm'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { readEntry, storageKeys } from '../helpers/storage'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

test('answers a native Reasonix MCP form and preserves draft values after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const directory = createTestDirectory('reasonix-mcp-form-')
  const script = writeMcpFormServer(directory, 'form-server.mjs')
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
    agentProvider: AgentProvider.REASONIX,
    optionValues: { tool_approval: 'yolo' },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await modelScript.queue({ toolCalls: [mcpToolCall(AgentProvider.REASONIX, 'mcp-form', { server: 'form_probe', tool: 'ask', input: {} })] })
  await modelScript.queue({ text: 'Form completed.' })
  await sendMessage(page, modelScript.prompt('Call the form_probe ask tool exactly once.'))
  await modelScript.waitForSteps(1)

  const form = await fillMcpProbeForm(page)
  await expect.poll(async () => {
    const key = (await storageKeys(page)).find(key => key.includes(`control-state:${agentId}:`))
    const value = key ? (await readEntry(page, key))?.v as { choices?: Record<string, string> } | undefined : undefined
    return value?.choices
  }).toEqual({ 'elicitation:"count"': '0', 'elicitation:"enabled"': 'false', 'elicitation:"color"': '"b"' })

  await page.reload()
  await expect(form.getByLabel('Count *')).toHaveValue('0')
  await expect(form.getByRole('button', { name: 'Enabled *', exact: true })).toHaveText('No')
  await expect(form.getByRole('button', { name: 'Color *', exact: true })).toHaveText('Blue')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page, 120_000)
  expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('FORM_ROUND_TRIP_OK')
  await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
  await expect(form).toHaveCount(0)
})
