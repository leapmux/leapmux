import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { applyPermissionPreset, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

const CODEX = AgentProvider.CODEX

function shellQuote(value: string): string {
  return `'${value.replaceAll(/'/g, String.raw`'\''`)}'`
}

function writeCommand(path: string, content: string): string {
  return `printf %s ${shellQuote(content)} > ${shellQuote(path)}`
}

codexTest.describe('codex permission requests', () => {
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
