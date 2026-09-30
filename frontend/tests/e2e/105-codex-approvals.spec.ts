import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { bashToolCall, codexEscalatedCommandToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import {
  applyPermissionPreset,
  expectSettingsOptionChosen,
  sendMessage,
  waitForAgentIdle,
  waitForSettingsHydrated,
} from './helpers/ui'

const CODEX = AgentProvider.CODEX

function shellQuote(value: string): string {
  return `'${value.replaceAll(/'/g, String.raw`'\''`)}'`
}

function writeCommand(path: string, content: string): string {
  return `printf %s ${shellQuote(content)} > ${shellQuote(path)}`
}

codexTest.describe('codex permission requests', () => {
  codexTest('runs a safe command without an approval request', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEX, 'safe-command', 'printf %s codex-safe-42')] },
      { text: 'The safe command finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run the safe command once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'codex-safe-42' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  })

  codexTest('runs an escalated command only after approval', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'approved-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    await modelScript.queue(
      { toolCalls: [codexEscalatedCommandToolCall('approve-command', writeCommand(file, 'approved-42'))] },
      { text: 'Approval completed.' },
    )
    await sendMessage(page, modelScript.prompt('Request approval for the scripted command.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toBeVisible()
    await expect(banner).toContainText('Run the scripted approval test.')
    expect(existsSync(file)).toBe(false)
    await page.getByTestId('control-allow-btn').click()
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)

    expect(readFileSync(file, 'utf8')).toBe('approved-42')
    await expect(banner).toHaveCount(0)
  })

  codexTest('leaves the file absent after a denied escalated command', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'denied-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    await modelScript.queue(
      { toolCalls: [codexEscalatedCommandToolCall('deny-command', writeCommand(file, 'denied-42'))] },
    )
    await sendMessage(page, modelScript.prompt('Request approval for the scripted command.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toBeVisible()
    expect(existsSync(file)).toBe(false)
    await page.getByTestId('control-deny-btn').click()
    // Codex ends this code-mode cell on denial. It sends no second model request.
    await waitForAgentIdle(page)

    expect(existsSync(file)).toBe(false)
    await expect(banner).toHaveCount(0)
  })

  codexTest('writes outside the workspace after the bypass preset applies', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    const file = join(createTestDirectory('codex-bypass-output-'), 'result.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsOptionChosen(page, 'permissionMode-never')
    await expectSettingsOptionChosen(page, 'sandbox_policy-danger-full-access')
    await expectSettingsOptionChosen(page, 'network_access-enabled')

    await modelScript.queue(
      { toolCalls: [bashToolCall(CODEX, 'bypass-command', writeCommand(file, 'bypass-42'))] },
      { text: 'Bypass command finished.' },
    )
    await sendMessage(page, modelScript.prompt('Write the scripted marker outside this workspace.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    expect(readFileSync(file, 'utf8')).toBe('bypass-42')
    await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  })
})
