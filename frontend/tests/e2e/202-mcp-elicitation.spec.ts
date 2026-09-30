import type { Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { openAgentViaAPI } from './helpers/api'
import { writeMcpFormServer } from './helpers/mcpFormServer'
import { fillMcpProbeForm } from './helpers/mcpProbeForm'
import { withMockModelScenario } from './helpers/mockModelScenario'
import { mcpToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { withMockPiModel } from './helpers/scriptedPiModel'
import { readEntry, storageKeys } from './helpers/storage'
import { expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

async function answerPiFormChoice(page: Page, prompt: string, option: string): Promise<void> {
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText(prompt)
  await banner.getByTestId(`question-option-${option}`).click()
  await page.getByTestId('control-submit-btn').filter({ visible: true }).click()
}

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

gooseTest.describe('Goose MCP input form', () => {
  gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')
  gooseTest('roundtrips zero, false, and blue through native form elicitation', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    void authenticatedGooseWorkspace
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.GOOSE, 'goose-form', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'The Goose form completed.' },
    )
    await sendMessage(page, modelScript.prompt('Call the form_probe ask tool exactly once.'))
    await modelScript.waitForSteps(1)
    const permission = page.getByTestId('control-banner').filter({ visible: true })
    await expect(permission).toContainText('form probe: ask')
    await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()

    const form = await fillMcpProbeForm(page)
    await page.reload()
    await expect(form.getByLabel('Count *')).toHaveValue('0')
    await expect(form.getByRole('button', { name: 'Enabled *', exact: true })).toHaveText('No')
    await expect(form.getByRole('button', { name: 'Color *', exact: true })).toHaveText('Blue')
    await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('FORM_ROUND_TRIP_OK')
    await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
    await expect(form).toHaveCount(0)
  })
})

test('answers a native Pi MCP form with zero and false values', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-mcp-form-')
  const script = writeMcpFormServer(directory, 'form-server.mjs')
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  await withMockPiModel(directory, leapmuxServer.mockModelUrl, async (settings) => {
    await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.PI,
      ...settings,
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Protocol test')
    await withMockModelScenario(leapmuxServer.mockModelUrl, [
      { toolCalls: [mcpToolCall(AgentProvider.PI, 'pi-form', { server: 'form_probe', tool: 'ask', input: {} })] },
      { text: 'Pi form completed.' },
    ], async (scenario) => {
      await sendMessage(page, scenario.prompt('Call the form_probe ask tool once.'))
      // Pi's MCP adapter asks for each schema field through its native UI.
      await answerPiFormChoice(page, 'MCP Input Request', 'Continue')
      await answerPiFormChoice(page, 'Count (required)', 'Enter value')
      const inputBanner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(inputBanner).toContainText('Count (required)')
      await page.locator('[data-testid="composer-editor"]:visible .ProseMirror').fill('0')
      await page.getByTestId('control-submit-btn').filter({ visible: true }).click()
      await answerPiFormChoice(page, 'Enabled (required)', 'No')
      await answerPiFormChoice(page, 'Color (required)', 'Blue (b)')
      await answerPiFormChoice(page, 'Review input for form_probe', 'Submit')
      await waitForAgentIdle(page, 120_000)
      const status = await scenario.status()
      expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('FORM_ROUND_TRIP_OK')
      await expect(messageBubbles(page).filter({ hasText: 'FORM_ROUND_TRIP_OK' }).first()).toBeVisible()
      await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
    })
  })
})

test('recovers Pi MCP permission arguments and sends the selected approval scope', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const directory = createTestDirectory('pi-mcp-permission-')
  const args = { query: 'x'.repeat(900), limit: 0, tail: 'END_MCP_ARGUMENTS' }
  const script = writeMcpFormServer(directory, 'permission-server.mjs', { expectedEchoArguments: args })
  writeFileSync(join(directory, '.mcp.json'), JSON.stringify({ settings: { approveTools: true }, mcpServers: { form_probe: { command: process.execPath, args: [script] } } }))
  await withMockPiModel(directory, leapmuxServer.mockModelUrl, async (settings) => {
    const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, directory, {
      agentProvider: AgentProvider.PI,
      ...settings,
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Protocol test')
    await withMockModelScenario(leapmuxServer.mockModelUrl, [
      { toolCalls: [mcpToolCall(AgentProvider.PI, 'mcp-call', { server: 'form_probe', tool: 'echo', input: args })] },
      { text: 'Protocol test complete.' },
    ], async (scenario) => {
      await sendMessage(page, scenario.prompt('Run the configured MCP permission probe.'))
      const banner = page.getByTestId('control-banner').filter({ visible: true })
      await expect(banner).toContainText('Permission Required')
      await expect(banner.locator('pre')).toContainText('END_MCP_ARGUMENTS')
      const scope = page.getByRole('radio', { name: 'Session', exact: true })
      await scope.click()
      // IndexedDB writes are asynchronous. Verify the committed draft before testing reload recovery.
      await expect.poll(async () => {
        const key = (await storageKeys(page)).find(key => key.includes(`control-state:${agentId}:`))
        const value = key ? (await readEntry(page, key))?.v as { choices?: Record<string, string> } | undefined : undefined
        return value?.choices?.['elicitation-accept-choice']
      }).toBe('session')
      await page.reload()
      await expect(banner.locator('pre')).toContainText('END_MCP_ARGUMENTS')
      await expect(scope).toBeChecked()
      await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
      await waitForAgentIdle(page, 120_000)
      const status = await scenario.status()
      expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('PERMISSION_ACCEPTED')
      await expect(messageBubbles(page).filter({ hasText: 'PERMISSION_ACCEPTED' }).first()).toBeVisible()
      await expect(messageBubbles(page).filter({ hasText: 'Approved for this session' }).first()).toBeVisible()
      await expect(banner).toHaveCount(0)
    })
  })
})
